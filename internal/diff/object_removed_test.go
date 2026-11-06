package diff

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gediff "github.com/argoproj/argo-cd/gitops-engine/v3/pkg/diff"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
	result, err := gediff.Diff(t.Context(), objectRemovedConfigMap("b"), objectRemovedConfigMap("a"),
		gediff.WithNormalizer(ignore), gediff.WithServerSideDiff(true), gediff.WithServerSideDryRunner(echoDryRunner{}),
		gediff.WithIgnoreMutationWebhook(false), gediff.WithManager("argocd-controller"))
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

	t.Run("added resource", func(t *testing.T) {
		results, failures, err := RunWithFailures(nil, []Document{objectRemovedDocument("broken", "b", false)}, Options{Unified: 3})
		if err != nil || len(failures) != 0 || len(results) != 1 || results[0].Change != ChangeAdded {
			t.Fatalf("RunWithFailures() = %#v, %#v, %v, want one added resource", results, failures, err)
		}
	})
}
