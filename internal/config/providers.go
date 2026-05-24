package config

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
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

func appendParsedResourceCompareOptions(settings *ArgoSettings, raw string, provenance diagnostic.Provenance, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var options ResourceCompareOptions
	if err := yaml.Unmarshal([]byte(raw), &options); err != nil {
		return append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource.compareoptions settings",
			Provenance: provenance,
		})
	}
	if strings.TrimSpace(options.IgnoreResourceStatusField) == "" {
		options.IgnoreResourceStatusField = "all"
	}
	options.Provenance = provenance
	settings.CompareOptions = options
	if !knownIgnoreResourceStatusField(options.IgnoreResourceStatusField) {
		diags = append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "unrecognized resource.compareoptions ignoreResourceStatusField value; treating as all",
			Provenance: provenance,
		})
	}
	return diags
}

func knownIgnoreResourceStatusField(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all", "crd", "none", "off", "false":
		return true
	default:
		return false
	}
}

func appendParsedResourceCustomizations(settings *ArgoSettings, raw string, provenance diagnostic.Provenance, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var customizations map[string]struct {
		IgnoreDifferences     string           `yaml:"ignoreDifferences"`
		IgnoreResourceUpdates string           `yaml:"ignoreResourceUpdates"`
		KnownTypeFields       []KnownTypeField `yaml:"knownTypeFields"`
		HealthLua             string           `yaml:"health.lua"`
		UseOpenLibs           *bool            `yaml:"health.lua.useOpenLibs"`
		Actions               string           `yaml:"actions"`
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
		customization := ResourceCustomization{
			Provenance: provenance,
		}
		hasCustomization := false
		if strings.TrimSpace(block.IgnoreDifferences) != "" {
			ignore, next := parseOverrideIgnoreDifferences(block.IgnoreDifferences, provenance)
			diags = append(diags, next...)
			if !hasErrorDiagnostic(next) {
				customization.IgnoreDifferences = ignore
				hasCustomization = true
			}
		}
		if strings.TrimSpace(block.IgnoreResourceUpdates) != "" {
			ignore, next := parseOverrideIgnoreDifferencesWithMessage(
				block.IgnoreResourceUpdates,
				provenance,
				"invalid resource customization ignoreResourceUpdates settings",
			)
			diags = append(diags, next...)
			if !hasErrorDiagnostic(next) {
				customization.IgnoreResourceUpdates = ignore
				hasCustomization = true
				diags = append(diags, advancedResourceCustomizationWarning(
					"resource customizations ignoreResourceUpdates are parsed but not applied to desired-vs-desired diffs",
					provenance,
				))
			}
		}
		if len(block.KnownTypeFields) > 0 {
			customization.KnownTypeFields = append([]KnownTypeField(nil), block.KnownTypeFields...)
			hasCustomization = true
		}
		if strings.TrimSpace(block.HealthLua) != "" {
			customization.HasHealthLua = true
			customization.healthLuaFingerprint = stringFingerprint(block.HealthLua)
			hasCustomization = true
			diags = append(diags, advancedResourceCustomizationWarning(
				"resource customizations health Lua is parsed as metadata only and is not executed offline",
				provenance,
			))
		}
		if block.UseOpenLibs != nil {
			customization.HasUseOpenLibs = true
			customization.UseOpenLibs = *block.UseOpenLibs
			hasCustomization = true
			diags = append(diags, advancedResourceCustomizationWarning(
				"resource customizations useOpenLibs is parsed as metadata only and is not executed offline",
				provenance,
			))
		}
		if strings.TrimSpace(block.Actions) != "" {
			actions, next := parseResourceActionsSummary(block.Actions, provenance)
			diags = append(diags, next...)
			if !hasErrorDiagnostic(next) {
				customization.Actions = actions
				hasCustomization = true
				diags = append(diags, advancedResourceCustomizationWarning(
					"resource customizations actions are parsed as metadata only and are not executed offline",
					provenance,
				))
			}
		}
		if hasCustomization {
			diags = addResourceCustomization(settings, key, customization, diags)
		}
	}
	return diags
}

