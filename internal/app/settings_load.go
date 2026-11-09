package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

// loadSettingsFromDiscovery loads and merges the discovered settings
// candidates. A committed values file or settings ConfigMap that a rendered
// settings ConfigMap read (RenderInputs) is left out: Helm merged it with the
// other values of its release and the chart defaults, and the rendered
// ConfigMap holds that result.
//
// scope holds the --argocd-namespace flag. It decides the controller
// namespace, and only with it do the settings objects and the AppProjects
// in other namespaces not count (settingsCandidatesInNamespace,
// projectInControllerNamespace); drydock warns about each. Without it, the
// argocd-cmd-params-cm ConfigMaps decide the namespace
// (config.ArgoSettings.ControllerNamespace), and every object counts except
// the settings ConfigMaps of an Argo CD install on this cluster that land in
// another namespace than the controller namespace, as in earlier releases,
// and those of another install (otherInstallSettings). Either way, every
// argocd-cmd-params-cm that reaches this cluster names a namespace
// (commandParametersNamespaces), and drydock warns when they do not agree
// with the namespace that it uses.
func loadSettingsFromDiscovery(root string, scope controllerNamespaceScope, discovered discovery.Result) (config.ArgoSettings, []diagnostic.Diagnostic, error) {
	settings, _, diags, err := loadSettingsWithLanding(root, scope, discovered)
	return settings, diags, err
}

// loadSettingsWithLanding is loadSettingsFromDiscovery that also returns
// the landing namespaces that the --argocd-namespace filter used, or nil
// without the flag, so the AppProject filter uses the same ones.
func loadSettingsWithLanding(root string, scope controllerNamespaceScope, discovered discovery.Result) (config.ArgoSettings, landingFunc, []diagnostic.Diagnostic, error) {
	active := activeSettingsCandidates(discovered.SettingsCandidates)
	loaded, loadErrors := loadSettingsCandidates(root, active)
	walker, landing, scoped, skip, diags := scopeSettingsCandidates(root, scope, discovered, active, loaded, loadErrors)
	if err := firstLoadError(scoped, loadErrors); err != nil {
		return config.DefaultSettings(), nil, diags, err
	}
	merged, mergeDiags := mergeLoadedSettings(loaded, scope, skip)
	found := commandParametersNamespaces(walker, active)
	other := otherInstallSettings(scoped, settingsWithNamedNamespaces(merged, found).ControllerNamespace(""))
	for key := range found.other {
		other[key] = struct{}{}
	}
	if len(other) > 0 {
		for key := range other {
			skip[key] = struct{}{}
		}
		merged, mergeDiags = mergeLoadedSettings(loaded, scope, skip)
	}
	diags = append(diags, mergeDiags...)
	merged = settingsWithNamedNamespaces(merged, found)
	diags = append(diags, controllerNamespaceDiagnostics(found, merged)...)
	diags = append(diags, projectNamespaceDiagnostics(discovered.Projects, merged, landing)...)
	// The repo-server rules read the argocd-cm candidates too, so they see
	// the same ones.
	inNamespace := discovered
	inNamespace.SettingsCandidates, _ = settingsCandidatesInNamespace(discovered.SettingsCandidates, scope.flag, landing, nil)
	inNamespace.SettingsCandidates = withoutSettingsCandidates(inNamespace.SettingsCandidates, other)
	workloads := collectWorkloadCandidates(root, inNamespace, merged, scope.renders)
	namespace, namespaceDiags := repoServerHelmNamespace(workloads)
	merged.RepoServerHelmNamespace = namespace
	diags = append(diags, namespaceDiags...)
	diags = append(diags, applyComponentWorkloadSettings(root, workloads, &merged)...)
	return merged, landing, diags, nil
}

