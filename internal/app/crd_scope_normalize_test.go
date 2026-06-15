package app

import (
	"testing"

	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func clusterScopedCRDManifest() render.Manifest {
	return render.Manifest{Object: &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "gatewayclasses.gateway.networking.k8s.io"},
		"spec": map[string]any{
			"group":    "gateway.networking.k8s.io",
			"scope":    "Cluster",
			"names":    map[string]any{"kind": "GatewayClass"},
			"versions": []any{map[string]any{"name": "v1"}},
		},
	}}}
}

func crManifest(apiVersion, kind, namespace, name string) render.Manifest {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	return render.Manifest{Object: obj}
}

func TestNormalizeCRDScopeStripsClusterScopedNamespace(t *testing.T) {
	manifests := []render.Manifest{
		clusterScopedCRDManifest(),
		crManifest("gateway.networking.k8s.io/v1", "GatewayClass", "kube-system", "cilium"),
		crManifest("example.com/v1", "Widget", "apps", "namespaced-cr"),
	}
	registry := manifest.BuildCRDScopeRegistry(manifestObjects(manifests))
	diags := normalizeCRDScope(manifests, registry)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
	if ns := manifests[1].Object.GetNamespace(); ns != "" {
		t.Fatalf("GatewayClass namespace = %q, want empty", ns)
	}
	if ns := manifests[2].Object.GetNamespace(); ns != "apps" {
		t.Fatalf("Widget namespace = %q, want apps (untouched)", ns)
	}
}

func TestNormalizeCRDScopeWarnsOnClusterScopedCollision(t *testing.T) {
	manifests := []render.Manifest{
		clusterScopedCRDManifest(),
		crManifest("gateway.networking.k8s.io/v1", "GatewayClass", "ns-a", "dup"),
		crManifest("gateway.networking.k8s.io/v1", "GatewayClass", "ns-b", "dup"),
	}
	registry := manifest.BuildCRDScopeRegistry(manifestObjects(manifests))
	diags := normalizeCRDScope(manifests, registry)
	found := false
	for _, d := range diags {
		if d.Code == "build.crd-scope-collision" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %#v, want build.crd-scope-collision", diags)
	}
}
