package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	sigsyaml "sigs.k8s.io/yaml"
)

func TestRenderApplicationLastSourceWins(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo",
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

func TestRenderOptionsCopiesSourceKustomizeAndArgoEnv(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Project:     "k3s",
			Destination: argoappv1.ApplicationDestination{Namespace: "workloads"},
		},
	}
	source := argoappv1.ApplicationSource{
		RepoURL:        "https://example.invalid/repo.git",
		Path:           "apps/demo",
		TargetRevision: "1234567890abcdef",
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			NamePrefix:  "source-",
			KubeVersion: "1.30.1",
			APIVersions: []string{"example.com/v1/Foo"},
		},
	}

	opts, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if opts.Kustomize == source.Kustomize {
		t.Fatal("renderOptions() reused source Kustomize pointer")
	}
	source.Kustomize.NamePrefix = "mutated-"
	if opts.Kustomize.NamePrefix != "source-" {
		t.Fatalf("Kustomize.NamePrefix = %q, want copied value", opts.Kustomize.NamePrefix)
	}
	if opts.KubeVersion != "1.30.1" {
		t.Fatalf("KubeVersion = %q, want source kustomize kube version", opts.KubeVersion)
	}
	if len(opts.APIVersions) != 1 || opts.APIVersions[0] != "example.com/v1/Foo" {
		t.Fatalf("APIVersions = %#v", opts.APIVersions)
	}
	if got := opts.ArgoEnv.Envsubst("$ARGOCD_APP_NAME:$ARGOCD_APP_NAMESPACE:$ARGOCD_APP_PROJECT_NAME:$ARGOCD_APP_SOURCE_PATH:$ARGOCD_APP_REVISION_SHORT_8"); got != "demo:workloads:k3s:apps/demo:12345678" {
		t.Fatalf("ArgoEnv substitution = %q", got)
	}
}

func TestParseKubeVersionNormalizesHelmSuffix(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	source := argoappv1.ApplicationSource{
		Helm: &argoappv1.ApplicationSourceHelm{
			KubeVersion: "1.32.1+parity",
		},
	}

	opts, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if opts.KubeVersion != "1.32.1" {
		t.Fatalf("KubeVersion = %q, want 1.32.1", opts.KubeVersion)
	}
}

func TestParseKubeVersionNormalizesKustomizeSuffix(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	source := argoappv1.ApplicationSource{
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			KubeVersion: "1.30.11+IKS",
		},
	}

	opts, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if opts.KubeVersion != "1.30.11" {
		t.Fatalf("KubeVersion = %q, want 1.30.11", opts.KubeVersion)
	}
}

func TestParseKubeVersionEmptyStaysEmpty(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	source := argoappv1.ApplicationSource{
		Helm: &argoappv1.ApplicationSourceHelm{},
	}

	opts, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if opts.KubeVersion != "" {
		t.Fatalf("KubeVersion = %q, want empty", opts.KubeVersion)
	}
}

func TestParseKubeVersionInvalidReturnsError(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
	source := argoappv1.ApplicationSource{
		Helm: &argoappv1.ApplicationSourceHelm{
			KubeVersion: "not-a-version",
		},
	}

	_, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err == nil {
		t.Fatal("renderOptions() error = nil, want kube version parse error")
	}
	if !strings.Contains(err.Error(), "not-a-version") {
		t.Fatalf("renderOptions() error = %v, want version string in error", err)
	}
}

func TestRenderOptionsCopiesDirectoryJsonnet(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
	}
	source := argoappv1.ApplicationSource{
		Directory: &argoappv1.ApplicationSourceDirectory{
			Jsonnet: argoappv1.ApplicationSourceJsonnet{
				ExtVars: []argoappv1.JsonnetVar{{Name: "name", Value: "from-source"}},
				TLAs:    []argoappv1.JsonnetVar{{Name: "namespace", Value: "default"}},
				Libs:    []string{"lib"},
			},
		},
	}

	opts, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	source.Directory.Jsonnet.ExtVars[0].Value = "mutated"
	source.Directory.Jsonnet.TLAs[0].Value = "mutated"
	source.Directory.Jsonnet.Libs[0] = "mutated"

	if opts.Jsonnet.ExtVars[0].Value != "from-source" {
		t.Fatalf("Jsonnet.ExtVars[0].Value = %q, want from-source", opts.Jsonnet.ExtVars[0].Value)
	}
	if opts.Jsonnet.TLAs[0].Value != "default" {
		t.Fatalf("Jsonnet.TLAs[0].Value = %q, want default", opts.Jsonnet.TLAs[0].Value)
	}
	if opts.Jsonnet.Libs[0] != "lib" {
		t.Fatalf("Jsonnet.Libs[0] = %q, want lib", opts.Jsonnet.Libs[0])
	}
}

func TestRenderOptionsAppliesCapabilityOverride(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{Destination: argoappv1.ApplicationDestination{Namespace: "demo"}},
	}
	source := argoappv1.ApplicationSource{
		Helm: &argoappv1.ApplicationSourceHelm{KubeVersion: "1.30.0", APIVersions: []string{"per-app.example.com/v1", "monitoring.coreos.com/v1"}},
	}
	opts, err := renderOptions(application, source, CapabilityOptions{
		KubeVersion: "1.34.0",
		APIVersions: []string{"monitoring.coreos.com/v1", "gateway.networking.k8s.io/v1"},
	}, defaultTrackingOptions())
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if opts.KubeVersion != "1.34.0" {
		t.Fatalf("KubeVersion = %q, want 1.34.0 (override wins)", opts.KubeVersion)
	}
	want := []string{"gateway.networking.k8s.io/v1", "monitoring.coreos.com/v1", "per-app.example.com/v1"}
	if !reflect.DeepEqual(opts.APIVersions, want) {
		t.Fatalf("APIVersions = %v, want deduped+sorted union %v", opts.APIVersions, want)
	}
}

func TestRenderOptionsNoCapabilityOverrideLeavesKubeVersionEmpty(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{Destination: argoappv1.ApplicationDestination{Namespace: "demo"}},
	}
	source := argoappv1.ApplicationSource{Helm: &argoappv1.ApplicationSourceHelm{}}
	opts, err := renderOptions(application, source, CapabilityOptions{}, defaultTrackingOptions())
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if opts.KubeVersion != "" {
		t.Fatalf("KubeVersion = %q, want empty (no override, no forced default)", opts.KubeVersion)
	}
}

func TestRenderApplicationCopiesProviderObjectsBeforeMutation(t *testing.T) {
	fixture := cm("shared", "fixture")
	renderers := StaticRenderers{
		"manifests": []render.Manifest{{Object: fixture}},
	}

	first := argoappv1.Application{
		Namespace: "argocd", Name: "first",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "first-ns"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "manifests"},
		},
	}
	second := argoappv1.Application{
		Namespace: "argocd", Name: "second",
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
		Namespace: "argocd", Name: "demo",
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

func TestRenderApplicationPassesAVPCompatibilityOption(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "manifests/demo",
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, _ render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		got = opts
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider, PluginOptions{EnableAVPCompat: true}); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if !got.EnableAVPCompat {
		t.Fatal("RenderOptions.EnableAVPCompat = false, want true")
	}
}

