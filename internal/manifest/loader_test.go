package manifest

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDecodeDocumentsSkipsEmptyFlattensListAndRecordsSource(t *testing.T) {
	input := strings.NewReader(`
---
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
  namespace: default
---
kind: List
apiVersion: v1
items:
  - apiVersion: v1
    kind: Service
    metadata:
      name: api
      namespace: default
---
`)

	docs, err := DecodeDocuments("test.yaml", input)
	if err != nil {
		t.Fatalf("DecodeDocuments() error = %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("len(docs) = %d, want 2", len(docs))
	}
	if docs[0].Path != "test.yaml" || docs[0].Index != 1 {
		t.Fatalf("docs[0] source = %s document %d, want test.yaml document 1", docs[0].Path, docs[0].Index)
	}
	if docs[1].Path != "test.yaml" || docs[1].Index != 2 {
		t.Fatalf("docs[1] source = %s document %d, want test.yaml document 2", docs[1].Path, docs[1].Index)
	}
	if docs[0].Object.GetKind() != "ConfigMap" || docs[1].Object.GetKind() != "Service" {
		t.Fatalf("unexpected kinds: %s %s", docs[0].Object.GetKind(), docs[1].Object.GetKind())
	}
	if docs[0].RootObject != docs[0].Object {
		t.Fatal("docs[0].RootObject does not point at the root object")
	}
	if docs[1].RootObject == nil || docs[1].RootObject.GetKind() != "List" {
		t.Fatalf("docs[1].RootObject kind = %q, want List", docs[1].RootObject.GetKind())
	}
}

func TestDecodeDocumentRootsPreservesListWrapper(t *testing.T) {
	input := strings.NewReader(`
kind: List
items: []
`)

	docs, err := DecodeDocumentRoots("list.yaml", input)
	if err != nil {
		t.Fatalf("DecodeDocumentRoots() error = %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1", len(docs))
	}
	if docs[0].Object.GetKind() != "List" {
		t.Fatalf("root kind = %q, want List", docs[0].Object.GetKind())
	}
	if docs[0].RootObject != docs[0].Object {
		t.Fatal("RootObject does not point at root object")
	}
}

func TestDecodeDocumentsDecodesJSON(t *testing.T) {
	input := strings.NewReader(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"team-a"}}`)

	docs, err := DecodeDocuments("namespace.json", input)
	if err != nil {
		t.Fatalf("DecodeDocuments() error = %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1", len(docs))
	}
	if docs[0].Object.GetKind() != "Namespace" || docs[0].Object.GetName() != "team-a" {
		t.Fatalf("unexpected object: %#v", docs[0].Object.Object)
	}
}

func TestDecodeDocumentsDuplicateKeyErrorIncludesParserContext(t *testing.T) {
	input := strings.NewReader(`apiVersion: v1
kind: Service
metadata:
  name: first
metadata:
  name: second
`)

	_, err := DecodeDocuments("service.yaml", input)
	if err == nil {
		t.Fatal("DecodeDocuments() error = nil, want duplicate key error")
	}
	for _, want := range []string{"service.yaml document 0", "decode YAML document failed", "mapping key \"metadata\" already defined"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("DecodeDocuments() error = %q, want %q", err.Error(), want)
		}
	}
}

func TestDecodeDocumentsNormalizesYAMLNumbersForUnstructuredDeepCopy(t *testing.T) {
	input := strings.NewReader(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  replicas: 1
`)

	docs, err := DecodeDocuments("deployment.yaml", input)
	if err != nil {
		t.Fatalf("DecodeDocuments() error = %v", err)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("DeepCopy() panic = %v", recovered)
		}
	}()
	copy := docs[0].Object.DeepCopy()
	replicas, found, err := unstructured.NestedInt64(copy.Object, "spec", "replicas")
	if err != nil {
		t.Fatalf("NestedInt64() error = %v", err)
	}
	if !found || replicas != 1 {
		t.Fatalf("replicas = %d, found %t; want 1, true", replicas, found)
	}
}

func TestDecodeDocumentsNormalizesYAMLTimestampScalars(t *testing.T) {
	input := strings.NewReader(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: release
data:
  releaseDate: 2026-05-23
`)

	docs, err := DecodeDocuments("configmap.yaml", input)
	if err != nil {
		t.Fatalf("DecodeDocuments() error = %v", err)
	}

	value, found, err := unstructured.NestedString(docs[0].Object.Object, "data", "releaseDate")
	if err != nil {
		t.Fatalf("NestedString() error = %v", err)
	}
	if !found || value != "2026-05-23T00:00:00Z" {
		t.Fatalf("releaseDate = %q, found %t; want 2026-05-23T00:00:00Z, true", value, found)
	}
	if copy := docs[0].Object.DeepCopy(); copy.GetName() != "release" {
		t.Fatalf("DeepCopy().GetName() = %q, want release", copy.GetName())
	}
}

func TestDecodeDocumentsUnsignedIntegerOverflowErrorDoesNotIncludeManifestValue(t *testing.T) {
	const secretValue = "9223372036854775808"
	input := strings.NewReader(`
apiVersion: v1
kind: Secret
metadata:
  name: credentials
data:
  token: ` + secretValue + `
`)

	_, err := DecodeDocuments("secret.yaml", input)
	if err == nil {
		t.Fatalf("DecodeDocuments() error = nil, want overflow error")
	}
	if !strings.Contains(err.Error(), "YAML integer overflows int64") {
		t.Fatalf("error = %q, want overflow message", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}

func TestDecodeDocumentsRootScalarErrorDoesNotIncludeManifestValue(t *testing.T) {
	const secretValue = "secret-token-value"
	input := strings.NewReader(secretValue)

	_, err := DecodeDocuments("secret.yaml", input)
	if err == nil {
		t.Fatalf("DecodeDocuments() error = nil, want root type error")
	}
	if !strings.Contains(err.Error(), "unsupported root type") {
		t.Fatalf("error = %q, want unsupported root type", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}

func TestDecodeDocumentsListItemsErrorDoesNotIncludeManifestValue(t *testing.T) {
	const secretValue = "secret-token-value"
	input := strings.NewReader(`
apiVersion: v1
kind: List
items: ` + secretValue + `
`)

	_, err := DecodeDocuments("secrets.yaml", input)
	if err == nil {
		t.Fatalf("DecodeDocuments() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "secrets.yaml document 0 /items is not a list") {
		t.Fatalf("error = %q, want path and document index", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}

func TestDecodeDocumentsListItemErrorDoesNotIncludeManifestValue(t *testing.T) {
	input := strings.NewReader(`
apiVersion: v1
kind: List
items:
  - secret-token-value
`)

	_, err := DecodeDocuments("secrets.yaml", input)
	if err == nil {
		t.Fatalf("DecodeDocuments() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "secrets.yaml document 0 list item") {
		t.Fatalf("error = %q, want path and document index", err)
	}
	if strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}

func TestResourceIdentity(t *testing.T) {
	doc := Document{
		Object: mustUnstructured(t, map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]any{
				"name":      "web",
				"namespace": "default",
			},
		}),
	}

	id := IdentityOf(doc.Object)
	if id.Group != "apps" || id.Kind != "Deployment" || id.Namespace != "default" || id.Name != "web" {
		t.Fatalf("identity = %#v", id)
	}
	if got := id.String(); got != "apps/Deployment default/web" {
		t.Fatalf("String() = %q", got)
	}
}

func mustUnstructured(t *testing.T, obj map[string]any) *unstructured.Unstructured {
	t.Helper()
	return &unstructured.Unstructured{Object: obj}
}
