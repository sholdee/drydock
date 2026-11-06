package luahealth

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/argoproj/argo-cd/gitops-engine/v3/pkg/health"
	argolua "github.com/argoproj/argo-cd/v3/util/lua"
	argosettings "github.com/argoproj/argo-cd/v3/util/settings"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/render"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
	sigsyaml "sigs.k8s.io/yaml"
)

// TestHealthLuaFromSettingsParityWithArgoCD loads the same argocd-cm data
// through Argo CD v3.6.1 and through drydock, then checks one resource.
// Argo CD builds the overrides with SettingsManager.GetResourceOverrides and
// runs util/lua ResourceHealthOverrides.GetResourceHealth on them. drydock
// loads the ConfigMap with config.LoadFromConfigMap and runs the Evaluator.
// drydock must report a health diagnostic exactly when Argo CD fails or
// returns its invalid-status result.
func TestHealthLuaFromSettingsParityWithArgoCD(t *testing.T) {
	const failing = "error(\"wildcard script ran\")\n"
	for _, tc := range []struct {
		name string
		data map[string]string
		// wantFail is what Argo CD must give. It is checked so that a case
		// cannot pass because both sides changed.
		wantFail bool
	}{
		{name: "white space split key hides wildcard", data: map[string]string{
			"resource.customizations.health.example.com_Widget": "  \n",
			"resource.customizations.health.example.com_*":      failing,
		}},
		{name: "white space block entry hides wildcard", data: map[string]string{
			"resource.customizations":                      "example.com/Widget:\n  health.lua: \"  \"\n",
			"resource.customizations.health.example.com_*": failing,
		}},
		{name: "comment only script hides wildcard", data: map[string]string{
			"resource.customizations.health.example.com_Widget": "-- nothing\n",
			"resource.customizations.health.example.com_*":      failing,
		}},
		{name: "white space only script", data: map[string]string{
			"resource.customizations.health.example.com_Widget": "\n\n",
		}},
		{name: "white space wildcard", data: map[string]string{
			"resource.customizations.health.example.com_*": " ",
		}},
		{name: "empty split key does not hide wildcard", data: map[string]string{
			"resource.customizations.health.example.com_Widget": "",
			"resource.customizations.health.example.com_*":      failing,
		}, wantFail: true},
		{name: "invalid status", data: map[string]string{
			"resource.customizations.health.example.com_Widget": "return { status = \"Bogus\" }\n",
		}, wantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := object("example.com/v1", "Widget", "default", "demo")

			upstreamFail := upstreamHealthFails(t, tc.data, obj.DeepCopy())
			if upstreamFail != tc.wantFail {
				t.Fatalf("Argo CD fails = %v, want %v", upstreamFail, tc.wantFail)
			}

			settings := loadConfigMapSettings(t, tc.data)
			diags := New(settings).Validate(context.Background(), Request{
				Application: ApplicationRef{Name: "demo", Namespace: "argocd"},
				Manifests:   []render.Manifest{{Object: obj}},
			})
			if (len(diags) > 0) != upstreamFail {
				t.Fatalf("drydock diagnostics = %#v, Argo CD fails = %v", diags, upstreamFail)
			}
		})
	}
}

func upstreamHealthFails(t *testing.T, data map[string]string, obj *unstructured.Unstructured) bool {
	t.Helper()
	cmData := map[string]string{"resource.compareoptions": "ignoreResourceStatusField: none\n"}
	maps.Copy(cmData, data)
	cm := &corev1.ConfigMap{Data: cmData}
	cm.Name = "argocd-cm"
	cm.Namespace = "argocd"
	cm.Labels = map[string]string{"app.kubernetes.io/part-of": "argocd"}
	manager := argosettings.NewSettingsManager(t.Context(), fake.NewClientset(cm), "argocd")
	overrides, err := manager.GetResourceOverrides()
	if err != nil {
		t.Fatalf("GetResourceOverrides() error = %v", err)
	}
	status, err := argolua.ResourceHealthOverrides(overrides).GetResourceHealth(obj)
	if err != nil {
		return true
	}
	return status != nil && status.Status == health.HealthStatusUnknown && status.Message == invalidHealthStatusMessage
}

func loadConfigMapSettings(t *testing.T, data map[string]string) config.ArgoSettings {
	t.Helper()
	doc, err := sigsyaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "argocd-cm"},
		"data":       data,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	settings, diags, err := config.LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	for _, diag := range diags {
		if diag.Severity == "error" {
			t.Fatalf("LoadFromConfigMap() diagnostics = %#v, want no error", diags)
		}
	}
	return settings
}