func TestRenderApplicationPassesKSOPSCompatibilityOption(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "manifests/demo",
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, _ render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		got = opts
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider, PluginOptions{EnableKSOPSCompat: true}); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if !got.EnableKSOPSCompat {
		t.Fatal("RenderOptions.EnableKSOPSCompat = false, want true")
	}
}

func TestRenderApplicationAVPCompatibilityReplacesRenderedManifestPlaceholders(t *testing.T) {
	fixture := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "demo",
		},
		"data": map[string]any{
			"domain": "argocd.<path:vaults/Kubernetes/items/cluster#domain>",
		},
	}}
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "manifests/demo",
			},
		},
	}
	renderers := StaticRenderers{
		"manifests/demo": []render.Manifest{{Object: fixture}},
	}

	result, err := RenderApplication(context.Background(), application, renderers, PluginOptions{EnableAVPCompat: true})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	value, _, _ := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "domain")
	if !strings.HasPrefix(value, "argocd.drydock-redacted-") {
		t.Fatalf("data.domain = %q, want redacted AVP value", value)
	}
	for _, forbidden := range []string{"vaults", "Kubernetes", "cluster", "<path:"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("data.domain = %q contains forbidden placeholder material %q", value, forbidden)
		}
	}
	originalValue, _, _ := unstructured.NestedString(fixture.Object, "data", "domain")
	if originalValue != "argocd.<path:vaults/Kubernetes/items/cluster#domain>" {
		t.Fatalf("fixture data.domain = %q, want provider object unchanged", originalValue)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, want AVP compatibility diagnostic", result.Diagnostics)
	}
}

func renderAVPCompatFixture(t *testing.T, fixture map[string]any) *unstructured.Unstructured {
	t.Helper()
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "manifests/demo",
			},
		},
	}
	renderers := StaticRenderers{
		"manifests/demo": []render.Manifest{{Object: &unstructured.Unstructured{Object: fixture}}},
	}
	result, err := RenderApplication(context.Background(), application, renderers, PluginOptions{EnableAVPCompat: true})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	return result.Manifests[0].Object
}

// labelTracking renders with Argo CD's label tracking method. A manifest
// holding a YAML-null annotation value never reaches Argo CD's output under
// the default annotation method: util/kube/kube.go SetAppInstanceAnnotation
// reads the map with nestedNullableStringMap and fails on the null, in Argo
// CD and in drydock alike. Label tracking touches only metadata.labels, so
// it is the configuration under which AVP's handling of such a manifest is
// observable.
var labelTracking = TrackingOptions{Method: string(argoappv1.TrackingMethodLabel)}

func renderAVPCompatManifests(t *testing.T, tracking TrackingOptions, manifests ...render.Manifest) RenderResult {
	t.Helper()
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "manifests/demo",
			},
		},
	}
	result, err := RenderApplicationWithOptions(context.Background(), application, StaticRenderers{"manifests/demo": manifests}, ApplicationRenderOptions{
		PluginOptions:   PluginOptions{EnableAVPCompat: true},
		TrackingOptions: tracking,
	})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	return result
}

func renderAVPCompatFixtureLabelTracked(t *testing.T, fixture map[string]any) *unstructured.Unstructured {
	t.Helper()
	result := renderAVPCompatManifests(t, labelTracking, render.Manifest{Object: &unstructured.Unstructured{Object: fixture}})
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	return result.Manifests[0].Object
}

// AVP switches to its generic `(?mU)<(.*)>` regex on the presence of the
// avp.kubernetes.io/path annotation KEY (pkg/kube/util.go:113-115). With an
// empty value no backend data is fetched (pkg/kube/template.go:33-48), so a
// generic key stays in place while an inline token matched through the
// generic span is substituted; the stray '<' before it is part of that span.
func TestRenderApplicationAVPCompatibilityEmptyPathAnnotationSelectsGenericRegex(t *testing.T) {
	got := renderAVPCompatFixture(t, map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":        "demo",
			"annotations": map[string]any{"avp.kubernetes.io/path": ""},
		},
		"data": map[string]any{
			"generic": "<foo>",
			"inline":  "a < b <path:x#y>",
		},
	})
	generic, _, _ := unstructured.NestedString(got.Object, "data", "generic")
	if generic != "<foo>" {
		t.Fatalf("data.generic = %q, want unchanged <foo> under an empty path annotation", generic)
	}
	inline, _, _ := unstructured.NestedString(got.Object, "data", "inline")
	if want := "a drydock-redacted-ed49687a5044"; inline != want {
		t.Fatalf("data.inline = %q, want %q (generic span from the stray '<')", inline, want)
	}
}

// AVP picks the base64-aware secretReplacement from the top-level kind
// string (pkg/kube/template.go:70-77) and applies it to every string in the
// object (pkg/kube/util.go:32-103, 217-227): a value that decodes as standard
// base64 to text with a <...> placeholder is substituted in the decoded text
// and re-encoded. ConfigMap values are never decoded (util.go:205-215).
func TestRenderApplicationAVPCompatibilityDecodesSecretBase64Values(t *testing.T) {
	const encoded = "PHBhdGg6YSNiPg=="                      // base64("<path:a#b>")
	const want = "ZHJ5ZG9jay1yZWRhY3RlZC1jMmI2OTdhNmU0MWE=" // base64("drydock-redacted-c2b697a6e41a")

	secret := renderAVPCompatFixture(t, map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":   "demo",
			"labels": map[string]any{"x": encoded},
		},
		"type":       "Opaque",
		"data":       map[string]any{"k": encoded},
		"stringData": map[string]any{"k": "<path:a#b>"},
	})
	if got, _, _ := unstructured.NestedString(secret.Object, "data", "k"); got != want {
		t.Fatalf("Secret data.k = %q, want %q", got, want)
	}
	if got, _, _ := unstructured.NestedString(secret.Object, "metadata", "labels", "x"); got != want {
		t.Fatalf("Secret metadata.labels.x = %q, want %q (AVP walks metadata too)", got, want)
	}
	if got, _, _ := unstructured.NestedString(secret.Object, "stringData", "k"); got != "drydock-redacted-c2b697a6e41a" {
		t.Fatalf("Secret stringData.k = %q, want plain marker", got)
	}

	configMap := renderAVPCompatFixture(t, map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "demo"},
		"data":       map[string]any{"k": encoded},
		"binaryData": map[string]any{"k": encoded},
	})
	for _, field := range []string{"data", "binaryData"} {
		if got, _, _ := unstructured.NestedString(configMap.Object, field, "k"); got != encoded {
			t.Fatalf("ConfigMap %s.k = %q, want unchanged %q", field, got, encoded)
		}
	}
}

