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
	overrides := map[string]argoappv1.ResourceOverride{"example.com/Widget": {KnownTypeFields: []argoappv1.KnownTypeField{{Field: "spec.resources", Type: "core/v1/ResourceRequirements"}}}}
	// The premise: the known-types normalizer fails on this object.
	knownTypes, err := normalizers.NewKnownTypesNormalizer(overrides)
	if err != nil {
		t.Fatalf("NewKnownTypesNormalizer() error = %v", err)
	}
	if err := knownTypes.Normalize(widget(1)); err == nil {
		t.Fatal("known-types Normalize() error = nil, want a failure on limits: notamap")
	}
	config, err := argodiff.NewDiffConfigBuilder().WithDiffSettings(
		[]argoappv1.ResourceIgnoreDifferences{{Group: "example.com", Kind: "Widget", JQPathExpressions: []string{".metadata.labels.foo"}}},
		overrides,
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

// TestUpstreamCreateAndDeleteNormalization pins how Argo CD v3.6.1
// normalizes a created or deleted resource. Server-side diff skips the ignore
// normalizer and the known-types normalizer: field rules, the status
// override, and known types do not apply. The aggregated-role normalization
// still does. The client-side diff applies all of them.
func TestUpstreamCreateAndDeleteNormalization(t *testing.T) {
	objects := map[string]func() *unstructured.Unstructured{
		"ConfigMap": func() *unstructured.Unstructured {
			return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "c", "namespace": "n"}, "data": map[string]any{"a": "1", "b": "2"}, "status": map[string]any{"s": "x"}}}
		},
		"Widget": func() *unstructured.Unstructured {
			return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"name": "w", "namespace": "n"}, "spec": map[string]any{"resources": map[string]any{"limits": map[string]any{"cpu": "0.1"}}}}}
		},
		"ClusterRole": func() *unstructured.Unstructured {
			return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]any{"name": "r"}, "aggregationRule": map[string]any{"clusterRoleSelectors": []any{}}, "rules": []any{map[string]any{"verbs": []any{"get"}}}}}
		},
	}
	ignores := []argoappv1.ResourceIgnoreDifferences{{Kind: "ConfigMap", JQPathExpressions: []string{".data.b"}}}
	overrides := map[string]argoappv1.ResourceOverride{
		"*/*":                {IgnoreDifferences: argoappv1.OverrideIgnoreDiff{JSONPointers: []string{"/status"}}},
		"example.com/Widget": {KnownTypeFields: []argoappv1.KnownTypeField{{Field: "spec.resources", Type: "core/v1/ResourceRequirements"}}},
	}
	for _, serverSide := range []bool{false, true} {
		config, err := argodiff.NewDiffConfigBuilder().
			WithDiffSettings(ignores, overrides, true, normalizers.IgnoreNormalizerOpts{}).
			WithNoCache().WithServerSideDiff(serverSide).WithServerSideDryRunner(echoDryRunner{}).
			WithManager("argocd-controller").WithIgnoreMutationWebhook(false).Build()
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		// The text the shown object contains when the normalization runs,
		// and when it does not.
		want := map[string][2]string{
			"ConfigMap": {`"data":{"a":"1"}`, `"b":"2"`},
			"Widget":    {`"cpu":"100m"`, `"cpu":"0.1"`},
		}
		for kind, object := range objects {
			for _, created := range []bool{true, false} {
				lives, targets := []*unstructured.Unstructured{nil}, []*unstructured.Unstructured{object()}
				if !created {
					lives, targets = targets, lives
				}
				result, err := argodiff.StateDiffs(t.Context(), lives, targets, config)
				if err != nil {
					t.Fatalf("StateDiffs() error = %v", err)
				}
				shown := string(result.Diffs[0].PredictedLive)
				if !created {
					shown = string(result.Diffs[0].NormalizedLive)
				}
				if kind == "ClusterRole" {
					if !strings.Contains(shown, `"rules":null`) {
						t.Fatalf("%s server-side=%t created=%t shows %s, want rules normalized", kind, serverSide, created, shown)
					}
					continue
				}
				wantText := want[kind][0]
				if serverSide {
					wantText = want[kind][1]
				}
				if !strings.Contains(shown, wantText) || strings.Contains(shown, `"status"`) != serverSide && kind == "ConfigMap" {
					t.Fatalf("%s server-side=%t created=%t shows %s, want %s", kind, serverSide, created, shown, wantText)
				}
			}
		}
	}
}

