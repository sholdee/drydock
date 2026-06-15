package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/render"
)

// findManifestByKindAndName returns the first manifest whose object has the
// given kind and name. Returns (zero, false) if not found.
func findManifestByKindAndName(manifests []render.Manifest, kind, name string) (render.Manifest, bool) {
	for _, m := range manifests {
		if m.Object == nil {
			continue
		}
		if m.Object.GetKind() == kind && m.Object.GetName() == name {
			return m, true
		}
	}
	return render.Manifest{}, false
}

// writeGatewayClassFixture writes an Application whose destination namespace is
// gateway-system, a GatewayClass CR (no namespace in source), and the
// corresponding CRD (spec.scope: Cluster).
func writeGatewayClassFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "gateway.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: gateway
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/gateway
  destination:
    server: https://kubernetes.default.svc
    namespace: gateway-system
`)
	writeTestFile(t, filepath.Join(root, "manifests", "gateway", "crd.yaml"), `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gatewayclasses.gateway.networking.k8s.io
spec:
  group: gateway.networking.k8s.io
  names:
    kind: GatewayClass
    plural: gatewayclasses
  scope: Cluster
  versions:
    - name: v1
      served: true
      storage: true
`)
	writeTestFile(t, filepath.Join(root, "manifests", "gateway", "gatewayclass.yaml"), `apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: cilium
`)
}

// TestOrchestratorBuildNormalizesClusterScopedCRNamespace verifies that the
// build pipeline strips the destination namespace from a cluster-scoped custom
// resource when its CRD (spec.scope: Cluster) is present in the same build.
func TestOrchestratorBuildNormalizesClusterScopedCRNamespace(t *testing.T) {
	root := t.TempDir()
	writeGatewayClassFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	m, ok := findManifestByKindAndName(result.Manifests, "GatewayClass", "cilium")
	if !ok {
		t.Fatalf("Manifests = %#v, want GatewayClass/cilium", result.Manifests)
	}
	if got := m.Object.GetNamespace(); got != "" {
		t.Fatalf("GatewayClass namespace = %q, want empty (cluster-scoped normalization)", got)
	}
}

// TestOrchestratorBuildDisabledCRDScopeSkipsNormalization verifies that when
// CRDScopeOptions.Disabled is true, the namespace strip is skipped and the
// CR retains the destination namespace Argo CD would have stamped onto it.
func TestOrchestratorBuildDisabledCRDScopeSkipsNormalization(t *testing.T) {
	root := t.TempDir()
	writeGatewayClassFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:            root,
		CRDScopeOptions: CRDScopeOptions{Disabled: true},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	m, ok := findManifestByKindAndName(result.Manifests, "GatewayClass", "cilium")
	if !ok {
		t.Fatalf("Manifests = %#v, want GatewayClass/cilium", result.Manifests)
	}
	if got := m.Object.GetNamespace(); got != "gateway-system" {
		t.Fatalf("GatewayClass namespace = %q, want gateway-system (normalization disabled)", got)
	}
}
