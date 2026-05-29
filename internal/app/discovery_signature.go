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
		KustomizeBuildOptions    []config.Value[string]                 `json:"kustomizeBuildOptions,omitempty"`
		HelmRepositories         map[string]config.RepositorySettings   `json:"helmRepositories,omitempty"`
		HelmValuesFileSchemes    []config.Value[string]                 `json:"helmValuesFileSchemes,omitempty"`
		HelmValuesFileSchemesSet bool                                   `json:"helmValuesFileSchemesSet,omitempty"`
		TrackingMethod           config.Value[string]                   `json:"trackingMethod,omitempty"`
		InstanceLabelKey         config.Value[string]                   `json:"instanceLabelKey,omitempty"`
		InstallationID           config.Value[string]                   `json:"installationID,omitempty"`
		ConfigManagementPlugins  map[string]renderSettingsPlugin        `json:"configManagementPlugins,omitempty"`
		ResourceCustomizations   map[string]renderSettingsCustomization `json:"resourceCustomizations,omitempty"`
	}{
		KustomizeBuildOptions:    settings.KustomizeBuildOptions,
		HelmRepositories:         settings.HelmRepositories,
		HelmValuesFileSchemes:    settings.HelmValuesFileSchemes,
		HelmValuesFileSchemesSet: settings.HelmValuesFileSchemesSet,
		TrackingMethod:           settings.TrackingMethod,
		InstanceLabelKey:         settings.InstanceLabelKey,
		InstallationID:           settings.InstallationID,
		ConfigManagementPlugins:  plugins,
		ResourceCustomizations:   renderSettingsCustomizations(settings.ResourceCustomizations),
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("fingerprint render-affecting Argo CD settings: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type renderSettingsPlugin struct {
	Name            string   `json:"name,omitempty"`
	Version         string   `json:"version,omitempty"`
	GenerateCommand []string `json:"generateCommand,omitempty"`
	GenerateArgs    []string `json:"generateArgs,omitempty"`
	HasInit         bool     `json:"hasInit,omitempty"`
}

type renderSettingsCustomization struct {
	HasHealthLua    bool   `json:"hasHealthLua,omitempty"`
	HealthLuaSHA256 string `json:"healthLuaSHA256,omitempty"`
	HasUseOpenLibs  bool   `json:"hasUseOpenLibs,omitempty"`
	UseOpenLibs     bool   `json:"useOpenLibs,omitempty"`
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
