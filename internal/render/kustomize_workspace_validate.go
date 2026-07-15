package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/pathsafety"
	"sigs.k8s.io/kustomize/api/types"
)

func validateWorkspaceLocalRef(boundaryRoot, dir, field, ref string) (string, os.FileInfo, error) {
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
	if err := rejectPathOutsideBoundary("kustomize "+field, path, boundaryRoot); err != nil {
		return "", nil, err
	}
	if err := rejectSymlinkedPath(boundaryRoot, path); err != nil {
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

func validateWorkspaceHelmFields(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	if len(kustomization.HelmChartInflationGenerator) != 0 {
		return fmt.Errorf("helmChartInflationGenerator is deprecated and unsupported")
	}
	if kustomization.HelmGlobals != nil && kustomization.HelmGlobals.ConfigHome != "" {
		return fmt.Errorf("helmGlobals.configHome is unsupported")
	}
	if len(kustomization.HelmCharts) == 0 {
		return nil
	}
	chartHome := kustomizationChartHome(*kustomization)
	if err := validateWorkspacePathRef(boundaryRoot, dir, "helmGlobals.chartHome", chartHome); err != nil {
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
		if err := validateWorkspaceHelmChartPath(boundaryRoot, dir, chartHome, helmChart); err != nil {
			return err
		}
		if err := validateWorkspaceHelmValueRefs(boundaryRoot, dir, helmChart); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceHelmChartPath(boundaryRoot, dir, chartHome string, helmChart types.HelmChart) error {
	chartPath := filepath.FromSlash(helmChart.Name)
	if helmChart.Repo != "" && helmChart.Version != "" {
		chartPath = filepath.Join(filepath.FromSlash(helmChart.Name+"-"+helmChart.Version), filepath.FromSlash(helmChart.Name))
	}
	localChartPath := filepath.Clean(filepath.Join(dir, filepath.FromSlash(chartHome), chartPath))
	if err := rejectPathOutsideBoundary("kustomize helmCharts.name", localChartPath, boundaryRoot); err != nil {
		return err
	}
	if err := rejectSymlinkedPath(boundaryRoot, localChartPath); err != nil {
		return fmt.Errorf("kustomize helmCharts.name %q: %w", helmChart.Name, err)
	}
	return nil
}

func validateWorkspaceHelmValueRefs(boundaryRoot, dir string, helmChart types.HelmChart) error {
	if helmChart.ValuesFile != "" && !isRemoteHelmValueFile(helmChart.ValuesFile) {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "helmCharts.valuesFile", helmChart.ValuesFile); err != nil {
			return err
		}
	}
	for _, valuesFile := range helmChart.AdditionalValuesFiles {
		if isRemoteHelmValueFile(valuesFile) {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "helmCharts.additionalValuesFiles", valuesFile); err != nil {
			return err
		}
	}
	return nil
}

//nolint:gocyclo // Mirrors Kustomize's path-bearing field surface explicitly.
func validateWorkspacePathBearingRefs(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	if path := kustomization.OpenAPI["path"]; path != "" {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "openapi.path", path); err != nil {
			return err
		}
	}
	for _, configuration := range kustomization.Configurations {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "configurations", configuration); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.Generators {
		// Inline generator entries (left for krusty by classification) are
		// YAML documents, not paths.
		if isInlineKustomizeGeneratorEntry(generator) {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "generators", generator); err != nil {
			return err
		}
	}
	for _, transformer := range kustomization.Transformers {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "transformers", transformer); err != nil {
			return err
		}
	}
	for _, validator := range kustomization.Validators {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "validators", validator); err != nil {
			return err
		}
	}
	for _, replacement := range kustomization.Replacements {
		if replacement.Path == "" {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "replacements.path", replacement.Path); err != nil {
			return err
		}
	}
	for _, crd := range kustomization.Crds {
		if err := validateWorkspacePathRef(boundaryRoot, dir, "crds", crd); err != nil {
			return err
		}
	}
	if err := validateWorkspacePatchRefs(boundaryRoot, dir, kustomization); err != nil {
		return err
	}
	if err := validateWorkspaceGeneratorListRefs(boundaryRoot, dir, kustomization); err != nil {
		return err
	}
	return nil
}

