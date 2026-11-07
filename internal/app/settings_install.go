package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/project"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// settingsInstall is what drydock knows, before it renders an Application,
// about whether the Application installs Argo CD from a Helm chart. Argo CD
// reads its settings from the argocd-cm, argocd-cmd-params-cm, and
// argocd-cmp-cm ConfigMaps that such an install puts in the cluster. Helm
// makes those ConfigMaps from all values of the release together and from
// the chart defaults, so drydock reads them from the rendered install and
// not from each values file on its own.
type settingsInstall struct {
	// valuesFilesRead lists every local Helm values file that the renders
	// of the Application read: Helm valueFiles, the values.yaml of a local
	// chart, and Kustomize helmCharts values files. Only files that exist
	// are listed.
	valuesFilesRead []string
	// files lists the other local files that the Kustomize renders of the
	// Application read. It holds files only, never directories.
	files []string
	// valuesFiles lists the files in valuesFilesRead that set Argo CD
	// settings (helmValuesSetArgoCDSettings).
	valuesFiles []string
	// inline reports Helm values written in the Application or in a
	// kustomization that set Argo CD settings.
	inline bool
	// chart reports that the Application renders a chart named argo-cd.
	chart bool
}

// argoCDChartName is the name of the argo-helm chart that installs Argo CD.
const argoCDChartName = "argo-cd"

// installsArgoCD reports whether the Application installs Argo CD from a Helm
// chart: its Helm values set Argo CD settings, or it renders the argo-cd
// chart. drydock renders such an Application to read its settings.
func (install settingsInstall) installsArgoCD() bool {
	return len(install.valuesFiles) > 0 || install.inline || install.chart
}

// settingsInstallProbe is a provider that holds only what
// sourceResolvesToRepoRoot reads, so drydock can find the installs before it
// builds a full provider. applicationSettingsInstall gives the same result
// with it as with the full provider of the same root and request.
func settingsInstallProbe(root string, request BuildRequest) localProvider {
	provider := localProvider{
		repoRoot:            root,
		sourceResolver:      sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, Offline: request.Offline}),
		controllerNamespace: defaultArgoCDControllerNamespace,
	}
	if matcher := newSelfRepoMatcher(request.selfRepo); len(matcher.urlKeys) > 0 {
		provider.selfRepoURLKeys = matcher.urlKeys
		provider.selfRepoRevisions = matcher.revisions
	}
	return provider
}

// applicationSettingsInstall returns the settingsInstall of application. It
// reads only local files and never acquires a source. Results are kept in
// request.settingsInstallMemo for one discovery run.
func applicationSettingsInstall(ctx context.Context, request BuildRequest, provider localProvider, application argoappv1.Application) settingsInstall {
	key, keyOK := settingsInstallMemoKey(application)
	if keyOK && request.settingsInstallMemo != nil {
		if cached, ok := request.settingsInstallMemo.Load(key); ok {
			if install, ok := cached.(settingsInstall); ok {
				return install
			}
		}
	}
	install := computeSettingsInstall(ctx, request.settingsInstallMemo, provider, application)
	if keyOK && request.settingsInstallMemo != nil && ctx.Err() == nil {
		request.settingsInstallMemo.Store(key, install)
	}
	return install
}

// settingsInstallMemoKey covers every input of computeSettingsInstall that
// comes from the Application: the name, namespace, and project, which the
// build environment and the .argocd-source-<app>.yaml file name read, the
// destination, which the build environment reads, and the sources, with the
// source hydrator, whose sync source replaces them.
func settingsInstallMemoKey(application argoappv1.Application) (string, bool) {
	data, err := json.Marshal(struct {
		Name        string                           `json:"name"`
		Namespace   string                           `json:"namespace"`
		Project     string                           `json:"project"`
		Destination argoappv1.ApplicationDestination `json:"destination"`
		Source      *argoappv1.ApplicationSource     `json:"source,omitempty"`
		Sources     argoappv1.ApplicationSources     `json:"sources,omitempty"`
		Hydrator    *argoappv1.SourceHydrator        `json:"sourceHydrator,omitempty"`
	}{application.Name, application.Namespace, application.Spec.Project, application.Spec.Destination, application.Spec.Source, application.Spec.Sources, application.Spec.SourceHydrator})
	if err != nil {
		return "", false
	}
	return "application\x00" + string(data), true
}

