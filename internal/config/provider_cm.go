package config

import (
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func applyCMMap(settings *ArgoSettings, values map[string]string, path, basePointer string) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	if values == nil {
		return diags
	}
	if raw := values["kustomize.buildOptions"]; raw != "" {
		settings.KustomizeBuildOptions = splitShellFields(raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".kustomize.buildOptions",
		})
	}
	diags = appendVersionedKustomizeDiagnostics(values, path, basePointer, diags)
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
	if raw := values["resource.compareoptions"]; strings.TrimSpace(raw) != "" {
		diags = appendParsedResourceCompareOptions(settings, raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.compareoptions",
		}, diags)
	}
	if raw := values["resource.customizations"]; strings.TrimSpace(raw) != "" {
		diags = appendParsedResourceCustomizations(settings, raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.customizations",
		}, diags)
	}
	diags = appendParsedSplitResourceCustomizations(settings, values, path, basePointer, diags)
	return diags
}

func appendVersionedKustomizeDiagnostics(values map[string]string, path, basePointer string, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	keys := make([]string, 0)
	for key := range values {
		if isVersionedKustomizeBuildOptionsKey(key) || isVersionedKustomizePathKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		message := key + " parsed but not applied: drydock uses embedded Kustomize libraries and does not select external Kustomize versions"
		if isVersionedKustomizePathKey(key) {
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
