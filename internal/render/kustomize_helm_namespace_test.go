package render

import (
	"context"
	"path/filepath"
	"testing"

	"sigs.k8s.io/kustomize/api/konfig"
)

// A helm chart that hardcodes an explicit namespace different from the
// kustomization's namespace transformer (mirrors cert-manager's leaderelection
// RBAC pinned to kube-system) must keep that namespace — kustomize's
// NamespaceTransformer skips helm-generated resources, and drydock must too.
func TestKustomizeRendererPreservesHelmExplicitNamespaceUnderNamespaceTransformer(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "charts", "demo")
	// Role: explicit namespace different from the kustomization transformer (must be preserved).
	// ConfigMap: namespace-less (must stay namespace-less at render time — the transformer skips
	// it too — so ApplyDestinationNamespace can later default it to the destination namespace).
	writeTestChart(t, chartDir, `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: demo:leaderelection
  namespace: kube-system
rules: []
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo-marker
data:
  marker: present
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: demo
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
    valuesFile: values.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "values.yaml"), "{}\n")

	acquirer := &fakeChartAcquirer{chartDir: chartDir}
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: acquirer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}

	roles := filterObjects(result, "Role")
	if len(roles) != 1 {
		t.Fatalf("len(roles) = %d, want 1", len(roles))
	}
	if ns := roles[0].GetNamespace(); ns != "kube-system" {
		t.Fatalf("Role namespace = %q, want %q (namespace transformer must skip helm-generated resources)", ns, "kube-system")
	}
	if _, ok := roles[0].GetAnnotations()[konfig.HelmGeneratedAnnotation]; ok {
		t.Fatalf("helm-generated build annotation leaked into rendered output")
	}

	configmaps := filterObjects(result, "ConfigMap")
	if len(configmaps) != 1 {
		t.Fatalf("len(configmaps) = %d, want 1", len(configmaps))
	}
	// Namespace-less helm resource must stay namespace-less at render time (transformer
	// skips it); ApplyDestinationNamespace defaults it to the destination namespace
	// downstream — matching ArgoCD. Without the fix the transformer would stamp the
	// kustomization namespace ("demo") here.
	if ns := configmaps[0].GetNamespace(); ns != "" {
		t.Fatalf("ConfigMap namespace = %q, want empty (namespace transformer must skip helm-generated resources)", ns)
	}
	if _, ok := configmaps[0].GetAnnotations()[konfig.HelmGeneratedAnnotation]; ok {
		t.Fatalf("helm-generated build annotation leaked into rendered output")
	}
}