type splitResourceCustomizationEntry struct {
	prefix  string
	section string
}

const (
	ignoreDifferencesSplitSection     = "ignoreDifferences"
	ignoreResourceUpdatesSplitSection = "ignoreResourceUpdates"
	knownTypeFieldsSplitSection       = "knownTypeFields"
	healthSplitSection                = "health"
	useOpenLibsSplitSection           = "useOpenLibs"
	actionsSplitSection               = "actions"
)

var splitResourceCustomizationEntries = []splitResourceCustomizationEntry{
	{prefix: "resource.customizations.ignoreDifferences.", section: ignoreDifferencesSplitSection},
	{prefix: "resource.customizations.ignoreResourceUpdates.", section: ignoreResourceUpdatesSplitSection},
	{prefix: "resource.customizations.knownTypeFields.", section: knownTypeFieldsSplitSection},
	{prefix: "resource.customizations.health.", section: healthSplitSection},
	{prefix: "resource.customizations.useOpenLibs.", section: useOpenLibsSplitSection},
	{prefix: "resource.customizations.actions.", section: actionsSplitSection},
}

func appendParsedSplitResourceCustomizations(settings *ArgoSettings, values map[string]string, path, basePointer string, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	keys := splitResourceCustomizationKeys(values)
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
		entry, ok := splitResourceCustomizationEntryForKey(key)
		if !ok {
			continue
		}
		customizationKey, diag := splitResourceCustomizationKey(strings.TrimPrefix(key, entry.prefix), provenance)
		if diag != nil {
			diags = append(diags, *diag)
			continue
		}
		customization, next := parseSplitResourceCustomization(entry.section, raw, provenance)
		diags = append(diags, next...)
		if hasErrorDiagnostic(next) {
			continue
		}
		diags = addResourceCustomization(settings, customizationKey, customization, diags)
	}
	return diags
}

func splitResourceCustomizationKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if _, ok := splitResourceCustomizationEntryForKey(key); ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func splitResourceCustomizationEntryForKey(key string) (splitResourceCustomizationEntry, bool) {
	for _, entry := range splitResourceCustomizationEntries {
		if strings.HasPrefix(key, entry.prefix) {
			return entry, true
		}
	}
	return splitResourceCustomizationEntry{}, false
}

func parseSplitResourceCustomization(section, raw string, provenance diagnostic.Provenance) (ResourceCustomization, []diagnostic.Diagnostic) {
	customization := ResourceCustomization{Provenance: provenance}
	switch section {
	case ignoreDifferencesSplitSection:
		ignore, diags := parseOverrideIgnoreDifferences(raw, provenance)
		customization.IgnoreDifferences = ignore
		return customization, diags
	case ignoreResourceUpdatesSplitSection:
		return parseSplitIgnoreResourceUpdates(raw, provenance, customization)
	case knownTypeFieldsSplitSection:
		fields, diags := parseKnownTypeFields(raw, provenance)
		customization.KnownTypeFields = fields
		return customization, diags
	case healthSplitSection:
		customization.HasHealthLua = true
		customization.healthLuaFingerprint = stringFingerprint(raw)
		return customization, []diagnostic.Diagnostic{advancedResourceCustomizationWarning(
			"resource customizations health Lua is parsed as metadata only and is not executed offline",
			provenance,
		)}
	case useOpenLibsSplitSection:
		return parseSplitUseOpenLibs(raw, provenance, customization)
	case actionsSplitSection:
		return parseSplitActions(raw, provenance, customization)
	default:
		return customization, nil
	}
}

