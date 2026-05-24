package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"go.yaml.in/yaml/v4"
)

func LoadFromHelmValues(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	var doc struct {
		Configs struct {
			CM map[string]string `yaml:"cm"`
		} `yaml:"configs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse helm values %s: %w", path, err)
	}

	diags := applyCMMap(&settings, doc.Configs.CM, path, "configs.cm")
	return settings, diags, nil
}

func LoadFromConfigMap(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse configmap %s: %w", path, err)
	}
	if doc.Kind != "ConfigMap" || doc.Metadata.Name != "argocd-cm" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file is not argocd-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	diags := applyCMMap(&settings, doc.Data, path, "data")
	return settings, diags, nil
}

func LoadRepositorySecret(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
		StringData map[string]string `yaml:"stringData"`
		Data       map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse repository secret %s: %w", path, err)
	}
	if doc.Kind != "Secret" || doc.Metadata.Labels["argocd.argoproj.io/secret-type"] != "repository" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file is not an Argo CD repository Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	url := secretStringField(doc.StringData, doc.Data, "url")
	if url == "" {
		return settings, nil, nil
	}

	var diags []diagnostic.Diagnostic
	enableOCI, diag := secretBoolField(doc.StringData, doc.Data, "enableOCI", path)
	if diag != nil {
		diags = append(diags, *diag)
	}
	settings.HelmRepositories[url] = RepositorySettings{
		Name:       secretStringField(doc.StringData, doc.Data, "name"),
		Type:       secretStringField(doc.StringData, doc.Data, "type"),
		URL:        url,
		EnableOCI:  enableOCI,
		Project:    secretStringField(doc.StringData, doc.Data, "project"),
		Provenance: diagnostic.Provenance{Path: path, Pointer: secretFieldPointer(doc.StringData, "url")},
	}
	return settings, diags, nil
}

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
	if raw := values["resource.customizations"]; strings.TrimSpace(raw) != "" {
		diags = appendParsedResourceCustomizations(settings, raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".resource.customizations",
		}, diags)
	}
	diags = appendParsedSplitResourceCustomizations(settings, values, path, basePointer, diags)
	return diags
}

func splitShellFields(raw string, provenance diagnostic.Provenance) []Value[string] {
	fields := strings.Fields(raw)
	out := make([]Value[string], 0, len(fields))
	for _, field := range fields {
		out = append(out, Value[string]{Value: field, Provenance: provenance})
	}
	return out
}

func appendParsedResourceFilters(dst *[]ResourceFilterRule, raw string, provenance diagnostic.Provenance, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var rules []ResourceFilterRule
	if err := yaml.Unmarshal([]byte(raw), &rules); err != nil {
		return append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource filter settings",
			Provenance: provenance,
		})
	}
	for i := range rules {
		rules[i].Provenance = provenance
	}
	*dst = append(*dst, rules...)
	return diags
}

func appendParsedResourceCustomizations(settings *ArgoSettings, raw string, provenance diagnostic.Provenance, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var customizations map[string]struct {
		IgnoreDifferences string `yaml:"ignoreDifferences"`
	}
	if err := yaml.Unmarshal([]byte(raw), &customizations); err != nil {
		return append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource.customizations settings",
			Provenance: provenance,
		})
	}

	keys := make([]string, 0, len(customizations))
	for key := range customizations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		block := customizations[key]
		if strings.TrimSpace(block.IgnoreDifferences) == "" {
			continue
		}
		ignore, next := parseOverrideIgnoreDifferences(block.IgnoreDifferences, provenance)
		diags = append(diags, next...)
		if hasErrorDiagnostic(next) {
			continue
		}
		diags = addResourceCustomization(settings, key, ResourceCustomization{
			IgnoreDifferences: ignore,
			Provenance:        provenance,
		}, diags)
	}
	return diags
}

func appendParsedSplitResourceCustomizations(settings *ArgoSettings, values map[string]string, path, basePointer string, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	const prefix = "resource.customizations.ignoreDifferences."
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := values[key]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		provenance := diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + "." + key,
		}
		customizationKey, diag := splitResourceCustomizationKey(strings.TrimPrefix(key, prefix), provenance)
		if diag != nil {
			diags = append(diags, *diag)
			continue
		}
		ignore, next := parseOverrideIgnoreDifferences(raw, provenance)
		diags = append(diags, next...)
		if hasErrorDiagnostic(next) {
			continue
		}
		diags = addResourceCustomization(settings, customizationKey, ResourceCustomization{
			IgnoreDifferences: ignore,
			Provenance:        provenance,
		}, diags)
	}
	return diags
}

func parseOverrideIgnoreDifferences(raw string, provenance diagnostic.Provenance) (OverrideIgnoreDifferences, []diagnostic.Diagnostic) {
	var ignore OverrideIgnoreDifferences
	if err := yaml.Unmarshal([]byte(raw), &ignore); err != nil {
		return ignore, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource customization ignoreDifferences settings",
			Provenance: provenance,
		}}
	}

	var diags []diagnostic.Diagnostic
	if len(ignore.JQPathExpressions) > 0 {
		diags = append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "resource customization jqPathExpressions are discovered but not enforced",
			Provenance: provenance,
		})
	}
	if len(ignore.ManagedFieldsManagers) > 0 {
		diags = append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "resource customization managedFieldsManagers are discovered but not enforced",
			Provenance: provenance,
		})
	}
	return ignore, diags
}

func splitResourceCustomizationKey(suffix string, provenance diagnostic.Provenance) (string, *diagnostic.Diagnostic) {
	if suffix == "all" {
		return "*/*", nil
	}
	if suffix == "" || strings.Count(suffix, "_") > 1 {
		return "", invalidSplitResourceCustomizationKeyDiagnostic(provenance)
	}
	if idx := strings.Index(suffix, "_"); idx >= 0 {
		if idx == 0 || idx == len(suffix)-1 {
			return "", invalidSplitResourceCustomizationKeyDiagnostic(provenance)
		}
		return suffix[:idx] + "/" + suffix[idx+1:], nil
	}
	return suffix, nil
}

func invalidSplitResourceCustomizationKeyDiagnostic(provenance diagnostic.Provenance) *diagnostic.Diagnostic {
	return &diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityError,
		Category:   "settings",
		Message:    "invalid resource customization split key",
		Provenance: provenance,
	}
}

func hasErrorDiagnostic(diags []diagnostic.Diagnostic) bool {
	for _, diag := range diags {
		if diag.Severity == diagnostic.SeverityError {
			return true
		}
	}
	return false
}

func addResourceCustomization(settings *ArgoSettings, key string, customization ResourceCustomization, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if settings.ResourceCustomizations == nil {
		settings.ResourceCustomizations = map[string]ResourceCustomization{}
	}
	existing, ok := settings.ResourceCustomizations[key]
	if ok {
		if reflect.DeepEqual(existing.IgnoreDifferences, customization.IgnoreDifferences) {
			return diags
		}
		return append(diags, conflictDiagnostic(
			fmt.Sprintf("conflicting resource customization settings discovered for %q", key),
			customization.Provenance,
		))
	}
	settings.ResourceCustomizations[key] = customization
	return diags
}

func secretStringField(stringData, data map[string]string, key string) string {
	if value, ok := stringData[key]; ok {
		return value
	}
	encoded, ok := data[key]
	if !ok {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return string(decoded)
}

func secretBoolField(stringData, data map[string]string, key, path string) (bool, *diagnostic.Diagnostic) {
	if value, ok := stringData[key]; ok {
		return parseSecretBool(value, diagnostic.Provenance{Path: path, Pointer: "stringData." + key})
	}
	encoded, ok := data[key]
	if !ok {
		return false, nil
	}
	if encoded == "" {
		return false, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false, invalidSecretBoolDiagnostic(diagnostic.Provenance{Path: path, Pointer: "data." + key})
	}
	return parseSecretBool(string(decoded), diagnostic.Provenance{Path: path, Pointer: "data." + key})
}

func parseSecretBool(raw string, provenance diagnostic.Provenance) (bool, *diagnostic.Diagnostic) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, invalidSecretBoolDiagnostic(provenance)
	}
	return value, nil
}

func invalidSecretBoolDiagnostic(provenance diagnostic.Provenance) *diagnostic.Diagnostic {
	return &diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityError,
		Category:   "settings",
		Message:    "invalid repository Secret enableOCI value",
		Provenance: provenance,
	}
}

func secretFieldPointer(stringData map[string]string, key string) string {
	if _, ok := stringData[key]; ok {
		return "stringData." + key
	}
	return "data." + key
}
