package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/pathsafety"
)

type kustomizeWorkspace struct {
	originalRepoRoot string
	tempRepoRoot     string
	sourceRoot       string
	opts             RenderOptions
	// buildSettings are the flags of the build. All the helmCharts entries
	// of the build share the slice of their --helm-api-versions values
	// (configureKustomizeHelmCapabilities).
	buildSettings  kustomizeBuildSettings
	visited        map[string]struct{}
	nextGraphIndex int
	nextPathIndex  int
	// ksopsSubstitutedFiles counts sops files substituted by ksops-compat
	// emulation. Per-write counting equals unique-file counting: the same
	// sops file referenced by two generators (or twice in one files: list)
	// emits duplicate resource IDs, which kustomize accumulation rejects
	// before any diagnostic is surfaced.
	ksopsSubstitutedFiles int
	// skippedExecFunctions counts the generator and transformer
	// configurations with an exec function that kustomize skips without
	// --enable-exec, and skippedEmptyFunctions the ones whose function
	// annotation names no program.
	skippedExecFunctions  int
	skippedEmptyFunctions int
	// rewrittenEntries maps the generated files that stand for generators:
	// and transformers: entries to the entries.
	rewrittenEntries map[string]string
	// sourceOptionDiagnostics holds the warnings from merging the
	// spec.source.kustomize options into the source kustomization.
	sourceOptionDiagnostics []diagnostic.Diagnostic
}

// diagnostics surfaces warnings accumulated while preparing the workspace:
// the source option warnings, then one ksops-compat substitution warning per
// source, mirroring the avp-compat-substituted pattern.
func (w *kustomizeWorkspace) diagnostics() []diagnostic.Diagnostic {
	diags := append([]diagnostic.Diagnostic(nil), w.sourceOptionDiagnostics...)
	if w.ksopsSubstitutedFiles != 0 {
		diags = append(diags, ksopsCompatDiagnostic(w.ksopsSubstitutedFiles))
	}
	if w.skippedExecFunctions+w.skippedEmptyFunctions != 0 {
		diags = append(diags, skippedFunctionsDiagnostic(w.skippedExecFunctions, w.skippedEmptyFunctions))
	}
	return diags
}

func renderKustomizeWithPreparedWorkspace(ctx context.Context, source ResolvedSource, graph []kustomizeGraphNode, opts RenderOptions, buildSettings kustomizeBuildSettings) ([]Manifest, []diagnostic.Diagnostic, error) {
	tempDir, err := os.MkdirTemp("", "drydock-kustomize-*")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	tempRepoRoot := filepath.Join(tempDir, "repo")
	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	if err := copyPreparedKustomizeWorkspaceTree(ctx, source.RepoRoot, root, tempRepoRoot, graph, opts); err != nil {
		return nil, nil, fmt.Errorf("copy repository to temp workspace: %w", err)
	}

	tempSource := ResolvedSource{
		RepoRoot: tempRepoRoot,
		Path:     source.Path,
	}
	tempRoot, err := sourceRoot(tempSource)
	if err != nil {
		return nil, nil, err
	}

	workspace := &kustomizeWorkspace{
		originalRepoRoot: filepath.Clean(source.RepoRoot),
		tempRepoRoot:     tempRepoRoot,
		sourceRoot:       filepath.Clean(tempRoot),
		opts:             opts,
		buildSettings:    buildSettings,
		visited:          make(map[string]struct{}),
	}
	if err := workspace.prepareKustomizationDir(ctx, tempRoot, tempRepoRoot, ""); err != nil {
		return nil, nil, workspace.restoreErrorPaths(err)
	}

	manifests, renderDiags, err := renderPlainKustomize(ctx, tempSource, tempRoot, buildSettings, opts)
	if err != nil {
		return nil, nil, workspace.restoreErrorPaths(err)
	}
	workspace.restoreRewrittenEntryPaths(manifests)
	return manifests, append(workspace.diagnostics(), renderDiags...), nil
}

