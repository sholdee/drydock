package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestOrchestratorProjectDiagnosticsDefaultHidesDeferredBuildSessionDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeDeferredDestinationNameProjectFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:   root,
		Strict: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if hasProjectDiagnostic(result.Diagnostics) {
		t.Fatalf("Diagnostics = %#v, want no project diagnostics in default actionable mode", result.Diagnostics)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
}

func TestOrchestratorProjectDiagnosticsAllRestoresStrictBuildSessionFailure(t *testing.T) {
	root := t.TempDir()
	writeDeferredDestinationNameProjectFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:                   root,
		Strict:                 true,
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeAll,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want strict deferred project diagnostic failure")
	}
	if !hasDiagnosticStableCode(result.Diagnostics, diagnostic.CodeProjectUnspecified) {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, diagnostic.CodeProjectUnspecified)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusSkipped},
	})
}

func TestOrchestratorProjectDiagnosticsDefaultHidesDeferredRenderedResourcePolicyDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeRenderedUnknownCRScopeFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:   root,
		Strict: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if hasDiagnosticCode(result.Diagnostics, renderedResourceScopeDeferredCode) {
		t.Fatalf("Diagnostics = %#v, want %s hidden in default actionable mode", result.Diagnostics, renderedResourceScopeDeferredCode)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
}

func TestOrchestratorProjectDiagnosticsAllRestoresStrictRenderedResourcePolicyFailure(t *testing.T) {
	root := t.TempDir()
	writeRenderedUnknownCRScopeFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:                   root,
		Strict:                 true,
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeAll,
	})
	assertBuildErrorContains(t, err, "1 Application failed", "argocd/demo", "diagnostic project")
	if !hasDiagnosticCode(result.Diagnostics, renderedResourceScopeDeferredCode) {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, renderedResourceScopeDeferredCode)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusFail},
	})
}

func TestOrchestratorProjectDiagnosticsOffHidesActionableRenderedResourcePolicyDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeRenderedResourcePolicyDeniedFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:                   root,
		Strict:                 true,
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeOff,
	})
	if err != nil {
		t.Fatalf("Build() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if hasProjectDiagnostic(result.Diagnostics) {
		t.Fatalf("Diagnostics = %#v, want no project diagnostics in off mode", result.Diagnostics)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
}

func TestOrchestratorProjectDiagnosticsActionableKeepsActionableRenderedResourcePolicyFailure(t *testing.T) {
	root := t.TempDir()
	writeRenderedResourcePolicyDeniedFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:                   root,
		Strict:                 true,
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeActionable,
	})
	assertBuildErrorContains(t, err, "1 Application failed", "argocd/demo", "diagnostic project")
	if !hasDiagnosticCode(result.Diagnostics, renderedResourceDeniedCode) {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, renderedResourceDeniedCode)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusFail},
	})
}

func TestDiffRequestBuildRequestPropagatesProjectDiagnosticsMode(t *testing.T) {
	request := DiffRequest{
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeOff,
	}

	left := request.buildRequest("left", nil)
	right := request.buildRequest("right", nil)

	if left.ProjectDiagnosticsMode != diagnostic.ProjectDiagnosticsModeOff {
		t.Fatalf("left ProjectDiagnosticsMode = %q, want off", left.ProjectDiagnosticsMode)
	}
	if right.ProjectDiagnosticsMode != diagnostic.ProjectDiagnosticsModeOff {
		t.Fatalf("right ProjectDiagnosticsMode = %q, want off", right.ProjectDiagnosticsMode)
	}
}

func hasProjectDiagnostic(diags []diagnostic.Diagnostic) bool {
	for _, diag := range diags {
		if diagnostic.ClassifyProjectDiagnostic(diag) != diagnostic.ProjectDiagnosticClassNonProject {
			return true
		}
	}
	return false
}

func hasDiagnosticStableCode(diags []diagnostic.Diagnostic, code string) bool {
	for _, diag := range diags {
		if diag.Code == code || diagnostic.StableCode(diag) == code {
			return true
		}
	}
	return false
}

func writeDeferredDestinationNameProjectFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: platform
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: workloads
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: rendered
`)
	writeTestFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/repo
  destinations:
    - server: https://kubernetes.default.svc
      namespace: '*'
`)
}
