package app

import (
	"context"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderApplicationLastSourceWins(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://one", Path: "one"},
				{RepoURL: "https://two", Path: "two"},
			},
		},
	}
	renderers := StaticRenderers{
		"one": []render.Manifest{{Object: cm("same", "old")}},
		"two": []render.Manifest{{Object: cm("same", "new")}},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	value, _, _ := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "value")
	if value != "new" {
		t.Fatalf("value = %q, want new", value)
	}
	if namespace := result.Manifests[0].Object.GetNamespace(); namespace != "default" {
		t.Fatalf("namespace = %q, want default", namespace)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Category != "repeated-resource" {
		t.Fatalf("diagnostic category = %q, want repeated-resource", result.Diagnostics[0].Category)
	}
}

func cm(name, value string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": name,
		},
		"data": map[string]any{
			"value": value,
		},
	}}
}