// scopeSettingsCandidates applies the --argocd-namespace filter to active
// and returns the walker that decides where committed objects land, the
// landing function of the filter (nil without the flag), the candidates
// that stay, the load keys of the ones that do not, and the skip warnings.
//
// The walker resolves destinations and source types with settings, which
// the filter decides. It starts from the candidates that the flag cannot
// skip: those whose own or rendered namespace is the flag namespace or
// empty. When the settings of the candidates that stay give other source
// type gates or clusters, it decides once more with those.
func scopeSettingsCandidates(root string, scope controllerNamespaceScope, discovered discovery.Result, active []discovery.SettingsCandidate, loaded []loadedSettingsCandidate, loadErrors map[string]error) (*committedFileWalker, landingFunc, []discovery.SettingsCandidate, map[string]struct{}, []diagnostic.Diagnostic) {
	if scope.flag == "" {
		all, _ := mergeLoadedSettings(loaded, scope, nil)
		walker := newCommittedFileWalker(scope.walkerProvider(root), discovered.Applications, all, scope.renders)
		return walker, nil, active, map[string]struct{}{}, nil
	}
	base, _ := mergeLoadedSettings(loaded, scope, outsideFlagNamespace(active, scope.flag))
	walker := newCommittedFileWalker(scope.walkerProvider(root), discovered.Applications, base, scope.renders)
	scoped, diags := settingsCandidatesInNamespace(active, scope.flag, walker.committedLandings, loadErrors)
	skip := withoutKeys(active, scoped)
	if kept, _ := mergeLoadedSettings(loaded, scope, skip); walkerSettingsKey(kept) != walkerSettingsKey(base) {
		walker = newCommittedFileWalker(scope.walkerProvider(root), discovered.Applications, kept, scope.renders)
		scoped, diags = settingsCandidatesInNamespace(active, scope.flag, walker.committedLandings, loadErrors)
		skip = withoutKeys(active, scoped)
	}
	return walker, walker.committedLandings, scoped, skip, diags
}

// outsideFlagNamespace returns the load keys of the candidates whose own or
// rendered namespace is set and is not namespace.
func outsideFlagNamespace(candidates []discovery.SettingsCandidate, namespace string) map[string]struct{} {
	skip := map[string]struct{}{}
	for _, candidate := range candidates {
		if own := candidateNamespace(candidate); controllerNamespaceScoped(candidate.Kind) && own != "" && own != namespace {
			skip[settingsCandidateLoadKey(candidate)] = struct{}{}
		}
	}
	return skip
}

// walkerSettingsKey is what a committedFileWalker reads from settings: the
// source type gates and the clusters that resolve destinations.
func walkerSettingsKey(settings config.ArgoSettings) string {
	data, err := json.Marshal(struct {
		Clusters  map[string]config.ClusterSettings `json:"clusters"`
		InCluster config.Value[bool]                `json:"inCluster"`
	}{settings.Clusters, settings.InClusterEnabled})
	if err != nil {
		// These settings always marshal. An error gives the source type
		// gates alone.
		return fmt.Sprintf("%+v", sourceTypeGatesFromSettings(settings))
	}
	return fmt.Sprintf("%+v\x00%s", sourceTypeGatesFromSettings(settings), data)
}

// firstLoadError returns the load error of the first candidate that stays.
func firstLoadError(candidates []discovery.SettingsCandidate, loadErrors map[string]error) error {
	for _, candidate := range candidates {
		if err := loadErrors[settingsCandidateLoadKey(candidate)]; err != nil {
			return err
		}
	}
	return nil
}

// loadedSettingsCandidate is a settings candidate with the settings that it
// holds and the diagnostics of reading them.
type loadedSettingsCandidate struct {
	key      string
	settings config.ArgoSettings
	diags    []diagnostic.Diagnostic
}

// loadSettingsCandidates loads every candidate. A candidate that does not
// load is in loadErrors: it fails the settings only when the
// --argocd-namespace filter keeps it.
func loadSettingsCandidates(root string, candidates []discovery.SettingsCandidate) ([]loadedSettingsCandidate, map[string]error) {
	var loaded []loadedSettingsCandidate
	loadErrors := map[string]error{}
	for _, candidate := range candidates {
		settings, diags, handled, err := loadSettingsCandidate(root, candidate)
		if !handled {
			continue
		}
		if err != nil {
			loadErrors[settingsCandidateLoadKey(candidate)] = err
			continue
		}
		loaded = append(loaded, loadedSettingsCandidate{key: settingsCandidateLoadKey(candidate), settings: settings, diags: diags})
	}
	return loaded, loadErrors
}

func settingsCandidateLoadKey(candidate discovery.SettingsCandidate) string {
	return fmt.Sprintf("%s\x00%s\x00%d", candidate.Kind, candidate.Path, candidate.DocumentIndex)
}

// mergeLoadedSettings merges the loaded candidates, without the ones whose
// keys skip holds, with the diagnostics of reading and merging them.
func mergeLoadedSettings(loaded []loadedSettingsCandidate, scope controllerNamespaceScope, skip map[string]struct{}) (config.ArgoSettings, []diagnostic.Diagnostic) {
	var candidates []config.ArgoSettings
	var diags []diagnostic.Diagnostic
	for _, item := range loaded {
		if _, skipped := skip[item.key]; skipped {
			continue
		}
		candidates = append(candidates, item.settings)
		diags = append(diags, item.diags...)
	}
	merged, mergeDiags := config.MergeDiscovered(candidates)
	return scope.apply(merged), append(diags, mergeDiags...)
}