func validateWorkspacePatchRefs(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	for _, patch := range kustomization.Patches {
		if patch.Path == "" {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "patches.path", patch.Path); err != nil {
			return err
		}
	}

	for _, patch := range kustomization.PatchesJson6902 { //nolint:staticcheck // Kustomize still accepts patchesJson6902; validate it to block unsafe refs.
		if patch.Path == "" {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "patchesJson6902.path", patch.Path); err != nil {
			return err
		}
	}

	for _, patch := range kustomization.PatchesStrategicMerge { //nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; validate it to block unsafe refs.
		path := string(patch)
		if isInlineStrategicMergePatch(path) {
			continue
		}
		if err := validateWorkspacePathRef(boundaryRoot, dir, "patchesStrategicMerge", path); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceGeneratorListRefs(boundaryRoot, dir string, kustomization *types.Kustomization) error {
	for _, generator := range kustomization.ConfigMapGenerator {
		if err := validateWorkspaceGeneratorRefs(boundaryRoot, dir, "configMapGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if err := validateWorkspaceGeneratorRefs(boundaryRoot, dir, "secretGenerator", generator.KvPairSources); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspaceGeneratorRefs(boundaryRoot, dir, field string, sources types.KvPairSources) error {
	for _, source := range sources.FileSources {
		if err := validateWorkspacePathRef(boundaryRoot, dir, field+".files", generatorFileSourcePath(source)); err != nil {
			return err
		}
	}
	for _, source := range sources.EnvSources {
		if err := validateWorkspacePathRef(boundaryRoot, dir, field+".envs", source); err != nil {
			return err
		}
	}
	if sources.EnvSource != "" {
		if err := validateWorkspacePathRef(boundaryRoot, dir, field+".env", sources.EnvSource); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspacePathRef(boundaryRoot, dir, field, ref string) error {
	_, _, err := validateWorkspaceLocalRef(boundaryRoot, dir, field, ref)
	return err
}

func rejectPathOutsideBoundary(kind, path, boundaryRoot string) error {
	rel, err := filepath.Rel(boundaryRoot, path)
	if err != nil || pathsafety.RelEscapes(rel) {
		return fmt.Errorf("%s %q escapes repository root %q", kind, path, boundaryRoot)
	}
	return nil
}

func rejectSymlinkedPath(root, path string) error {
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil || pathsafety.RelEscapes(rel) {
		return fmt.Errorf("path %q escapes source root %q", path, root)
	}
	if rel == "." {
		return nil
	}

	current := filepath.Clean(root)
	for component := range strings.SplitSeq(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q includes symlink component %q", path, component)
		}
	}
	return nil
}

func generatedKustomizeWorkspacePath(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("generated kustomize path must not be empty")
	}
	cleanRel := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(cleanRel) || cleanRel == "." || pathsafety.RelEscapes(cleanRel) {
		return "", fmt.Errorf("generated kustomize path %q must be relative inside %q", rel, root)
	}
	generatedPath := filepath.Join(root, cleanRel)
	if err := rejectPathOutsideBoundary("generated kustomize path", generatedPath, root); err != nil {
		return "", err
	}
	if err := rejectSymlinkedPath(root, generatedPath); err != nil {
		return "", fmt.Errorf("generated kustomize path %q: %w", rel, err)
	}
	return generatedPath, nil
}

// writeGeneratedKustomizeWorkspaceFile writes a drydock-generated file at rel
// under root, failing closed when anything already exists at the destination.
// Prepared workspace trees materialize repository files as hard links
// (copyWorkspaceFile), so an O_TRUNC rewrite of a path the repository already
// committed would write THROUGH the shared inode into the user's original
// file. All generated-file writers (ksops-compat placeholders, generated helm
// values, generated helm manifests) must route through this helper: the Lstat
// produces the actionable error, and the O_EXCL open makes the rejection
// race-free.
func writeGeneratedKustomizeWorkspaceFile(root, rel string, data []byte) error {
	path, err := generatedKustomizeWorkspacePath(root, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return generatedKustomizeWorkspacePathExistsError(rel)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return generatedKustomizeWorkspacePathExistsError(rel)
		}
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func generatedKustomizeWorkspacePathExistsError(rel string) error {
	return fmt.Errorf("generated workspace path %q already exists in the source tree; the repository contains a file colliding with drydock's generated namespace — rename or remove it", rel)
}
