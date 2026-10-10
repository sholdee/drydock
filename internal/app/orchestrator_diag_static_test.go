package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

const staticProjectSourceDenied = `source repository "https://github.com/example/repo" is not permitted by AppProject "platform"`

// writeStaticProjectDenialFixture writes an Application whose source
// repository the local AppProject does not permit: a spec-level denial that
// needs no rendering to detect.
func writeStaticProjectDenialFixture(t *testing.T, root string) {
	t.Helper()
	writeBuildApplicationWithProject(t, root, "demo", "demo-cm", "platform", "https://github.com/example/repo", "workloads")
	writeTestFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/other
  destinations:
    - server: https://kubernetes.default.svc
      namespace: workloads
`)
}

func TestOrchestratorDiagStaticReportsProjectDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeStaticProjectDenialFixture(t, root)

	static, err := Orchestrator{}.DiagStatic(context.Background(), DiagRequest{Path: root})
	if err != nil {
		t.Fatalf("DiagStatic() error = %v", err)
	}
	if !hasDiagnosticMessage(static.Diagnostics, staticProjectSourceDenied) {
		t.Fatalf("DiagStatic() Diagnostics = %#v, want source repository denial", static.Diagnostics)
	}
	if len(static.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(static.Applications))
	}

	// The render-backed path reports the same spec-level denial, so static
	// and rendered diag agree on everything that does not need manifests.
	rendered, err := Orchestrator{}.Diag(context.Background(), DiagRequest{Path: root})
	if err != nil {
		t.Fatalf("Diag() error = %v", err)
	}
	if !hasDiagnosticMessage(rendered.Diagnostics, staticProjectSourceDenied) {
		t.Fatalf("Diag() Diagnostics = %#v, want source repository denial", rendered.Diagnostics)
	}
}

func TestOrchestratorDiagStaticHonoursProjectDiagnosticsOff(t *testing.T) {
	root := t.TempDir()
	writeStaticProjectDenialFixture(t, root)

	result, err := Orchestrator{}.DiagStatic(context.Background(), DiagRequest{Path: root, ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeOff})
	if err != nil {
		t.Fatalf("DiagStatic() error = %v", err)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "project" {
			t.Fatalf("Diagnostics = %#v, want no project diagnostics with --project-diagnostics=off", result.Diagnostics)
		}
	}
}

func TestOrchestratorDiagStaticStrictFailsProjectDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeStaticProjectDenialFixture(t, root)

	result, err := Orchestrator{}.DiagStatic(context.Background(), DiagRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatalf("DiagStatic() error = nil, want strict failure: %#v", result.Diagnostics)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "project" && diag.Severity != diagnostic.SeverityError {
			t.Fatalf("strict project diagnostic severity = %s, want error: %#v", diag.Severity, diag)
		}
	}
	if !hasDiagnosticMessage(result.Diagnostics, staticProjectSourceDenied) {
		t.Fatalf("Diagnostics = %#v, want the denial retained alongside the strict failure", result.Diagnostics)
	}
}

func TestOrchestratorDiagStaticCleanProjectReportsNothing(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithProject(t, root, "demo", "demo-cm", "platform", "https://github.com/example/repo", "workloads")
	writeTestFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/repo
  destinations:
    - server: https://kubernetes.default.svc
      namespace: workloads
`)

	result, err := Orchestrator{}.DiagStatic(context.Background(), DiagRequest{Path: root, Strict: true})
	if err != nil {
		t.Fatalf("DiagStatic() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none for a permitted Application", result.Diagnostics)
	}
}
