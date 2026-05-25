package render

import (
	"context"
	"path/filepath"
	"testing"
)

func TestKustomizeRendererRendersResources(t *testing.T) {
	renderer := KustomizeRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications"),
		Path:     "kustomize",
	}

	result, diags, err := renderer.Render(context.Background(), source, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Object.GetKind() != "ConfigMap" || result[0].Object.GetName() != "kustomized" {
		t.Fatalf("unexpected object: %#v", result[0].Object)
	}
	if result[0].Path != filepath.Join("kustomize", "kustomization.yaml") {
		t.Fatalf("Path = %q, want kustomize/kustomization.yaml", result[0].Path)
	}
}

func TestKustomizeRendererAllowsRepoRootLocalComponents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "namespace", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - serviceaccount.yaml
`)
	writeFile(t, filepath.Join(root, "components", "namespace", "serviceaccount.yaml"), `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: demo
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: demo
components:
  - ../../components/namespace
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
}
