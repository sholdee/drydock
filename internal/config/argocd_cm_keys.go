package config

import "strings"

// argoCDConfigMapSettingKeys are the argocd-cm keys that applyCMMap reads.
// TestArgoCDConfigMapSettingKeysListEveryKeyApplyCMMapReads keeps this list in
// step with provider_cm.go.
var argoCDConfigMapSettingKeys = map[string]struct{}{
	"application.instanceLabelKey":          {},
	"application.resourceTrackingMethod":    {},
	"cluster.inClusterEnabled":              {},
	"helm.valuesFileSchemes":                {},
	"installationID":                        {},
	"kustomize.buildOptions":                {},
	"resource.compareoptions":               {},
	"resource.exclusions":                   {},
	"resource.ignoreResourceUpdatesEnabled": {},
	"resource.inclusions":                   {},
	"resource.selectors":                    {},
}

// IsArgoCDConfigMapSettingKey reports whether drydock reads key from
// argocd-cm. Argo CD v3.6.1 reads every key that starts with
// resource.customizations, with no final dot, as resource customization
// settings (util/settings/settings.go:1224). That prefix also covers the
// resource.customizations block key itself.
func IsArgoCDConfigMapSettingKey(key string) bool {
	if _, ok := argoCDConfigMapSettingKeys[key]; ok {
		return true
	}
	return strings.HasPrefix(key, "resource.customizations")
}

// IsArgoCDHelmValuesConfigMapKey reports whether a key under the argo-cd
// chart's configs.cm changes the settings drydock reads: an argocd-cm key, or
// resourceExclusionsAdditional, which the chart adds to resource.exclusions.
func IsArgoCDHelmValuesConfigMapKey(key string) bool {
	return key == helmCMResourceExclusionsAdditionalKey || IsArgoCDConfigMapSettingKey(key)
}
