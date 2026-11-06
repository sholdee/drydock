package diff

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gediff "github.com/argoproj/argo-cd/gitops-engine/v3/pkg/diff"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argodiff "github.com/argoproj/argo-cd/v3/util/argo/diff"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// upstreamServerSideDiffOptions are the server-side diff options of the
// controller, with echoDryRunner as the API server. The controller default
// is IgnoreMutationWebhook(true), which needs a GVK parser built from an
// OpenAPI document. That would make gnostic-models, protobuf and the
// replace-pinned kube-openapi direct dependencies of drydock, so these tests
// use IgnoreMutationWebhook(false). echoDryRunner adds no webhook mutation,
// so the webhook step has nothing to remove, and a one-off run with the
// default gave the same result for every case.
func upstreamServerSideDiffOptions(t *testing.T, normalizer gediff.Normalizer) []gediff.Option {
	t.Helper()
	return []gediff.Option{
		gediff.WithNormalizer(normalizer),
		gediff.WithServerSideDiff(true),
		gediff.WithServerSideDryRunner(echoDryRunner{}),
		gediff.WithIgnoreMutationWebhook(false),
		gediff.WithManager("argocd-controller"),
	}
}

func withLastApplied(value string) *unstructured.Unstructured {
	object := objectRemovedConfigMap(value)
	object.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"demo","namespace":"default"},"data":{"value":"` + value + `"}}`})
	return object
}

func objectRemovedConfigMap(value string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "demo",
			"namespace": "default",
			"managedFields": []any{map[string]any{
				"manager": "argocd-controller", "operation": "Apply", "apiVersion": "v1", "fieldsType": "FieldsV1",
				"fieldsV1": map[string]any{"f:data": map[string]any{"f:value": map[string]any{}}},
			}},
		},
		"data": map[string]any{"value": value},
	}}
}

// echoDryRunner stands in for the server-side apply dry run: it returns the
// desired object as the predicted live object.
type echoDryRunner struct{}

func (echoDryRunner) Run(_ context.Context, obj *unstructured.Unstructured, _ string) (string, error) {
	data, err := json.Marshal(obj.Object)
	return string(data), err
}

// TestUpstreamIgnoreRuleThatRemovesTheObject pins what Argo CD v3.6.1 does
// when a jq ignore rule removes the whole object, such as ".". The ignore
// normalizer ends with the JSON text null and cannot decode it, so the object
// becomes null and Normalize returns an error, which gitops-engine only logs.
// Then the client-side diff fails, which fails the comparison of the
// Application, while server-side diff compares null with null.
func TestUpstreamIgnoreRuleThatRemovesTheObject(t *testing.T) {
	ignore, err := normalizers.NewIgnoreNormalizer([]argoappv1.ResourceIgnoreDifferences{
		{Kind: "ConfigMap", JQPathExpressions: []string{"."}},
	}, map[string]argoappv1.ResourceOverride{}, normalizers.IgnoreNormalizerOpts{})
	if err != nil {
		t.Fatalf("NewIgnoreNormalizer() error = %v", err)
	}

	object := objectRemovedConfigMap("a")
	if err := ignore.Normalize(object); err == nil || object.Object != nil {
		t.Fatalf("Normalize() error = %v, object = %#v, want an error and a null object", err, object.Object)
	}
	object = objectRemovedConfigMap("a")
	gediff.Normalize(object, gediff.WithNormalizer(ignore))
	if object.Object != nil {
		t.Fatalf("gitops-engine Normalize() object = %#v, want null", object.Object)
	}

	// Client-side two-way and three-way diff fail.
	if _, err := gediff.Diff(t.Context(), objectRemovedConfigMap("b"), objectRemovedConfigMap("a"), gediff.WithNormalizer(ignore)); err == nil || !strings.Contains(err.Error(), "Invalid JSON Document") {
		t.Fatalf("two-way Diff() error = %v, want Invalid JSON Document", err)
	}
	live := objectRemovedConfigMap("a")
	live.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"demo","namespace":"default"},"data":{"value":"a"}}`})
	if _, err := gediff.Diff(t.Context(), objectRemovedConfigMap("b"), live, gediff.WithNormalizer(ignore)); err == nil {
		t.Fatal("three-way Diff() error = nil, want a failure")
	}
	// Server-side diff compares null with null.
	result, err := gediff.Diff(t.Context(), objectRemovedConfigMap("b"), objectRemovedConfigMap("a"), upstreamServerSideDiffOptions(t, ignore)...)
	if err != nil || result.Modified {
		t.Fatalf("server-side Diff() = %#v, %v, want not modified", result, err)
	}
	// A new resource is modified in either mode.
	result, err = gediff.Diff(t.Context(), objectRemovedConfigMap("b"), nil, gediff.WithNormalizer(ignore))
	if err != nil || !result.Modified {
		t.Fatalf("Diff() of a new resource = %#v, %v, want modified", result, err)
	}
}

