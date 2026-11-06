package luahealth

import (
	"context"
	"testing"

	"github.com/sholdee/drydock/internal/render"
)

// TestHealthDiagnosticUsesHealthLuaProvenance checks that a health Lua
// failure points at the argocd-cm entry that holds the script, not at a
// split key that sets another field of the same customization.
func TestHealthDiagnosticUsesHealthLuaProvenance(t *testing.T) {
	for name, script := range map[string]string{
		"runtime error": "error(\"broken\")\n",
		"syntax error":  "return {\n",
	} {
		t.Run(name, func(t *testing.T) {
			block := "example.com/Widget:\n  health.lua: |\n    " + script
			settings := loadConfigMapSettings(t, map[string]string{
				"resource.customizations":                                      block,
				"resource.customizations.ignoreDifferences.example.com_Widget": "jsonPointers: [/a]\n",
			})
			diags := New(settings).Validate(context.Background(), Request{
				Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
				Manifests:   []render.Manifest{{Object: object("example.com/v1", "Widget", "default", "demo")}},
			})
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %#v, want one", diags)
			}
			if got := diags[0].Provenance.Pointer; got != "data.resource.customizations" {
				t.Fatalf("provenance pointer = %q, want data.resource.customizations", got)
			}
		})
	}
}
