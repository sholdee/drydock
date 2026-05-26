package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sholdee/drydock/internal/diagnostic"
	goyaml "go.yaml.in/yaml/v4"
	"sigs.k8s.io/kustomize/api/types"
)

type kustomizeWorkspace struct {
	originalRepoRoot string
	tempRepoRoot     string
	opts             RenderOptions
	visited          map[string]struct{}
	nextGraphIndex   int
	nextPathIndex    int
}

func renderKustomizeWithPreparedWorkspace(ctx context.Context, source ResolvedSource, graph []kustomizeGraphNode, opts RenderOptions, buildSettings kustomizeBuildSettings) ([]Manifest, []diagnostic.Diagnostic, error) {
	tempDir, err := os.MkdirTemp("", "drydock-kustomize-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tempDir)

	tempRepoRoot := filepath.Join(tempDir, "repo")
	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	if err := copyPreparedKustomizeWorkspaceTree(source.RepoRoot, root, tempRepoRoot, graph); err != nil {
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
		opts:             opts,
		visited:          make(map[string]struct{}),
	}
	if err := workspace.prepareKustomizationDir(ctx, tempRoot, tempRepoRoot, ""); err != nil {
		return nil, nil, err
	}

	return renderPlainKustomize(ctx, tempSource, tempRoot, buildSettings)
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
	var kustomization types.Kustomization
	if err := goyaml.Unmarshal(content, &kustomization); err != nil {
		return fmt.Errorf("decode kustomization %s: %w", manifestPath, err)
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

	if len(kustomization.HelmCharts) != 0 {
		nodeRelDir, err := relativeManifestPath(w.tempRepoRoot, dir)
		if err != nil {
			return err
		}
		generatedResources, err := renderKustomizeHelmCharts(ctx, w.tempRepoRoot, dir, nodeRelDir, childInheritedHelmNamespace, kustomizationChartHome(kustomization), graphIndex, kustomization.HelmCharts, w.opts)
		if err != nil {
			return err
		}
		kustomization.HelmCharts = nil
		kustomization.Resources = append(kustomization.Resources, generatedResources...)
	}

	resources, err := w.prepareKustomizeRefs(ctx, dir, boundaryRoot, "resources", graphIndex, kustomization.Resources, childInheritedHelmNamespace, true)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	kustomization.Resources = resources

	//nolint:staticcheck // Kustomize still accepts bases; rewrite it for parity.
	bases, err := w.prepareKustomizeRefs(ctx, dir, boundaryRoot, "bases", graphIndex, kustomization.Bases, childInheritedHelmNamespace, false)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	//nolint:staticcheck // Kustomize still accepts bases; rewrite it for parity.
	kustomization.Bases = bases

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

	if err := validateWorkspacePathBearingRefs(boundaryRoot, dir, &kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	data, err := goyaml.Marshal(&kustomization)
	if err != nil {
		return fmt.Errorf("encode temp kustomization %s: %w", manifestPath, err)
	}
	if err := os.WriteFile(kustomizationFile, data, 0o644); err != nil {
		return fmt.Errorf("write temp kustomization %s: %w", manifestPath, err)
	}
	return nil
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

		localPath, info, err := validateWorkspaceLocalRef(boundaryRoot, dir, field, ref)
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
