package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

func TestRenderApplicationCopiesProviderObjectsBeforeMutation(t *testing.T) {
	fixture := cm("shared", "fixture")
	renderers := StaticRenderers{
		"manifests": []render.Manifest{{Object: fixture}},
	}

	first := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "first"},
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "first-ns"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "manifests"},
		},
	}
	second := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "second"},
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "second-ns"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "manifests"},
		},
	}

	firstResult, err := RenderApplication(context.Background(), first, renderers)
	if err != nil {
		t.Fatalf("RenderApplication(first) error = %v", err)
	}
	secondResult, err := RenderApplication(context.Background(), second, renderers)
	if err != nil {
		t.Fatalf("RenderApplication(second) error = %v", err)
	}

	if namespace := firstResult.Manifests[0].Object.GetNamespace(); namespace != "first-ns" {
		t.Fatalf("first namespace = %q, want first-ns", namespace)
	}
	if namespace := secondResult.Manifests[0].Object.GetNamespace(); namespace != "second-ns" {
		t.Fatalf("second namespace = %q, want second-ns", namespace)
	}
	if namespace := fixture.GetNamespace(); namespace != "" {
		t.Fatalf("fixture namespace = %q, want empty", namespace)
	}
}

func TestRenderApplicationPassesHelmValues(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "chart",
				Helm: &argoappv1.ApplicationSourceHelm{
					Values: "value: from-values\nnested:\n  from: values\n",
				},
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, _ render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		got = opts
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if got.ValuesObject["value"] != "from-values" {
		t.Fatalf("ValuesObject[value] = %#v, want from-values", got.ValuesObject["value"])
	}
	nested, ok := got.ValuesObject["nested"].(map[string]any)
	if !ok {
		t.Fatalf("ValuesObject[nested] = %#v, want map", got.ValuesObject["nested"])
	}
	if nested["from"] != "values" {
		t.Fatalf("ValuesObject[nested][from] = %#v, want values", nested["from"])
	}
}

func TestRenderApplicationPassesHelmIgnoreMissingValueFiles(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "chart",
				Helm: &argoappv1.ApplicationSourceHelm{
					ValueFiles:              []string{"optional.yaml"},
					IgnoreMissingValueFiles: true,
				},
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, _ render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		got = opts
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if !got.IgnoreMissingValueFiles {
		t.Fatalf("IgnoreMissingValueFiles = false, want true")
	}
	if len(got.ValueFiles) != 1 || got.ValueFiles[0] != "optional.yaml" {
		t.Fatalf("ValueFiles = %#v, want optional.yaml", got.ValueFiles)
	}
}

func TestRenderApplicationValuesObjectOverridesHelmValues(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "chart",
				Helm: &argoappv1.ApplicationSourceHelm{
					Values: "value: from-values\nonlyValues: should-not-survive\nnested:\n  from: values\n  onlyValues: should-not-survive\n",
					ValuesObject: &runtime.RawExtension{Raw: []byte(`{
						"value": "from-values-object",
						"nested": {"from": "values-object"}
					}`)},
				},
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, _ render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		got = opts
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if got.ValuesObject["value"] != "from-values-object" {
		t.Fatalf("ValuesObject[value] = %#v, want from-values-object", got.ValuesObject["value"])
	}
	if _, ok := got.ValuesObject["onlyValues"]; ok {
		t.Fatalf("ValuesObject[onlyValues] is present; valuesObject should replace values")
	}
	nested, ok := got.ValuesObject["nested"].(map[string]any)
	if !ok {
		t.Fatalf("ValuesObject[nested] = %#v, want map", got.ValuesObject["nested"])
	}
	if nested["from"] != "values-object" {
		t.Fatalf("ValuesObject[nested][from] = %#v, want values-object", nested["from"])
	}
	if _, ok := nested["onlyValues"]; ok {
		t.Fatalf("ValuesObject[nested][onlyValues] is present; valuesObject should replace nested values")
	}
}

func TestRenderApplicationRejectsNonMappingHelmValues(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "chart",
				Helm: &argoappv1.ApplicationSourceHelm{
					Values: "- not\n- a\n- mapping\n",
				},
			},
		},
	}

	_, err := RenderApplication(context.Background(), application, StaticRenderers{})
	if err == nil {
		t.Fatalf("expected helm values error")
	}
	if !strings.Contains(err.Error(), "helm values must be a YAML mapping") {
		t.Fatalf("error = %q, want YAML mapping context", err.Error())
	}
	if !strings.Contains(err.Error(), "Application argocd/demo source[0]") {
		t.Fatalf("error = %q, want application source context", err.Error())
	}
}

func TestRenderApplicationProviderErrorIncludesSourceContext(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://repo", Path: "apps/main", Name: "main"},
			},
		},
	}
	provider := providerFunc(func(context.Context, render.ResolvedSource, render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		return nil, nil, errors.New("provider failed")
	})

	_, err := RenderApplication(context.Background(), application, provider)
	if err == nil {
		t.Fatalf("expected provider error")
	}
	for _, want := range []string{"Application argocd/demo", "source[0]", `name="main"`, `path="apps/main"`, "provider failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}

func TestRenderApplicationDiagnosticsIncludeSourceContextAndPreserveProvenance(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://repo", Path: "apps/main", Name: "main"},
			},
		},
	}
	provider := providerFunc(func(context.Context, render.ResolvedSource, render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		return nil, []diagnostic.Diagnostic{{
			Severity: diagnostic.SeverityWarning,
			Category: "provider-warning",
			Message:  "original warning",
			Provenance: diagnostic.Provenance{
				Path:    "apps/main/config.yaml",
				Pointer: "/spec/template",
			},
		}}, nil
	})

	result, err := RenderApplication(context.Background(), application, provider)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1", len(result.Diagnostics))
	}
	got := result.Diagnostics[0]
	for _, want := range []string{"Application argocd/demo", "source[0]", `name="main"`, `path="apps/main"`, "original warning"} {
		if !strings.Contains(got.Message, want) {
			t.Fatalf("diagnostic message = %q, want %q", got.Message, want)
		}
	}
	if got.Provenance.Path != "apps/main/config.yaml" {
		t.Fatalf("Provenance.Path = %q, want provider path", got.Provenance.Path)
	}
	if got.Provenance.Pointer != "/spec/template" {
		t.Fatalf("Provenance.Pointer = %q, want provider pointer", got.Provenance.Pointer)
	}
}

func TestRenderApplicationSkipsRefOnlySources(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://values", Ref: "values"},
				{RepoURL: "https://repo", Path: "apps/main"},
			},
		},
	}
	var calls []string
	provider := providerFunc(func(_ context.Context, source render.ResolvedSource, _ render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		calls = append(calls, source.Path)
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(calls) != 1 || calls[0] != "apps/main" {
		t.Fatalf("calls = %#v, want only apps/main", calls)
	}
}

func TestRenderApplicationRendersSingleSourceFallback(t *testing.T) {
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "single"},
		},
	}
	renderers := StaticRenderers{
		"single": []render.Manifest{{Object: cm("only", "value")}},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if result.Manifests[0].Object.GetNamespace() != "default" {
		t.Fatalf("namespace = %q, want default", result.Manifests[0].Object.GetNamespace())
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

type providerFunc func(context.Context, render.ResolvedSource, render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error)

func (f providerFunc) RenderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return f(ctx, source, opts)
}