// drydock matches TestUpstreamCreateAndDeleteNormalization: under
// server-side diff a created or deleted resource shows the whole object, and
// drydock's own display steps still run on it. The client-side diff applies
// the rules.
func TestRunCreateAndDeleteNormalization(t *testing.T) {
	configMap := func(rule string, serverSide bool) Document {
		return Document{
			Parent:   Parent{Namespace: "argocd", Name: "demo"},
			Resource: Resource{Kind: "ConfigMap", Namespace: "default", Name: "demo"},
			Body:     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n  namespace: default\ndata:\n  a: \"1\"\n  b: \"2\"\nbinaryData:\n  blob: aGVsbG8gd29ybGQ=\n",
			Normalization: Normalization{
				ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{rule}}},
				ServerSideDiff:   serverSide,
			},
		}
	}
	for _, tt := range []struct {
		name, rule        string
		serverSide        bool
		wantText, notText []string
	}{
		{name: "server-side, whole object rule", rule: ".", serverSide: true, wantText: []string{"a: \"1\"", "b: \"2\"", "blob:"}, notText: []string{"aGVsbG8gd29ybGQ=", "null"}},
		{name: "server-side, field rule", rule: ".data.b", serverSide: true, wantText: []string{"b: \"2\""}, notText: []string{"aGVsbG8gd29ybGQ="}},
		{name: "client-side, whole object rule", rule: ".", wantText: []string{"null"}, notText: []string{"a: \"1\""}},
		{name: "client-side, field rule", rule: ".data.b", wantText: []string{"a: \"1\""}, notText: []string{"b: \"2\"", "aGVsbG8gd29ybGQ="}},
	} {
		for _, created := range []bool{true, false} {
			name := tt.name + map[bool]string{true: ", created", false: ", deleted"}[created]
			t.Run(name, func(t *testing.T) {
				var left, right []Document
				if created {
					right = []Document{configMap(tt.rule, tt.serverSide)}
				} else {
					left = []Document{configMap(tt.rule, tt.serverSide)}
				}
				results, failures, err := RunWithFailures(left, right, Options{Unified: 3})
				if err != nil || len(failures) != 0 || len(results) != 1 {
					t.Fatalf("RunWithFailures() = %#v, %#v, %v, want one change", results, failures, err)
				}
				for _, want := range tt.wantText {
					if !strings.Contains(results[0].Diff, want) {
						t.Fatalf("Diff = %q, want %q", results[0].Diff, want)
					}
				}
				for _, not := range tt.notText {
					if strings.Contains(results[0].Diff, not) {
						t.Fatalf("Diff = %q, must not contain %q", results[0].Diff, not)
					}
				}
			})
		}
	}
}

// A Secret whose object an ignore rule removes keeps null on that side
// through Secret redaction, as a ConfigMap does.
func TestRunSecretRemovedObjectKeepsNull(t *testing.T) {
	secret := func(value string) Document {
		return Document{
			Parent:   Parent{Namespace: "argocd", Name: "demo"},
			Resource: Resource{Kind: "Secret", Namespace: "default", Name: "demo"},
			Body:     "apiVersion: v1\nkind: Secret\nmetadata:\n  name: demo\n  namespace: default\ndata:\n  key: " + value + "\n",
			Normalization: Normalization{
				ApplicationRules: []IgnoreRule{{JQPathExpressions: []string{`select(.data.key == "Yg==")`}}},
			},
		}
	}
	results, failures, err := RunWithFailures([]Document{secret("YQ==")}, []Document{secret("Yg==")}, Options{Unified: 3})
	if err != nil || len(failures) != 0 || len(results) != 1 || results[0].Change != ChangeModified || !strings.Contains(results[0].Diff, "+null") {
		t.Fatalf("RunWithFailures() = %#v, %#v, %v, want a change to null", results, failures, err)
	}
	if strings.Contains(results[0].Diff, "YQ==") {
		t.Fatalf("Diff = %q, leaks the Secret value", results[0].Diff)
	}
	results, failures, err = RunWithFailures(nil, []Document{secret("Yg==")}, Options{Unified: 3})
	if err != nil || len(failures) != 0 || len(results) != 1 || results[0].Change != ChangeAdded || !strings.Contains(results[0].Diff, "+null") {
		t.Fatalf("RunWithFailures() = %#v, %#v, %v, want an added null", results, failures, err)
	}
}

