package diff

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func annotationDeploymentDocument(replicas string, annotationPointers []string) Document {
	doc := deploymentDocument(replicas, nil)
	doc.Normalization.AnnotationJSONPointers = annotationPointers
	return doc
}

func TestRunAnnotationJSONPointersSuppressDiff(t *testing.T) {
	left := []Document{annotationDeploymentDocument("1", []string{"/spec/replicas"})}
	right := []Document{annotationDeploymentDocument("2", []string{"/spec/replicas"})}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want annotation-ignored replicas diff suppressed: %#v", len(results), results)
	}
}

func TestRunAnnotationJSONPointersStayActiveWithShowIgnoredFields(t *testing.T) {
	left := []Document{annotationDeploymentDocument("1", []string{"/spec/replicas"})}
	right := []Document{annotationDeploymentDocument("2", []string{"/spec/replicas"})}

	results, err := Run(left, right, Options{Unified: 3, ShowIgnoredFields: true})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want annotation ignore active under ShowIgnoredFields: %#v", len(results), results)
	}
}

func TestRunAnnotationJSONPointersReadFromCurrentSide(t *testing.T) {
	// Only the current (head) side carries the annotation: its rule applies to
	// both sides, as Argo CD applies a target-side rule to live and target.
	left := []Document{annotationDeploymentDocument("1", nil)}
	right := []Document{annotationDeploymentDocument("2", []string{"/spec/replicas"})}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want head-side annotation to suppress replicas diff: %#v", len(results), results)
	}
}

func TestRunAnnotationJSONPointersIgnoreBaselineWhenCurrentSideExists(t *testing.T) {
	// Only the baseline carries the annotation and the resource still exists
	// on the current side: the head-side manifest is authoritative, so the
	// baseline rule does not hide the change.
	left := []Document{annotationDeploymentDocument("1", []string{"/spec/replicas"})}
	right := []Document{annotationDeploymentDocument("2", nil)}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want replicas diff kept: %#v", len(results), results)
	}
	for _, want := range []string{"-  replicas: 1", "+  replicas: 2"} {
		if !strings.Contains(results[0].Diff, want) {
			t.Fatalf("Diff = %q, want substring %q", results[0].Diff, want)
		}
	}
}

// TestRunAnnotationJSONPointersSkipRemovedResource checks that a resource
// that exists only on the baseline side is not normalized by its own
// annotation. Argo CD reads the annotation only from target objects
// (util/argo/diff/normalize.go:14-15, ignore.go:130-131), and a removed
// resource has no target.
func TestRunAnnotationJSONPointersSkipRemovedResource(t *testing.T) {
	left := []Document{annotationDeploymentDocument("1", []string{"/spec/replicas"})}

	results, err := Run(left, nil, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || results[0].Change != ChangeRemoved {
		t.Fatalf("results = %#v, want one removed resource", results)
	}
	if !strings.Contains(results[0].Diff, "-  replicas: 1") {
		t.Fatalf("Diff = %q, want baseline annotation pointer not applied to removed resource", results[0].Diff)
	}
}

func TestRunAnnotationJSONPointersSkipInvalidAndRootPointers(t *testing.T) {
	pointers := []string{
		"",
		"spec/replicas",
		"/spec/template/spec/containers/web/image",
		"/spec/template/spec/containers/-",
		"/bad~2escape",
		"/metadata/annotations/missing",
		"/spec/template/spec/containers/0/env/1",
	}
	left := []Document{annotationDeploymentDocument("1", pointers)}
	right := []Document{annotationDeploymentDocument("2", pointers)}

	results, err := Run(left, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v, want invalid annotation pointers skipped", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want resource kept (root pointer must not remove it): %#v", len(results), results)
	}
	diff := results[0].Diff
	for _, want := range []string{"-  replicas: 1", "+  replicas: 2"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("Diff = %q, want substring %q", diff, want)
		}
	}
	if !strings.Contains(results[0].After, "name: first") {
		t.Fatalf("After = %q, want untouched env entry kept", results[0].After)
	}
	if strings.Contains(results[0].After, "name: second") {
		t.Fatalf("After = %q, want valid pointer /spec/template/spec/containers/0/env/1 applied", results[0].After)
	}
}

func TestRunAnnotationJSONPointersUseJSONPatchPathSemantics(t *testing.T) {
	// Argo CD applies annotation pointers with evanphx/json-patch, which drops
	// the first path segment unchecked and accepts negative array indexes.
	pointers := []string{"#/spec/replicas", "/spec/template/spec/containers/-1/env/-1"}
	left := annotationDeploymentDocument("1", pointers)
	right := annotationDeploymentDocument("2", pointers)
	right.Body = strings.Replace(right.Body, "value: two", "value: changed", 1)

	results, err := Run([]Document{left}, []Document{right}, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want replicas and last env entry ignored: %#v", len(results), results)
	}
}

func TestRunAnnotationRootPointerDoesNotHideAddedResource(t *testing.T) {
	right := []Document{annotationDeploymentDocument("2", []string{""})}

	results, err := Run(nil, right, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || results[0].Change != ChangeAdded {
		t.Fatalf("results = %#v, want added resource kept despite root annotation pointer", results)
	}
}

func TestRunAnnotationJSONPointersComposeWithStrictPointers(t *testing.T) {
	left := annotationDeploymentDocument("1", []string{"/spec/template/spec/containers/0/env"})
	right := annotationDeploymentDocument("2", []string{"/spec/template/spec/containers/0/env"})
	left.Normalization.ApplicationRules = []IgnoreRule{{JSONPointers: []string{"/spec/replicas"}}}
	right.Normalization.ApplicationRules = []IgnoreRule{{JSONPointers: []string{"/spec/replicas"}}}
	right.Body = strings.Replace(right.Body, "value: two", "value: changed", 1)

	results, err := Run([]Document{left}, []Document{right}, Options{Unified: 3})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want strict and annotation pointers both applied: %#v", len(results), results)
	}
}