// settingsWithNamedNamespaces returns settings with the namespaces that the
// argocd-cmd-params-cm ConfigMaps name (commandParametersNamespaces) in
// place of the ones that the merge collected.
func settingsWithNamedNamespaces(settings config.ArgoSettings, found namedNamespaces) config.ArgoSettings {
	settings.CommandParametersNamespace = ""
	settings.CommandParametersNamespaces = nil
	for namespace := range found.named {
		settings.CommandParametersNamespaces = append(settings.CommandParametersNamespaces, namespace)
	}
	slices.Sort(settings.CommandParametersNamespaces)
	return settings
}

// otherInstallSettings returns the load keys of the settings ConfigMaps
// of an Argo CD install on this cluster that land in another namespace
// than controllerNamespace (SettingsCandidate.InstallNamespace). Such an
// install runs another Argo CD. commandParametersNamespaces finds the
// settings ConfigMaps of the installs that do not reconcile the
// Application that renders them.
func otherInstallSettings(candidates []discovery.SettingsCandidate, controllerNamespace string) map[string]struct{} {
	other := map[string]struct{}{}
	for _, candidate := range candidates {
		if !discovery.IsSettingsConfigMapKind(candidate.Kind) || candidate.InstallNamespace == "" {
			continue
		}
		if candidate.InstallNamespace != controllerNamespace {
			other[settingsCandidateLoadKey(candidate)] = struct{}{}
		}
	}
	return other
}

// withoutKeys returns the load keys of the candidates in all that kept
// does not hold.
func withoutKeys(all, kept []discovery.SettingsCandidate) map[string]struct{} {
	keep := make(map[string]struct{}, len(kept))
	for _, candidate := range kept {
		keep[settingsCandidateLoadKey(candidate)] = struct{}{}
	}
	skip := map[string]struct{}{}
	for _, candidate := range all {
		if _, ok := keep[settingsCandidateLoadKey(candidate)]; !ok {
			skip[settingsCandidateLoadKey(candidate)] = struct{}{}
		}
	}
	return skip
}

func withoutSettingsCandidates(candidates []discovery.SettingsCandidate, skip map[string]struct{}) []discovery.SettingsCandidate {
	if len(skip) == 0 {
		return candidates
	}
	kept := candidates[:0:0]
	for _, candidate := range candidates {
		if _, skipped := skip[settingsCandidateLoadKey(candidate)]; !skipped {
			kept = append(kept, candidate)
		}
	}
	return kept
}

// activeSettingsCandidates returns the settings candidates that are
// settings sources of their own, once each: not a committed file that a
// rendered settings ConfigMap read (supersededBySettingsRender).
func activeSettingsCandidates(all []discovery.SettingsCandidate) []discovery.SettingsCandidate {
	var active []discovery.SettingsCandidate
	seen := make(map[string]struct{}, len(all))
	renderInputs := renderedSettingsInputs(all)
	for _, candidate := range all {
		if supersededBySettingsRender(candidate, renderInputs) {
			continue
		}
		key := settingsCandidateLoadKey(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		active = append(active, candidate)
	}
	return active
}

func loadSettingsCandidate(root string, candidate discovery.SettingsCandidate) (config.ArgoSettings, []diagnostic.Diagnostic, bool, error) {
	path := filepath.Join(root, candidate.Path)
	switch candidate.Kind {
	case "argocd-cm":
		if candidate.Object != nil {
			settings, diags, err := config.LoadFromConfigMapObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadFromConfigMapDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "argocd-cmd-params-cm":
		if candidate.Object != nil {
			settings, diags, err := config.LoadCommandParametersConfigMapObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadCommandParametersConfigMapDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "argocd-cmp-cm":
		if candidate.Object != nil {
			settings, diags, err := config.LoadConfigManagementPluginConfigMapObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadConfigManagementPluginConfigMapDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "argocd-values":
		if candidate.Object != nil {
			settings, diags, err := config.LoadFromHelmValuesObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadFromHelmValuesDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "repository-secret":
		if candidate.Object != nil {
			settings, diags, err := config.LoadRepositorySecretObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadRepositorySecretDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	case "cluster-secret":
		if candidate.Object != nil {
			settings, diags, err := config.LoadClusterSecretObject(candidate.Path, candidate.Object)
			return settings, diags, true, err
		}
		settings, diags, err := config.LoadClusterSecretDocument(path, candidate.DocumentIndex)
		return settings, diags, true, err
	default:
		return config.ArgoSettings{}, nil, false, nil
	}
}