// AVP v1.18.1 pins k8s.io/apimachinery v0.29.1 (go.mod:33), whose
// GetAnnotations is NestedStringMap: a YAML-null annotation value makes the
// whole map nil, so the path annotation key reads as ABSENT and the specific
// `<path:...#...>` regex applies (pkg/kube/util.go:113-115). drydock's
// apimachinery would coerce the null to "" and keep the key, which selects
// the generic regex; the compat pass must not do that.
func TestRenderApplicationAVPCompatibilityNullPathAnnotationReadsAsAbsent(t *testing.T) {
	got := renderAVPCompatFixtureLabelTracked(t, map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":        "demo",
			"annotations": map[string]any{"avp.kubernetes.io/path": nil},
		},
		"data": map[string]any{
			"stray":    "a < b <path:x#y>",
			"generic":  "<foo path:x#y>",
			"fourpart": "<path:a#b#c#d>",
			"bracket":  "<path:a>b#c>",
		},
	})
	want := map[string]string{
		"stray":    "a < b drydock-redacted-ed49687a5044", // the stray '<' is not part of a specific-regex match
		"generic":  "<foo path:x#y>",                      // only the generic regex would match this
		"fourpart": "<path:a#b#c#d>",                      // the specific regex admits 2 or 3 parts
		"bracket":  "drydock-redacted-df5e0cacff05",       // specific regex: path "a>b", key "c"
	}
	for key, wantValue := range want {
		if value, _, _ := unstructured.NestedString(got.Object, "data", key); value != wantValue {
			t.Fatalf("data.%s = %q, want %q under a null path annotation", key, value, wantValue)
		}
	}
}

// A null value under ANY annotation key hides every annotation from AVP
// (NestedStringMap returns nil on the first non-string value), including a
// well-formed avp.kubernetes.io/path next to it. Helm charts render
// `annotation: {{ .Values.x }}` with an empty value exactly like this.
func TestRenderApplicationAVPCompatibilityNullSiblingAnnotationHidesPathAnnotation(t *testing.T) {
	got := renderAVPCompatFixtureLabelTracked(t, map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "demo",
			"annotations": map[string]any{
				"avp.kubernetes.io/path": "P",
				"foo":                    nil,
			},
		},
		"data": map[string]any{
			"generic": "<x>",
			"inline":  "a < b <path:x#y>",
		},
	})
	if generic, _, _ := unstructured.NestedString(got.Object, "data", "generic"); generic != "<x>" {
		t.Fatalf("data.generic = %q, want unchanged <x> (AVP reads no annotations here)", generic)
	}
	inline, _, _ := unstructured.NestedString(got.Object, "data", "inline")
	if want := "a < b drydock-redacted-ed49687a5044"; inline != want {
		t.Fatalf("data.inline = %q, want %q (specific regex keeps the stray '<')", inline, want)
	}
}

// cmd/generate.go:93-102: AVP skips Replace() when avp.kubernetes.io/ignore
// parses true with strconv.ParseBool and emits the object verbatim.
func TestRenderApplicationAVPCompatibilityHonoursIgnoreAnnotation(t *testing.T) {
	const placeholder = "<path:a#b>"
	const marker = "drydock-redacted-c2b697a6e41a"
	cases := []struct {
		name        string
		annotations map[string]any
		want        string
	}{
		{name: "true", annotations: map[string]any{"avp.kubernetes.io/ignore": "true"}, want: placeholder},
		{name: "True", annotations: map[string]any{"avp.kubernetes.io/ignore": "True"}, want: placeholder},
		{name: "1", annotations: map[string]any{"avp.kubernetes.io/ignore": "1"}, want: placeholder},
		{name: "t", annotations: map[string]any{"avp.kubernetes.io/ignore": "t"}, want: placeholder},
		{name: "false", annotations: map[string]any{"avp.kubernetes.io/ignore": "false"}, want: marker},
		{name: "unparsable", annotations: map[string]any{"avp.kubernetes.io/ignore": "yes"}, want: marker},
		{name: "ignore with path annotation", annotations: map[string]any{"avp.kubernetes.io/ignore": "true", "avp.kubernetes.io/path": "P"}, want: placeholder},
		// generate.go:93 reads the map through the same strict accessor, so a
		// null sibling hides the ignore annotation as well.
		{name: "ignore hidden by null sibling", annotations: map[string]any{"avp.kubernetes.io/ignore": "true", "other": nil}, want: marker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := renderAVPCompatManifests(t, labelTracking, render.Manifest{Object: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "demo", "annotations": tc.annotations},
				"data":       map[string]any{"k": placeholder},
			}}})
			if got, _, _ := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "k"); got != tc.want {
				t.Fatalf("data.k = %q, want %q", got, tc.want)
			}
			if wantDiag := tc.want == marker; hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") != wantDiag {
				t.Fatalf("avp-compat-substituted diagnostic present = %v, want %v (Diagnostics = %#v)", !wantDiag, wantDiag, result.Diagnostics)
			}
		})
	}
}