func objectRemovedDocument(app, value string, serverSideDiff bool) Document {
	return Document{
		Parent:   Parent{Namespace: "argocd", Name: app},
		Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "demo"},
		Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n  namespace: default\ndata:\n  value: " + value + "\n",
		Normalization: Normalization{
			ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{"."}}},
			ServerSideDiff:   serverSideDiff,
		},
	}
}

// drydock matches TestUpstreamIgnoreRuleThatRemovesTheObject: under
// client-side diff the comparison of that Application fails, also with
// identical bodies, and the other Applications still diff. Under
// server-side diff the resource compares as null on both sides.
func TestRunIgnoreRuleThatRemovesTheObject(t *testing.T) {
	other := func(value string) Document {
		doc := objectRemovedDocument("other", value, false)
		doc.Normalization = Normalization{}
		return doc
	}
	for _, tt := range []struct {
		name         string
		left, right  string
		wantFailures int
	}{
		{name: "changed", left: "a", right: "b", wantFailures: 1},
		{name: "identical", left: "a", right: "a", wantFailures: 1},
	} {
		t.Run("client-side "+tt.name, func(t *testing.T) {
			results, failures, err := RunWithFailures(
				[]Document{objectRemovedDocument("broken", tt.left, false), other("a")},
				[]Document{objectRemovedDocument("broken", tt.right, false), other("b")},
				Options{Unified: 3})
			if err != nil {
				t.Fatalf("RunWithFailures() error = %v", err)
			}
			if len(failures) != tt.wantFailures || failures[0].Parent.Name != "broken" {
				t.Fatalf("failures = %#v, want one for Application broken", failures)
			}
			if len(results) != 1 || results[0].Parent.Name != "other" {
				t.Fatalf("results = %#v, want only the change of Application other", results)
			}
		})
	}

	t.Run("server-side", func(t *testing.T) {
		results, failures, err := RunWithFailures(
			[]Document{objectRemovedDocument("broken", "a", true)},
			[]Document{objectRemovedDocument("broken", "b", true)},
			Options{Unified: 3})
		if err != nil || len(failures) != 0 || len(results) != 0 {
			t.Fatalf("RunWithFailures() = %#v, %#v, %v, want no change", results, failures, err)
		}
	})
}

// A new resource is added in both modes. Server-side diff shows the whole
// new object, the client-side diff shows null
// (TestUpstreamIgnoreRuleThatRemovesOneObject).
func TestRunIgnoreRuleThatRemovesTheObjectOfANewResource(t *testing.T) {
	for _, serverSide := range []bool{false, true} {
		mode := map[bool]string{false: "client-side", true: "server-side"}[serverSide]
		t.Run(mode, func(t *testing.T) {
			results, failures, err := RunWithFailures(nil, []Document{objectRemovedDocument("broken", "b", serverSide)}, Options{Unified: 3})
			if err != nil || len(failures) != 0 || len(results) != 1 || results[0].Change != ChangeAdded {
				t.Fatalf("RunWithFailures() = %#v, %#v, %v, want one added resource", results, failures, err)
			}
			if got, want := strings.Contains(results[0].Diff, "+  value: b"), serverSide; got != want {
				t.Fatalf("Diff = %q, want the whole object only under server-side diff", results[0].Diff)
			}
		})
	}
}

