package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/gitref"
	"sigs.k8s.io/kustomize/api/types"
)

type kustomizeInputCollector struct {
	repoRoot string
	paths    map[string]gitref.PathDigestPath
	// bestEffort skips a ref that cannot be collected instead of failing the
	// walk. Only changed-only selection sets it; digest keys never do.
	bestEffort bool
}

// KustomizeInputDigestPaths returns the committed repository-relative local
// inputs needed to key a persistent render cache entry for a Kustomize source.
func KustomizeInputDigestPaths(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]gitref.PathDigestPath, error) {
	root, err := sourceRoot(source)
	if err != nil {
		return nil, err
	}
	_, graph, err := collectKustomizeGraphForPreparation(ctx, source.RepoRoot, root)
	if err != nil {
		return nil, err
	}
	sourceOptionGraph, sourceOptionPaths, err := sourceKustomizeWorkspaceAdditions(ctx, source.RepoRoot, root, opts)
	if err != nil {
		return nil, err
	}
	graph = append(graph, sourceOptionGraph...)

	collector := &kustomizeInputCollector{
		repoRoot: filepath.Clean(source.RepoRoot),
		paths:    map[string]gitref.PathDigestPath{},
	}
	for _, node := range graph {
		if err := collector.collectNode(ctx, node); err != nil {
			return nil, err
		}
	}
	for _, path := range sourceOptionPaths {
		if err := collector.addAbsPath(ctx, path, false); err != nil {
			return nil, err
		}
	}
	out := make([]gitref.PathDigestPath, 0, len(collector.paths))
	for _, item := range collector.paths {
		out = append(out, item)
	}
	return out, nil
}

// KustomizeSelectionPaths returns the repository-relative local inputs of the
// Kustomize graph rooted at sourcePath, for changed-only ownership. It walks
// the same graph and refs as KustomizeInputDigestPaths, including the
// source-level components and patches in kustomize, but is best-effort: a ref
// that is remote, missing, outside repoRoot, or symlinked is skipped rather
// than failing the walk. An error means the graph itself could not be read;
// callers fall back to spec.source.path ownership.
func KustomizeSelectionPaths(ctx context.Context, repoRoot, sourcePath string, kustomize *argoappv1.ApplicationSourceKustomize) ([]string, error) {
	root, err := sourceRoot(ResolvedSource{RepoRoot: repoRoot, Path: sourcePath})
	if err != nil {
		return nil, err
	}
	_, graph, err := collectKustomizeGraphForPreparation(ctx, repoRoot, root)
	if err != nil {
		return nil, err
	}
	collector := &kustomizeInputCollector{
		repoRoot:   filepath.Clean(repoRoot),
		paths:      map[string]gitref.PathDigestPath{},
		bestEffort: true,
	}
	for _, node := range graph {
		if err := collector.collectNode(ctx, node); err != nil {
			return nil, err
		}
	}
	if err := collector.collectSourceKustomizeRefs(ctx, root, kustomize); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(collector.paths))
	for path := range collector.paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

// collectSourceKustomizeRefs adds the spec.source.kustomize components and
// patch files that rendering merges into the source kustomization
// (applySourceKustomizeOptions). The digest collects the same refs through
// sourceKustomizeWorkspaceAdditions; this best-effort form keeps a
// component's own directory when its graph cannot be read.
func (c *kustomizeInputCollector) collectSourceKustomizeRefs(ctx context.Context, sourceRoot string, kustomize *argoappv1.ApplicationSourceKustomize) error {
	if kustomize == nil {
		return nil
	}
	for _, component := range kustomize.Components {
		if err := c.addKustomizeRef(ctx, sourceRoot, "kustomize.components", component, false); err != nil {
			return err
		}
		component = strings.TrimSpace(component)
		if component == "" || isRemoteKustomizeRef(component) || filepath.IsAbs(component) {
			continue
		}
		componentRoot := filepath.Clean(filepath.Join(sourceRoot, filepath.FromSlash(component)))
		_, graph, err := collectKustomizeGraphForPreparation(ctx, c.repoRoot, componentRoot)
		if err != nil {
			if err := c.skip(ctx, err); err != nil {
				return err
			}
			continue
		}
		for _, node := range graph {
			if err := c.collectNode(ctx, node); err != nil {
				return err
			}
		}
	}
	for _, patch := range kustomize.Patches {
		if err := c.addKustomizeRef(ctx, sourceRoot, "kustomize.patches.path", patch.Path, false); err != nil {
			return err
		}
	}
	return nil
}

// skip reports err unless the collector is best-effort, in which case the
// failing ref is dropped. Context cancellation always propagates.
func (c *kustomizeInputCollector) skip(ctx context.Context, err error) error {
	if err == nil || !c.bestEffort {
		return err
	}
	return ctx.Err()
}