// AVP decodes a `kind: List` into ONE object (cmd/util.go:46-50): the
// replacer comes from the List's kind (pkg/kube/template.go:52,70-77, so a
// Secret item is never base64-decoded) and the regex from the List's
// annotations (template.go:32-33, util.go:113), while the walk still visits
// every string inside items (util.go:54-61). drydock flattens Lists before
// the compat pass and carries the List as Manifest.RootObject.
func TestRenderApplicationAVPCompatibilityUsesListRootContext(t *testing.T) {
	const encoded = "PHBhdGg6YSNiPg==" // base64("<path:a#b>")
	item := func(kind, name string, annotations map[string]any, data map[string]any) map[string]any {
		metadata := map[string]any{"name": name}
		if annotations != nil {
			metadata["annotations"] = annotations
		}
		return map[string]any{"apiVersion": "v1", "kind": kind, "metadata": metadata, "data": data}
	}
	list := func(annotations map[string]any, items ...map[string]any) *unstructured.Unstructured {
		object := map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{}}
		if annotations != nil {
			object["metadata"] = map[string]any{"annotations": annotations}
		}
		raw := make([]any, 0, len(items))
		for _, entry := range items {
			raw = append(raw, entry)
		}
		object["items"] = raw
		return &unstructured.Unstructured{Object: object}
	}
	// renderList flattens the List the way manifest.DecodeDocuments does:
	// one Manifest per item, each item map shared with the root's items.
	renderList := func(t *testing.T, annotations map[string]any, items ...map[string]any) []render.Manifest {
		t.Helper()
		root := list(annotations, items...)
		manifests := make([]render.Manifest, 0, len(items))
		for _, entry := range items {
			manifests = append(manifests, render.Manifest{Object: &unstructured.Unstructured{Object: entry}, RootObject: root})
		}
		return renderAVPCompatManifests(t, TrackingOptions{}, manifests...).Manifests
	}
	dataOf := func(t *testing.T, manifests []render.Manifest, name, key string) string {
		t.Helper()
		found, ok := manifestByName(manifests, name)
		if !ok {
			t.Fatalf("manifests = %#v, want %s", manifests, name)
		}
		value, _, _ := unstructured.NestedString(found.Object.Object, "data", key)
		return value
	}

	t.Run("unannotated List keeps item kind and annotations out of the context", func(t *testing.T) {
		manifests := renderList(t, nil,
			item("Secret", "secret", nil, map[string]any{"k": encoded, "plain": "<path:a#b>"}),
			item("ConfigMap", "configmap", map[string]any{"avp.kubernetes.io/path": "P"}, map[string]any{"k": "<x>", "inline": "<path:a#b>"}),
		)
		if got := dataOf(t, manifests, "secret", "k"); got != encoded {
			t.Fatalf("Secret item data.k = %q, want unchanged %q (the List is not a Secret)", got, encoded)
		}
		if got := dataOf(t, manifests, "secret", "plain"); got != "drydock-redacted-c2b697a6e41a" {
			t.Fatalf("Secret item data.plain = %q, want plain inline marker", got)
		}
		if got := dataOf(t, manifests, "configmap", "k"); got != "<x>" {
			t.Fatalf("ConfigMap item data.k = %q, want unchanged <x> (the List has no path annotation)", got)
		}
		if got := dataOf(t, manifests, "configmap", "inline"); got != "drydock-redacted-c2b697a6e41a" {
			t.Fatalf("ConfigMap item data.inline = %q, want inline marker", got)
		}
	})

	t.Run("List annotations govern every item", func(t *testing.T) {
		manifests := renderList(t, map[string]any{"avp.kubernetes.io/path": "P"},
			item("ConfigMap", "configmap", nil, map[string]any{"k": "<x>"}),
		)
		if got := dataOf(t, manifests, "configmap", "k"); got != "drydock-redacted-4870c4cc1dfc" {
			t.Fatalf("ConfigMap item data.k = %q, want generic marker for path:P#x", got)
		}
	})

	t.Run("List ignore annotation skips every item", func(t *testing.T) {
		manifests := renderList(t, map[string]any{"avp.kubernetes.io/ignore": "true"},
			item("ConfigMap", "configmap", nil, map[string]any{"k": "<path:a#b>"}),
		)
		if got := dataOf(t, manifests, "configmap", "k"); got != "<path:a#b>" {
			t.Fatalf("ConfigMap item data.k = %q, want unchanged placeholder", got)
		}
	})
}

// The same List semantics through the directory renderer, which is where
// drydock flattens the List and records its root.
func TestRenderApplicationAVPCompatibilityDirectoryListItemsUseListContext(t *testing.T) {
	root := t.TempDir()
	writeAppTestFile(t, filepath.Join(root, "apps", "demo", "list.yaml"), `
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Secret
    metadata:
      name: list-secret
    data:
      k: PHBhdGg6YSNiPg==
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: list-configmap
      annotations:
        avp.kubernetes.io/path: P
    data:
      k: "<x>"
      inline: "<path:a#b>"
`)
	application := rendererSelectionApplication("demo", argoappv1.ApplicationSource{
		RepoURL:   "https://repo",
		Path:      "apps/demo",
		Directory: &argoappv1.ApplicationSourceDirectory{},
	})

	result, err := RenderApplication(context.Background(), application, localProvider{repoRoot: root}, PluginOptions{EnableAVPCompat: true})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	secret, ok := manifestByName(result.Manifests, "list-secret")
	if !ok {
		t.Fatalf("manifests = %#v, want list-secret", result.Manifests)
	}
	if got, _, _ := unstructured.NestedString(secret.Object.Object, "data", "k"); got != "PHBhdGg6YSNiPg==" {
		t.Fatalf("Secret item data.k = %q, want unchanged base64 (AVP walks the List with genericReplacement)", got)
	}
	configMap, ok := manifestByName(result.Manifests, "list-configmap")
	if !ok {
		t.Fatalf("manifests = %#v, want list-configmap", result.Manifests)
	}
	if got, _, _ := unstructured.NestedString(configMap.Object.Object, "data", "k"); got != "<x>" {
		t.Fatalf("ConfigMap item data.k = %q, want unchanged <x> (the List carries no path annotation)", got)
	}
	if got, _, _ := unstructured.NestedString(configMap.Object.Object, "data", "inline"); got != "drydock-redacted-c2b697a6e41a" {
		t.Fatalf("ConfigMap item data.inline = %q, want inline marker", got)
	}
}

func TestRenderApplicationLeavesAVPPlaceholdersUnchangedByDefault(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "manifests/demo",
			},
		},
	}
	renderers := StaticRenderers{
		"manifests/demo": []render.Manifest{{
			Object: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name": "demo",
				},
				"data": map[string]any{
					"domain": "argocd.<path:vaults/Kubernetes/items/cluster#domain>",
				},
			}},
		}},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	value, _, _ := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "domain")
	if value != "argocd.<path:vaults/Kubernetes/items/cluster#domain>" {
		t.Fatalf("data.domain = %q, want raw placeholder", value)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, did not want AVP compatibility diagnostic", result.Diagnostics)
	}
}

func TestRenderApplicationPassesDirectoryOptions(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "manifests",
				Directory: &argoappv1.ApplicationSourceDirectory{
					Recurse: true,
					Include: "*.yaml",
					Exclude: "disabled/*",
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
	if !got.DirectoryRecurse {
		t.Fatalf("DirectoryRecurse = false, want true")
	}
	if got.DirectoryInclude != "*.yaml" {
		t.Fatalf("DirectoryInclude = %q, want *.yaml", got.DirectoryInclude)
	}
	if got.DirectoryExclude != "disabled/*" {
		t.Fatalf("DirectoryExclude = %q, want disabled/*", got.DirectoryExclude)
	}
}

func TestRenderApplicationPassesHelmIgnoreMissingValueFiles(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
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

func TestRenderApplicationPassesSameRepoRefRootsForHelmValueFiles(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: " https://example.com/repo.git/ ", Path: "some/path", Ref: "values"},
				{
					RepoURL: "https://example.com/repo",
					Path:    "chart",
					Helm: &argoappv1.ApplicationSourceHelm{
						ValueFiles: []string{"$values/foo.yaml"},
					},
				},
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		if source.Path == "chart" {
			got = opts
		}
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if got.RefRoots["$values"] != "." {
		t.Fatalf("RefRoots[$values] = %q, want .", got.RefRoots["$values"])
	}
	if len(got.RefSources) != 0 {
		t.Fatalf("RefSources = %#v, want empty same-repo refs", got.RefSources)
	}
	if len(got.ValueFiles) != 1 || got.ValueFiles[0] != "$values/foo.yaml" {
		t.Fatalf("ValueFiles = %#v, want $values/foo.yaml", got.ValueFiles)
	}
}

func TestRenderApplicationPassesSameRepoRefRootsForHelmFileParameters(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://example.com/repo", Path: "some/path", Ref: "values"},
				{
					RepoURL: "https://example.com/repo",
					Path:    "chart",
					Helm: &argoappv1.ApplicationSourceHelm{
						FileParameters: []argoappv1.HelmFileParameter{
							{Name: "fileValue", Path: "$values/foo.txt"},
						},
					},
				},
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		if source.Path == "chart" {
			got = opts
		}
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if got.RefRoots["$values"] != "." {
		t.Fatalf("RefRoots[$values] = %q, want .", got.RefRoots["$values"])
	}
	if len(got.RefSources) != 0 {
		t.Fatalf("RefSources = %#v, want empty same-repo refs", got.RefSources)
	}
	if len(got.HelmFileParameters) != 1 || got.HelmFileParameters[0].Path != "$values/foo.txt" {
		t.Fatalf("HelmFileParameters = %#v, want $values/foo.txt", got.HelmFileParameters)
	}
}

func TestRenderApplicationPassesSameRepoSiblingPathRefSourceForChartOnlyHelmValueFiles(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://example.com/repo", TargetRevision: "main", Ref: "values"},
				{RepoURL: "https://example.com/repo", TargetRevision: "main", Path: "manifests/anchor"},
				{
					RepoURL:        "https://charts.example.test",
					TargetRevision: "1.2.3",
					Chart:          "demo",
					Helm: &argoappv1.ApplicationSourceHelm{
						ValueFiles: []string{"$values/root-values.yaml"},
					},
				},
			},
		},
	}
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		if source.Chart == "demo" {
			got = opts
		}
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if got.RefRoots["$values"] != "" {
		t.Fatalf("RefRoots[$values] = %q, want empty path-bearing ref source", got.RefRoots["$values"])
	}
	refSource := got.RefSources["$values"]
	if refSource.Path != "manifests/anchor" {
		t.Fatalf("RefSources[$values].Path = %q, want manifests/anchor", refSource.Path)
	}
	if refSource.RepoURL != "https://example.com/repo" {
		t.Fatalf("RefSources[$values].RepoURL = %q, want same repo", refSource.RepoURL)
	}
	if refSource.TargetRevision != "main" {
		t.Fatalf("RefSources[$values].TargetRevision = %q, want main", refSource.TargetRevision)
	}
}

