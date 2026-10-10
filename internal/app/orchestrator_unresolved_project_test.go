package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// writeUnresolvedProjectFixture writes an apps-in-any-namespace Application
// whose AppProject lives only on the cluster: the repository declares none
// (issue #389).
func writeUnresolvedProjectFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "myapp.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: myapp
  namespace: argocd-tenant-a
spec:
  project: myproject
  source:
    repoURL: https://github.com/example/repo
    path: manifests/myapp
  destination:
    server: https://kubernetes.default.svc
    namespace: workloads
`)
	writeTestFile(t, filepath.Join(root, "manifests", "myapp", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: myapp-cm
data:
  value: demo
`)
}

func TestOrchestratorBuildUnresolvedProjectIsQuietInActionableMode(t *testing.T) {
	root := t.TempDir()
	writeUnresolvedProjectFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "project" {
			t.Fatalf("Diagnostics = %#v, want no project diagnostics in actionable mode", result.Diagnostics)
		}
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
}

func TestOrchestratorBuildUnresolvedProjectReportsDeferredDiagnosticInAllMode(t *testing.T) {
	root := t.TempDir()
	writeUnresolvedProjectFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeAll})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	var project []diagnostic.Diagnostic
	for _, diag := range result.Diagnostics {
		if diag.Category == "project" {
			project = append(project, diag)
		}
	}
	if len(project) != 1 || project[0].Code != diagnostic.CodeProjectUnresolved {
		t.Fatalf("project diagnostics = %#v, want exactly one %s", project, diagnostic.CodeProjectUnresolved)
	}
	if !hasDiagnosticMessage(result.Diagnostics, `references AppProject "myproject", which is not declared in the repository`) {
		t.Fatalf("Diagnostics = %#v, want unresolved-project message", result.Diagnostics)
	}
}

func TestOrchestratorDiffDedupesDiagnosticsAcrossSides(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeUnresolvedProjectFixture(t, left)
	writeUnresolvedProjectFixture(t, right)
	writeTestFile(t, filepath.Join(right, "manifests", "myapp", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: myapp-cm
data:
  value: changed
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:               left,
		RightPath:              right,
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeAll,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	// Both sides validate the same Application against the same absent
	// project; the diagnostic must appear once, as build and test report it.
	count := 0
	for _, diag := range result.Diagnostics {
		if diag.Code == diagnostic.CodeProjectUnresolved {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("unresolved-project diagnostics = %d, want 1 after dedupe: %#v", count, result.Diagnostics)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
}