func (c *kustomizeInputCollector) collectNode(ctx context.Context, node kustomizeGraphNode) error {
	if err := c.addAbsPath(ctx, node.File, false); err != nil {
		return err
	}
	// Every kustomization filename variant in the node's directory is a
	// render input even when absent: kustomize errors on multiple variants
	// and resolves them by precedence, so a variant appearing or vanishing
	// must rotate the key. Optional-missing is itself a digest record.
	for _, name := range kustomizationFileNames {
		if err := c.addAbsPath(ctx, filepath.Join(node.Dir, name), true); err != nil {
			return err
		}
	}
	kustomization := node.Kustomization
	if err := c.collectHelmRefs(ctx, node.Dir, kustomization); err != nil {
		return err
	}
	if err := c.collectOperandRefs(ctx, node.Dir, kustomization); err != nil {
		return err
	}
	if err := c.collectAuxiliaryRefs(ctx, node.Dir, kustomization); err != nil {
		return err
	}
	if err := c.collectPatchRefs(ctx, node.Dir, kustomization); err != nil {
		return err
	}
	for _, generator := range kustomization.ConfigMapGenerator {
		if err := c.collectGeneratorRefs(ctx, node.Dir, generator.KvPairSources); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if err := c.collectGeneratorRefs(ctx, node.Dir, generator.KvPairSources); err != nil {
			return err
		}
	}
	return nil
}

