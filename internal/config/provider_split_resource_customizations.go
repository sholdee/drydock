package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	sigsyaml "sigs.k8s.io/yaml"
)

// resourceCustomizationsKey is the argocd-cm key that holds the
// resource.customizations block. Argo CD also uses it, with no trailing dot,
// as the prefix for split keys.
const resourceCustomizationsKey = "resource.customizations"

// Override fields that a split key can set. useOpenLibs is the split-key name
// of the block field health.lua.useOpenLibs.
const (
	overrideFieldHealth                = "health"
	overrideFieldUseOpenLibs           = "useOpenLibs"
	overrideFieldActions               = "actions"
	overrideFieldIgnoreDifferences     = "ignoreDifferences"
	overrideFieldIgnoreResourceUpdates = "ignoreResourceUpdates"
	overrideFieldKnownTypeFields       = "knownTypeFields"
)

// resourceOverrideBuild is the resource override map that Argo CD's
// SettingsManager.GetResourceOverrides builds from one argocd-cm, plus the
// metadata drydock needs to report where each field came from.
type resourceOverrideBuild struct {
	overrides map[string]argoappv1.ResourceOverride
	// fieldProvenance records, per override key and field, the argocd-cm
	// entry that last set the field.
	fieldProvenance map[string]map[string]diagnostic.Provenance
	// keyProvenance records the argocd-cm entry that last changed the
	// override key.
	keyProvenance map[string]diagnostic.Provenance
	// splitKeySource records, per override key and field, the split key
	// that set the field.
	splitKeySource map[string]map[string]string
	// useOpenLibsSet records override keys whose useOpenLibs value was
	// written explicitly, so an explicit false stays visible.
	useOpenLibsSet map[string]bool
}

func (b *resourceOverrideBuild) setField(overrideKey, field string, provenance diagnostic.Provenance) {
	if b.fieldProvenance[overrideKey] == nil {
		b.fieldProvenance[overrideKey] = map[string]diagnostic.Provenance{}
	}
	b.fieldProvenance[overrideKey][field] = provenance
	b.keyProvenance[overrideKey] = provenance
}

// appendResourceOverridesFromSplitKeys ports Argo CD v3.6.1
// SettingsManager.appendResourceOverridesFromSplitKeys
// (util/settings/settings.go:1222-1287). A split key has the form
// resource.customizations.<type>.<group_kind>. It replaces one field of the
// override for its key and keeps the other fields, including fields that the
// resource.customizations block set.
//
// Argo CD walks the ConfigMap data in Go map order, which is not defined.
// The order changes the result only when two keys set the same field of the
// same override key. With valid ConfigMap keys, that happens only through
// Argo CD's prefix check, which has no trailing dot: it also accepts keys such
// as resource.customizationsX.health.Service. drydock walks the keys in
// sorted order, so the last key in that order wins, and it reports a warning.
//
// Argo CD returns the first error and builds no overrides at all. drydock
// reports every error it finds; the caller then keeps no overrides.
func appendResourceOverridesFromSplitKeys(build *resourceOverrideBuild, values map[string]string, path, basePointer string, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.HasPrefix(key, resourceCustomizationsKey) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		provenance := diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + "." + key,
		}

		parts := strings.SplitN(key, ".", 4)
		if len(parts) < 4 {
			continue
		}

		overrideKey, err := convertToOverrideKey(parts[3])
		if err != nil {
			diags = append(diags, resourceCustomizationError(
				fmt.Sprintf("invalid resource customization split key %q: %v", key, err),
				provenance,
			))
			continue
		}
		if overrideKey == "all" {
			overrideKey = "*/*"
		}

		overrideVal := build.overrides[overrideKey]
		customizationType := parts[2]
		if err := setResourceOverrideField(&overrideVal, customizationType, value); err != nil {
			diags = append(diags, resourceCustomizationError(
				fmt.Sprintf("invalid resource customization %v (split key %q)", err, key),
				provenance,
			))
			continue
		}
		if customizationType == overrideFieldUseOpenLibs {
			build.useOpenLibsSet[overrideKey] = true
		}

		if build.splitKeySource[overrideKey] == nil {
			build.splitKeySource[overrideKey] = map[string]string{}
		}
		if previous, ok := build.splitKeySource[overrideKey][customizationType]; ok {
			diags = append(diags, diagnostic.Diagnostic{
				Severity: diagnostic.SeverityWarning,
				Category: "settings",
				Message: fmt.Sprintf(
					"resource customization split keys %q and %q both set %s for %q; Argo CD applies them in random order, drydock keeps %q",
					previous, key, customizationType, overrideKey, key,
				),
				Provenance: provenance,
			})
		}
		build.splitKeySource[overrideKey][customizationType] = key
		build.setField(overrideKey, customizationType, provenance)
		build.overrides[overrideKey] = overrideVal
	}
	return diags
}

// setResourceOverrideField sets one field of override from a split key value,
// as the switch in Argo CD's appendResourceOverridesFromSplitKeys does
// (util/settings/settings.go:1248-1284). Health Lua and actions are kept as
// written. useOpenLibs uses strconv.ParseBool with no trimming. The other
// values decode with sigs.k8s.io/yaml.
func setResourceOverrideField(override *argoappv1.ResourceOverride, customizationType, value string) error {
	switch customizationType {
	case overrideFieldHealth:
		override.HealthLua = value
	case overrideFieldUseOpenLibs:
		useOpenLibs, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("useOpenLibs value: %w", err)
		}
		override.UseOpenLibs = useOpenLibs
	case overrideFieldActions:
		override.Actions = value
	case overrideFieldIgnoreDifferences:
		var ignore argoappv1.OverrideIgnoreDiff
		if err := sigsyaml.Unmarshal([]byte(value), &ignore); err != nil {
			return fmt.Errorf("ignoreDifferences settings: %w", err)
		}
		override.IgnoreDifferences = ignore
	case overrideFieldIgnoreResourceUpdates:
		var ignore argoappv1.OverrideIgnoreDiff
		if err := sigsyaml.Unmarshal([]byte(value), &ignore); err != nil {
			return fmt.Errorf("ignoreResourceUpdates settings: %w", err)
		}
		override.IgnoreResourceUpdates = ignore
	case overrideFieldKnownTypeFields:
		var knownTypeFields []argoappv1.KnownTypeField
		if err := sigsyaml.Unmarshal([]byte(value), &knownTypeFields); err != nil {
			return fmt.Errorf("knownTypeFields settings: %w", err)
		}
		override.KnownTypeFields = knownTypeFields
	default:
		return fmt.Errorf("type %q: not supported", customizationType)
	}
	return nil
}

// convertToOverrideKey ports Argo CD v3.6.1 convertToOverrideKey
// (util/settings/settings.go:1292-1300). It splits the suffix on "_". Two
// parts become group/kind, even when a part is empty, so "_Service" becomes
// "/Service" and "Service_" becomes "Service/". One non-empty part is kept
// as it is, dots included. An empty suffix or more than one "_" is an
// error.
func convertToOverrideKey(groupKind string) (string, error) {
	parts := strings.Split(groupKind, "_")
	if len(parts) == 2 {
		return fmt.Sprintf("%s/%s", parts[0], parts[1]), nil
	} else if len(parts) == 1 && groupKind != "" {
		return groupKind, nil
	}
	return "", fmt.Errorf("group kind should be in format `resource.customizations.<type>.<group_kind>` or `resource.customizations.<type>.<kind>`, got group kind: '%s'", groupKind)
}

func resourceCustomizationError(message string, provenance diagnostic.Provenance) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityError,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
}
