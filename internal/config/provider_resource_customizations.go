package config

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	sigsyaml "sigs.k8s.io/yaml"
)

// appendParsedResourceOverrides builds the resource customizations of one
// argocd-cm the way Argo CD v3.6.1 SettingsManager.GetResourceOverrides does
// (util/settings/settings.go:1127-1143). It decodes the
// resource.customizations block with sigs.k8s.io/yaml into Argo CD's own
// ResourceOverride type, so block keys are kept as written and field names
// follow the JSON tag rules. Then the split keys replace single fields
// (appendResourceOverridesFromSplitKeys). The ignore-status override that
// Argo CD adds afterwards comes from resource.compareoptions, which drydock
// handles on its own.
//
// Argo CD fails the whole call on the first error and uses no overrides.
// drydock reports each error as an error diagnostic and keeps no resource
// customizations from this ConfigMap.
func appendParsedResourceOverrides(settings *ArgoSettings, values map[string]string, path, basePointer string, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	build := &resourceOverrideBuild{
		overrides:       map[string]argoappv1.ResourceOverride{},
		fieldProvenance: map[string]map[string]diagnostic.Provenance{},
		keyProvenance:   map[string]diagnostic.Provenance{},
		splitKeySource:  map[string]map[string]string{},
		useOpenLibsSet:  map[string]bool{},
	}
	failed := false
	if value, ok := values[resourceCustomizationsKey]; ok && value != "" {
		provenance := diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + "." + resourceCustomizationsKey,
		}
		var openLibs map[string]struct {
			UseOpenLibs *bool `json:"health.lua.useOpenLibs"`
		}
		err := decodeResourceOverrides(value, &build.overrides)
		if err == nil {
			err = sigsyaml.Unmarshal([]byte(value), &openLibs)
		}
		if err != nil {
			failed = true
			build.overrides = map[string]argoappv1.ResourceOverride{}
			diags = append(diags, resourceCustomizationError(
				fmt.Sprintf("invalid resource.customizations settings: %v", err),
				provenance,
			))
		}
		for key := range build.overrides {
			for _, field := range []string{
				overrideFieldHealth,
				overrideFieldUseOpenLibs,
				overrideFieldActions,
				overrideFieldIgnoreDifferences,
				overrideFieldIgnoreResourceUpdates,
				overrideFieldKnownTypeFields,
			} {
				build.setField(key, field, provenance)
			}
			if openLibs[key].UseOpenLibs != nil {
				build.useOpenLibsSet[key] = true
			}
		}
	}

	next := appendResourceOverridesFromSplitKeys(build, values, path, basePointer, nil)
	diags = append(diags, next...)
	if failed || hasErrorDiagnostic(next) {
		return diags
	}

	keys := make([]string, 0, len(build.overrides))
	for key := range build.overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		customization, ok, next := resourceCustomizationFromOverride(key, build.overrides[key], build)
		diags = append(diags, next...)
		if !ok {
			continue
		}
		if settings.ResourceCustomizations == nil {
			settings.ResourceCustomizations = map[string]ResourceCustomization{}
		}
		settings.ResourceCustomizations[key] = customization
	}
	return diags
}

// decodeResourceOverrides decodes the resource.customizations block into
// Argo CD's ResourceOverride map. Argo CD's ResourceOverride.UnmarshalJSON
// (pkg/apis/application/v1alpha1/types.go:2641-2658) panics with a nil
// pointer error on a null entry, such as "Service:" with no value, so Argo
// CD cannot load such a block. drydock turns the panic into an error.
func decodeResourceOverrides(value string, overrides *map[string]argoappv1.ResourceOverride) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("an entry with no value makes Argo CD panic: %v", recovered)
		}
	}()
	return sigsyaml.Unmarshal([]byte(value), overrides)
}

