package app

import (
	"context"
	"path/filepath"
	"testing"
)

func writeIgnoreMissingComponentsApplication(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
    kustomize:
      ignoreMissingComponents: true
      components:
        - ../components/missing
        - ../components/first
        - ../components/second
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "kustomization.yaml"), "resources:\n  - cm.yaml\n")
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	for _, name := range []string{"first", "second"} {
		writeTestFile(t, filepath.Join(root, "manifests", "components", name, "kustomization.yaml"),
			"apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - cm.yaml\n")
		writeTestFile(t, filepath.Join(root, "manifests", "components", name, "cm.yaml"),
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: component-"+name+"\n")
	}
}

// TestOrchestratorBuildIgnoreMissingComponentsAddsEveryValidComponent pins
// Argo CD v3.6.2 (util/kustomize/kustomize.go:355-372): with
// ignoreMissingComponents every valid component is added. Argo CD v3.6.1
// added only the first one, and drydock then warned about component-second.
// No resource is missing now, so there is no warning and --strict passes.
func TestOrchestratorBuildIgnoreMissingComponentsAddsEveryValidComponent(t *testing.T) {
	root := t.TempDir()
	writeIgnoreMissingComponentsApplication(t, root)

	for _, strict := range []bool{false, true} {
		result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, Strict: strict})
		if err != nil {
			t.Fatalf("Build() strict=%t error = %v", strict, err)
		}
		for _, name := range []string{"component-first", "component-second"} {
			if _, ok := manifestByName(result.Manifests, name); !ok {
				t.Errorf("strict=%t Manifests missing %s", strict, name)
			}
		}
		assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
			{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
		})
		for _, diag := range result.Diagnostics {
			if diag.Category == "kustomize" {
				t.Errorf("strict=%t Diagnostics hold %#v, want no kustomize diagnostic", strict, diag)
			}
		}
	}
}