func parseSplitIgnoreResourceUpdates(raw string, provenance diagnostic.Provenance, customization ResourceCustomization) (ResourceCustomization, []diagnostic.Diagnostic) {
	ignore, diags := parseOverrideIgnoreDifferencesWithMessage(
		raw,
		provenance,
		"invalid resource customization ignoreResourceUpdates settings",
	)
	customization.IgnoreResourceUpdates = ignore
	if hasErrorDiagnostic(diags) {
		return customization, diags
	}
	return customization, append(diags, advancedResourceCustomizationWarning(
		"resource customizations ignoreResourceUpdates are parsed but not applied to desired-vs-desired diffs",
		provenance,
	))
}

func parseSplitUseOpenLibs(raw string, provenance diagnostic.Provenance, customization ResourceCustomization) (ResourceCustomization, []diagnostic.Diagnostic) {
	value, diag := parseSettingsBool(raw, provenance, "invalid resource customization useOpenLibs value")
	if diag != nil {
		return customization, []diagnostic.Diagnostic{*diag}
	}
	customization.HasUseOpenLibs = true
	customization.UseOpenLibs = value
	return customization, []diagnostic.Diagnostic{advancedResourceCustomizationWarning(
		"resource customizations useOpenLibs is parsed as metadata only and is not executed offline",
		provenance,
	)}
}

func parseSplitActions(raw string, provenance diagnostic.Provenance, customization ResourceCustomization) (ResourceCustomization, []diagnostic.Diagnostic) {
	actions, diags := parseResourceActionsSummary(raw, provenance)
	customization.Actions = actions
	if hasErrorDiagnostic(diags) {
		return customization, diags
	}
	return customization, append(diags, advancedResourceCustomizationWarning(
		"resource customizations actions are parsed as metadata only and are not executed offline",
		provenance,
	))
}

func parseOverrideIgnoreDifferences(raw string, provenance diagnostic.Provenance) (OverrideIgnoreDifferences, []diagnostic.Diagnostic) {
	return parseOverrideIgnoreDifferencesWithMessage(raw, provenance, "invalid resource customization ignoreDifferences settings")
}

func parseOverrideIgnoreDifferencesWithMessage(raw string, provenance diagnostic.Provenance, message string) (OverrideIgnoreDifferences, []diagnostic.Diagnostic) {
	var ignore OverrideIgnoreDifferences
	if err := yaml.Unmarshal([]byte(raw), &ignore); err != nil {
		return ignore, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    message,
			Provenance: provenance,
		}}
	}

	return ignore, nil
}

func parseKnownTypeFields(raw string, provenance diagnostic.Provenance) ([]KnownTypeField, []diagnostic.Diagnostic) {
	var fields []KnownTypeField
	if err := yaml.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource customization knownTypeFields settings",
			Provenance: provenance,
		}}
	}
	return fields, nil
}

func parseResourceActionsSummary(raw string, provenance diagnostic.Provenance) (ResourceActionsSummary, []diagnostic.Diagnostic) {
	var actions argoappv1.ResourceActions
	if err := yaml.Unmarshal([]byte(raw), &actions); err != nil {
		return ResourceActionsSummary{}, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource customization actions settings",
			Provenance: provenance,
		}}
	}

	summary := ResourceActionsSummary{
		HasActions:          true,
		HasDiscoveryLua:     strings.TrimSpace(actions.ActionDiscoveryLua) != "",
		MergeBuiltinActions: actions.MergeBuiltinActions,
		fingerprint:         stringFingerprint(raw),
	}
	for _, definition := range actions.Definitions {
		summary.ActionNames = append(summary.ActionNames, definition.Name)
	}
	return summary, nil
}

