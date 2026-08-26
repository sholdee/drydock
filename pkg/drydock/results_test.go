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
			Name: "demo", Namespace: "argocd",
			Spec: argoappv1.ApplicationSpec{Project: "default"},
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
		PluginExecutions: []app.PluginExecution{{
			AppNamespace: "argocd",
			AppName:      "demo",
			SourceIndex:  1,
			SourceName:   "values",
			SourcePath:   "apps/demo",
			PluginName:   "exec-renderer",
			Engine:       "exec",
			Runtime:      "docker",
			Image:        "registry.example.test/plugins/render@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Phase:        "generate",
			Command:      "renderer",
			Duration:     "12ms",
		}},
	})

	assertGoldenResultApplication(t, result.Applications)
	manifest := assertGoldenResultManifest(t, result.Manifests)
	originalData, ok := object["data"].(map[string]any)
	if !ok {
		t.Fatalf("original data = %#v, want map", object["data"])
	}
	originalItems, ok := originalData["items"].([]any)
	if !ok {
		t.Fatalf("original items = %#v, want slice", originalData["items"])
	}
	originalData["value"] = "after"
	originalItems[0] = "changed"
	data, ok := manifest.Object["data"].(map[string]any)
	if !ok {
		t.Fatalf("public manifest data = %#v, want map", manifest.Object["data"])
	}
	assertGoldenResultManifestClone(t, data)
	assertGoldenResultDiagnostic(t, result.Diagnostics)
	assertGoldenResultPluginExecution(t, result.PluginExecutions)
}

func assertGoldenResultApplication(t *testing.T, applications []Application) {
	t.Helper()
	if len(applications) != 1 || applications[0].Project != "default" {
		t.Fatalf("Applications = %#v, want default project", applications)
	}
}

func assertGoldenResultManifest(t *testing.T, manifests []Manifest) Manifest {
	t.Helper()
	if len(manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(manifests))
	}
	manifest := manifests[0]
	if manifest.Application.Name != "demo" {
		t.Fatalf("manifest Application.Name = %q, want demo", manifest.Application.Name)
	}
	if manifest.SourceIndex != 1 || manifest.SourceName != "values" || manifest.SourcePath != "apps/demo" {
		t.Fatalf("manifest source = %#v, want values source", manifest)
	}
	return manifest
}

func assertGoldenResultManifestClone(t *testing.T, data map[string]any) {
	t.Helper()
	if data["value"] != "before" {
		t.Fatalf("public manifest data.value = %q, want cloned before value", data["value"])
	}
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("public manifest data.items = %#v, want slice", data["items"])
	}
	if items[0] != "one" {
		t.Fatalf("public manifest data.items[0] = %q, want cloned item", items[0])
	}
}

func assertGoldenResultDiagnostic(t *testing.T, diagnostics []Diagnostic) {
	t.Helper()
	if len(diagnostics) != 1 {
		t.Fatalf("Diagnostics = %#v, want one diagnostic", diagnostics)
	}
	diag := diagnostics[0]
	if diag.Code == "" {
		t.Fatalf("diagnostic Code = empty, want stable code")
	}
	if diag.Severity != "error" || diag.Category != "render" || diag.Provenance.Path != "app.yaml" {
		t.Fatalf("diagnostic = %#v, want public render diagnostic", diag)
	}
}

func assertGoldenResultPluginExecution(t *testing.T, executions []PluginExecution) {
	t.Helper()
	if len(executions) != 1 {
		t.Fatalf("PluginExecutions = %#v, want one execution", executions)
	}
	execution := executions[0]
	if execution.Application.Namespace != "argocd" || execution.Application.Name != "demo" {
		t.Fatalf("PluginExecution.Application = %#v, want argocd/demo", execution.Application)
	}
	if execution.SourceIndex != 1 || execution.SourceName != "values" || execution.SourcePath != "apps/demo" {
		t.Fatalf("PluginExecution source = %#v, want values source", execution)
	}
	if execution.PluginName != "exec-renderer" || execution.Engine != "exec" || execution.Phase != "generate" || execution.Command != "renderer" || execution.Duration != "12ms" {
		t.Fatalf("PluginExecution = %#v, want exec metadata", execution)
	}
	if execution.Runtime != "docker" || execution.Image != "registry.example.test/plugins/render@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("PluginExecution = %#v, want runtime/image metadata", execution)
	}
}
