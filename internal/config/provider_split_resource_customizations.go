package config

import (
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
)

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
		customization.HealthLuaSHA256 = stringFingerprint(raw)
		customization.HealthLua = raw
		customization.healthLuaFingerprint = customization.HealthLuaSHA256
		return customization, nil
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
	return customization, nil
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
func splitResourceCustomizationKey(suffix string, provenance diagnostic.Provenance) (string, *diagnostic.Diagnostic) {
	if suffix == "all" {
		return "*/*", nil
	}
	if suffix == "" || strings.Count(suffix, "_") > 1 {
		return "", invalidSplitResourceCustomizationKeyDiagnostic(provenance)
	}
	if idx := strings.Index(suffix, "_"); idx >= 0 {
		if idx == len(suffix)-1 {
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