// A knownTypeFields field that fails is reported once, with the field.
func TestRunWithReportKnownTypeFailure(t *testing.T) {
	widget := func(replicas string) Document {
		return Document{
			Parent:   Parent{Namespace: "argocd", Name: "widget"},
			Resource: Resource{Group: "example.com", Kind: "Widget", Namespace: "default", Name: "w"},
			Body:     "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: w\n  namespace: default\nspec:\n  replicas: " + replicas + "\n  ok:\n    limits:\n      cpu: \"0.1\"\n  resources:\n    limits: notamap\n",
			Normalization: Normalization{
				KnownTypeFields: []KnownTypeField{
					{Field: "spec.ok", Type: "core/v1/ResourceRequirements"},
					{Field: "spec.resources", Type: "core/v1/ResourceRequirements"},
				},
			},
		}
	}
	report, err := RunWithReport([]Document{widget("1")}, []Document{widget("2")}, Options{Unified: 3})
	if err != nil {
		t.Fatalf("RunWithReport() error = %v", err)
	}
	if len(report.KnownTypeFailures) != 1 || report.KnownTypeFailures[0].Field != "spec.resources" || report.KnownTypeFailures[0].Parent.Name != "widget" {
		t.Fatalf("KnownTypeFailures = %#v, want one for spec.resources", report.KnownTypeFailures)
	}
	// The field before the failing one is still normalized, as in Argo CD.
	if len(report.Results) != 1 || strings.Contains(report.Results[0].Diff, "0.1") {
		t.Fatalf("Results = %#v, want spec.ok normalized", report.Results)
	}
	report, err = RunWithReport([]Document{widget("1")}, []Document{widget("1")}, Options{Unified: 3})
	if err != nil || len(report.KnownTypeFailures) != 0 {
		t.Fatalf("RunWithReport() of an unchanged resource = %#v, %v, want no report", report.KnownTypeFailures, err)
	}
}

// A resource that moves to another Application is a change in Argo CD, not a
// create, so the server-side create and delete path does not apply. drydock
// shows a removal and an addition, and normalizes the addition with the new
// Application's rules.
func TestRunMovedResourceKeepsFullNormalization(t *testing.T) {
	deployment := func(app, replicas string, normalization Normalization) Document {
		return Document{
			Parent:        Parent{Namespace: "argocd", Name: app},
			Resource:      Resource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "web"},
			Body:          "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: default\nspec:\n  replicas: " + replicas + "\n",
			Normalization: normalization,
		}
	}
	rulesOfB := Normalization{ApplicationRules: []IgnoreRule{{JSONPointers: []string{"/spec/replicas"}}}, ServerSideDiff: true}
	results, failures, err := RunWithFailures(
		[]Document{deployment("a", "1", Normalization{})},
		[]Document{deployment("b", "2", rulesOfB)},
		Options{Unified: 3})
	if err != nil || len(failures) != 0 || len(results) != 2 {
		t.Fatalf("RunWithFailures() = %#v, %#v, %v, want a removal and an addition", results, failures, err)
	}
	for _, result := range results {
		switch result.Parent.Name {
		case "a":
			if result.Change != ChangeRemoved || !strings.Contains(result.Diff, "-  replicas: 1") {
				t.Fatalf("Application a result = %#v, want the removal with replicas", result)
			}
		case "b":
			if result.Change != ChangeAdded || strings.Contains(result.Diff, "replicas") {
				t.Fatalf("Application b result = %#v, want the addition with the replicas rule applied", result)
			}
		}
	}

	// A resource that is new everywhere takes the create path: the rule
	// does not apply under server-side diff.
	results, _, err = RunWithFailures(nil, []Document{deployment("b", "2", rulesOfB)}, Options{Unified: 3})
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Diff, "+  replicas: 2") {
		t.Fatalf("RunWithFailures() of a new resource = %#v, %v, want replicas shown", results, err)
	}
}

