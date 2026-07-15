package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/gitref"
	"sigs.k8s.io/kustomize/api/types"
)

type kustomizeInputCollector struct {
	repoRoot string
	paths    map[string]gitref.PathDigestPath
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
		return fmt.Errorf("kustomize %s %q is a remote Helm value file", field, redactKustomizeRef(ref))
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
		if err := c.addKustomizeRef(ctx, dir, "transformers", ref, false); err != nil {
			return err
		}
	}
	for _, ref := range kustomization.Validators {
		if err := c.addKustomizeRef(ctx, dir, "validators", ref, false); err != nil {
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

// collectGeneratorManifestRef digests one generators: entry. Inline entries
// are part of the kustomization file, which is always digested — but an
// inline KSOPS document's files: referents are separate render inputs of
// ksops-compat emulation, so they join the digest too, resolved relative to
// the kustomization directory (the base directory emulation uses for inline
// entries). Local path entries are digested by content, and when the manifest
// parses as a KSOPS generator its files: referents join the digest too —
// sops edits must rotate the render cache key.
func (c *kustomizeInputCollector) collectGeneratorManifestRef(ctx context.Context, dir, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if docs, inline := inlineKustomizeGeneratorDocuments(ref); inline {
		for _, fileRef := range ksopsGeneratorFileRefsFromDocuments(docs) {
			if err := c.addLocalRef(ctx, dir, "generators.files", fileRef, false); err != nil {
				return err
			}
		}
		return nil
	}
	if err := c.addKustomizeRef(ctx, dir, "generators", ref, false); err != nil {
		return err
	}
	if _, _, ok, err := remoteRequestForKustomizeRef(ref); err != nil || ok {
		return err
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
		return err
	}
	if ok {
		if request.Kind != "git-repo" || !isPinnedKustomizeRemoteRevision(parsed.Revision) {
			return fmt.Errorf("kustomize %s %q is not a pinned remote Git ref", field, redactKustomizeRef(ref))
		}
		return nil
	}
	return c.addLocalRef(ctx, dir, field, ref, optional)
}

func (c *kustomizeInputCollector) addLocalRef(ctx context.Context, dir, field, ref string, optional bool) error {
	if filepath.IsAbs(ref) {
		return fmt.Errorf("kustomize %s %q must be relative", field, ref)
	}
	if isRemoteKustomizeRef(ref) {
		return unsupportedRemoteKustomizeRefError(field, ref)
	}
	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	if err := rejectPathOutsideBoundary("kustomize "+field, path, c.repoRoot); err != nil {
		return err
	}
	return c.addAbsPath(ctx, path, optional)
}

func (c *kustomizeInputCollector) addAbsPath(ctx context.Context, path string, optional bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