func TestRenderApplicationPassesCrossRepoHelmValueRef(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://values-user:values-secret@example.com/values.git?token=values-token#values-frag", Ref: "values"},
				{
					RepoURL: "https://source-user:source-secret@example.com/repo.git?token=source-token#source-frag",
					Path:    "chart",
					Helm: &argoappv1.ApplicationSourceHelm{
						ValueFiles: []string{"$values/foo.yaml"},
					},
				},
			},
		},
	}
	calls := 0
	var got render.RenderOptions
	provider := providerFunc(func(_ context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		calls++
		if source.Path == "chart" {
			got = opts
		}
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	refSource := got.RefSources["$values"]
	if refSource.Path != "" {
		t.Fatalf("RefSources[$values].Path = %q, want empty cross-repo path", refSource.Path)
	}
	if refSource.RepoURL != "https://values-user:values-secret@example.com/values.git?token=values-token#values-frag" {
		t.Fatalf("RefSources[$values].RepoURL = %q, want values repo", refSource.RepoURL)
	}
}

func TestRenderApplicationIgnoresUnusedCrossRepoRef(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://example.com/values", Ref: "values"},
				{
					RepoURL: "https://example.com/repo",
					Path:    "chart",
					Helm: &argoappv1.ApplicationSourceHelm{
						ValueFiles: []string{"local.yaml"},
					},
				},
			},
		},
	}
	calls := 0
	provider := providerFunc(func(_ context.Context, _ render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		calls++
		if len(opts.RefSources) != 0 {
			t.Fatalf("RefSources = %#v, want empty for unused ref", opts.RefSources)
		}
		return nil, nil, nil
	})

	if _, err := RenderApplication(context.Background(), application, provider); err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestRenderApplicationPassesHelmRenderSwitches(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "chart",
				Helm: &argoappv1.ApplicationSourceHelm{
					Parameters: []argoappv1.HelmParameter{
						{Name: "value", Value: "from-param", ForceString: true},
					},
					FileParameters: []argoappv1.HelmFileParameter{
						{Name: "fileValue", Path: "message.txt"},
					},
					SkipCrds:             true,
					SkipTests:            true,
					SkipSchemaValidation: true,
					PassCredentials:      true,
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
	if !got.IncludeCRDsSet {
		t.Fatalf("IncludeCRDsSet = false, want true")
	}
	if got.IncludeCRDs {
		t.Fatalf("IncludeCRDs = true, want false")
	}
	if !got.SkipTests {
		t.Fatalf("SkipTests = false, want true")
	}
	if !got.SkipSchemaValidation {
		t.Fatalf("SkipSchemaValidation = false, want true")
	}
	if !got.PassCredentials {
		t.Fatalf("PassCredentials = false, want true")
	}
	if len(got.HelmParameters) != 1 || got.HelmParameters[0].Name != "value" || !got.HelmParameters[0].ForceString {
		t.Fatalf("HelmParameters = %#v, want force-string value parameter", got.HelmParameters)
	}
	if len(got.HelmFileParameters) != 1 || got.HelmFileParameters[0].Name != "fileValue" || got.HelmFileParameters[0].Path != "message.txt" {
		t.Fatalf("HelmFileParameters = %#v, want fileValue file parameter", got.HelmFileParameters)
	}
}

func TestRenderApplicationPassesPluginOptions(t *testing.T) {
	stringValue := "fast"
	application := argoappv1.Application{
		Namespace: "argocd", Name: "plugin-app",
		Spec: argoappv1.ApplicationSpec{
			Project:     "platform",
			Destination: argoappv1.ApplicationDestination{Namespace: "workloads"},
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://repo",
				Path:    "apps/plugin",
				Plugin: &argoappv1.ApplicationSourcePlugin{
					Name: "cue",
					Env: argoappv1.Env{
						{Name: "FEATURE", Value: "enabled"},
					},
					Parameters: argoappv1.ApplicationSourcePluginParameters{
						{Name: "mode", String_: &stringValue},
					},
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
	if got.Plugin == nil {
		t.Fatalf("Plugin = nil, want plugin config")
	}
	if got.AppName != "plugin-app" {
		t.Fatalf("AppName = %q, want plugin-app", got.AppName)
	}
	if got.AppNamespace != "argocd" {
		t.Fatalf("AppNamespace = %q, want argocd", got.AppNamespace)
	}
	if got.Project != "platform" {
		t.Fatalf("Project = %q, want platform", got.Project)
	}
	if got.Namespace != "workloads" {
		t.Fatalf("Namespace = %q, want workloads", got.Namespace)
	}
	if got.Plugin.Name != "cue" {
		t.Fatalf("Plugin.Name = %q, want cue", got.Plugin.Name)
	}
	if len(got.Plugin.Env) != 1 || got.Plugin.Env[0].Name != "FEATURE" || got.Plugin.Env[0].Value != "enabled" {
		t.Fatalf("Plugin.Env = %#v, want FEATURE=enabled", got.Plugin.Env)
	}
	if len(got.Plugin.Parameters) != 1 || got.Plugin.Parameters[0].Name != "mode" || got.Plugin.Parameters[0].String_ == nil || *got.Plugin.Parameters[0].String_ != "fast" {
		t.Fatalf("Plugin.Parameters = %#v, want string parameter mode=fast", got.Plugin.Parameters)
	}
}

func TestLocalProviderAnchorsRelativeRefRootsUnderRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeAppTestValueChart(t, filepath.Join(root, "chart"))
	writeAppTestFile(t, filepath.Join(root, "foo.yaml"), `
value: from-ref
`)

	manifests, diags, err := (localProvider{repoRoot: root}).RenderSource(context.Background(), render.ResolvedSource{
		Path: "chart",
	}, render.RenderOptions{
		AppName:    "demo",
		RefRoots:   map[string]string{"$values": "."},
		ValueFiles: []string{"$values/foo.yaml"},
	})
	if err != nil {
		t.Fatalf("RenderSource() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(manifests) != 1 {
		t.Fatalf("len(manifests) = %d, want 1", len(manifests))
	}
	value, _, _ := unstructured.NestedString(manifests[0].Object.Object, "data", "value")
	if value != "from-ref" {
		t.Fatalf("data.value = %q, want from-ref", value)
	}
}

func TestLocalProviderRejectsAbsoluteRefRoots(t *testing.T) {
	root := t.TempDir()
	absoluteRefRoot := t.TempDir()
	writeAppTestValueChart(t, filepath.Join(root, "chart"))

	manifests, diags, err := (localProvider{repoRoot: root}).RenderSource(context.Background(), render.ResolvedSource{
		Path: "chart",
	}, render.RenderOptions{
		AppName:    "demo",
		RefRoots:   map[string]string{"$values": absoluteRefRoot},
		ValueFiles: []string{"$values/foo.yaml"},
	})
	if err == nil {
		t.Fatal("RenderSource() error = nil, want absolute ref root error")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("RenderSource() error = %v, want absolute ref root context", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(manifests) != 0 {
		t.Fatalf("manifests = %#v, want none", manifests)
	}
}

func TestRenderApplicationUsesExplicitDirectoryRenderer(t *testing.T) {
	root := t.TempDir()
	writeDirectorySelectionFixture(t, filepath.Join(root, "apps", "demo"))
	application := rendererSelectionApplication("demo", argoappv1.ApplicationSource{
		RepoURL:   "https://repo",
		Path:      "apps/demo",
		Directory: &argoappv1.ApplicationSourceDirectory{},
	})

	result, err := RenderApplication(context.Background(), application, localProvider{repoRoot: root})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "directory-only"); !ok {
		t.Fatalf("manifests = %#v, want directory-only manifest from directory renderer", result.Manifests)
	}
}

func TestRenderApplicationRendersDirectoryJsonnetSemanticFixture(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "semantic-remediation", "directory-jsonnet", "edges")
	data, err := os.ReadFile(filepath.Join(root, "application.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var application argoappv1.Application
	if err := sigsyaml.Unmarshal(data, &application); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}

	result, err := RenderApplication(context.Background(), application, localProvider{repoRoot: root})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	for _, name := range []string{"hidden-edge", "jsonnet-edge"} {
		if _, ok := manifestByName(result.Manifests, name); !ok {
			t.Fatalf("manifests = %#v, want %s from semantic fixture", result.Manifests, name)
		}
	}
	for _, unexpected := range []string{"skipped-edge", "generated-from-data"} {
		if _, ok := manifestByName(result.Manifests, unexpected); ok {
			t.Fatalf("manifests = %#v, did not expect %s", result.Manifests, unexpected)
		}
	}
}

func TestRenderApplicationUsesExplicitLocalHelmRenderer(t *testing.T) {
	root := t.TempDir()
	writeRendererSelectionFixture(t, filepath.Join(root, "apps", "demo"))
	application := rendererSelectionApplication("demo", argoappv1.ApplicationSource{
		RepoURL: "https://repo",
		Path:    "apps/demo",
		Helm:    &argoappv1.ApplicationSourceHelm{},
	})

	result, err := RenderApplication(context.Background(), application, localProvider{repoRoot: root})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "demo"); !ok {
		t.Fatalf("manifests = %#v, want Helm release manifest", result.Manifests)
	}
	if _, ok := manifestByName(result.Manifests, "kustomize-only"); ok {
		t.Fatalf("manifests = %#v, explicit Helm source rendered as Kustomize", result.Manifests)
	}
}

func TestRenderApplicationDiscoveryPrefersKustomizeOverChart(t *testing.T) {
	root := t.TempDir()
	writeRendererSelectionFixture(t, filepath.Join(root, "apps", "demo"))
	application := rendererSelectionApplication("demo", argoappv1.ApplicationSource{
		RepoURL: "https://repo",
		Path:    "apps/demo",
	})

	result, err := RenderApplication(context.Background(), application, localProvider{repoRoot: root})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "kustomize-only"); !ok {
		t.Fatalf("manifests = %#v, want Kustomize manifest", result.Manifests)
	}
	if _, ok := manifestByName(result.Manifests, "demo"); ok {
		t.Fatalf("manifests = %#v, mixed source rendered as Helm", result.Manifests)
	}
}

func TestRenderApplicationAppliesArgocdSourceOverridesBeforeRendering(t *testing.T) {
	root := t.TempDir()
	writeSourceOverrideChart(t, filepath.Join(root, "apps", "demo"), "demo")
	writeSourceOverrideChart(t, filepath.Join(root, "apps", "other"), "other")
	writeAppTestFile(t, filepath.Join(root, "apps", "demo", ".argocd-source.yaml"), `
path: apps/other
repoURL: https://other.example.invalid/repo.git
targetRevision: ignored
helm:
  values: |
    message: global
`)
	writeAppTestFile(t, filepath.Join(root, "apps", "demo", ".argocd-source-demo.yaml"), `
chart: ignored
helm:
  values: |
    message: app-specific
`)
	application := rendererSelectionApplication("demo", argoappv1.ApplicationSource{
		RepoURL: "https://repo.example.invalid/repo.git",
		Path:    "apps/demo",
	})

	result, err := RenderApplication(context.Background(), application, localProvider{repoRoot: root})
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	manifest := assertManifestNamed(t, result.Manifests, "demo")
	message, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "message")
	if message != "app-specific" {
		t.Fatalf("data.message = %q, want app-specific", message)
	}
	sourcePath, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "sourcePath")
	if sourcePath != "demo" {
		t.Fatalf("data.sourcePath = %q, want original path chart", sourcePath)
	}
}

func TestRenderApplicationResolvesArgocdSourceOverrideByInstanceName(t *testing.T) {
	root := t.TempDir()
	writeSourceOverrideChart(t, filepath.Join(root, "apps", "demo"), "demo")
	// The repo-server names the per-Application file after the instance name,
	// so for an Application outside the controller namespace the bare-name
	// file is not an override at all.
	writeAppTestFile(t, filepath.Join(root, "apps", "demo", ".argocd-source-demo.yaml"), `
helm:
  values: |
    message: bare-name-file-must-be-ignored
`)
	writeAppTestFile(t, filepath.Join(root, "apps", "demo", ".argocd-source-tenant_demo.yaml"), `
helm:
  values: |
    message: instance-name-file
`)
	testCases := []struct {
		name                 string
		provider             localProvider
		applicationNamespace string
		want                 string
	}{
		{name: "tenant namespace, default controller namespace", provider: localProvider{repoRoot: root}, applicationNamespace: "tenant", want: "instance-name-file"},
		{name: "tenant namespace, explicit controller namespace", provider: localProvider{repoRoot: root, controllerNamespace: "argocd"}, applicationNamespace: "tenant", want: "instance-name-file"},
		{name: "controller namespace keeps the bare name", provider: localProvider{repoRoot: root, controllerNamespace: "tenant"}, applicationNamespace: "tenant", want: "bare-name-file-must-be-ignored"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			application := rendererSelectionApplication("demo", argoappv1.ApplicationSource{
				RepoURL: "https://repo.example.invalid/repo.git",
				Path:    "apps/demo",
			})
			application.Namespace = testCase.applicationNamespace
			result, err := RenderApplication(context.Background(), application, testCase.provider)
			if err != nil {
				t.Fatalf("RenderApplication() error = %v", err)
			}
			manifest := assertManifestNamed(t, result.Manifests, "demo")
			message, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "message")
			if message != testCase.want {
				t.Fatalf("data.message = %q, want %q", message, testCase.want)
			}
		})
	}
}