// The field a known-types failure names comes from a replay that leaves out
// unsupported types and keeps the Argo Rollout default apart.
func TestRunWithReportNamesTheFailingKnownTypeField(t *testing.T) {
	rollout := func(replicas string, fields []KnownTypeField, spec string) Document {
		return Document{
			Parent:        Parent{Namespace: "argocd", Name: "r"},
			Resource:      Resource{Group: "argoproj.io", Kind: "Rollout", Namespace: "default", Name: "r"},
			Body:          "apiVersion: argoproj.io/v1alpha1\nkind: Rollout\nmetadata:\n  name: r\n  namespace: default\nspec:\n  replicas: " + replicas + "\n" + spec,
			Normalization: Normalization{KnownTypeFields: fields},
		}
	}
	badPodSpec := "  template:\n    spec:\n      containers: notalist\n"
	for _, tt := range []struct {
		name           string
		fields         []KnownTypeField
		spec           string
		wantField      string
		wantRolloutDef bool
	}{
		{
			// Only an unsupported type: Argo CD uses the Rollout default.
			name:           "unsupported type only",
			fields:         []KnownTypeField{{Field: "spec.strategy.foo", Type: "core/v1/NoSuchType"}},
			spec:           badPodSpec,
			wantField:      "spec.template.spec",
			wantRolloutDef: true,
		},
		{
			name:      "configured field fails",
			fields:    []KnownTypeField{{Field: "spec.resources", Type: "core/v1/ResourceRequirements"}},
			spec:      "  resources:\n    limits: notamap\n",
			wantField: "spec.resources",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report, err := RunWithReport([]Document{rollout("1", tt.fields, tt.spec)}, []Document{rollout("2", tt.fields, tt.spec)}, Options{Unified: 3})
			if err != nil {
				t.Fatalf("RunWithReport() error = %v", err)
			}
			if len(report.KnownTypeFailures) != 1 || report.KnownTypeFailures[0].Field != tt.wantField || report.KnownTypeFailures[0].RolloutDefault != tt.wantRolloutDef {
				t.Fatalf("KnownTypeFailures = %#v, want %s (Rollout default %t)", report.KnownTypeFailures, tt.wantField, tt.wantRolloutDef)
			}
		})
	}
}

// A known-types failure is reported only for a change that the diff shows.
func TestRunWithReportKnownTypeFailureOnlyForShownChanges(t *testing.T) {
	widget := func(label string) Document {
		return Document{
			Parent:   Parent{Namespace: "argocd", Name: "w"},
			Resource: Resource{Group: "example.com", Kind: "Widget", Namespace: "default", Name: "w"},
			Body:     "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: w\n  namespace: default\n  labels:\n    foo: " + label + "\nspec:\n  resources:\n    limits: notamap\n",
			Normalization: Normalization{
				ApplicationRules: []IgnoreRule{{JSONPointers: []string{"/metadata/labels"}}},
				KnownTypeFields:  []KnownTypeField{{Field: "spec.resources", Type: "core/v1/ResourceRequirements"}},
			},
		}
	}
	report, err := RunWithReport([]Document{widget("a")}, []Document{widget("b")}, Options{Unified: 3})
	if err != nil || len(report.Results) != 0 || len(report.KnownTypeFailures) != 0 {
		t.Fatalf("RunWithReport() = %#v, %v, want no change and no report", report, err)
	}
}