func computeSettingsInstall(ctx context.Context, memo *sync.Map, provider localProvider, application argoappv1.Application) settingsInstall {
	// The sync source of a source hydrator renders from another branch in
	// most cases, so its values files are not files of this tree. drydock
	// still reads the settings ConfigMaps that its render holds.
	if application.Spec.SourceHydrator != nil {
		return settingsInstall{}
	}
	plan, err := Plan(application)
	if err != nil {
		return settingsInstall{}
	}
	builder := settingsInstallBuilder{memo: memo, root: provider.repoRoot}
	for _, sourcePlan := range plan.Sources {
		if sourcePlan.RefOnly {
			continue
		}
		builder.addSource(ctx, provider, application, plan, sourcePlan)
	}
	builder.install.valuesFilesRead = uniqueStrings(builder.install.valuesFilesRead)
	builder.install.files = uniqueStrings(builder.install.files)
	builder.install.valuesFiles = uniqueStrings(builder.install.valuesFiles)
	return builder.install
}

type settingsInstallBuilder struct {
	memo    *sync.Map
	root    string
	install settingsInstall
}

// addValuesFile adds a local Helm values file that a render reads, when it
// exists as a regular file.
func (b *settingsInstallBuilder) addValuesFile(rel string, parser valuesParser) {
	info, err := os.Lstat(filepath.Join(b.root, filepath.FromSlash(rel)))
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	b.install.valuesFilesRead = append(b.install.valuesFilesRead, rel)
	if helmValuesFileSetsArgoCDSettings(b.memo, b.root, rel, parser) {
		b.install.valuesFiles = append(b.install.valuesFiles, rel)
	}
}

func (b *settingsInstallBuilder) addSource(ctx context.Context, provider localProvider, application argoappv1.Application, plan PlanResult, sourcePlan SourcePlan) {
	sourcePath, local := settingsInstallLocalSourcePath(provider, sourcePlan.Source)
	if local {
		sourcePlan = settingsInstallMergedSource(provider, application, sourcePlan, b.root, filepath.Join(b.root, filepath.FromSlash(sourcePath)))
	}
	source := sourcePlan.Source
	sourceType := settingsInstallSourceType(sourcePlan, b.root, sourcePath, local)
	if sourceType == argoappv1.ApplicationSourceTypeKustomize && local {
		b.addKustomize(ctx, sourcePath, source.Kustomize)
	}
	if sourceType != argoappv1.ApplicationSourceTypeHelm {
		return
	}
	if path.Base(source.Chart) == argoCDChartName {
		b.install.chart = true
	}
	if local {
		chartDir := filepath.Join(b.root, filepath.FromSlash(sourcePath))
		// Helm reads the values.yaml files of the chart and of its unpacked
		// subcharts as defaults.
		for _, rel := range render.HelmChartValuesFiles(b.root, chartDir) {
			b.addValuesFile(rel, valuesParserHelm)
		}
		if localChartIsArgoCD(chartDir) {
			b.install.chart = true
		}
	}
	if source.Helm == nil {
		return
	}
	for _, rel := range settingsInstallHelmValueFiles(provider, application, plan, sourcePlan, sourcePath, local) {
		b.addValuesFile(rel, valuesParserHelm)
	}
	if helmSourceInlineSetsArgoCDSettings(source.Helm) {
		b.install.inline = true
	}
}

// settingsInstallMergedSource merges the .argocd-source override files of a
// local source into it, as PrepareSource does before the render picks the
// source type and the value files. The merge reads only local files. When it
// fails, the render fails too, and drydock uses the source as written.
func settingsInstallMergedSource(provider localProvider, application argoappv1.Application, sourcePlan SourcePlan, root, sourceDir string) SourcePlan {
	merged, err := mergeArgocdSourceOverrides(sourcePlan.Source, root, sourceDir, provider.overrideInstanceName(application))
	if err != nil {
		return sourcePlan
	}
	explicitType, err := merged.ExplicitType()
	if err != nil {
		return sourcePlan
	}
	sourcePlan.Source = merged
	sourcePlan.ExplicitType = ""
	if explicitType != nil {
		sourcePlan.ExplicitType = *explicitType
	}
	return sourcePlan
}