// When the rule removes the object on one side only, Argo CD reports a
// change in both modes (TestUpstreamIgnoreRuleThatRemovesOneObject), so
// drydock shows a change with null on that side.
func TestRunIgnoreRuleThatRemovesOneObject(t *testing.T) {
	doc := func(value string, serverSide bool, binaryData bool) Document {
		body := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n  namespace: default\ndata:\n  value: " + value + "\n"
		if binaryData {
			body += "binaryData:\n  blob: aGVsbG8=\n"
		}
		return Document{
			Parent:   Parent{Namespace: "argocd", Name: "demo"},
			Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "demo"},
			Body:     body,
			Normalization: Normalization{
				ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{`select(.data.value == "b")`}}},
				ServerSideDiff:   serverSide,
			},
		}
	}
	for _, serverSide := range []bool{false, true} {
		for _, binaryData := range []bool{false, true} {
			for _, tt := range []struct{ name, left, right string }{
				{name: "current null", left: "a", right: "b"},
				{name: "baseline null", left: "b", right: "a"},
			} {
				name := map[bool]string{false: "client-side", true: "server-side"}[serverSide] + " " + tt.name
				if binaryData {
					name += " with binaryData"
				}
				t.Run(name, func(t *testing.T) {
					results, failures, err := RunWithFailures([]Document{doc(tt.left, serverSide, binaryData)}, []Document{doc(tt.right, serverSide, binaryData)}, Options{Unified: 3})
					if err != nil || len(failures) != 0 {
						t.Fatalf("RunWithFailures() failures = %#v, error = %v, want a change", failures, err)
					}
					if len(results) != 1 || results[0].Change != ChangeModified || !strings.Contains(results[0].Diff, "null") {
						t.Fatalf("results = %#v, want one change with a null side", results)
					}
				})
			}
		}
	}
}

// A known-types error is only logged by Argo CD
// (TestUpstreamKnownTypeErrorIsLogged). drydock compares the object as it
// is, so an unchanged resource does not fail the diff and a changed one is
// shown. The jq rule makes the identical bodies go through normalization.
func TestRunKnownTypeErrorIsIgnored(t *testing.T) {
	widget := func(replicas string) Document {
		return Document{
			Parent:   Parent{Namespace: "argocd", Name: "widget"},
			Resource: Resource{Group: "example.com", Kind: "Widget", Namespace: "default", Name: "w"},
			Body:     "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: w\n  namespace: default\nspec:\n  replicas: " + replicas + "\n  resources:\n    limits: notamap\n",
			Normalization: Normalization{
				ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{".metadata.labels.foo"}}},
				KnownTypeFields:  []KnownTypeField{{Field: "spec.resources", Type: "core/v1/ResourceRequirements"}},
			},
		}
	}
	other := func(value string) Document {
		doc := objectRemovedDocument("other", value, false)
		doc.Normalization = Normalization{}
		return doc
	}
	for _, tt := range []struct {
		name        string
		right       string
		wantWidgets int
	}{{"unchanged", "1", 0}, {"changed", "2", 1}} {
		t.Run(tt.name, func(t *testing.T) {
			results, failures, err := RunWithFailures([]Document{widget("1"), other("a")}, []Document{widget(tt.right), other("b")}, Options{Unified: 3})
			if err != nil || len(failures) != 0 {
				t.Fatalf("RunWithFailures() failures = %#v, error = %v", failures, err)
			}
			if len(results) != 1+tt.wantWidgets {
				t.Fatalf("results = %#v, want %d widget changes and the other Application's change", results, tt.wantWidgets)
			}
		})
	}
}