func advancedResourceCustomizationWarning(message string, provenance diagnostic.Provenance) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
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
		merged, ok := mergeResourceCustomizationSections(existing, customization)
		if ok {
			settings.ResourceCustomizations[key] = merged
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

func mergeResourceCustomizationSections(existing, incoming ResourceCustomization) (ResourceCustomization, bool) {
	merged := existing
	if !hasResourceCustomizationProvenance(merged) && hasResourceCustomizationProvenance(incoming) {
		merged.Provenance = incoming.Provenance
	}
	var ok bool
	if merged.IgnoreDifferences, ok = mergeOverrideSection(merged.IgnoreDifferences, incoming.IgnoreDifferences); !ok {
		return existing, false
	}
	if merged.IgnoreResourceUpdates, ok = mergeOverrideSection(merged.IgnoreResourceUpdates, incoming.IgnoreResourceUpdates); !ok {
		return existing, false
	}
	return mergeAdvancedResourceCustomizationSections(merged, incoming, existing)
}

func mergeAdvancedResourceCustomizationSections(merged, incoming, existing ResourceCustomization) (ResourceCustomization, bool) {
	var ok bool
	if merged.KnownTypeFields, ok = mergeKnownTypeFieldsSection(merged.KnownTypeFields, incoming.KnownTypeFields); !ok {
		return existing, false
	}
	if merged, ok = mergeHealthLuaSection(merged, incoming); !ok {
		return existing, false
	}
	if merged, ok = mergeUseOpenLibsSection(merged, incoming); !ok {
		return existing, false
	}
	if merged.Actions, ok = mergeActionsSection(merged.Actions, incoming.Actions); !ok {
		return existing, false
	}
	return merged, true
}

func mergeKnownTypeFieldsSection(existing, incoming []KnownTypeField) ([]KnownTypeField, bool) {
	if len(incoming) == 0 {
		return existing, true
	}
	if len(existing) > 0 && !reflect.DeepEqual(existing, incoming) {
		return existing, false
	}
	if len(existing) == 0 {
		return incoming, true
	}
	return existing, true
}

func mergeHealthLuaSection(existing, incoming ResourceCustomization) (ResourceCustomization, bool) {
	if !incoming.HasHealthLua {
		return existing, true
	}
	if existing.HasHealthLua && existing.healthLuaFingerprint != incoming.healthLuaFingerprint {
		return existing, false
	}
	existing.HasHealthLua = true
	if existing.healthLuaFingerprint == "" {
		existing.healthLuaFingerprint = incoming.healthLuaFingerprint
	}
	return existing, true
}

func mergeUseOpenLibsSection(existing, incoming ResourceCustomization) (ResourceCustomization, bool) {
	if !incoming.HasUseOpenLibs {
		return existing, true
	}
	if existing.HasUseOpenLibs && existing.UseOpenLibs != incoming.UseOpenLibs {
		return existing, false
	}
	if !existing.HasUseOpenLibs {
		existing.HasUseOpenLibs = true
		existing.UseOpenLibs = incoming.UseOpenLibs
	}
	return existing, true
}

func mergeActionsSection(existing, incoming ResourceActionsSummary) (ResourceActionsSummary, bool) {
	if !incoming.HasActions {
		return existing, true
	}
	if existing.HasActions && !reflect.DeepEqual(existing, incoming) {
		return existing, false
	}
	if !existing.HasActions {
		return incoming, true
	}
	return existing, true
}

func stringFingerprint(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return fmt.Sprintf("%x", sum)
}

func mergeOverrideSection(existing, incoming OverrideIgnoreDifferences) (OverrideIgnoreDifferences, bool) {
	if !hasOverrideSection(incoming) {
		return existing, true
	}
	if hasOverrideSection(existing) && !reflect.DeepEqual(existing, incoming) {
		return existing, false
	}
	if !hasOverrideSection(existing) {
		return incoming, true
	}
	return existing, true
}

func hasOverrideSection(section OverrideIgnoreDifferences) bool {
	return len(section.JSONPointers) > 0 ||
		len(section.JQPathExpressions) > 0 ||
		len(section.ManagedFieldsManagers) > 0
}

func hasResourceCustomizationProvenance(customization ResourceCustomization) bool {
	return customization.Provenance.Path != "" || customization.Provenance.Pointer != ""
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

func parseSettingsBool(raw string, provenance diagnostic.Provenance, message string) (bool, *diagnostic.Diagnostic) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    message,
			Provenance: provenance,
		}
	}
	return value, nil
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