// resourceCustomizationFromOverride converts one Argo CD ResourceOverride
// into drydock's ResourceCustomization. It reports false when the override
// sets nothing, because an empty override changes no diff or health result.
// Health Lua and actions that hold only white space count as unset.
func resourceCustomizationFromOverride(key string, override argoappv1.ResourceOverride, build *resourceOverrideBuild) (ResourceCustomization, bool, []diagnostic.Diagnostic) {
	var diags []diagnostic.Diagnostic
	fieldProvenance := build.fieldProvenance[key]
	customization := ResourceCustomization{
		Provenance:            build.keyProvenance[key],
		IgnoreDifferences:     overrideIgnoreDifferencesFromArgo(override.IgnoreDifferences),
		IgnoreResourceUpdates: overrideIgnoreDifferencesFromArgo(override.IgnoreResourceUpdates),
	}
	hasCustomization := hasOverrideSection(customization.IgnoreDifferences)
	if hasOverrideSection(customization.IgnoreResourceUpdates) {
		hasCustomization = true
		diags = append(diags, advancedResourceCustomizationWarning(
			"resource customizations ignoreResourceUpdates are parsed but not applied to desired-vs-desired diffs",
			fieldProvenance[overrideFieldIgnoreResourceUpdates],
		))
	}
	for _, field := range override.KnownTypeFields {
		customization.KnownTypeFields = append(customization.KnownTypeFields, KnownTypeField{Field: field.Field, Type: field.Type})
		hasCustomization = true
	}
	// Argo CD uses any non-empty health.lua, even one that holds only white
	// space (util/lua/lua.go:254 and 571). Such a script returns nothing, so
	// Argo CD's Lua health gives no status and the built-in Lua script and
	// wildcard keys do not apply.
	if override.HealthLua != "" {
		customization.HasHealthLua = true
		customization.HealthLuaSHA256 = stringFingerprint(override.HealthLua)
		customization.HealthLua = override.HealthLua
		customization.healthLuaFingerprint = customization.HealthLuaSHA256
		hasCustomization = true
	}
	if override.UseOpenLibs || build.useOpenLibsSet[key] {
		customization.HasUseOpenLibs = true
		customization.UseOpenLibs = override.UseOpenLibs
		hasCustomization = true
	}
	// Argo CD keeps actions as a string when it loads settings and decodes
	// them only when it lists or runs actions (util/lua/lua.go:501 and 533).
	// A non-empty value, even white space only, replaces the built-in
	// actions there. drydock never runs actions, so it accepts every value.
	if override.Actions != "" {
		customization.Actions = resourceActionsSummary(override)
		hasCustomization = true
		diags = append(diags, advancedResourceCustomizationWarning(
			"resource customizations actions are parsed as metadata only and are not executed offline",
			fieldProvenance[overrideFieldActions],
		))
	}
	return customization, hasCustomization, diags
}

func overrideIgnoreDifferencesFromArgo(ignore argoappv1.OverrideIgnoreDiff) OverrideIgnoreDifferences {
	return OverrideIgnoreDifferences{
		JSONPointers:          ignore.JSONPointers,
		JQPathExpressions:     ignore.JQPathExpressions,
		ManagedFieldsManagers: ignore.ManagedFieldsManagers,
	}
}

// resourceActionsSummary summarizes the actions of an override. It decodes
// them with Argo CD's own ResourceOverride.GetActions. When that fails, the
// summary has only the fingerprint of the raw value. Argo CD accepts such a
// value when it loads settings and fails only when it lists or runs the
// actions, which drydock never does, so drydock reports no diagnostic.
func resourceActionsSummary(override argoappv1.ResourceOverride) ResourceActionsSummary {
	summary := ResourceActionsSummary{
		HasActions:  true,
		fingerprint: stringFingerprint(override.Actions),
	}
	actions, err := override.GetActions()
	if err != nil {
		return summary
	}
	summary.HasDiscoveryLua = strings.TrimSpace(actions.ActionDiscoveryLua) != ""
	summary.MergeBuiltinActions = actions.MergeBuiltinActions
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
	return summary
}
func advancedResourceCustomizationWarning(message string, provenance diagnostic.Provenance) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
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
