package drydock

import (
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderResultFromBuildGoldenClonesManifestsAndStabilizesDiagnostics(t *testing.T) {
	object := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "demo",
		},
		"data": map[string]any{
			"value": "before",
			"items": []any{"one"},
		},
	}
	result := renderResultFromBuild(app.BuildResult{
		Applications: []argoappv1.Application{{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "argocd"},
			Spec:       argoappv1.ApplicationSpec{Project: "default"},
		}},
		ApplicationManifests: []app.ApplicationManifest{{
			Application: argoappv1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "argocd"},
				Spec:       argoappv1.ApplicationSpec{Project: "default"},
			},
			Manifest: render.Manifest{
				SourceIndex: 1,
				SourceName:  "values",
				Path:        "apps/demo",
				Object:      &unstructured.Unstructured{Object: object},
			},
		}},
		Diagnostics: []diagnostic.Diagnostic{{
			Severity: diagnostic.SeverityError,
			Category: "render",
			Message:  "render failed",
			Provenance: diagnostic.Provenance{
				Path: "app.yaml",
			},
		}},
	})

	if len(result.Applications) != 1 || result.Applications[0].Project != "default" {
		t.Fatalf("Applications = %#v, want default project", result.Applications)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Application.Name != "demo" {
		t.Fatalf("manifest Application.Name = %q, want demo", manifest.Application.Name)
	}
	if manifest.SourceIndex != 1 || manifest.SourceName != "values" || manifest.SourcePath != "apps/demo" {
		t.Fatalf("manifest source = %#v, want values source", manifest)
	}

	object["data"].(map[string]any)["value"] = "after"
	object["data"].(map[string]any)["items"].([]any)[0] = "changed"
	data := manifest.Object["data"].(map[string]any)
	if data["value"] != "before" {
		t.Fatalf("public manifest data.value = %q, want cloned before value", data["value"])
	}
	if data["items"].([]any)[0] != "one" {
		t.Fatalf("public manifest data.items[0] = %q, want cloned item", data["items"].([]any)[0])
	}

	if len(result.Diagnostics) != 1 {
		t.Fatalf("Diagnostics = %#v, want one diagnostic", result.Diagnostics)
	}
	diag := result.Diagnostics[0]
	if diag.Code == "" {
		t.Fatalf("diagnostic Code = empty, want stable code")
	}
	if diag.Severity != "error" || diag.Category != "render" || diag.Provenance.Path != "app.yaml" {
		t.Fatalf("diagnostic = %#v, want public render diagnostic", diag)
	}
}
