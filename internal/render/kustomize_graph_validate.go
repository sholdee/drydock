package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	goyaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/types"
)

type kustomizeGraphValidator struct {
	repoRoot                  string
	sourceRoot                string
	allowAcquirableRemoteRefs bool
	visited                   map[string]struct{}
	nodes                     []kustomizeGraphNode
}

type kustomizeGraphNode struct {
	Dir                    string
	File                   string
	ManifestPath           string
	InheritedHelmNamespace string
	Kustomization          types.Kustomization
}

func validateKustomizeGraph(ctx context.Context, repoRoot, sourceRoot string) (string, error) {
	manifestPath, _, err := collectKustomizeGraph(ctx, repoRoot, sourceRoot)
	return manifestPath, err
}

func collectKustomizeGraph(ctx context.Context, repoRoot, sourceRoot string) (string, []kustomizeGraphNode, error) {
	validator := kustomizeGraphValidator{
		repoRoot:   filepath.Clean(repoRoot),
		sourceRoot: filepath.Clean(sourceRoot),
		visited:    make(map[string]struct{}),
	}
	manifestPath, err := validator.validateKustomizationDir(ctx, sourceRoot, "")
	return manifestPath, validator.nodes, err
}

func collectKustomizeGraphForPreparation(ctx context.Context, repoRoot, sourceRoot string) (string, []kustomizeGraphNode, error) {
	validator := kustomizeGraphValidator{
		repoRoot:                  filepath.Clean(repoRoot),
		sourceRoot:                filepath.Clean(sourceRoot),
		allowAcquirableRemoteRefs: true,
		visited:                   make(map[string]struct{}),
	}
	manifestPath, err := validator.validateKustomizationDir(ctx, sourceRoot, "")
	return manifestPath, validator.nodes, err
}

func (v *kustomizeGraphValidator) validateKustomizationDir(ctx context.Context, dir, inheritedHelmNamespace string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	dir = filepath.Clean(dir)
	if err := v.rejectRepoRootEscape("kustomization directory", dir); err != nil {
		return "", err
	}
	if _, ok := v.visited[dir]; ok {
		return "", nil
	}
	v.visited[dir] = struct{}{}

	kustomizationFile, err := findKustomizationFile(dir)
	if err != nil {
		return "", err
	}
	manifestPath, err := relativeManifestPath(v.repoRoot, kustomizationFile)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(kustomizationFile)
	if err != nil {
		return "", err
	}
	var kustomization types.Kustomization
	if err := goyaml.Unmarshal(content, &kustomization); err != nil {
		return "", fmt.Errorf("decode kustomization %s: %w", manifestPath, err)
	}
	if err := v.validateKustomization(ctx, filepath.Dir(kustomizationFile), manifestPath, &kustomization, inheritedHelmNamespace); err != nil {
		return "", err
	}
	v.nodes = append(v.nodes, kustomizeGraphNode{
		Dir:                    filepath.Dir(kustomizationFile),
		File:                   kustomizationFile,
		ManifestPath:           manifestPath,
		InheritedHelmNamespace: inheritedHelmNamespace,
		Kustomization:          kustomization,
	})
	return manifestPath, nil
}

func findKustomizationFile(root string) (string, error) {
	for _, name := range kustomizationFileNames {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("kustomization file %q is a symlink", path)
		}
		return path, nil
	}
	return "", fmt.Errorf("kustomization file not found in %q", root)
}