func TestRenderApplicationRejectsArgocdSourceOverrideExplicitTypeConflict(t *testing.T) {
	root := t.TempDir()
	writeDirectorySelectionFixture(t, filepath.Join(root, "apps", "demo"))
	writeAppTestFile(t, filepath.Join(root, "apps", "demo", ".argocd-source.yaml"), `
helm:
  values: |
    message: conflict
`)
	application := rendererSelectionApplication("demo", argoappv1.ApplicationSource{
		RepoURL:   "https://repo.example.invalid/repo.git",
		Path:      "apps/demo",
		Directory: &argoappv1.ApplicationSourceDirectory{},
	})

	_, err := RenderApplication(context.Background(), application, localProvider{repoRoot: root})
	if err == nil {
		t.Fatal("RenderApplication() error = nil, want explicit source conflict")
	}
	if !strings.Contains(err.Error(), "multiple application sources defined") {
		t.Fatalf("RenderApplication() error = %v, want explicit source conflict", err)
	}
}

func TestRenderApplicationValuesObjectOverridesHelmValues(t *testing.T) {
	application := argoappv1.Application{
		Namespace: "argocd", Name: "demo",
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
		Namespace: "argocd", Name: "demo",
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
		Namespace: "argocd", Name: "demo",
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
		Namespace: "argocd", Name: "demo",
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
		Name: "demo",
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
		Name: "demo",
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

func TestRenderApplicationExcludesHelmTestHookPod(t *testing.T) {
	// helm.sh/hook: test → IS a hook; excluded from the managed-resources view.
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "chart"},
		},
	}
	testPod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name": "test-pod",
			"annotations": map[string]any{
				"helm.sh/hook": "test",
			},
		},
		"spec": map[string]any{"restartPolicy": "Never"},
	}}
	renderers := StaticRenderers{
		"chart": []render.Manifest{
			{Object: cm("keep", "yes")},
			{Object: testPod},
		},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1 (hook excluded): %#v", len(result.Manifests), result.Manifests)
	}
	if result.Manifests[0].Object.GetName() != "keep" {
		t.Fatalf("manifest name = %q, want keep", result.Manifests[0].Object.GetName())
	}
}