// settingsInstallHelmValueFiles returns the local Helm value files that the
// render of sourcePlan reads. It builds the render options as the render
// does, with the Argo CD build environment for env substitution, and finds
// the $ref roots with renderRefsForSource, as the render does: a ref with
// the repository and revision of the source is the source root, and any
// other ref counts when its source resolves to this repository root. Then
// render.HelmValueFileReads resolves the entries as the renderer does,
// globs included.
func settingsInstallHelmValueFiles(provider localProvider, application argoappv1.Application, plan PlanResult, sourcePlan SourcePlan, sourcePath string, local bool) []string {
	opts, err := renderOptions(application, sourcePlan.Source, CapabilityOptions{}, settingsInstallTracking(provider))
	if err != nil {
		return nil
	}
	opts.ArgoEnv = settingsInstallRenderEnv(provider, application, sourcePlan.Source)
	refRoots, refSources, err := renderRefsForSource(plan, sourcePlan, helmRefInputPaths(opts))
	if err != nil {
		return nil
	}
	// The render anchors a ref with the repository and revision of the source
	// at the root of that source (localProvider.renderSource). That root is
	// this repository root only for a local source or a chart from a chart
	// repository (resolveSourceRootIdentity). Otherwise such a ref reads
	// another copy of the repository, not a local file.
	anchored := map[string]string{}
	if provider.sourceRootIsRepoRoot(settingsInstallResolvedSource(sourcePlan.Source)) {
		if anchored, err = anchorLocalRefRoots(provider.repoRoot, refRoots); err != nil {
			return nil
		}
	}
	for refKey, refSource := range refSources {
		if provider.sourceResolvesToRepoRoot(refSource) {
			anchored[refKey] = provider.repoRoot
		}
	}
	opts.RefRoots = anchored
	return render.HelmValueFileReads(provider.repoRoot, render.ResolvedSource{RepoRoot: provider.repoRoot, Path: sourcePath}, opts, !local)
}

func settingsInstallTracking(provider localProvider) TrackingOptions {
	tracking := defaultTrackingOptions()
	if provider.controllerNamespace != "" {
		tracking.ControllerNamespace = provider.controllerNamespace
	}
	return tracking
}

// settingsInstallRenderEnv is the Argo CD build environment of a source, as
// the render passes it to the Helm value file loader. A source that renders
// a local tree gets the ARGOCD_APP_REVISION that the render gives it
// (localSourceAppRevision): the commit of the tree, the ls-remote form for
// a multi-source Application, and the HEAD commit of a --repo-map target.
// Any other source keeps the spec targetRevision, because drydock finds
// these files without acquiring anything. source is merged with its
// .argocd-source overrides, but the merge keeps the spec targetRevision, as
// the repo-server's does (mergeSourceParameters,
// reposerver/repository/repository.go:1946-1949), so the revision resolves
// from the spec targetRevision as in the render (PrepareSource).
func settingsInstallRenderEnv(provider localProvider, application argoappv1.Application, source argoappv1.ApplicationSource) argoappv1.Env {
	controllerNamespace := settingsInstallTracking(provider).ControllerNamespace
	if revision := provider.localSourceAppRevision(source, application.Spec.HasMultipleSources()); revision != "" {
		return argoRenderEnvAt(application, source, revision, controllerNamespace)
	}
	return argoRenderEnv(application, source, controllerNamespace)
}

// settingsInstallSourceType returns the source type as Argo CD picks it: the
// explicit type of the source when it sets one, else the type that
// discoverLocalSourceType finds in the directory. A chart from a chart
// repository is Helm. An empty result means a directory or a plugin.
func settingsInstallSourceType(sourcePlan SourcePlan, root, sourcePath string, local bool) argoappv1.ApplicationSourceType {
	if sourcePlan.ExplicitType != "" {
		return sourcePlan.ExplicitType
	}
	source := sourcePlan.Source
	if source.Chart != "" && source.Path == "" {
		return argoappv1.ApplicationSourceTypeHelm
	}
	if !local {
		return ""
	}
	// The settings are not known yet here, so every tool counts as on.
	sourceType, err := discoverLocalSourceType(filepath.Join(root, filepath.FromSlash(sourcePath)), sourceTypeGates{})
	if err != nil {
		return ""
	}
	return sourceType
}

