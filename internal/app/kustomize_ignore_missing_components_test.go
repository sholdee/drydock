package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
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

// TestOrchestratorBuildIgnoreMissingComponentsDropsLaterValidComponents pins
// Argo CD v3.6.1 (util/kustomize/kustomize.go:346-372): with
// ignoreMissingComponents only the first valid component is added. drydock
// warns about the dropped component-second, and --strict promotes that
// warning to an error, because the render lacks resources the components
// list names.
func TestOrchestratorBuildIgnoreMissingComponentsDropsLaterValidComponents(t *testing.T) {
	root := t.TempDir()
	writeIgnoreMissingComponentsApplication(t, root)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "component-first"); !ok {
		t.Errorf("Manifests missing the first valid component")
	}
	if _, ok := manifestByName(result.Manifests, "component-second"); ok {
		t.Errorf("Manifests include component-second, want it dropped as Argo CD v3.6.1 does")
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	assertDiagnosticSeverity(t, result.Diagnostics, render.KustomizeIgnoreMissingComponentsDroppedCode, diagnostic.SeverityWarning)

	result, err = (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatalf("strict Build() error = nil, want the %s warning promoted to an error", render.KustomizeIgnoreMissingComponentsDroppedCode)
	}
	assertDiagnosticSeverity(t, result.Diagnostics, render.KustomizeIgnoreMissingComponentsDroppedCode, diagnostic.SeverityError)
}

func assertDiagnosticSeverity(t *testing.T, diags []diagnostic.Diagnostic, code string, severity diagnostic.Severity) {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == code {
			if diag.Severity != severity {
				t.Fatalf("diagnostic = %#v, want severity %s", diag, severity)
			}
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want %s", diags, code)
}
