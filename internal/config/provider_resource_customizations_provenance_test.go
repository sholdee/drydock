package config

import (
	"strings"
	"testing"
)

// TestResourceCustomizationFieldProvenance checks that each field of a
// customization keeps the argocd-cm entry that set it, even when a split
// key for another field of the same key comes later.
func TestResourceCustomizationFieldProvenance(t *testing.T) {
	settings := DefaultSettings()
	diags := applyCMMap(&settings, map[string]string{
		"resource.customizations":                           "Service:\n  health.lua: return {}\n",
		"resource.customizations.ignoreDifferences.Service": "jsonPointers: [/a]\n",
	}, "argocd-cm.yaml", "data")
	if hasErrorDiagnostic(diags) {
		t.Fatalf("diagnostics = %#v", diags)
	}
	customization := settings.ResourceCustomizations["Service"]
	if got := customization.HealthLuaProvenance().Pointer; got != "data.resource.customizations" {
		t.Fatalf("health Lua provenance = %q, want the block", got)
	}
	if got := customization.FieldProvenance(overrideFieldIgnoreDifferences).Pointer; got != "data.resource.customizations.ignoreDifferences.Service" {
		t.Fatalf("ignoreDifferences provenance = %q, want the split key", got)
	}
}

// TestMergeResourceCustomizationConflictUsesFieldProvenance checks that a
// conflict between two settings sources points at the entry that sets the
// conflicting field, and that a merged field keeps its own source.
func TestMergeResourceCustomizationConflictUsesFieldProvenance(t *testing.T) {
	first := DefaultSettings()
	applyCMMap(&first, map[string]string{
		"resource.customizations":                           "Service:\n  health.lua: return {}\n",
		"resource.customizations.ignoreDifferences.Service": "jsonPointers: [/a]\n",
	}, "first.yaml", "data")
	second := DefaultSettings()
	applyCMMap(&second, map[string]string{
		"resource.customizations.health.Service":          "return { status = \"Healthy\" }\n",
		"resource.customizations.knownTypeFields.Service": "- field: spec\n  type: core/v1/PodSpec\n",
	}, "second.yaml", "data")
	_, diags := MergeDiscovered([]ArgoSettings{first, second})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "conflicting resource customization") {
		t.Fatalf("diagnostics = %#v, want one conflict", diags)
	}
	if got := diags[0].Provenance; got.Path != "second.yaml" || got.Pointer != "data.resource.customizations.health.Service" {
		t.Fatalf("conflict provenance = %#v, want the second health split key", got)
	}

	third := DefaultSettings()
	applyCMMap(&third, map[string]string{
		"resource.customizations.knownTypeFields.Service": "- field: spec\n  type: core/v1/PodSpec\n",
	}, "third.yaml", "data")
	merged, diags := MergeDiscovered([]ArgoSettings{first, third})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	customization := merged.ResourceCustomizations["Service"]
	if got := customization.FieldProvenance(overrideFieldKnownTypeFields); got.Path != "third.yaml" {
		t.Fatalf("knownTypeFields provenance = %#v, want third.yaml", got)
	}
	if got := customization.HealthLuaProvenance(); got.Path != "first.yaml" || got.Pointer != "data.resource.customizations" {
		t.Fatalf("health Lua provenance = %#v, want the first block", got)
	}
}