func TestRenderApplicationExcludesArgoHookPostSync(t *testing.T) {
	// argocd.argoproj.io/hook: PostSync in a directory source → IS a hook; excluded.
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Source: &argoappv1.ApplicationSource{
				RepoURL:   "https://repo",
				Path:      "manifests",
				Directory: &argoappv1.ApplicationSourceDirectory{},
			},
		},
	}
	hookJob := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name": "post-sync-job",
			"annotations": map[string]any{
				"argocd.argoproj.io/hook": "PostSync",
			},
		},
	}}
	renderers := StaticRenderers{
		"manifests": []render.Manifest{
			{Object: cm("keep", "yes")},
			{Object: hookJob},
		},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1 (argocd hook excluded): %#v", len(result.Manifests), result.Manifests)
	}
	if result.Manifests[0].Object.GetName() != "keep" {
		t.Fatalf("manifest name = %q, want keep", result.Manifests[0].Object.GetName())
	}
}

func TestRenderApplicationKeepsHelmCRDInstallHook(t *testing.T) {
	// helm.sh/hook: crd-install → NOT treated as a hook (Argo CD exception); kept.
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "chart"},
		},
	}
	crdObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata": map[string]any{
			"name": "my-crd",
			"annotations": map[string]any{
				"helm.sh/hook": "crd-install",
			},
		},
	}}
	renderers := StaticRenderers{
		"chart": []render.Manifest{
			{Object: cm("keep", "yes")},
			{Object: crdObj},
		},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 2 {
		t.Fatalf("len(Manifests) = %d, want 2 (crd-install kept): %#v", len(result.Manifests), result.Manifests)
	}
}

func TestRenderApplicationKeepsArgoSkipOnlyHook(t *testing.T) {
	// argocd.argoproj.io/hook: Skip → Skip-only is NOT treated as a hook; kept.
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "manifests"},
		},
	}
	skipObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "skip-cm",
			"annotations": map[string]any{
				// Repeated values de-duplicate upstream, so Skip,Skip is
				// still Skip-only and kept.
				"argocd.argoproj.io/hook": "Skip,Skip",
			},
		},
	}}
	renderers := StaticRenderers{
		"manifests": []render.Manifest{
			{Object: cm("keep", "yes")},
			{Object: skipObj},
		},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 2 {
		t.Fatalf("len(Manifests) = %d, want 2 (Skip-only kept): %#v", len(result.Manifests), result.Manifests)
	}
}

func TestRenderApplicationExcludesUnrecognizedArgoHookValue(t *testing.T) {
	// argocd.argoproj.io/hook with only unrecognized values is still a hook
	// upstream (gitops-engine IsHook returns !Skip whenever the annotation is
	// present, and unrecognized values yield no Skip); excluded.
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "manifests"},
		},
	}
	unrecognizedObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "unrecognized-cm",
			"annotations": map[string]any{
				"argocd.argoproj.io/hook": "Garbage",
			},
		},
	}}
	renderers := StaticRenderers{
		"manifests": []render.Manifest{
			{Object: cm("keep", "yes")},
			{Object: unrecognizedObj},
		},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1 (unrecognized argo hook excluded): %#v", len(result.Manifests), result.Manifests)
	}
	if result.Manifests[0].Object.GetName() != "keep" {
		t.Fatalf("kept object = %q, want keep", result.Manifests[0].Object.GetName())
	}
}

