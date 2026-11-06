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
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// settingsInstall is what drydock knows, before it renders an Application,
// about whether the Application installs Argo CD from Helm values. Argo CD
// reads its settings from the argocd-cm, argocd-cmd-params-cm, and
// argocd-cmp-cm ConfigMaps that such an install puts in the cluster. Helm
// makes those ConfigMaps from all values of the release together and from
// the chart defaults, so drydock reads them from the rendered install and
// not from each values file on its own.
type settingsInstall struct {
	// inputs lists the repository paths that the renders of the Application
	// read and that can hold Argo CD settings: Helm values files and every
	// Kustomize input. An entry can be a directory.
	inputs []string
	// valuesFiles lists the local Helm values files among inputs that set
	// Argo CD settings (helmValuesSetArgoCDSettings).
	valuesFiles []string
	// inline reports Helm values written in the Application or in a
	// kustomization that set Argo CD settings.
	inline bool
}

// installsArgoCD reports whether the Application's Helm values set Argo CD
// settings, so that drydock renders it to read them.
func (install settingsInstall) installsArgoCD() bool {
	return len(install.valuesFiles) > 0 || install.inline
}

// settingsInstallProbe is a provider that holds only what
// sourceResolvesToRepoRoot reads, so drydock can find the installs before it
// builds a full provider. applicationSettingsInstall gives the same result
// with it as with the full provider of the same root and request.
func settingsInstallProbe(root string, request BuildRequest) localProvider {
	provider := localProvider{
		repoRoot:       root,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, Offline: request.Offline}),
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

func settingsInstallMemoKey(application argoappv1.Application) (string, bool) {
	data, err := json.Marshal(struct {
		Source  *argoappv1.ApplicationSource `json:"source,omitempty"`
		Sources argoappv1.ApplicationSources `json:"sources,omitempty"`
	}{application.Spec.Source, application.Spec.Sources})
	if err != nil {
		return "", false
	}
	return "application\x00" + string(data), true
}

func computeSettingsInstall(ctx context.Context, memo *sync.Map, provider localProvider, application argoappv1.Application) settingsInstall {
	plan, err := Plan(application)
	if err != nil {
		return settingsInstall{}
	}
	builder := settingsInstallBuilder{memo: memo, root: provider.repoRoot}
	for _, sourcePlan := range plan.Sources {
		if sourcePlan.RefOnly {
			continue
		}
		builder.addSource(ctx, provider, plan, sourcePlan)
	}
	builder.install.inputs = uniqueStrings(builder.install.inputs)
	builder.install.valuesFiles = uniqueStrings(builder.install.valuesFiles)
	return builder.install
}

type settingsInstallBuilder struct {
	memo    *sync.Map
	root    string
	install settingsInstall
}

func (b *settingsInstallBuilder) addValuesFile(rel string) {
	b.install.inputs = append(b.install.inputs, rel)
	if helmValuesFileSetsArgoCDSettings(b.memo, b.root, rel) {
		b.install.valuesFiles = append(b.install.valuesFiles, rel)
	}
}

func (b *settingsInstallBuilder) addSource(ctx context.Context, provider localProvider, plan PlanResult, sourcePlan SourcePlan) {
	source := sourcePlan.Source
	sourcePath, local := settingsInstallLocalSourcePath(provider, source)
	if source.Helm != nil {
		for _, valueFile := range source.Helm.ValueFiles {
			if rel, ok := settingsInstallHelmValueFile(provider, plan, sourcePath, local, valueFile); ok {
				b.addValuesFile(rel)
			}
		}
		if helmSourceInlineSetsArgoCDSettings(source.Helm) {
			b.install.inline = true
		}
	}
	if !local {
		return
	}
	dir := filepath.Join(b.root, filepath.FromSlash(sourcePath))
	switch {
	case sourcePlan.ExplicitType != argoappv1.ApplicationSourceTypeHelm && hasKustomizationFile(dir):
		b.addKustomize(ctx, sourcePath, source.Kustomize)
	case sourceRootHasLocalChart(dir):
		// Helm reads the values.yaml of the chart as its defaults.
		b.addValuesFile(path.Join(sourcePath, "values.yaml"))
	}
}

