package manifest

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func crd(group, kind, scope string, versions ...string) *unstructured.Unstructured {
	versionEntries := make([]any, 0, len(versions))
	for _, v := range versions {
		versionEntries = append(versionEntries, map[string]any{"name": v})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "objs." + group},
		"spec": map[string]any{
			"group":    group,
			"scope":    scope,
			"names":    map[string]any{"kind": kind},
			"versions": versionEntries,
		},
	}}
}

func TestBuildCRDScopeRegistryRecordsClusterScope(t *testing.T) {
	reg := BuildCRDScopeRegistry([]*unstructured.Unstructured{
		crd("gateway.networking.k8s.io", "GatewayClass", "Cluster", "v1"),
		crd("example.com", "Widget", "Namespaced", "v1"),
	})
	if !reg.IsClusterScoped(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GatewayClass"}) {
		t.Fatal("IsClusterScoped(GatewayClass) = false, want true")
	}
	if reg.IsClusterScoped(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"}) {
		t.Fatal("IsClusterScoped(Widget) = true, want false")
	}
}

func TestCRDScopeRegistryScopeReportsKnownAndUnknown(t *testing.T) {
	reg := BuildCRDScopeRegistry([]*unstructured.Unstructured{crd("example.com", "Widget", "Namespaced", "v1")})
	scope, ok := reg.Scope(schema.GroupVersionKind{Group: "example.com", Kind: "Widget"})
	if !ok || scope != CRDScopeNamespaced {
		t.Fatalf("Scope(Widget) = %q, %v; want %q, true", scope, ok, CRDScopeNamespaced)
	}
	if _, ok := reg.Scope(schema.GroupVersionKind{Group: "example.com", Kind: "Unknown"}); ok {
		t.Fatal("Scope(Unknown) ok = true, want false")
	}
}

func TestIsClusterScopedFallsBackToBuiltIn(t *testing.T) {
	reg := BuildCRDScopeRegistry(nil)
	if !reg.IsClusterScoped(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}) {
		t.Fatal("IsClusterScoped(ClusterRole) = false, want built-in true")
	}
}

func TestBuildCRDScopeRegistryIgnoresNonCRDAndMalformed(t *testing.T) {
	reg := BuildCRDScopeRegistry([]*unstructured.Unstructured{
		nil,
		{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap"}},
		{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"spec":       map[string]any{"group": "x.io"},
		}},
	})
	if len(reg) != 0 {
		t.Fatalf("registry size = %d, want 0", len(reg))
	}
}
