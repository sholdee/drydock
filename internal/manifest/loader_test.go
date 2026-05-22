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