func (v *kustomizeGraphValidator) validateKustomization(ctx context.Context, dir, manifestPath string, kustomization *types.Kustomization, inheritedHelmNamespace string) error {
	if err := v.validateHelmFields(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validateOperandRefs(ctx, dir, kustomization, inheritedHelmNamespace); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validateAuxiliaryRefs(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validatePatchRefs(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	if err := v.validateGeneratorListRefs(dir, kustomization); err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}
	return nil
}

func (v *kustomizeGraphValidator) validateHelmFields(dir string, kustomization *types.Kustomization) error {
	if len(kustomization.HelmChartInflationGenerator) != 0 {
		return fmt.Errorf("helmChartInflationGenerator is deprecated and unsupported")
	}
	if kustomization.HelmGlobals != nil && kustomization.HelmGlobals.ConfigHome != "" {
		return fmt.Errorf("helmGlobals.configHome is unsupported")
	}
	if len(kustomization.HelmCharts) == 0 {
		return nil
	}
	if _, _, err := v.validateLocalRef(dir, "helmGlobals.chartHome", kustomizationChartHome(*kustomization)); err != nil {
		return err
	}
	for _, helmChart := range kustomization.HelmCharts {
		if helmChart.NameTemplate != "" {
			return fmt.Errorf("helmCharts.nameTemplate is unsupported")
		}
		if helmChart.Devel {
			return fmt.Errorf("helmCharts.devel is unsupported")
		}
		if helmChart.Debug {
			return fmt.Errorf("helmCharts.debug is unsupported")
		}
		if err := v.validateHelmValueRefs(dir, helmChart); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateHelmValueRefs(dir string, helmChart types.HelmChart) error {
	if helmChart.ValuesFile != "" && !isRemoteHelmValueFile(helmChart.ValuesFile) {
		if err := v.validatePathRef(dir, "helmCharts.valuesFile", helmChart.ValuesFile); err != nil {
			return err
		}
	}
	for _, valuesFile := range helmChart.AdditionalValuesFiles {
		if isRemoteHelmValueFile(valuesFile) {
			continue
		}
		if err := v.validatePathRef(dir, "helmCharts.additionalValuesFiles", valuesFile); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateOperandRefs(ctx context.Context, dir string, kustomization *types.Kustomization, inheritedHelmNamespace string) error {
	childInheritedHelmNamespace := inheritedHelmNamespace
	if kustomization.Namespace != "" {
		childInheritedHelmNamespace = kustomization.Namespace
	}
	for _, resource := range kustomization.Resources {
		if err := v.validateResourceRef(ctx, dir, "resources", resource, childInheritedHelmNamespace); err != nil {
			return err
		}
	}

	for _, base := range kustomization.Bases { //nolint:staticcheck // Kustomize still accepts bases; validate it to block unsafe refs.
		if err := v.validateKustomizationRef(ctx, dir, "bases", base, childInheritedHelmNamespace); err != nil {
			return err
		}
	}
	for _, component := range kustomization.Components {
		if err := v.validateKustomizationRef(ctx, dir, "components", component, childInheritedHelmNamespace); err != nil {
			return err
		}
	}
	for _, crd := range kustomization.Crds {
		if err := v.validatePathRef(dir, "crds", crd); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateAuxiliaryRefs(dir string, kustomization *types.Kustomization) error {
	if path := kustomization.OpenAPI["path"]; path != "" {
		if err := v.validatePathRef(dir, "openapi.path", path); err != nil {
			return err
		}
	}
	for _, configuration := range kustomization.Configurations {
		if err := v.validatePathRef(dir, "configurations", configuration); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.Generators {
		if err := v.validateGeneratorManifestRef(dir, generator); err != nil {
			return err
		}
	}
	for _, transformer := range kustomization.Transformers {
		if err := v.validatePathRef(dir, "transformers", transformer); err != nil {
			return err
		}
	}
	for _, validator := range kustomization.Validators {
		if err := v.validatePathRef(dir, "validators", validator); err != nil {
			return err
		}
	}
	for _, replacement := range kustomization.Replacements {
		if replacement.Path == "" {
			continue
		}
		if err := v.validatePathRef(dir, "replacements.path", replacement.Path); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validatePatchRefs(dir string, kustomization *types.Kustomization) error {
	for _, patch := range kustomization.Patches {
		if patch.Path == "" {
			continue
		}
		if err := v.validatePathRef(dir, "patches.path", patch.Path); err != nil {
			return err
		}
	}

	for _, patch := range kustomization.PatchesJson6902 { //nolint:staticcheck // Kustomize still accepts patchesJson6902; validate it to block unsafe refs.
		if patch.Path == "" {
			continue
		}
		if err := v.validatePathRef(dir, "patchesJson6902.path", patch.Path); err != nil {
			return err
		}
	}

	for _, patch := range kustomization.PatchesStrategicMerge { //nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; validate it to block unsafe refs.
		path := string(patch)
		if isInlineStrategicMergePatch(path) {
			continue
		}
		if err := v.validatePathRef(dir, "patchesStrategicMerge", path); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateGeneratorListRefs(dir string, kustomization *types.Kustomization) error {
	for _, generator := range kustomization.ConfigMapGenerator {
		if err := v.validateGeneratorRefs(dir, "configMapGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if err := v.validateGeneratorRefs(dir, "secretGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateResourceRef(ctx context.Context, dir, field, ref, inheritedHelmNamespace string) error {
	if isAcquirableRemoteKustomizeResource(ref) {
		return nil
	}
	path, info, err := v.validateLocalRef(dir, field, ref)
	if err != nil || info == nil || !info.IsDir() {
		return err
	}
	_, err = v.validateKustomizationDir(ctx, path, inheritedHelmNamespace)
	return err
}

func (v *kustomizeGraphValidator) validateKustomizationRef(ctx context.Context, dir, field, ref, inheritedHelmNamespace string) error {
	if v.allowAcquirableRemoteRefs && isAcquirableRemoteKustomizeResource(ref) {
		return nil
	}
	path, info, err := v.validateLocalRef(dir, field, ref)
	if err != nil || info == nil || !info.IsDir() {
		return err
	}
	_, err = v.validateKustomizationDir(ctx, path, inheritedHelmNamespace)
	return err
}

func (v *kustomizeGraphValidator) validatePathRef(dir, field, ref string) error {
	if v.allowAcquirableRemoteRefs {
		if _, _, ok, err := remoteRequestForKustomizeRef(ref); err == nil && ok {
			return nil
		}
	}
	_, _, err := v.validateLocalRef(dir, field, ref)
	return err
}

// validateGeneratorManifestRef validates one generators: entry. Inline
// entries (YAML documents, not paths — kustomize tries NewResMapFromBytes
// before treating an entry as a path) have no manifest path to validate;
// treating them as paths risks spurious ENAMETOOLONG failures. An inline
// KSOPS document's files: referents ARE paths, though — render inputs read
// during ksops-compat emulation, resolved relative to the kustomization
// directory (matching prepareKustomizeGeneratorEntry) — so they are
// boundary-validated here. Path entries are validated like other path refs,
// and when the referenced manifest parses as a KSOPS generator its files:
// referents are boundary-validated relative to the manifest's own directory.
// Fork-PR content is attacker-influenced in both shapes.
func (v *kustomizeGraphValidator) validateGeneratorManifestRef(dir, ref string) error {
	if docs, inline := inlineKustomizeGeneratorDocuments(ref); inline {
		for _, fileRef := range ksopsGeneratorFileRefsFromDocuments(docs) {
			if _, _, err := v.validateLocalRef(dir, "generators.files", fileRef); err != nil {
				return err
			}
		}
		return nil
	}
	if v.allowAcquirableRemoteRefs {
		if _, _, ok, err := remoteRequestForKustomizeRef(ref); err == nil && ok {
			return nil
		}
	}
	path, info, err := v.validateLocalRef(dir, "generators", ref)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return err
	}
	for _, fileRef := range ksopsGeneratorFileRefs(path) {
		if _, _, err := v.validateLocalRef(filepath.Dir(path), "generators.files", fileRef); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateGeneratorRefs(dir, field string, sources types.KvPairSources) error {
	for _, source := range sources.FileSources {
		if err := v.validatePathRef(dir, field+".files", generatorFileSourcePath(source)); err != nil {
			return err
		}
	}
	for _, source := range sources.EnvSources {
		if err := v.validatePathRef(dir, field+".envs", source); err != nil {
			return err
		}
	}
	if sources.EnvSource != "" {
		if err := v.validatePathRef(dir, field+".env", sources.EnvSource); err != nil {
			return err
		}
	}
	return nil
}

func (v *kustomizeGraphValidator) validateLocalRef(dir, field, ref string) (string, os.FileInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil, nil
	}
	if isRemoteKustomizeRef(ref) {
		return "", nil, unsupportedRemoteKustomizeRefError(field, ref)
	}
	if filepath.IsAbs(ref) {
		return "", nil, fmt.Errorf("kustomize %s %q must be relative", field, ref)
	}

	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	if err := v.rejectRepoRootEscape("kustomize "+field, path); err != nil {
		return "", nil, err
	}
	if err := rejectSymlinkedPath(v.repoRoot, path); err != nil {
		return "", nil, fmt.Errorf("kustomize %s %q: %w", field, ref, err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil, nil
		}
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("kustomize %s %q is a symlink", field, ref)
	}
	return path, info, nil
}

func (v *kustomizeGraphValidator) rejectRepoRootEscape(kind, path string) error {
	return rejectPathOutsideBoundary(kind, path, v.repoRoot)
}