func (b *settingsInstallBuilder) addKustomize(ctx context.Context, sourcePath string, kustomize *argoappv1.ApplicationSourceKustomize) {
	values, err := render.KustomizeHelmValues(ctx, b.root, sourcePath, kustomize)
	if err != nil {
		return
	}
	// The graph paths already hold the values files.
	b.install.inputs = append(b.install.inputs, values.Paths...)
	for _, rel := range values.ValuesFiles {
		if helmValuesFileSetsArgoCDSettings(b.memo, b.root, rel) {
			b.install.valuesFiles = append(b.install.valuesFiles, rel)
		}
	}
	for _, inline := range values.ValuesInline {
		if helmValuesSetArgoCDSettings(inline) {
			b.install.inline = true
		}
	}
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

// settingsInstallHelmValueFile returns the repository path of a Helm value
// file that the render reads from this repository root. A $ref value file
// is relative to the root of the referenced source, and any other value
// file is relative to the source path of a local chart. A remote value
// file, a value file of a chart from a chart repository, and a glob have no
// single local path.
func settingsInstallHelmValueFile(provider localProvider, plan PlanResult, sourcePath string, local bool, valueFile string) (string, bool) {
	if strings.ContainsAny(valueFile, "*?[") || strings.Contains(valueFile, "://") {
		return "", false
	}
	if strings.HasPrefix(valueFile, "$") {
		refKey, refPath, ok := splitHelmValueFileRef(valueFile)
		if !ok {
			return "", false
		}
		refPlan, ok := plan.Refs[refKey]
		if !ok || !provider.sourceResolvesToRepoRoot(settingsInstallResolvedSource(refPlan.Source)) {
			return "", false
		}
		return cleanSelectionRelativePath(refPath)
	}
	if !local {
		return "", false
	}
	return cleanSelectionRelativePath(path.Join(sourcePath, valueFile))
}

// helmValuesFileSetsArgoCDSettings reads a local Helm values file as Helm
// does and reports whether it sets Argo CD settings. A file that is missing,
// is not a regular file, or does not decode sets none.
func helmValuesFileSetsArgoCDSettings(memo *sync.Map, root, rel string) bool {
	full := filepath.Join(root, filepath.FromSlash(rel))
	key := "values\x00" + full
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
			var values map[string]any
			if err := sigsyaml.Unmarshal(data, &values); err == nil {
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
		if manifest.Object != nil && isSettingsConfigMapObject(manifest.Object) {
			out = append(out, manifest)
		}
	}
	return out
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

// markSettingsRenderInputs records inputs as the RenderInputs of every
// settings ConfigMap in result.
func markSettingsRenderInputs(result *discovery.Result, inputs []string) {
	for i := range result.SettingsCandidates {
		if discovery.IsSettingsConfigMapKind(result.SettingsCandidates[i].Kind) {
			result.SettingsCandidates[i].RenderInputs = append([]string(nil), inputs...)
		}
	}
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
// an Application whose Helm values set Argo CD settings. drydock then reads
// the settings from the values files on their own, as it does for a values
// file of an install it does not render.
func settingsInstallRenderFailedDiagnostic(appFile discovery.ApplicationFile, install settingsInstall, renderErr error) diagnostic.Diagnostic {
	name := applicationDisplayName(appFile.Application)
	var message string
	provenance := diagnostic.Provenance{Path: filepath.ToSlash(appFile.Path)}
	if len(install.valuesFiles) > 0 {
		message = fmt.Sprintf("cannot render Application %s to read the Argo CD settings that its Helm values set; drydock reads the settings from each values file on its own instead, without the chart defaults: %v", name, renderErr)
		provenance = diagnostic.Provenance{Path: install.valuesFiles[0]}
	} else {
		message = fmt.Sprintf("cannot render Application %s to read the Argo CD settings that its inline Helm values set; drydock does not read these settings: %v", name, renderErr)
	}
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

// renderedSettingsInputs returns the RenderInputs of every rendered settings
// ConfigMap in discovered.
func renderedSettingsInputs(candidates []discovery.SettingsCandidate) []string {
	var inputs []string
	for _, candidate := range candidates {
		if candidate.Tier == discovery.SourceTierStatic || candidate.Tier == discovery.SourceTierExternalManifest {
			continue
		}
		inputs = append(inputs, candidate.RenderInputs...)
	}
	return inputs
}

// supersededBySettingsRender reports whether a committed settings file is an
// input of a rendered settings ConfigMap: a Helm values file or a
// ConfigMap at or under one of inputs. The rendered ConfigMap is what the
// cluster gets from that file, so it replaces the file.
func supersededBySettingsRender(candidate discovery.SettingsCandidate, inputs []string) bool {
	if candidate.Tier != discovery.SourceTierStatic || len(inputs) == 0 {
		return false
	}
	if candidate.Kind != "argocd-values" && !discovery.IsSettingsConfigMapKind(candidate.Kind) {
		return false
	}
	for _, input := range inputs {
		if pathUnderRoot(candidate.Path, input) {
			return true
		}
	}
	return false
}
