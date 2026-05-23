package render

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
	goyaml "go.yaml.in/yaml/v4"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

type KustomizeRenderer struct{}

func (KustomizeRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(opts.BuildOptions) != 0 {
		return nil, nil, fmt.Errorf("kustomize build options are not supported yet")
	}

	root, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}

	manifestPath, err := validateKustomizeGraph(ctx, source.RepoRoot, root)
	if err != nil {
		return nil, nil, err
	}

	kustomizer := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := kustomizer.Run(filesys.MakeFsOnDisk(), root)
	if err != nil {
		return nil, nil, fmt.Errorf("kustomize build %s: %w", root, err)
	}

	rendered, err := resMap.AsYaml()
	if err != nil {
		return nil, nil, fmt.Errorf("kustomize build %s: serialize manifests: %w", root, err)
	}

	docs, err := manifest.DecodeDocuments(manifestPath, bytes.NewReader(rendered))
	if err != nil {
		return nil, nil, err
	}

	out := make([]Manifest, 0, len(docs))
	for _, doc := range docs {
		out = append(out, Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	return out, nil, nil
}

var kustomizationFileNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

type kustomizeGraphValidator struct {
	repoRoot   string
	sourceRoot string
	visited    map[string]struct{}
}

func validateKustomizeGraph(ctx context.Context, repoRoot, sourceRoot string) (string, error) {
	validator := kustomizeGraphValidator{
		repoRoot:   filepath.Clean(repoRoot),
		sourceRoot: filepath.Clean(sourceRoot),
		visited:    make(map[string]struct{}),
	}
	return validator.validateKustomizationDir(ctx, sourceRoot)
}

func (v *kustomizeGraphValidator) validateKustomizationDir(ctx context.Context, dir string) (string, error) {
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
	if err := v.validateKustomization(ctx, filepath.Dir(kustomizationFile), manifestPath, &kustomization); err != nil {
		return "", err
	}
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

func (v *kustomizeGraphValidator) validateKustomization(ctx context.Context, dir, manifestPath string, kustomization *types.Kustomization) error {
	if err := v.validateOperandRefs(ctx, dir, kustomization); err != nil {
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

func (v *kustomizeGraphValidator) validateOperandRefs(ctx context.Context, dir string, kustomization *types.Kustomization) error {
	for _, resource := range kustomization.Resources {
		if err := v.validateResourceRef(ctx, dir, "resources", resource); err != nil {
			return err
		}
	}
	//nolint:staticcheck // Kustomize still accepts bases; validate it to block unsafe refs.
	for _, base := range kustomization.Bases {
		if err := v.validateKustomizationRef(ctx, dir, "bases", base); err != nil {
			return err
		}
	}
	for _, component := range kustomization.Components {
		if err := v.validateKustomizationRef(ctx, dir, "components", component); err != nil {
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
		if err := v.validatePathRef(dir, "generators", generator); err != nil {
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
	//nolint:staticcheck // Kustomize still accepts patchesJson6902; validate it to block unsafe refs.
	for _, patch := range kustomization.PatchesJson6902 {
		if patch.Path == "" {
			continue
		}
		if err := v.validatePathRef(dir, "patchesJson6902.path", patch.Path); err != nil {
			return err
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; validate it to block unsafe refs.
	for _, patch := range kustomization.PatchesStrategicMerge {
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

func (v *kustomizeGraphValidator) validateResourceRef(ctx context.Context, dir, field, ref string) error {
	path, info, err := v.validateLocalRef(dir, field, ref)
	if err != nil || info == nil || !info.IsDir() {
		return err
	}
	_, err = v.validateKustomizationDir(ctx, path)
	return err
}

func (v *kustomizeGraphValidator) validateKustomizationRef(ctx context.Context, dir, field, ref string) error {
	path, info, err := v.validateLocalRef(dir, field, ref)
	if err != nil || info == nil || !info.IsDir() {
		return err
	}
	_, err = v.validateKustomizationDir(ctx, path)
	return err
}

func (v *kustomizeGraphValidator) validatePathRef(dir, field, ref string) error {
	_, _, err := v.validateLocalRef(dir, field, ref)
	return err
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
		return "", nil, fmt.Errorf("kustomize %s %q is a remote ref; remote Kustomize refs are unsupported", field, ref)
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
	rel, err := filepath.Rel(v.repoRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s %q escapes repository root %q", kind, path, v.repoRoot)
	}
	return nil
}

func rejectSymlinkedPath(root, path string) error {
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes source root %q", path, root)
	}
	if rel == "." {
		return nil
	}

	current := filepath.Clean(root)
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
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

func generatorFileSourcePath(source string) string {
	if before, after, ok := strings.Cut(source, "="); ok && before != "" {
		return after
	}
	return source
}

func isInlineStrategicMergePatch(patch string) bool {
	return strings.Contains(patch, "\n")
}

func isRemoteKustomizeRef(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "git::") || strings.HasPrefix(lower, "git@") {
		return true
	}
	if isColonStyleKustomizeRemoteRef(trimmed) {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Scheme != "" {
		return true
	}
	if strings.Contains(lower, "?ref=") && strings.Contains(lower, "//") {
		return true
	}
	for _, host := range []string{"github.com/", "gitlab.com/", "bitbucket.org/"} {
		if strings.HasPrefix(lower, host) {
			return true
		}
	}
	return false
}

func isColonStyleKustomizeRemoteRef(ref string) bool {
	beforeColon, afterColon, ok := strings.Cut(ref, ":")
	if !ok || beforeColon == "" || afterColon == "" {
		return false
	}
	if strings.ContainsAny(beforeColon, `/\`) {
		return false
	}

	host := beforeColon
	if user, afterAt, ok := strings.Cut(beforeColon, "@"); ok {
		return user != "" && afterAt != "" && !strings.ContainsAny(afterAt, `/\`)
	}
	host = strings.ToLower(host)
	return isKnownGitHost(host) || looksLikeRemoteHost(host)
}

func isKnownGitHost(host string) bool {
	for _, known := range []string{"github.com", "gitlab.com", "bitbucket.org"} {
		if host == known {
			return true
		}
	}
	return false
}

func looksLikeRemoteHost(host string) bool {
	return strings.Contains(host, ".")
}
