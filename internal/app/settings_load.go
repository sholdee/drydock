package app

import (
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
// argocdNamespace is the --argocd-namespace flag. It decides the controller
// namespace, and only with it do the settings objects and the AppProjects
// in other namespaces not count (settingsCandidatesInNamespace,
// projectInControllerNamespace); drydock warns about each. Without it, the
// argocd-cmd-params-cm ConfigMaps decide the namespace
// (config.ArgoSettings.ControllerNamespace), and every object counts except
// the settings ConfigMaps that an Argo CD install renders in another
// namespace, as in earlier releases (otherInstallSettings). Either way,
// every argocd-cmd-params-cm that reaches this cluster names a namespace
// (commandParametersNamespaces), and drydock warns when they do not agree
// with the namespace that it uses.
func loadSettingsFromDiscovery(root, argocdNamespace string, discovered discovery.Result) (config.ArgoSettings, []diagnostic.Diagnostic, error) {
	active := activeSettingsCandidates(discovered.SettingsCandidates)
	scoped, diags := settingsCandidatesInNamespace(active, argocdNamespace)
	loaded, err := loadSettingsCandidates(root, scoped)
	if err != nil {
		return config.DefaultSettings(), diags, err
	}
	merged, mergeDiags := mergeLoadedSettings(loaded, argocdNamespace, nil)
	named := commandParametersNamespaces(root, active, discovered.Applications, merged)
	other := otherInstallSettings(scoped, settingsWithNamedNamespaces(merged, named).ControllerNamespace(""))
	if len(other) > 0 {
		merged, mergeDiags = mergeLoadedSettings(loaded, argocdNamespace, other)
	}
	diags = append(diags, mergeDiags...)
	merged = settingsWithNamedNamespaces(merged, named)
	diags = append(diags, controllerNamespaceDiagnostics(named, merged)...)
	diags = append(diags, projectNamespaceDiagnostics(discovered.Projects, merged)...)
	// The repo-server rules read the argocd-cm candidates too, so they see
	// the same ones.
	inNamespace := discovered
	inNamespace.SettingsCandidates, _ = settingsCandidatesInNamespace(discovered.SettingsCandidates, argocdNamespace)
	inNamespace.SettingsCandidates = withoutSettingsCandidates(inNamespace.SettingsCandidates, other)
	namespace, namespaceDiags := repoServerHelmNamespace(root, inNamespace, merged)
	merged.RepoServerHelmNamespace = namespace
	diags = append(diags, namespaceDiags...)
	return merged, diags, nil
}

// loadedSettingsCandidate is a settings candidate with the settings that it
// holds and the diagnostics of reading them.
type loadedSettingsCandidate struct {
	key      string
	settings config.ArgoSettings
	diags    []diagnostic.Diagnostic
}

func loadSettingsCandidates(root string, candidates []discovery.SettingsCandidate) ([]loadedSettingsCandidate, error) {
	var loaded []loadedSettingsCandidate
	for _, candidate := range candidates {
		settings, diags, handled, err := loadSettingsCandidate(root, candidate)
		if !handled {
			continue
		}
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, loadedSettingsCandidate{key: settingsCandidateLoadKey(candidate), settings: settings, diags: diags})
	}
	return loaded, nil
}

func settingsCandidateLoadKey(candidate discovery.SettingsCandidate) string {
	return fmt.Sprintf("%s\x00%s\x00%d", candidate.Kind, candidate.Path, candidate.DocumentIndex)
}

// mergeLoadedSettings merges the loaded candidates, without the ones whose
// keys skip holds, with the diagnostics of reading and merging them.
func mergeLoadedSettings(loaded []loadedSettingsCandidate, argocdNamespace string, skip map[string]struct{}) (config.ArgoSettings, []diagnostic.Diagnostic) {
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
	merged.ArgoCDNamespace = argocdNamespace
	return merged, append(diags, mergeDiags...)
}

// settingsWithNamedNamespaces returns settings with the namespaces that the
// argocd-cmd-params-cm ConfigMaps name (commandParametersNamespaces) in
// place of the ones that the merge collected.
func settingsWithNamedNamespaces(settings config.ArgoSettings, named map[string][]string) config.ArgoSettings {
	settings.CommandParametersNamespace = ""
	settings.CommandParametersNamespaces = nil
	for namespace := range named {
		settings.CommandParametersNamespaces = append(settings.CommandParametersNamespaces, namespace)
	}
	slices.Sort(settings.CommandParametersNamespaces)
	return settings
}

// otherInstallSettings returns the load keys of the settings ConfigMaps
// that an Argo CD install on this cluster renders in another namespace than
// controllerNamespace (SettingsCandidate.ArgoCDInstall). That install runs
// another Argo CD, so they are not settings of this one. A ConfigMap
// without a namespace stays.
func otherInstallSettings(candidates []discovery.SettingsCandidate, controllerNamespace string) map[string]struct{} {
	other := map[string]struct{}{}
	for _, candidate := range candidates {
		if !candidate.ArgoCDInstall || !discovery.IsSettingsConfigMapKind(candidate.Kind) {
			continue
		}
		if namespace := candidateNamespace(candidate); namespace != "" && namespace != controllerNamespace {
			other[settingsCandidateLoadKey(candidate)] = struct{}{}
		}
	}
	return other
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