func (c *kustomizeInputCollector) collectHelmRefs(ctx context.Context, dir string, kustomization types.Kustomization) error {
	if len(kustomization.HelmCharts) == 0 {
		return nil
	}
	chartHome := kustomizationChartHome(kustomization)
	for _, helmChart := range kustomization.HelmCharts {
		chartPath := localKustomizeHelmChartPath(dir, chartHome, helmChart)
		chartOptional := helmChart.Repo != ""
		if err := c.addAbsPath(ctx, chartPath, chartOptional); err != nil {
			return fmt.Errorf("kustomize helmCharts.name %q: %w", helmChart.Name, err)
		}
		if err := c.collectHelmValueRef(ctx, dir, "helmCharts.valuesFile", helmChart.ValuesFile); err != nil {
			return err
		}
		for _, valuesFile := range helmChart.AdditionalValuesFiles {
			if err := c.collectHelmValueRef(ctx, dir, "helmCharts.additionalValuesFiles", valuesFile); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *kustomizeInputCollector) collectHelmValueRef(ctx context.Context, dir, field, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if isRemoteHelmValueFile(ref) {
		return c.skip(ctx, fmt.Errorf("kustomize %s %q is a remote Helm value file", field, redactKustomizeRef(ref)))
	}
	return c.addLocalRef(ctx, dir, field, ref, false)
}

func (c *kustomizeInputCollector) collectOperandRefs(ctx context.Context, dir string, kustomization types.Kustomization) error {
	for _, resource := range kustomization.Resources {
		if err := c.addKustomizeRef(ctx, dir, "resources", resource, false); err != nil {
			return err
		}
	}
	for _, base := range kustomization.Bases { //nolint:staticcheck // Kustomize still accepts bases.
		if err := c.addKustomizeRef(ctx, dir, "bases", base, false); err != nil {
			return err
		}
	}
	for _, component := range kustomization.Components {
		if err := c.addKustomizeRef(ctx, dir, "components", component, false); err != nil {
			return err
		}
	}
	for _, crd := range kustomization.Crds {
		if err := c.addKustomizeRef(ctx, dir, "crds", crd, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *kustomizeInputCollector) collectAuxiliaryRefs(ctx context.Context, dir string, kustomization types.Kustomization) error {
	if err := c.addKustomizeRef(ctx, dir, "openapi.path", kustomization.OpenAPI["path"], false); err != nil {
		return err
	}
	for _, ref := range kustomization.Configurations {
		if err := c.addKustomizeRef(ctx, dir, "configurations", ref, false); err != nil {
			return err
		}
	}
	for _, ref := range kustomization.Generators {
		if err := c.collectGeneratorManifestRef(ctx, dir, ref); err != nil {
			return err
		}
	}
	for _, ref := range kustomization.Transformers {
		if err := c.collectPluginConfigEntry(ctx, dir, "transformers", ref); err != nil {
			return err
		}
	}
	for _, ref := range kustomization.Validators {
		if err := c.collectPluginConfigEntry(ctx, dir, "validators", ref); err != nil {
			return err
		}
	}
	for _, replacement := range kustomization.Replacements {
		if err := c.addKustomizeRef(ctx, dir, "replacements.path", replacement.Path, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *kustomizeInputCollector) collectPatchRefs(ctx context.Context, dir string, kustomization types.Kustomization) error {
	for _, patch := range kustomization.Patches {
		if err := c.addKustomizeRef(ctx, dir, "patches.path", patch.Path, false); err != nil {
			return err
		}
	}
	for _, patch := range kustomization.PatchesJson6902 { //nolint:staticcheck // Kustomize still accepts patchesJson6902.
		if err := c.addKustomizeRef(ctx, dir, "patchesJson6902.path", patch.Path, false); err != nil {
			return err
		}
	}
	for _, patch := range kustomization.PatchesStrategicMerge { //nolint:staticcheck // Kustomize still accepts patchesStrategicMerge.
		ref := string(patch)
		if isInlineStrategicMergePatch(ref) {
			continue
		}
		if err := c.addKustomizeRef(ctx, dir, "patchesStrategicMerge", ref, false); err != nil {
			return err
		}
	}
	return nil
}

// collectGeneratorManifestRef digests one generators: entry: the entry and
// its builtin configs' referents like any plugin config entry
// (collectPluginConfigEntry), plus the files: referents of its KSOPS
// documents.
func (c *kustomizeInputCollector) collectGeneratorManifestRef(ctx context.Context, dir, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if err := c.collectKSOPSGeneratorFileRefs(ctx, dir, ref); err != nil {
		return err
	}
	return c.collectPluginConfigEntry(ctx, dir, "generators", ref)
}

// collectKSOPSGeneratorFileRefs adds the files: referents of a generators:
// entry's KSOPS documents — separate render inputs of ksops-compat
// emulation; sops edits must rotate the render cache key. Emulation resolves
// them relative to the kustomization directory for inline entries and to the
// manifest's own directory for local path entries.
func (c *kustomizeInputCollector) collectKSOPSGeneratorFileRefs(ctx context.Context, dir, ref string) error {
	if docs, inline := inlineKustomizeGeneratorDocuments(ref); inline {
		for _, fileRef := range ksopsGeneratorFileRefsFromDocuments(docs) {
			if err := c.addLocalRef(ctx, dir, "generators.files", fileRef, false); err != nil {
				return err
			}
		}
		return nil
	}
	if isRemoteKustomizeRef(ref) {
		// collectPluginConfigEntry reports remote entries.
		return nil
	}
	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	for _, fileRef := range ksopsGeneratorFileRefs(path) {
		if err := c.addLocalRef(ctx, filepath.Dir(path), "generators.files", fileRef, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *kustomizeInputCollector) collectGeneratorRefs(ctx context.Context, dir string, sources types.KvPairSources) error {
	for _, source := range sources.FileSources {
		if err := c.addKustomizeRef(ctx, dir, "generator.files", generatorFileSourcePath(source), false); err != nil {
			return err
		}
	}
	for _, source := range sources.EnvSources {
		if err := c.addKustomizeRef(ctx, dir, "generator.envs", source, false); err != nil {
			return err
		}
	}
	return c.addKustomizeRef(ctx, dir, "generator.env", sources.EnvSource, false)
}

func (c *kustomizeInputCollector) addKustomizeRef(ctx context.Context, dir, field, ref string, optional bool) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	request, parsed, ok, err := remoteRequestForKustomizeRef(ref)
	if err != nil {
		return c.skip(ctx, err)
	}
	if ok {
		if request.Kind != "git-repo" || !isPinnedKustomizeRemoteRevision(parsed.Revision) {
			return c.skip(ctx, fmt.Errorf("kustomize %s %q is not a pinned remote Git ref", field, redactKustomizeRef(ref)))
		}
		return nil
	}
	return c.addLocalRef(ctx, dir, field, ref, optional)
}

func (c *kustomizeInputCollector) addLocalRef(ctx context.Context, dir, field, ref string, optional bool) error {
	path, err := c.resolveLocalRef(dir, field, ref)
	if err != nil {
		return c.skip(ctx, err)
	}
	return c.addAbsPath(ctx, path, optional)
}

// resolveLocalRef resolves a local ref against dir, rejecting absolute,
// remote and out-of-repository refs.
func (c *kustomizeInputCollector) resolveLocalRef(dir, field, ref string) (string, error) {
	if filepath.IsAbs(ref) {
		return "", fmt.Errorf("kustomize %s %q must be relative", field, ref)
	}
	if isRemoteKustomizeRef(ref) {
		return "", unsupportedRemoteKustomizeRefError(field, ref)
	}
	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	if err := rejectPathOutsideBoundary("kustomize "+field, path, c.repoRoot); err != nil {
		return "", err
	}
	return path, nil
}

func (c *kustomizeInputCollector) addAbsPath(ctx context.Context, path string, optional bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.skip(ctx, c.addAbsPathStrict(ctx, path, optional))
}

func (c *kustomizeInputCollector) addAbsPathStrict(ctx context.Context, path string, optional bool) error {
	path = filepath.Clean(path)
	if err := rejectPathOutsideBoundary("kustomize input", path, c.repoRoot); err != nil {
		return err
	}
	if err := rejectSymlinkedPath(c.repoRoot, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) && optional {
			return c.addDigestPath(path, true)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is a symlink", path)
	}
	if info.IsDir() {
		if err := rejectSymlinksInTree(ctx, path); err != nil {
			return err
		}
	}
	return c.addDigestPath(path, optional)
}

func (c *kustomizeInputCollector) addDigestPath(path string, optional bool) error {
	rel, err := relativeManifestPath(c.repoRoot, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if existing, ok := c.paths[rel]; ok {
		optional = existing.Optional && optional
	}
	c.paths[rel] = gitref.PathDigestPath{Path: rel, Optional: optional}
	return nil
}

func rejectSymlinksInTree(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q is a symlink", path)
		}
		return nil
	})
}

func isPinnedKustomizeRemoteRevision(revision string) bool {
	if len(revision) != 40 {
		return false
	}
	for _, r := range revision {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