func TestRenderApplicationKeepsNonHookResources(t *testing.T) {
	// Resources with no hook annotations are included unchanged.
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://repo", Path: "manifests"},
		},
	}
	renderers := StaticRenderers{
		"manifests": []render.Manifest{
			{Object: cm("first", "a")},
			{Object: cm("second", "b")},
		},
	}

	result, err := RenderApplication(context.Background(), application, renderers)
	if err != nil {
		t.Fatalf("RenderApplication() error = %v", err)
	}
	if len(result.Manifests) != 2 {
		t.Fatalf("len(Manifests) = %d, want 2 (non-hook resources kept)", len(result.Manifests))
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

func writeAppTestValueChart(t *testing.T, chartDir string) {
	t.Helper()
	writeAppTestFile(t, filepath.Join(chartDir, "Chart.yaml"), `
apiVersion: v2
name: chart
version: 0.1.0
`)
	writeAppTestFile(t, filepath.Join(chartDir, "values.yaml"), `
value: default
`)
	writeAppTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
`)
}

func rendererSelectionApplication(name string, source argoappv1.ApplicationSource) argoappv1.Application {
	return argoappv1.Application{
		Name: name, Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source:      &source,
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
}

func writeRendererSelectionFixture(t *testing.T, root string) {
	t.Helper()
	writeAppTestFile(t, filepath.Join(root, "Chart.yaml"), `
apiVersion: v2
name: renderer-selection
version: 0.1.0
`)
	writeAppTestFile(t, filepath.Join(root, "templates", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  renderer: helm
`)
	writeAppTestFile(t, filepath.Join(root, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - kustomize.yaml
`)
	writeAppTestFile(t, filepath.Join(root, "kustomize.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: kustomize-only
data:
  renderer: kustomize
`)
	writeAppTestFile(t, filepath.Join(root, "directory.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: directory-only
data:
  renderer: directory
`)
}

func writeSourceOverrideChart(t *testing.T, root, sourcePath string) {
	t.Helper()
	writeAppTestFile(t, filepath.Join(root, "Chart.yaml"), `
apiVersion: v2
name: source-overrides
version: 0.1.0
`)
	writeAppTestFile(t, filepath.Join(root, "templates", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  message: {{ .Values.message | default "default" | quote }}
  sourcePath: `+sourcePath+`
`)
}

func writeDirectorySelectionFixture(t *testing.T, root string) {
	t.Helper()
	writeAppTestFile(t, filepath.Join(root, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - kustomize.yaml
`)
	writeAppTestFile(t, filepath.Join(root, "kustomize.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: kustomize-only
data:
  renderer: kustomize
`)
	writeAppTestFile(t, filepath.Join(root, "directory.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: directory-only
data:
  renderer: directory
`)
}

func writeAppTestFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimPrefix(data, "\n")), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func TestArgoRenderEnvMatchesArgoCDBuildEnvironment(t *testing.T) {
	source := argoappv1.ApplicationSource{
		RepoURL:        "https://example.invalid/repo.git",
		Path:           "apps/demo",
		TargetRevision: "1234567890abcdef",
	}
	testCases := []struct {
		name                string
		appNamespace        string
		controllerNamespace string
		destination         string
		wantName            string
		wantNamespace       string
	}{
		{name: "app in controller namespace", appNamespace: "argocd", controllerNamespace: "argocd", destination: "workloads", wantName: "demo", wantNamespace: "workloads"},
		{name: "app outside controller namespace", appNamespace: "team-a", controllerNamespace: "argocd", destination: "workloads", wantName: "team-a_demo", wantNamespace: "workloads"},
		{name: "app without metadata namespace", appNamespace: "", controllerNamespace: "argocd", destination: "workloads", wantName: "demo", wantNamespace: "workloads"},
		{name: "custom controller namespace", appNamespace: "argo-system", controllerNamespace: "argo-system", destination: "workloads", wantName: "demo", wantNamespace: "workloads"},
		{name: "empty destination namespace", appNamespace: "argocd", controllerNamespace: "argocd", destination: "", wantName: "demo", wantNamespace: ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			application := argoappv1.Application{
				Name: "demo", Namespace: testCase.appNamespace,
				Spec: argoappv1.ApplicationSpec{
					Project:     "k3s",
					Destination: argoappv1.ApplicationDestination{Namespace: testCase.destination},
				},
			}
			env := argoRenderEnv(application, source, testCase.controllerNamespace)
			got := env.Envsubst("$ARGOCD_APP_NAME|$ARGOCD_APP_NAMESPACE")
			want := testCase.wantName + "|" + testCase.wantNamespace
			if got != want {
				t.Fatalf("ARGOCD_APP_NAME|ARGOCD_APP_NAMESPACE = %q, want %q", got, want)
			}
		})
	}

	// The remaining seven entries are unchanged by this fix and stay pinned.
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{Project: "k3s", Destination: argoappv1.ApplicationDestination{Namespace: "workloads"}},
	}
	env := argoRenderEnv(application, source, "argocd")
	got := env.Envsubst("$ARGOCD_APP_PROJECT_NAME|$ARGOCD_APP_REVISION|$ARGOCD_APP_REVISION_SHORT|$ARGOCD_APP_REVISION_SHORT_8|$ARGOCD_APP_SOURCE_REPO_URL|$ARGOCD_APP_SOURCE_PATH|$ARGOCD_APP_SOURCE_TARGET_REVISION")
	want := "k3s|1234567890abcdef|1234567|12345678|https://example.invalid/repo.git|apps/demo|1234567890abcdef"
	if got != want {
		t.Fatalf("remaining ARGOCD_APP_* = %q, want %q", got, want)
	}
	if len(env) != 9 {
		t.Fatalf("len(argoRenderEnv) = %d, want 9 entries like the Argo CD repo-server", len(env))
	}
}

func TestRenderOptionsNormalizesTrackingOptionsForArgoEnv(t *testing.T) {
	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{Destination: argoappv1.ApplicationDestination{Namespace: "workloads"}},
	}
	source := argoappv1.ApplicationSource{Path: "apps/demo"}

	// Zero TrackingOptions must normalize to the default controller namespace
	// ("argocd"), so an app in that namespace keeps its bare instance name.
	opts, err := renderOptions(application, source, CapabilityOptions{}, TrackingOptions{})
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if got := opts.ArgoEnv.Envsubst("$ARGOCD_APP_NAME"); got != "demo" {
		t.Fatalf("ARGOCD_APP_NAME with zero TrackingOptions = %q, want %q (controller namespace must be normalized before InstanceName)", got, "demo")
	}

	opts, err = renderOptions(application, source, CapabilityOptions{}, TrackingOptions{ControllerNamespace: "argo-system"})
	if err != nil {
		t.Fatalf("renderOptions() error = %v", err)
	}
	if got := opts.ArgoEnv.Envsubst("$ARGOCD_APP_NAME"); got != "argocd_demo" {
		t.Fatalf("ARGOCD_APP_NAME with custom controller namespace = %q, want %q", got, "argocd_demo")
	}
}
