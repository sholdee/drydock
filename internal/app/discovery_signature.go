package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/discovery"
)

func settingsSignature(settings config.ArgoSettings) (string, error) {
	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("fingerprint Argo CD settings: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func renderSettingsSignature(settings config.ArgoSettings) (string, error) {
	plugins := make(map[string]renderSettingsPlugin, len(settings.ConfigManagementPlugins))
	for key, plugin := range settings.ConfigManagementPlugins {
		plugins[key] = renderSettingsPlugin{
			Name:            plugin.Name,
			Version:         plugin.Version,
			GenerateCommand: append([]string(nil), plugin.GenerateCommand...),
			GenerateArgs:    append([]string(nil), plugin.GenerateArgs...),
			HasInit:         plugin.HasInit,
		}
	}
	input := struct {
		KustomizeBuildOptions []string `json:"kustomizeBuildOptions,omitempty"`
		// KustomizeVersions decides whether a source that names a
		// kustomize version fails, so it is a render input. Without a found
		// argocd-cm, every name counts as registered, so
		// ArgoCDConfigMapFound is a render input too. Without it, settings
		// without an argocd-cm and an argocd-cm with "--enable-helm" and no
		// versions would share a signature.
		KustomizeVersions        []string                               `json:"kustomizeVersions,omitempty"`
		ArgoCDConfigMapFound     bool                                   `json:"argoCDConfigMapFound,omitempty"`
		HelmRepositories         map[string]renderSettingsRepository    `json:"helmRepositories,omitempty"`
		HelmValuesFileSchemes    []string                               `json:"helmValuesFileSchemes,omitempty"`
		HelmValuesFileSchemesSet bool                                   `json:"helmValuesFileSchemesSet,omitempty"`
		TrackingMethod           string                                 `json:"trackingMethod,omitempty"`
		InstanceLabelKey         string                                 `json:"instanceLabelKey,omitempty"`
		InstallationID           string                                 `json:"installationID,omitempty"`
		ConfigManagementPlugins  map[string]renderSettingsPlugin        `json:"configManagementPlugins,omitempty"`
		ResourceCustomizations   map[string]renderSettingsCustomization `json:"resourceCustomizations,omitempty"`
		// MaxCombinedDirectoryManifestsSize decides whether a directory
		// source fails, so it is a render input.
		MaxCombinedDirectoryManifestsSize string `json:"maxCombinedDirectoryManifestsSize"`
		// AllowOutOfBoundsSymlinks decides whether a source fails while its
		// repository holds an out-of-bounds symlink, so it is a render
		// input. It is signed as the value the repo-server parses.
		AllowOutOfBoundsSymlinks bool `json:"allowOutOfBoundsSymlinks,omitempty"`
		// DisabledSourceTypes lists the argocd-cm keys helm.enable,
		// kustomize.enable, and jsonnet.enable that turn their tool off.
		// They change the renderer of a source or the files a directory
		// source evaluates, so they are render inputs. They are signed as
		// Argo CD reads them, and the defaults leave the list empty, so the
		// signature of settings without the keys does not change.
		DisabledSourceTypes []string `json:"disabledSourceTypes,omitempty"`
		// HelmNamespace is the namespace that helm uses without a
		// --namespace, from the repo-server of a rendered or committed
		// install. It is left out for "default", so settings without such
		// an install keep their signature.
		HelmNamespace string `json:"helmNamespace,omitempty"`
	}{
		// The words that reach kustomize build. Without a found argocd-cm
		// they are "--enable-helm", which renders as a found argocd-cm
		// with that value renders.
		KustomizeBuildOptions:             settings.EffectiveKustomizeBuildOptions(),
		KustomizeVersions:                 settings.KustomizeVersionNames(),
		ArgoCDConfigMapFound:              settings.ArgoCDConfigMapFound,
		HelmRepositories:                  renderSettingsRepositories(settings.HelmRepositories),
		HelmValuesFileSchemes:             renderSettingsValues(settings.HelmValuesFileSchemes),
		HelmValuesFileSchemesSet:          settings.HelmValuesFileSchemesSet,
		TrackingMethod:                    settings.TrackingMethod.Value,
		InstanceLabelKey:                  settings.InstanceLabelKey.Value,
		InstallationID:                    settings.InstallationID.Value,
		ConfigManagementPlugins:           plugins,
		ResourceCustomizations:            renderSettingsCustomizations(settings.ResourceCustomizations),
		MaxCombinedDirectoryManifestsSize: maxCombinedDirectoryManifestsSizeSignature(settings),
		AllowOutOfBoundsSymlinks:          allowOutOfBoundsSymlinks(settings),
		DisabledSourceTypes:               settings.DisabledSourceTypeKeys(),
		HelmNamespace:                     helmNamespaceSignature(settings),
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("fingerprint render-affecting Argo CD settings: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// changedOnlySettingsSignature fingerprints every Argo CD setting a diff side
// resolved, for changed-only selection (classifySettingsChange): a settings
// change can reach Applications other than the one that owns the file that
// carries it. It covers the render settings (renderSettingsSignature,
// including the plugin generate commands that JSON leaves out), plugin
// discovery rules, and every field the settings serialize, so a setting
// renderSettingsSignature does not need but a diff reads — resource filters,
// compare options, ignoreDifferences, clusters — or one added later is never
// missed. Command parameters are left out as renderSettingsSignature leaves
// them out: only diag reads them. The jq timeout of
// controller.ignore.normalizer.jq.timeout is signed as the parsed duration:
// it can change the diff of any Application with a jq ignore rule, but it
// changes no render, so it stays out of renderSettingsSignature and the
// render cache keys. Provenance is dropped: it names the side's own tree, so
// identical settings in two trees would otherwise always differ.
func changedOnlySettingsSignature(settings config.ArgoSettings) (string, error) {
	renderSig, err := renderSettingsSignature(settings)
	if err != nil {
		return "", err
	}
	settings.CommandParameters = nil
	settings.HelmValuesFileSchemesSource = config.Provenance{}
	// renderSig carries the parsed size limit, so a new spelling of the same
	// limit is not a change.
	settings.MaxCombinedDirectoryManifestsSize = config.Value[string]{}
	// renderSig carries the parsed allow flag, so "TRUE" and "true", or ""
	// and "no", are not a change.
	settings.AllowOutOfBoundsSymlinks = config.Value[string]{}
	// renderSig carries the source type keys as Argo CD reads them, so ""
	// and "true" are not a change.
	settings.SourceTypeEnable = nil
	// renderSig carries the namespace that helm uses.
	settings.RepoServerHelmNamespace = nil
	// The progressive syncs flag changes ApplicationSet generation, not
	// renders, so it is signed here as the value the controller parses.
	progressiveSyncs := config.ParseEnableProgressiveSyncs(settings.EnableProgressiveSyncs.Value)
	settings.EnableProgressiveSyncs = config.Value[string]{}
	// The Git file globbing and hidden directory flags change
	// ApplicationSet generation too, so they are signed the same way.
	generation := appsetGenerationSettingsFrom(settings)
	settings.EnableNewGitFileGlobbing = config.Value[string]{}
	settings.IncludeHiddenDirectories = config.Value[string]{}
	// Signed below as the parsed duration, so "60s" and "1m" are not a
	// change.
	jqTimeout := config.ParseIgnoreNormalizerJQTimeout(settings.IgnoreNormalizerJQTimeout.Value)
	settings.IgnoreNormalizerJQTimeout = config.Value[string]{}
	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("fingerprint Argo CD settings: %w", err)
	}
	var fields any
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", fmt.Errorf("fingerprint Argo CD settings: %w", err)
	}
	discover := make(map[string]config.ConfigManagementPluginDiscovery, len(settings.ConfigManagementPlugins))
	for key, plugin := range settings.ConfigManagementPlugins {
		discover[key] = plugin.Discover
	}
	signed := []any{renderSig, withoutProvenance(fields), discover, progressiveSyncs, jqTimeout}
	// Appended only when set, so the signature of settings without them
	// does not change.
	if generation.NewGitFileGlobbing || generation.IncludeHiddenDirectories {
		signed = append(signed, generation.NewGitFileGlobbing, generation.IncludeHiddenDirectories)
	}
	data, err = json.Marshal(signed)
	if err != nil {
		return "", fmt.Errorf("fingerprint Argo CD settings: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// withoutProvenance removes, in place, every "provenance" field of decoded
// settings JSON. Only a provenance-shaped value (path and pointer) is
// removed, so a map entry that happens to use the key keeps counting.
func withoutProvenance(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if provenance, ok := typed["provenance"].(map[string]any); ok && provenanceShaped(provenance) {
			delete(typed, "provenance")
		}
		for key, item := range typed {
			typed[key] = withoutProvenance(item)
		}
	case []any:
		for i, item := range typed {
			typed[i] = withoutProvenance(item)
		}
	}
	return value
}

func provenanceShaped(value map[string]any) bool {
	for key := range value {
		if key != "path" && key != "pointer" {
			return false
		}
	}
	return true
}

type renderSettingsPlugin struct {
	Name            string   `json:"name,omitempty"`
	Version         string   `json:"version,omitempty"`
	GenerateCommand []string `json:"generateCommand,omitempty"`
	GenerateArgs    []string `json:"generateArgs,omitempty"`
	HasInit         bool     `json:"hasInit,omitempty"`
}

type renderSettingsRepository struct {
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"`
	URL       string `json:"url,omitempty"`
	EnableOCI bool   `json:"enableOCI,omitempty"`
	Project   string `json:"project,omitempty"`
}

type renderSettingsCustomization struct {
	HasHealthLua    bool   `json:"hasHealthLua,omitempty"`
	HealthLuaSHA256 string `json:"healthLuaSHA256,omitempty"`
	HasUseOpenLibs  bool   `json:"hasUseOpenLibs,omitempty"`
	UseOpenLibs     bool   `json:"useOpenLibs,omitempty"`
}

func renderSettingsValues[T comparable](input []config.Value[T]) []T {
	if len(input) == 0 {
		return nil
	}
	out := make([]T, 0, len(input))
	for _, value := range input {
		out = append(out, value.Value)
	}
	return out
}

func renderSettingsRepositories(input map[string]config.RepositorySettings) map[string]renderSettingsRepository {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]renderSettingsRepository, len(input))
	for key, repository := range input {
		out[key] = renderSettingsRepository{
			Name:      repository.Name,
			Type:      repository.Type,
			URL:       repository.URL,
			EnableOCI: repository.EnableOCI,
			Project:   repository.Project,
		}
	}
	return out
}

func renderSettingsCustomizations(input map[string]config.ResourceCustomization) map[string]renderSettingsCustomization {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]renderSettingsCustomization, len(input))
	for key, value := range input {
		out[key] = renderSettingsCustomization{
			HasHealthLua:    value.HasHealthLua,
			HealthLuaSHA256: value.HealthLuaSHA256,
			HasUseOpenLibs:  value.HasUseOpenLibs,
			UseOpenLibs:     value.UseOpenLibs,
		}
	}
	return out
}

