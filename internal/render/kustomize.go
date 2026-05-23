package render

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/home-operations/argocd-local/internal/chart"
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

	_, graph, err := collectKustomizeGraph(ctx, source.RepoRoot, root)
	if err != nil {
		return nil, nil, err
	}
	if kustomizeGraphHasHelmCharts(graph) {
		return renderKustomizeWithHelmCharts(ctx, source, opts, root, graph)
	}

	return renderPlainKustomize(ctx, source, root)
}

var kustomizationFileNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

func renderPlainKustomize(ctx context.Context, source ResolvedSource, root string) ([]Manifest, []diagnostic.Diagnostic, error) {
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

func kustomizeGraphHasHelmCharts(graph []kustomizeGraphNode) bool {
	for _, node := range graph {
		if len(node.Kustomization.HelmCharts) != 0 {
			return true
		}
	}
	return false
}

func renderKustomizeWithHelmCharts(ctx context.Context, source ResolvedSource, opts RenderOptions, _ string, graph []kustomizeGraphNode) ([]Manifest, []diagnostic.Diagnostic, error) {
	tempDir, err := os.MkdirTemp("", "argocd-local-kustomize-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tempDir)

	tempRepoRoot := filepath.Join(tempDir, "repo")
	if err := copyRegularTree(source.RepoRoot, tempRepoRoot); err != nil {
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

	for i, node := range graph {
		if len(node.Kustomization.HelmCharts) == 0 {
			continue
		}
		nodeRelDir, err := relativeManifestPath(source.RepoRoot, node.Dir)
		if err != nil {
			return nil, nil, err
		}
		tempNodeDir := filepath.Join(tempRepoRoot, nodeRelDir)
		generatedResources, err := renderKustomizeHelmCharts(ctx, tempRepoRoot, tempNodeDir, nodeRelDir, node.Kustomization.Namespace, i, node.Kustomization.HelmCharts, opts)
		if err != nil {
			return nil, nil, err
		}

		kustomization := node.Kustomization
		kustomization.HelmCharts = nil
		kustomization.Resources = append(kustomization.Resources, generatedResources...)
		data, err := goyaml.Marshal(&kustomization)
		if err != nil {
			return nil, nil, fmt.Errorf("encode temp kustomization %s: %w", node.ManifestPath, err)
		}
		tempKustomizationFile := filepath.Join(tempNodeDir, filepath.Base(node.File))
		if err := os.WriteFile(tempKustomizationFile, data, 0o644); err != nil {
			return nil, nil, fmt.Errorf("write temp kustomization %s: %w", node.ManifestPath, err)
		}
	}

	return renderPlainKustomize(ctx, tempSource, tempRoot)
}

func renderKustomizeHelmCharts(ctx context.Context, tempRepoRoot, tempSourceRoot, valueFilesBaseDir, namespaceFallback string, graphIndex int, helmCharts []types.HelmChart, opts RenderOptions) ([]string, error) {
	acquirer := opts.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}

	generatedResources := make([]string, 0, len(helmCharts))
	for i, helmChart := range helmCharts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		request := chart.Request{
			Repository: helmChart.Repo,
			Name:       helmChart.Name,
			Version:    helmChart.Version,
			Kind:       kustomizeHelmChartRepositoryKind(helmChart.Repo),
		}
		result, err := acquirer.Acquire(ctx, request, chart.Options{
			CacheDir: opts.ChartCacheDir,
			Offline:  opts.OfflineCharts,
			Refresh:  opts.RefreshCharts,
		})
		if err != nil {
			return nil, fmt.Errorf("acquire kustomize helm chart %s: %w", helmChart.Name, err)
		}

		baseName := safeGeneratedKustomizeHelmBaseName(helmChart.Name, helmChart.Version)
		generatedName := fmt.Sprintf("%03d-%03d-%s", graphIndex, i, baseName)
		chartRel := filepath.Join(".argocd-local", "charts", generatedName)
		chartDst := filepath.Join(tempRepoRoot, chartRel)
		if err := copyRegularTree(result.ChartDir, chartDst); err != nil {
			return nil, fmt.Errorf("copy acquired helm chart %s: %w", helmChart.Name, err)
		}

		rendered, _, err := (HelmRenderer{}).Render(ctx, ResolvedSource{
			RepoRoot: tempRepoRoot,
			Path:     chartRel,
		}, renderOptionsForKustomizeHelmChart(helmChart, valueFilesBaseDir, namespaceFallback, opts, acquirer))
		if err != nil {
			return nil, err
		}
		if len(rendered) == 0 {
			continue
		}

		generatedResource := filepath.ToSlash(filepath.Join(".argocd-local", "helm", generatedName+".yaml"))
		generatedPath := filepath.Join(tempSourceRoot, filepath.FromSlash(generatedResource))
		if err := writeGeneratedHelmManifests(generatedPath, rendered); err != nil {
			return nil, err
		}
		generatedResources = append(generatedResources, generatedResource)
	}
	return generatedResources, nil
}

func renderOptionsForKustomizeHelmChart(helmChart types.HelmChart, valueFilesBaseDir, namespaceFallback string, opts RenderOptions, acquirer chart.Acquirer) RenderOptions {
	valueFiles := make([]string, 0, 1+len(helmChart.AdditionalValuesFiles))
	if helmChart.ValuesFile != "" {
		valueFiles = append(valueFiles, helmChart.ValuesFile)
	}
	valueFiles = append(valueFiles, helmChart.AdditionalValuesFiles...)

	namespace := helmChart.Namespace
	if namespace == "" {
		namespace = namespaceFallback
	}

	return RenderOptions{
		AppName:           helmChart.Name,
		ReleaseName:       helmChart.ReleaseName,
		Namespace:         namespace,
		KubeVersion:       helmChart.KubeVersion,
		APIVersions:       append([]string(nil), helmChart.ApiVersions...),
		ValueFiles:        valueFiles,
		ValueFilesBaseDir: valueFilesBaseDir,
		ValuesObject:      cloneValues(helmChart.ValuesInline),
		ValuesMergeMode:   helmChart.ValuesMerge,
		ChartCacheDir:     opts.ChartCacheDir,
		OfflineCharts:     opts.OfflineCharts,
		RefreshCharts:     opts.RefreshCharts,
		ChartAcquirer:     acquirer,
		IncludeCRDs:       helmChart.IncludeCRDs,
		IncludeCRDsSet:    true,
		SkipHooks:         helmChart.SkipHooks,
		SkipTests:         helmChart.SkipTests,
	}
}

func kustomizeHelmChartRepositoryKind(repository string) chart.RepositoryKind {
	if strings.HasPrefix(strings.TrimSpace(repository), "oci://") {
		return chart.RepositoryOCI
	}
	return chart.RepositoryHTTP
}

func safeGeneratedKustomizeHelmBaseName(name, version string) string {
	joined := strings.Trim(strings.TrimSpace(name)+"-"+strings.TrimSpace(version), "-")
	if joined == "" {
		joined = "chart"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(joined)
}

func writeGeneratedHelmManifests(path string, manifests []Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buffer bytes.Buffer
	for _, manifest := range manifests {
		if manifest.Object == nil {
			continue
		}
		data, err := goyaml.Marshal(manifest.Object.Object)
		if err != nil {
			return fmt.Errorf("encode generated helm manifest: %w", err)
		}
		if _, err := buffer.WriteString("---\n"); err != nil {
			return err
		}
		if _, err := buffer.Write(data); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write generated helm manifests %s: %w", path, err)
	}
	return nil
}

func copyRegularTree(srcRoot, dstRoot string) error {
	srcRoot = filepath.Clean(srcRoot)
	dstRoot = filepath.Clean(dstRoot)

	return filepath.WalkDir(srcRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy source path %q escapes source root %q", path, srcRoot)
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			if entry.Type().IsRegular() {
				return nil
			}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("copy source path %q is a symlink", path)
		}

		dstPath := filepath.Clean(filepath.Join(dstRoot, rel))
		dstRel, err := filepath.Rel(dstRoot, dstPath)
		if err != nil || dstRel == ".." || strings.HasPrefix(dstRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy destination path %q escapes destination root %q", dstPath, dstRoot)
		}

		if entry.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("copy source path %q is not a regular file", path)
		}
		return copyRegularFile(path, dstPath)
	})
}

func copyRegularFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}

type kustomizeGraphValidator struct {
	repoRoot   string
	sourceRoot string
	visited    map[string]struct{}
	nodes      []kustomizeGraphNode
}

type kustomizeGraphNode struct {
	Dir           string
	File          string
	ManifestPath  string
	Kustomization types.Kustomization
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
	manifestPath, err := validator.validateKustomizationDir(ctx, sourceRoot)
	return manifestPath, validator.nodes, err
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
	v.nodes = append(v.nodes, kustomizeGraphNode{
		Dir:           filepath.Dir(kustomizationFile),
		File:          kustomizationFile,
		ManifestPath:  manifestPath,
		Kustomization: kustomization,
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