// TestRemoveAnnotationJSONPointersParityWithArgoCD checks the lenient pointer
// removal against Argo CD v3.6.1's ignore normalizer, which turns each
// annotation pointer into a JSON-patch remove op and skips failed applies.
func TestRemoveAnnotationJSONPointersParityWithArgoCD(t *testing.T) {
	body := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
  annotations:
    a/b: slash
    t~x: tilde
    bad~2: literal
    t~: trailing
    keep: me
spec:
  replicas: 3
  paused: null
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:v1
          env:
            - name: first
              value: one
            - name: second
              value: two
        - name: sidecar
          image: ghcr.io/example/sidecar:v1
`
	tests := []struct {
		name     string
		pointers []string
	}{
		{name: "field", pointers: []string{"/spec/replicas"}},
		{name: "escaped keys", pointers: []string{"/metadata/annotations/a~1b", "/metadata/annotations/t~0x"}},
		{name: "array element", pointers: []string{"/spec/template/spec/containers/1"}},
		{name: "nested array element", pointers: []string{"/spec/template/spec/containers/0/env/0"}},
		{name: "sequential array removals", pointers: []string{"/spec/template/spec/containers/0", "/spec/template/spec/containers/0"}},
		{name: "missing field", pointers: []string{"/spec/missing", "/metadata/labels/missing"}},
		{name: "out of range index", pointers: []string{"/spec/template/spec/containers/5"}},
		{name: "root pointer", pointers: []string{""}},
		{name: "root pointer then field", pointers: []string{"", "/spec/replicas"}},
		{name: "missing leading slash", pointers: []string{"spec/replicas"}},
		{name: "non-numeric array index", pointers: []string{"/spec/template/spec/containers/web/image", "/spec/replicas"}},
		{name: "append index", pointers: []string{"/spec/template/spec/containers/-"}},
		{name: "duplicate pointer", pointers: []string{"/spec/replicas", "/spec/replicas"}},
		{name: "uri fragment prefix", pointers: []string{"#/spec/replicas"}},
		{name: "dot prefix", pointers: []string{"./spec/replicas"}},
		{name: "arbitrary first segment hits top-level key", pointers: []string{"x/metadata"}},
		{name: "arbitrary first segment then nested key", pointers: []string{"x/spec/replicas"}},
		{name: "slash only addresses empty key", pointers: []string{"/"}},
		{name: "negative index last element", pointers: []string{"/spec/template/spec/containers/-1"}},
		{name: "negative index first element", pointers: []string{"/spec/template/spec/containers/-2"}},
		{name: "negative index out of range", pointers: []string{"/spec/template/spec/containers/-3"}},
		{name: "negative index intermediate", pointers: []string{"/spec/template/spec/containers/-1/image"}},
		{name: "negative index nested", pointers: []string{"/spec/template/spec/containers/-2/env/-1"}},
		{name: "signed and zero-padded indexes", pointers: []string{"/spec/template/spec/containers/+0/env/01"}},
		{name: "unknown escape kept literal", pointers: []string{"/metadata/annotations/bad~2"}},
		{name: "trailing tilde kept literal", pointers: []string{"/metadata/annotations/t~"}},
		{name: "unknown escape missing key", pointers: []string{"/metadata/annotations/bad~2escape"}},
		{name: "traverse null", pointers: []string{"/spec/paused/x"}},
		{name: "traverse scalar", pointers: []string{"/spec/replicas/x"}},
		{name: "index into object", pointers: []string{"/spec/template/0"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			object, err := decodeDiffYAML(body)
			if err != nil {
				t.Fatalf("decodeDiffYAML() error = %v", err)
			}
			got := removeJSONPatchPointers(object, tt.pointers)

			upstream := upstreamAnnotationNormalize(t, body, tt.pointers)
			if !reflect.DeepEqual(jsonRoundTrip(t, got), jsonRoundTrip(t, upstream)) {
				t.Fatalf("removeJSONPatchPointers(%q)\n got: %#v\nwant: %#v", tt.pointers, jsonRoundTrip(t, got), jsonRoundTrip(t, upstream))
			}
		})
	}
}

func upstreamAnnotationNormalize(t *testing.T, body string, pointers []string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := yaml.Unmarshal([]byte(body), &object); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	obj := &unstructured.Unstructured{Object: jsonRoundTrip(t, object)}
	normalizer, err := normalizers.NewIgnoreNormalizer([]argoappv1.ResourceIgnoreDifferences{{
		Group:        "apps",
		Kind:         "Deployment",
		Name:         obj.GetName(),
		Namespace:    obj.GetNamespace(),
		JSONPointers: pointers,
	}}, nil, normalizers.IgnoreNormalizerOpts{})
	if err != nil {
		t.Fatalf("NewIgnoreNormalizer() error = %v", err)
	}
	if err := normalizer.Normalize(obj); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	return obj.Object
}

func jsonRoundTrip(t *testing.T, object map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
}