func discoveryFingerprint(result discovery.Result, settingsSig string) (string, error) {
	parts := []any{
		settingsSig,
		discoveryApplicationsFingerprint(result.Applications),
		discoveryApplicationSetsFingerprint(result.ApplicationSets),
		discoveryProjectsFingerprint(result.Projects),
		discoverySettingsFingerprint(result.SettingsCandidates),
	}
	data, err := json.Marshal(parts)
	if err != nil {
		return "", fmt.Errorf("fingerprint discovery result: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func discoveryApplicationsFingerprint(items []discovery.ApplicationFile) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, objectContentFingerprint(applicationDiscoveryKey(item.Application), item.Application))
	}
	sort.Strings(out)
	return out
}

func discoveryApplicationSetsFingerprint(items []discovery.ApplicationSetFile) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, objectContentFingerprint(applicationSetDiscoveryKey(item.ApplicationSet), item.ApplicationSet))
	}
	sort.Strings(out)
	return out
}

func discoveryProjectsFingerprint(items []discovery.ProjectFile) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, objectContentFingerprint(projectDiscoveryKey(item.Project), item.Project))
	}
	sort.Strings(out)
	return out
}

func discoverySettingsFingerprint(items []discovery.SettingsCandidate) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		key := settingsDiscoveryKey(item)
		if item.Object != nil {
			out = append(out, objectContentFingerprint(key, item.Object.Object))
			continue
		}
		out = append(out, fmt.Sprintf("%s:%s:%d:%s", key, item.Path, item.DocumentIndex, item.Kind))
	}
	sort.Strings(out)
	return out
}

func objectContentFingerprint(key string, value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return key + ":[unfingerprintable]"
	}
	sum := sha256.Sum256(data)
	return key + ":" + hex.EncodeToString(sum[:])
}

func helmNamespaceSignature(settings config.ArgoSettings) string {
	if namespace := settings.HelmNamespace(); namespace != config.DefaultHelmNamespace {
		return namespace
	}
	return ""
}