// TestUpstreamIgnoreRuleThatRemovesOneObject pins what Argo CD v3.6.1 does
// when an ignore rule removes the object on one side only. The rule removes
// the object when data.value is "b".
func TestUpstreamIgnoreRuleThatRemovesOneObject(t *testing.T) {
	ignore, err := normalizers.NewIgnoreNormalizer([]argoappv1.ResourceIgnoreDifferences{
		{Kind: "ConfigMap", JQPathExpressions: []string{`select(.data.value == "b")`}},
	}, map[string]argoappv1.ResourceOverride{}, normalizers.IgnoreNormalizerOpts{})
	if err != nil {
		t.Fatalf("NewIgnoreNormalizer() error = %v", err)
	}
	clientSide := []gediff.Option{gediff.WithNormalizer(ignore)}
	for _, tt := range []struct {
		name         string
		desired      string
		live         *unstructured.Unstructured
		options      []gediff.Option
		wantModified bool
	}{
		// The two-way diff hides a null desired object, but a client-side
		// sync leaves the last-applied annotation, and the three-way diff
		// then reports the change.
		{name: "client-side two-way, desired null", desired: "b", live: objectRemovedConfigMap("a"), options: clientSide},
		{name: "client-side three-way, desired null", desired: "b", live: withLastApplied("a"), options: clientSide, wantModified: true},
		{name: "client-side two-way, live null", desired: "a", live: objectRemovedConfigMap("b"), options: clientSide, wantModified: true},
		{name: "client-side three-way, live null", desired: "a", live: withLastApplied("b"), options: clientSide, wantModified: true},
		{name: "server-side, desired null", desired: "b", live: objectRemovedConfigMap("a"), options: upstreamServerSideDiffOptions(t, ignore), wantModified: true},
		{name: "server-side, live null", desired: "a", live: objectRemovedConfigMap("b"), options: upstreamServerSideDiffOptions(t, ignore), wantModified: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := gediff.Diff(t.Context(), objectRemovedConfigMap(tt.desired), tt.live, tt.options...)
			if err != nil {
				t.Fatalf("Diff() error = %v", err)
			}
			if result.Modified != tt.wantModified {
				t.Fatalf("Diff().Modified = %t, want %t", result.Modified, tt.wantModified)
			}
		})
	}

	// A new resource under server-side diff shows the whole object: the
	// ignore rules do not run for it.
	result, err := gediff.Diff(t.Context(), objectRemovedConfigMap("b"), nil, upstreamServerSideDiffOptions(t, ignore)...)
	if err != nil || !result.Modified || !strings.Contains(string(result.PredictedLive), `"value":"b"`) {
		t.Fatalf("server-side Diff() of a new resource = %s, %v, want the whole object", result.PredictedLive, err)
	}
	result, err = gediff.Diff(t.Context(), objectRemovedConfigMap("b"), nil, clientSide...)
	if err != nil || !result.Modified || string(result.PredictedLive) != "null" {
		t.Fatalf("client-side Diff() of a new resource = %s, %v, want null", result.PredictedLive, err)
	}
}

// TestUpstreamKnownTypeErrorIsLogged pins that Argo CD logs a known-types
// error and compares the object as it is: the jq rule still applies, the
// field keeps its value, and only a real change is modified.
func TestUpstreamKnownTypeErrorIsLogged(t *testing.T) {
	widget := func(replicas float64) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": "w", "namespace": "default", "labels": map[string]any{"foo": "x"}},
			"spec":       map[string]any{"replicas": replicas, "resources": map[string]any{"limits": "notamap"}},
		}}
	}
	config, err := argodiff.NewDiffConfigBuilder().WithDiffSettings(
		[]argoappv1.ResourceIgnoreDifferences{{Group: "example.com", Kind: "Widget", JQPathExpressions: []string{".metadata.labels.foo"}}},
		map[string]argoappv1.ResourceOverride{"example.com/Widget": {KnownTypeFields: []argoappv1.KnownTypeField{{Field: "spec.resources", Type: "core/v1/ResourceRequirements"}}}},
		false, normalizers.IgnoreNormalizerOpts{}).WithNoCache().Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, tt := range []struct {
		live, desired float64
		wantModified  bool
	}{{1, 2, true}, {1, 1, false}} {
		result, err := argodiff.StateDiffs(t.Context(), []*unstructured.Unstructured{widget(tt.live)}, []*unstructured.Unstructured{widget(tt.desired)}, config)
		if err != nil {
			t.Fatalf("StateDiffs() error = %v", err)
		}
		if got := result.Diffs[0]; got.Modified != tt.wantModified || strings.Contains(string(got.PredictedLive), `"foo"`) {
			t.Fatalf("StateDiffs() = modified %t, predicted %s, want modified %t and the label removed", got.Modified, got.PredictedLive, tt.wantModified)
		}
	}
}