//nolint:gocyclo // Coordinates helm inflation, remote graph rewriting, and validation.
func (w *kustomizeWorkspace) prepareKustomizationDir(ctx context.Context, dir, boundaryRoot, inheritedHelmNamespace string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir = filepath.Clean(dir)
	boundaryRoot = filepath.Clean(boundaryRoot)
	if err := rejectPathOutsideBoundary("kustomization directory", dir, boundaryRoot); err != nil {
		return err
	}
	key := boundaryRoot + "\x00" + dir
	if _, ok := w.visited[key]; ok {
		return nil
	}
	w.visited[key] = struct{}{}

	kustomizationFile, err := findKustomizationFile(dir)
	if err != nil {
		return err
	}
	manifestPath, err := relativeManifestPath(w.tempRepoRoot, kustomizationFile)
	if err != nil {
		manifestPath = kustomizationFile
	}
	content, err := os.ReadFile(kustomizationFile)
	if err != nil {
		return err
	}
	kustomization, err := decodeKustomization(manifestPath, content)
	if err != nil {
		return err
	}
	if dir == w.sourceRoot {
		originDir, err := w.originalPath(dir)
		if err != nil {
			return err
		}
		diags, err := applySourceKustomizeOptions(&kustomization, kustomizationFile, content, originDir, w.originalRepoRoot, w.opts)
		if err != nil {
			return fmt.Errorf("%s: %w", manifestPath, err)
		}
		w.sourceOptionDiagnostics = append(w.sourceOptionDiagnostics, diags...)
	}

	graphIndex := w.nextGraphIndex
	w.nextGraphIndex++
	childInheritedHelmNamespace := inheritedHelmNamespace
	if kustomization.Namespace != "" {
		childInheritedHelmNamespace = kustomization.Namespace
	}

	if err := validateWorkspaceHelmFields(boundaryRoot, dir, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	// kustomize accumulates the resources of a kustomization, with the
	// builds of its bases, before it runs its generators, and runs the
	// generators before it adds the components (KustTarget.accumulateTarget,
	// api/internal/target/kusttarget.go:202-235 at kustomize/api v0.21.3).
	// So a resource error, and the helmCharts of a base, come before the
	// helmCharts of this kustomization.
	resources, err := w.prepareKustomizeRefs(ctx, dir, boundaryRoot, "resources", graphIndex, kustomization.Resources, childInheritedHelmNamespace, true)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	kustomization.Resources = resources
	// The steps of the generators, for checkKustomizeGeneratorOrder.
	generationSteps := kustomizeFieldGeneratorSteps(&kustomization)

	if len(kustomization.HelmCharts) != 0 {
		if err := missingKustomizeResourceError(dir, kustomization.Resources); err != nil {
			return fmt.Errorf("%s: %w", manifestPath, err)
		}
		nodeRelDir, err := relativeManifestPath(w.tempRepoRoot, dir)
		if err != nil {
			return err
		}
		generatedResources, err := renderKustomizeHelmCharts(ctx, w.tempRepoRoot, dir, nodeRelDir, childInheritedHelmNamespace, kustomizationChartHome(kustomization), graphIndex, kustomization.HelmCharts, w.buildSettings, w.opts)
		if err != nil {
			return err
		}
		kustomization.HelmCharts = nil
		kustomization.Resources = append(kustomization.Resources, generatedResources...)
		if ids, ok := kustomizeResourceFileIDs(dir, generatedResources); ok {
			generationSteps = append(generationSteps, kustomizeGenerationStep{creates: ids, behavior: "unspecified", moved: true})
		}
	}

	components, err := w.prepareKustomizeRefs(ctx, dir, boundaryRoot, "components", graphIndex, kustomization.Components, childInheritedHelmNamespace, false)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	kustomization.Components = components

	node := kustomizePathRefNode{
		dir:          dir,
		boundaryRoot: boundaryRoot,
		graphIndex:   graphIndex,
	}
	if err := w.rewriteKustomizePathBearingRefs(ctx, node, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	// Generator classification runs after the path-ref rewrite above so
	// remote-acquired generator manifests are materialized in the workspace
	// before they are classified.
	entrySteps, err := w.prepareKustomizeGenerators(ctx, dir, boundaryRoot, graphIndex, &kustomization)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	generationSteps = append(generationSteps, entrySteps...)
	if err := checkKustomizeGeneratorOrder(generationSteps); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := w.prepareKustomizeTransformers(ctx, dir, boundaryRoot, graphIndex, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	if err := validateWorkspacePathBearingRefs(boundaryRoot, dir, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	// The rewrite encodes the kustomize-decoded kustomization, so its keys
	// are the canonical ones and its deprecated fields are already folded:
	// kustomize reads from it what it would read from the original.
	data, err := format.MarshalYAML(&kustomization)
	if err != nil {
		return fmt.Errorf("encode temp kustomization %s: %w", manifestPath, err)
	}
	if err := os.WriteFile(kustomizationFile, data, 0o644); err != nil {
		return fmt.Errorf("write temp kustomization %s: %w", manifestPath, err)
	}
	return nil
}

// originalPath maps a path in the temp workspace repository to the same
// path in the original repository.
func (w *kustomizeWorkspace) originalPath(path string) (string, error) {
	rel, err := filepath.Rel(w.tempRepoRoot, path)
	if err != nil || pathsafety.RelEscapes(rel) {
		return "", fmt.Errorf("workspace path %q escapes workspace root %q", path, w.tempRepoRoot)
	}
	return filepath.Join(w.originalRepoRoot, rel), nil
}

func (w *kustomizeWorkspace) prepareKustomizeRefs(ctx context.Context, dir, boundaryRoot, field string, graphIndex int, refs []string, inheritedHelmNamespace string, allowFileRefs bool) ([]string, error) {
	out := append([]string(nil), refs...)
	for i, ref := range refs {
		request, parsed, ok, err := remoteRequestForKustomizeRef(ref)
		if err != nil {
			return nil, err
		}
		if ok {
			if !allowFileRefs && parsed.Kind == kustomizeRemoteHTTPFile {
				return nil, fmt.Errorf("kustomize %s %q is a remote file ref; it must resolve to a Kustomization directory", field, redactKustomizeRef(parsed.Original))
			}
			mode := remotePathDir
			if allowFileRefs {
				mode = remotePathAny
			}
			rewritten, recurseDir, recurseBoundaryRoot, err := w.acquireAndCopyKustomizeRef(ctx, dir, field, graphIndex, i, request, parsed, mode, true)
			if err != nil {
				return nil, err
			}
			out[i] = rewritten
			if recurseDir != "" {
				if err := w.prepareKustomizationDir(ctx, recurseDir, recurseBoundaryRoot, inheritedHelmNamespace); err != nil {
					return nil, err
				}
			}
			continue
		}

		localPath, info, err := validateLocalKustomizeRef(boundaryRoot, dir, field, ref)
		if err != nil {
			return nil, err
		}
		if info != nil && info.IsDir() {
			if err := w.prepareKustomizationDir(ctx, localPath, boundaryRoot, inheritedHelmNamespace); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// missingKustomizeResourceError returns the error that kustomize reports
// for the first local resource of a kustomization that does not exist. The
// render reaches it only through krusty, after the helmCharts of the
// kustomization, so it is checked here first, in kustomize's order. The
// text is the one kustomize prints for the resource (accumulateResources),
// with the path relative to the kustomization.
func missingKustomizeResourceError(dir string, resources []string) error {
	for _, resource := range resources {
		if isAcquirableRemoteKustomizeResource(resource) || strings.Contains(resource, "://") {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(resource))
		if _, err := os.Lstat(target); err != nil && os.IsNotExist(err) {
			return fmt.Errorf("accumulating resources: accumulation err='accumulating resources from '%s': %s: no such file or directory'", resource, resource)
		}
	}
	return nil
}
