package manifest

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResourceFilterDropSkipsSecrets(t *testing.T) {
	filter := ResourceFilter{SkipSecrets: true}

	if !filter.Drop(testResource("v1", "Secret")) {
		t.Fatalf("Drop(Secret) = false, want true")
	}
	if filter.Drop(testResource("v1", "ConfigMap")) {
		t.Fatalf("Drop(ConfigMap) = true, want false")
	}
}

func TestResourceFilterDropSkipsCRDs(t *testing.T) {
	filter := ResourceFilter{SkipCRDs: true}

	if !filter.Drop(testResource("apiextensions.k8s.io/v1", "CustomResourceDefinition")) {
		t.Fatalf("Drop(CustomResourceDefinition) = false, want true")
	}
	if filter.Drop(testResource("apps/v1", "Deployment")) {
		t.Fatalf("Drop(Deployment) = true, want false")
	}
}

func TestResourceFilterDropSkipsConfiguredKinds(t *testing.T) {
	filter := ResourceFilter{SkipKinds: []string{"Deployment"}}

	if !filter.Drop(testResource("apps/v1", "Deployment")) {
		t.Fatalf("Drop(Deployment) = false, want true")
	}
	if filter.Drop(testResource("batch/v1", "Job")) {
		t.Fatalf("Drop(Job) = true, want false")
	}
}

func TestResourceFilterDropMatchesSkipKindsByKindOnly(t *testing.T) {
	filter := ResourceFilter{SkipKinds: []string{"Widget"}}

	if !filter.Drop(testResource("example.com/v1", "Widget")) {
		t.Fatalf("Drop(example.com Widget) = false, want true")
	}
	if !filter.Drop(testResource("other.example.com/v1", "Widget")) {
		t.Fatalf("Drop(other.example.com Widget) = false, want true")
	}

	groupOnlyFilter := ResourceFilter{SkipKinds: []string{"example.com"}}
	if groupOnlyFilter.Drop(testResource("example.com/v1", "Widget")) {
		t.Fatalf("Drop(example.com Widget) with group-only SkipKinds entry = true, want false")
	}

	groupQualifiedFilter := ResourceFilter{SkipKinds: []string{"example.com/Widget"}}
	if groupQualifiedFilter.Drop(testResource("example.com/v1", "Widget")) {
		t.Fatalf("Drop(example.com Widget) with group-qualified SkipKinds entry = true, want false")
	}
}

func TestResourceFilterDropTrimsSkipKindsAndIgnoresEmptyEntries(t *testing.T) {
	filter := ResourceFilter{SkipKinds: []string{"  Deployment\t", "", " \n "}}

	if !filter.Drop(testResource("apps/v1", "Deployment")) {
		t.Fatalf("Drop(Deployment) = false, want true")
	}
	if filter.Drop(testResource("v1", "")) {
		t.Fatalf("Drop(empty kind) = true, want false")
	}
}

func TestResourceFilterEmptyKeepsAllObjects(t *testing.T) {
	filter := ResourceFilter{}

	if !filter.Empty() {
		t.Fatalf("Empty() = false, want true")
	}
	if filter.Drop(testResource("v1", "Secret")) {
		t.Fatalf("Drop(Secret) = true, want false")
	}
	if filter.Drop(testResource("apiextensions.k8s.io/v1", "CustomResourceDefinition")) {
		t.Fatalf("Drop(CustomResourceDefinition) = true, want false")
	}
	if filter.Drop(testResource("apps/v1", "Deployment")) {
		t.Fatalf("Drop(Deployment) = true, want false")
	}
}

func TestResourceFilterDropKeepsNilObjects(t *testing.T) {
	filter := ResourceFilter{
		SkipKinds:   []string{"Deployment"},
		SkipCRDs:    true,
		SkipSecrets: true,
	}

	if filter.Drop(nil) {
		t.Fatalf("Drop(nil) = true, want false")
	}
}

func testResource(apiVersion string, kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata": map[string]any{
				"name": "test",
			},
		},
	}
}
