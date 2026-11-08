package config

import (
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func applyCMMap(settings *ArgoSettings, values map[string]string, path, basePointer string) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	settings.ArgoCDConfigMapFound = true
	if values == nil {
		return diags
	}
	if raw := values["kustomize.buildOptions"]; raw != "" {
		settings.KustomizeBuildOptions = splitShellFields(raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".kustomize.buildOptions",
		})
	}
	if raw, ok := values["helm.valuesFileSchemes"]; ok {
		provenance := diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".helm.valuesFileSchemes",
		}
		settings.HelmValuesFileSchemes = splitCommaValues(raw, provenance)
		settings.HelmValuesFileSchemesSet = true
		settings.HelmValuesFileSchemesSource = provenance
	}
	diags = appendVersionedKustomizeDiagnostics(values, path, basePointer, diags)
	diags = append(diags, applyKustomizeVersionKeys(settings, values, path, basePointer)...)
	applySourceTypeEnableKeys(settings, values, path, basePointer)
	if raw := values["application.resourceTrackingMethod"]; raw != "" {
		settings.TrackingMethod = Value[string]{
			Value: raw,
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + ".application.resourceTrackingMethod",
			},
		}
	}
	if raw := values["application.instanceLabelKey"]; raw != "" {
		settings.InstanceLabelKey = Value[string]{
			Value: raw,
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + ".application.instanceLabelKey",
			},
		}
	}
	if raw := values["installationID"]; raw != "" {
		settings.InstallationID = Value[string]{
			Value: raw,
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + ".installationID",
			},
		}
	}
	if raw, ok := values["resource.ignoreResourceUpdatesEnabled"]; ok {
		provenance := diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.ignoreResourceUpdatesEnabled",
		}
		value, diag := parseSettingsBool(raw, provenance, "invalid resource.ignoreResourceUpdatesEnabled value")
		if diag != nil {
			diags = append(diags, *diag)
		} else {
			settings.IgnoreResourceUpdatesEnabled = Value[bool]{
				Value:      value,
				Provenance: provenance,
			}
		}
	}
	if raw, ok := values["cluster.inClusterEnabled"]; ok {
		// Argo CD SettingsManager.IsInClusterEnabled: only the exact string
		// "false" disables the in-cluster destination.
		settings.InClusterEnabled = Value[bool]{
			Value: raw != "false",
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + ".cluster.inClusterEnabled",
			},
		}
	}
	if raw := values["resource.exclusions"]; strings.TrimSpace(raw) != "" {
		diags = appendParsedResourceFilters(&settings.ResourceExclusions, raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.exclusions",
		}, diags)
	}
	if raw := values["resource.inclusions"]; strings.TrimSpace(raw) != "" {
		diags = appendParsedResourceFilters(&settings.ResourceInclusions, raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.inclusions",
		}, diags)
	}
	if raw := values["resource.selectors"]; strings.TrimSpace(raw) != "" {
		diags = appendParsedResourceSelectors(&settings.ResourceSelectors, raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.selectors",
		}, diags)
	}
	if raw := values["resource.compareoptions"]; strings.TrimSpace(raw) != "" {
		diags = appendParsedResourceCompareOptions(settings, raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.compareoptions",
		}, diags)
	}
	diags = appendParsedResourceOverrides(settings, values, path, basePointer, diags)
	return diags
}

// applySourceTypeEnableKeys reads helm.enable, kustomize.enable, and
// jsonnet.enable as written (see ArgoSettings.SourceTypeEnabled).
func applySourceTypeEnableKeys(settings *ArgoSettings, values map[string]string, path, basePointer string) {
	if raw, ok := values["helm.enable"]; ok {
		setSourceTypeEnable(settings, HelmEnableKey, raw, path, basePointer)
	}
	if raw, ok := values["kustomize.enable"]; ok {
		setSourceTypeEnable(settings, KustomizeEnableKey, raw, path, basePointer)
	}
	if raw, ok := values["jsonnet.enable"]; ok {
		setSourceTypeEnable(settings, JsonnetEnableKey, raw, path, basePointer)
	}
}

func appendVersionedKustomizeDiagnostics(values map[string]string, path, basePointer string, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	keys := make([]string, 0)
	for key := range values {
		if isVersionedKustomizeBuildOptionsKey(key) || isVersionedKustomizePathKey(key) || isVersionedKustomizeVersionKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		// Argo CD v3.6.1 stores the value with the version but never
		// reads it again: kustomize build gets kustomize.buildOptions,
		// whichever kustomize version runs (util/kustomize/kustomize.go:
		// 391-392).
		message := key + " parsed but not applied: Argo CD v3.6.1 passes only kustomize.buildOptions to kustomize build, whichever kustomize version runs"
		// Argo CD registers a kustomize binary for both key forms.
		if isVersionedKustomizePathKey(key) || isVersionedKustomizeVersionKey(key) {
			message = key + " parsed but not applied: drydock uses embedded Kustomize libraries and does not select external Kustomize binary paths"
		}
		diags = append(diags, diagnostic.Diagnostic{
			Severity: diagnostic.SeverityWarning,
			Category: "settings",
			Message:  message,
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + "." + key,
			},
		})
	}
	return diags
}

func isVersionedKustomizeBuildOptionsKey(key string) bool {
	return strings.HasPrefix(key, "kustomize.buildOptions.") && strings.TrimPrefix(key, "kustomize.buildOptions.") != ""
}

func isVersionedKustomizeVersionKey(key string) bool {
	return strings.HasPrefix(key, "kustomize.version.") && strings.TrimPrefix(key, "kustomize.version.") != ""
}

func isVersionedKustomizePathKey(key string) bool {
	return strings.HasPrefix(key, "kustomize.path.") && strings.TrimPrefix(key, "kustomize.path.") != ""
}

func splitShellFields(raw string, provenance diagnostic.Provenance) []Value[string] {
	fields := strings.Fields(raw)
	out := make([]Value[string], 0, len(fields))
	for _, field := range fields {
		out = append(out, Value[string]{Value: field, Provenance: provenance})
	}
	return out
}

func splitCommaValues(raw string, provenance diagnostic.Provenance) []Value[string] {
	items := strings.Split(raw, ",")
	out := make([]Value[string], 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, Value[string]{Value: item, Provenance: provenance})
	}
	return out
}
