package config

import (
	"crypto/sha256"
	"fmt"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
	"reflect"
	"sort"
	"strings"
)

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
			customization.HealthLuaSHA256 = stringFingerprint(block.HealthLua)
			customization.HealthLua = block.HealthLua
			customization.healthLuaFingerprint = customization.HealthLuaSHA256
			hasCustomization = true
		}
		if block.UseOpenLibs != nil {
			customization.HasUseOpenLibs = true
			customization.UseOpenLibs = *block.UseOpenLibs
			hasCustomization = true
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
	if summary.HasDiscoveryLua {
		summary.DiscoveryLuaSHA256 = stringFingerprint(actions.ActionDiscoveryLua)
	}
	for index, definition := range actions.Definitions {
		summary.ActionNames = append(summary.ActionNames, definition.Name)
		if strings.TrimSpace(definition.ActionLua) != "" {
			summary.ActionLuaSHA256 = append(summary.ActionLuaSHA256, ResourceActionLuaHash{
				Name:   definition.Name,
				Index:  index,
				SHA256: stringFingerprint(definition.ActionLua),
			})
		}
	}
	sort.SliceStable(summary.ActionLuaSHA256, func(i, j int) bool {
		left := summary.ActionLuaSHA256[i]
		right := summary.ActionLuaSHA256[j]
		if left.Name == right.Name {
			return left.Index < right.Index
		}
		return left.Name < right.Name
	})
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
	existing.HealthLuaSHA256 = incoming.HealthLuaSHA256
	if existing.HealthLua == "" {
		existing.HealthLua = incoming.HealthLua
	}
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
