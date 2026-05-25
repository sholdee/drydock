package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
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

	_, graph, err := collectKustomizeGraphForPreparation(ctx, source.RepoRoot, root)
	if err != nil {
		return nil, nil, err
	}
	if kustomizeGraphHasHelmCharts(graph) || hasAcquirableRemoteKustomizeGraphRefs(graph) {
		return renderKustomizeWithPreparedWorkspace(ctx, source, graph, opts)
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

func hasAcquirableRemoteKustomizeGraphRefs(graph []kustomizeGraphNode) bool {
	for _, node := range graph {
		for _, resource := range node.Kustomization.Resources {
			if isAcquirableRemoteKustomizeResource(resource) {
				return true
			}
		}
		//nolint:staticcheck // Kustomize still accepts bases; scan it for remote refs.
		for _, base := range node.Kustomization.Bases {
			if isAcquirableRemoteKustomizeResource(base) {
				return true
			}
		}
		for _, component := range node.Kustomization.Components {
			if isAcquirableRemoteKustomizeResource(component) {
				return true
			}
		}
		if hasAcquirableRemoteKustomizePathRefs(node.Kustomization) {
			return true
		}
	}
	return false
}

//nolint:gocyclo // Mirrors Kustomize's path-bearing field surface explicitly.
func hasAcquirableRemoteKustomizePathRefs(kustomization types.Kustomization) bool {
	if isAcquirableRemoteKustomizePathRef(kustomization.OpenAPI["path"]) {
		return true
	}
	for _, ref := range kustomization.Configurations {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Generators {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Transformers {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Validators {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, ref := range kustomization.Crds {
		if isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, replacement := range kustomization.Replacements {
		if isAcquirableRemoteKustomizePathRef(replacement.Path) {
			return true
		}
	}
	for _, patch := range kustomization.Patches {
		if isAcquirableRemoteKustomizePathRef(patch.Path) {
			return true
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesJson6902; scan it for remote refs.
	for _, patch := range kustomization.PatchesJson6902 {
		if isAcquirableRemoteKustomizePathRef(patch.Path) {
			return true
		}
	}
	//nolint:staticcheck // Kustomize still accepts patchesStrategicMerge; scan it for remote refs.
	for _, patch := range kustomization.PatchesStrategicMerge {
		ref := string(patch)
		if !isInlineStrategicMergePatch(ref) && isAcquirableRemoteKustomizePathRef(ref) {
			return true
		}
	}
	for _, generator := range kustomization.ConfigMapGenerator {
		if hasAcquirableRemoteGeneratorRefs(generator.KvPairSources) {
			return true
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if hasAcquirableRemoteGeneratorRefs(generator.KvPairSources) {
			return true
		}
	}
	return false
}

func hasAcquirableRemoteGeneratorRefs(sources types.KvPairSources) bool {
	for _, source := range sources.FileSources {
		if isAcquirableRemoteKustomizePathRef(generatorFileSourcePath(source)) {
			return true
		}
	}
	for _, source := range sources.EnvSources {
		if isAcquirableRemoteKustomizePathRef(source) {
			return true
		}
	}
	return isAcquirableRemoteKustomizePathRef(sources.EnvSource)
}

func isAcquirableRemoteKustomizePathRef(ref string) bool {
	_, _, ok, err := remoteRequestForKustomizeRef(ref)
	return err == nil && ok
}

func renderKustomizeHelmCharts(ctx context.Context, tempRepoRoot, tempSourceRoot, valueFilesBaseDir, namespaceFallback, chartHome string, graphIndex int, helmCharts []types.HelmChart, opts RenderOptions) ([]string, error) {
	acquirer := opts.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}

	generatedResources := make([]string, 0, len(helmCharts))
	for i, helmChart := range helmCharts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		baseName := safeGeneratedKustomizeHelmBaseName(helmChart.Name, helmChart.Version)
		generatedName := fmt.Sprintf("%03d-%03d-%s", graphIndex, i, baseName)
		chartRel, err := resolveKustomizeHelmChart(ctx, tempRepoRoot, tempSourceRoot, chartHome, generatedName, helmChart, opts, acquirer)
		if err != nil {
			return nil, err
		}

		helmOpts, err := renderOptionsForKustomizeHelmChart(helmChart, tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, namespaceFallback, generatedName, opts, acquirer)
		if err != nil {
			return nil, err
		}

		rendered, _, err := (HelmRenderer{}).Render(ctx, ResolvedSource{
			RepoRoot: tempRepoRoot,
			Path:     chartRel,
		}, helmOpts)
		if err != nil {
			return nil, err
		}
		if len(rendered) == 0 {
			continue
		}

		generatedResource := filepath.ToSlash(filepath.Join(".drydock", "helm", generatedName+".yaml"))
		generatedPath, err := generatedKustomizeWorkspacePath(tempSourceRoot, generatedResource)
		if err != nil {
			return nil, err
		}
		if err := writeGeneratedHelmManifests(generatedPath, rendered); err != nil {
			return nil, err
		}
		generatedResources = append(generatedResources, generatedResource)
	}
	return generatedResources, nil
}

func resolveKustomizeHelmChart(ctx context.Context, tempRepoRoot, tempSourceRoot, chartHome, generatedName string, helmChart types.HelmChart, opts RenderOptions, acquirer chart.Acquirer) (string, error) {
	if chartRel, ok, err := resolveLocalKustomizeHelmChart(tempRepoRoot, tempSourceRoot, chartHome, helmChart); ok || err != nil {
		return chartRel, err
	}
	if helmChart.Repo == "" {
		return "", fmt.Errorf("kustomize helm chart %q has no local chart and no repo", helmChart.Name)
	}

	request := chart.Request{
		Repository: helmChart.Repo,
		Name:       helmChart.Name,
		Version:    helmChart.Version,
		Kind:       ChartRepositoryKind(helmChart.Repo, opts.OCIChartRepositories),
	}
	result, err := acquirer.Acquire(ctx, request, chart.Options{
		CacheDir:    opts.ChartCacheDir,
		Offline:     opts.OfflineCharts,
		Refresh:     opts.RefreshCharts,
		Credentials: opts.ChartCredentials,
	})
	if err != nil {
		recordKustomizeChartCacheEvent(opts, request, err, chart.Result{})
		return "", fmt.Errorf("acquire kustomize helm chart %s: %s", helmChart.Name, redactKustomizeChartAcquireError(err, request.Repository, opts.ChartCredentials))
	}
	recordKustomizeChartCacheEvent(opts, request, nil, result)

	chartRel := filepath.ToSlash(filepath.Join(".drydock", "charts", generatedName))
	chartDst, err := generatedKustomizeWorkspacePath(tempRepoRoot, chartRel)
	if err != nil {
		return "", err
	}
	if err := copyRegularTree(result.ChartDir, chartDst); err != nil {
		return "", fmt.Errorf("copy acquired helm chart %s: %w", helmChart.Name, err)
	}
	return chartRel, nil
}

func recordKustomizeChartCacheEvent(opts RenderOptions, request chart.Request, acquireErr error, acquired chart.Result) {
	if opts.CacheEventRecorder == nil {
		return
	}
	input := cacheevent.AcquisitionEventInput{
		Source:            cacheevent.SourceChart,
		Target:            request.Repository,
		RequestedRevision: request.Version,
		Offline:           opts.OfflineCharts,
		Refresh:           opts.RefreshCharts,
		SensitiveValues:   chartSensitiveValues(opts.ChartCredentials),
	}
	if acquireErr != nil {
		input.Err = acquireErr
		opts.CacheEventRecorder.Record(cacheevent.NewAcquisitionError(input).Event)
		return
	}
	input.Revision = acquired.Version
	input.FromCache = acquired.FromCache
	input.Network = !acquired.FromCache
	opts.CacheEventRecorder.Record(cacheevent.NewAcquisitionEvent(input))
}

func redactKustomizeChartAcquireError(err error, repository string, credentials chart.ChartCredentials) string {
	if err == nil {
		return ""
	}
	return cacheevent.RedactEventError(
		err.Error(),
		cacheevent.RedactTarget(repository),
		[]string{repository},
		chartSensitiveValues(credentials)...,
	)
}

func resolveLocalKustomizeHelmChart(repoRoot, kustomizationDir, chartHome string, helmChart types.HelmChart) (string, bool, error) {
	chartPath := filepath.FromSlash(helmChart.Name)
	if helmChart.Repo != "" && helmChart.Version != "" {
		chartPath = filepath.Join(filepath.FromSlash(helmChart.Name+"-"+helmChart.Version), filepath.FromSlash(helmChart.Name))
	}

	path := filepath.Join(kustomizationDir, filepath.FromSlash(chartHome), chartPath)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, nil
	}
	rel, err := relativeManifestPath(repoRoot, path)
	if err != nil {
		return "", false, err
	}
	return rel, true, nil
}

func renderOptionsForKustomizeHelmChart(helmChart types.HelmChart, tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, namespaceFallback, generatedName string, opts RenderOptions, acquirer chart.Acquirer) (RenderOptions, error) {
	valueFiles := make([]string, 0, 1+len(helmChart.AdditionalValuesFiles))
	valuesObject := cloneValues(helmChart.ValuesInline)
	valuesMergeMode := helmChart.ValuesMerge
	if helmChart.ValuesFile != "" {
		valueFiles = append(valueFiles, helmChart.ValuesFile)
	}
	valueFiles = append(valueFiles, helmChart.AdditionalValuesFiles...)
	if len(helmChart.ValuesInline) != 0 {
		generatedValuesFile, err := writeKustomizeHelmGeneratedValuesFile(tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, generatedName, helmChart)
		if err != nil {
			return RenderOptions{}, err
		}
		valueFiles = append([]string{generatedValuesFile}, helmChart.AdditionalValuesFiles...)
		valuesObject = nil
		valuesMergeMode = ""
	}

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
		ValuesObject:      valuesObject,
		ValuesMergeMode:   valuesMergeMode,
		ChartCacheDir:     opts.ChartCacheDir,
		OfflineCharts:     opts.OfflineCharts,
		RefreshCharts:     opts.RefreshCharts,
		ChartCredentials:  opts.ChartCredentials,
		ChartAcquirer:     acquirer,
		IncludeCRDs:       helmChart.IncludeCRDs,
		IncludeCRDsSet:    true,
		SkipHooks:         helmChart.SkipHooks,
		SkipTests:         helmChart.SkipTests,
	}, nil
}

func writeKustomizeHelmGeneratedValuesFile(tempRepoRoot, tempSourceRoot, chartRel, valueFilesBaseDir, generatedName string, helmChart types.HelmChart) (string, error) {
	primaryValues := map[string]any{}
	loadPrimaryValues, err := shouldLoadHelmValueFiles(helmChart.ValuesMerge, helmChart.ValuesInline)
	if err != nil {
		return "", err
	}
	if loadPrimaryValues {
		valueFilesBase := valueFilesBaseDir
		valueFile := helmChart.ValuesFile
		ignoreMissing := false
		if valueFile == "" {
			valueFilesBase = chartRel
			valueFile = "values.yaml"
			ignoreMissing = true
		}
		primaryValues, err = loadHelmValueFiles(tempRepoRoot, valueFilesBase, nil, []string{valueFile}, ignoreMissing)
		if err != nil {
			return "", err
		}
	}
	values, err := mergeHelmValues(primaryValues, cloneValues(helmChart.ValuesInline), helmChart.ValuesMerge)
	if err != nil {
		return "", err
	}

	generatedRel := filepath.ToSlash(filepath.Join(".drydock", "values", generatedName+".yaml"))
	generatedPath, err := generatedKustomizeWorkspacePath(tempSourceRoot, generatedRel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(generatedPath), 0o755); err != nil {
		return "", err
	}
	data, err := goyaml.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode generated helm values %s: %w", generatedRel, err)
	}
	if err := os.WriteFile(generatedPath, data, 0o644); err != nil {
		return "", fmt.Errorf("write generated helm values %s: %w", generatedRel, err)
	}
	return generatedRel, nil
}

func safeGeneratedKustomizeHelmBaseName(name, version string) string {
	joined := strings.Trim(strings.TrimSpace(name)+"-"+strings.TrimSpace(version), "-")
	if joined == "" {
		joined = "chart"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(joined)
}

func kustomizationChartHome(kustomization types.Kustomization) string {
	if kustomization.HelmGlobals != nil && kustomization.HelmGlobals.ChartHome != "" {
		return kustomization.HelmGlobals.ChartHome
	}
	return types.HelmDefaultHome
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