func (b *settingsInstallBuilder) addKustomize(ctx context.Context, sourcePath string, kustomize *argoappv1.ApplicationSourceKustomize) {
	values, err := render.KustomizeHelmValues(ctx, b.root, sourcePath, kustomize)
	if err != nil {
		return
	}
	for _, rel := range values.HelmParsedValuesFiles {
		b.addValuesFile(rel, valuesParserHelm)
	}
	for _, rel := range values.KustomizeParsedValuesFiles {
		b.addValuesFile(rel, valuesParserKustomize)
	}
	valuesFiles := make(map[string]struct{}, len(values.ValuesFiles))
	for _, rel := range values.ValuesFiles {
		valuesFiles[rel] = struct{}{}
	}
	for _, rel := range values.Files {
		if _, ok := valuesFiles[rel]; !ok {
			b.install.files = append(b.install.files, rel)
		}
	}
	for _, inline := range values.ValuesInline {
		if helmValuesSetArgoCDSettings(inline) {
			b.install.inline = true
		}
	}
	for _, name := range values.ChartNames {
		if path.Base(name) == argoCDChartName {
			b.install.chart = true
		}
	}
}

// localChartIsArgoCD reports whether the local chart in dir is the argo-cd
// chart, or an umbrella chart that depends on it.
func localChartIsArgoCD(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		return false
	}
	var chart struct {
		Name         string `json:"name"`
		Dependencies []struct {
			Name string `json:"name"`
		} `json:"dependencies"`
	}
	if err := sigsyaml.Unmarshal(data, &chart); err != nil {
		return false
	}
	if chart.Name == argoCDChartName {
		return true
	}
	for _, dependency := range chart.Dependencies {
		if dependency.Name == argoCDChartName {
			return true
		}
	}
	return false
}

// explicitDiscoverKustomizePaths returns the clean --discover-kustomize
// paths of request.
func explicitDiscoverKustomizePaths(request BuildRequest) map[string]struct{} {
	paths := make(map[string]struct{}, len(request.DiscoverKustomizePaths))
	for _, raw := range request.DiscoverKustomizePaths {
		if clean, err := cleanLocalSourcePath(raw); err == nil {
			paths[filepath.ToSlash(clean)] = struct{}{}
		}
	}
	return paths
}

// applicationRendersExplicitKustomizePath reports whether a source of
// application renders, from this repository root, a path that
// --discover-kustomize renders too.
func applicationRendersExplicitKustomizePath(provider localProvider, application argoappv1.Application, explicit map[string]struct{}) bool {
	if len(explicit) == 0 || application.Spec.SourceHydrator != nil {
		return false
	}
	sources := applicationRenderSources(application.Spec)
	for _, source := range sources {
		if sourcePath, ok := settingsInstallLocalSourcePath(provider, source); ok {
			if _, found := explicit[sourcePath]; found {
				return true
			}
		}
	}
	return false
}

// settingsInstallLocalSourcePath returns the clean repository-relative path
// of a source that renders from this repository root.
func settingsInstallLocalSourcePath(provider localProvider, source argoappv1.ApplicationSource) (string, bool) {
	if source.Path == "" || !provider.sourceResolvesToRepoRoot(settingsInstallResolvedSource(source)) {
		return "", false
	}
	clean, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(clean), true
}

func settingsInstallResolvedSource(source argoappv1.ApplicationSource) render.ResolvedSource {
	return render.ResolvedSource{
		Path:           source.Path,
		Chart:          source.Chart,
		RepoURL:        source.RepoURL,
		TargetRevision: source.TargetRevision,
	}
}

// valuesParser names the parser that the render reads a values file with.
type valuesParser string

const (
	// valuesParserHelm is the Helm value file loader (render.ParseHelmValuesFile).
	valuesParserHelm valuesParser = "helm"
	// valuesParserKustomize is the Kustomize helmCharts valuesFile read that
	// merges valuesInline (render.ParseKustomizeHelmValuesFile).
	valuesParserKustomize valuesParser = "kustomize"
)

// helmValuesFileSetsArgoCDSettings reads a local values file with the parser
// that the render uses for it and reports whether it sets Argo CD settings.
// A file that is missing, is not a regular file, or does not decode sets
// none.
func helmValuesFileSetsArgoCDSettings(memo *sync.Map, root, rel string, parser valuesParser) bool {
	full := filepath.Join(root, filepath.FromSlash(rel))
	key := "values\x00" + string(parser) + "\x00" + full
	if memo != nil {
		if cached, ok := memo.Load(key); ok {
			if sets, ok := cached.(bool); ok {
				return sets
			}
		}
	}
	sets := false
	if info, err := os.Lstat(full); err == nil && info.Mode().IsRegular() {
		if data, err := os.ReadFile(full); err == nil {
			parse := render.ParseHelmValuesFile
			if parser == valuesParserKustomize {
				parse = render.ParseKustomizeHelmValuesFile
			}
			if values, err := parse(rel, data); err == nil {
				sets = helmValuesSetArgoCDSettings(values)
			}
		}
	}
	if memo != nil {
		memo.Store(key, sets)
	}
	return sets
}

// helmValuesSetArgoCDSettings reports whether Helm values set a value under
// configs.cm, configs.params, or configs.cmp, the argo-cd chart values that
// become argocd-cm, argocd-cmd-params-cm, and argocd-cmp-cm.
func helmValuesSetArgoCDSettings(values map[string]any) bool {
	configs, ok := values["configs"].(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"cm", "params", "cmp"} {
		if section, ok := configs[key].(map[string]any); ok && len(section) > 0 {
			return true
		}
	}
	return false
}

func helmSourceInlineSetsArgoCDSettings(helm *argoappv1.ApplicationSourceHelm) bool {
	if raw := helm.ValuesYAML(); len(raw) > 0 {
		var values map[string]any
		if err := sigsyaml.Unmarshal(raw, &values); err == nil && helmValuesSetArgoCDSettings(values) {
			return true
		}
	}
	for _, parameter := range helm.Parameters {
		if helmParameterSetsArgoCDSettings(parameter.Name) {
			return true
		}
	}
	for _, parameter := range helm.FileParameters {
		if helmParameterSetsArgoCDSettings(parameter.Name) {
			return true
		}
	}
	return false
}

func helmParameterSetsArgoCDSettings(name string) bool {
	for _, prefix := range []string{"configs.cm.", "configs.params.", "configs.cmp."} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// settingsConfigMapManifests keeps the rendered argocd-cm,
// argocd-cmd-params-cm, and argocd-cmp-cm ConfigMaps. drydock renders an
// Application only for its settings when it would not render it for
// discovery, so it keeps no other object from that render.
func settingsConfigMapManifests(manifests []render.Manifest) []render.Manifest {
	var out []render.Manifest
	for _, manifest := range manifests {
		if manifest.Object != nil && (isSettingsConfigMapObject(manifest.Object) || isRepoServerSettingsObject(manifest.Object)) {
			out = append(out, manifest)
		}
	}
	return out
}

// isRepoServerSettingsObject reports the repo-server Deployment and
// ServiceAccount, which decide the namespace that helm uses
// (config.RepoServerHelmNamespace).
func isRepoServerSettingsObject(obj *unstructured.Unstructured) bool {
	return discovery.IsRepoServerObject(obj, "apps", "Deployment") || discovery.IsRepoServerObject(obj, "", "ServiceAccount")
}

func isSettingsConfigMapObject(obj *unstructured.Unstructured) bool {
	gvk := obj.GroupVersionKind()
	if gvk.Group != "" || gvk.Version != "v1" || gvk.Kind != "ConfigMap" {
		return false
	}
	switch obj.GetName() {
	case "argocd-cm", "argocd-cmd-params-cm", "argocd-cmp-cm":
		return true
	default:
		return false
	}
}

// markSettingsRenderInputs records the files that the render of install
// read on every settings ConfigMap in result that lands in namespace, the
// Argo CD namespace. A rendered object without a namespace lands in the
// destination namespace of the Application.
func markSettingsRenderInputs(result *discovery.Result, install settingsInstall, destinationNamespace, namespace string) {
	for i := range result.SettingsCandidates {
		candidate := &result.SettingsCandidates[i]
		if !discovery.IsSettingsConfigMapKind(candidate.Kind) || settingsCandidateNamespace(*candidate, destinationNamespace) != namespace {
			continue
		}
		candidate.RenderValuesFiles = append([]string(nil), install.valuesFilesRead...)
		candidate.RenderFiles = append([]string(nil), install.files...)
	}
}

func settingsCandidateNamespace(candidate discovery.SettingsCandidate, destinationNamespace string) string {
	namespace := candidate.Namespace
	if candidate.Object != nil {
		namespace = candidate.Object.GetNamespace()
	}
	if namespace == "" {
		return destinationNamespace
	}
	return namespace
}

// settingsConfigMapsInNamespace drops the settings ConfigMaps and the
// repo-server objects of result that do not land in namespace. Other
// settings candidates stay.
func settingsConfigMapsInNamespace(result discovery.Result, destinationNamespace, namespace string) discovery.Result {
	kept := result.SettingsCandidates[:0:0]
	for _, candidate := range result.SettingsCandidates {
		namespaced := discovery.IsSettingsConfigMapKind(candidate.Kind) || discovery.IsRepoServerKind(candidate.Kind)
		if !namespaced || settingsCandidateNamespace(candidate, destinationNamespace) == namespace {
			kept = append(kept, candidate)
		}
	}
	result.SettingsCandidates = kept
	return result
}

// settingsInstallInCluster reports whether an Application deploys to the
// cluster that drydock emulates Argo CD for: its destination resolves to the
// in-cluster server, the way project validation resolves it. drydock reads
// the settings of an Argo CD install only on that cluster: an Argo CD that
// an Application installs on another cluster runs other Applications.
func settingsInstallInCluster(application argoappv1.Application, settings config.ArgoSettings) bool {
	server, ok := project.ResolveDestinationServer(application.Spec.Destination, settings)
	return ok && server == argoappv1.KubernetesInternalAPIServerAddr
}

func hasSettingsConfigMapCandidate(result discovery.Result) bool {
	for _, candidate := range result.SettingsCandidates {
		if discovery.IsSettingsConfigMapKind(candidate.Kind) {
			return true
		}
	}
	return false
}

// settingsInstallRenderFailedDiagnostic reports that drydock could not render
// an Application that installs Argo CD from a Helm chart. Its values files
// then stay settings sources of their own, unless another install that
// rendered read them too.
func settingsInstallRenderFailedDiagnostic(appFile discovery.ApplicationFile, install settingsInstall, renderErr error) diagnostic.Diagnostic {
	name := applicationDisplayName(appFile.Application)
	message := fmt.Sprintf("cannot render Application %s to read the Argo CD settings that it installs: %v; ", name, renderErr)
	provenance := diagnostic.Provenance{Path: filepath.ToSlash(appFile.Path)}
	if len(install.valuesFiles) > 0 {
		message += "drydock reads configs.cm and configs.cmp from each of its Helm values files on its own instead, unless another rendered install reads the file, without the chart defaults and without configs.params"
		provenance = diagnostic.Provenance{Path: install.valuesFiles[0]}
	} else {
		message += "drydock does not read the settings of this install"
	}
	return diagnostic.Diagnostic{
		Code:       diagnostic.CodeSettingsInstallRenderFailed,
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
}

// settingsRenderInputs is the union of the files that the rendered settings
// ConfigMaps read.
type settingsRenderInputs struct {
	valuesFiles map[string]struct{}
	files       map[string]struct{}
}

// renderedSettingsInputs returns the files that every rendered settings
// ConfigMap in candidates read. A committed ConfigMap that won a collision
// with a rendered one carries the files of the rendered one
// (mergeSettingsCandidates), so it counts too.
func renderedSettingsInputs(candidates []discovery.SettingsCandidate) settingsRenderInputs {
	inputs := settingsRenderInputs{valuesFiles: map[string]struct{}{}, files: map[string]struct{}{}}
	for _, candidate := range candidates {
		for _, file := range candidate.RenderValuesFiles {
			inputs.valuesFiles[cleanSettingsInputPath(file)] = struct{}{}
		}
		for _, file := range candidate.RenderFiles {
			inputs.files[cleanSettingsInputPath(file)] = struct{}{}
		}
	}
	return inputs
}

func (inputs settingsRenderInputs) empty() bool {
	return len(inputs.valuesFiles) == 0 && len(inputs.files) == 0
}

func cleanSettingsInputPath(value string) string {
	return path.Clean(filepath.ToSlash(value))
}

// supersededBySettingsRender reports whether a committed settings file is a
// file that a rendered settings ConfigMap read: a Helm values file that the
// render read as values, or a settings ConfigMap file that a Kustomize render
// read. The rendered ConfigMap is what the cluster gets from that file, so it
// replaces the file. A file that the render did not read, even one in a
// directory that the render read from, stays a settings source.
func supersededBySettingsRender(candidate discovery.SettingsCandidate, inputs settingsRenderInputs) bool {
	if candidate.Tier != discovery.SourceTierStatic {
		return false
	}
	candidatePath := cleanSettingsInputPath(candidate.Path)
	switch {
	case candidate.Kind == "argocd-values":
		_, ok := inputs.valuesFiles[candidatePath]
		return ok
	case discovery.IsSettingsConfigMapKind(candidate.Kind):
		_, ok := inputs.files[candidatePath]
		return ok
	default:
		return false
	}
}
