package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

const (
	renderedResourceDeniedCode        = "project.resource-denied"
	renderedResourceScopeDeferredCode = "project.resource-scope-deferred"
)

func TestOrchestratorBuildReportsRenderedResourcePolicyDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeRenderedResourcePolicyDeniedFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	diag := assertRenderedResourcePolicyDiagnostic(t, result.Diagnostics)
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("resource policy diagnostic severity = %s, want warning", diag.Severity)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
}

func TestOrchestratorBuildStrictFailsRenderedResourcePolicyDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeRenderedResourcePolicyDeniedFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	assertBuildErrorContains(t, err, "1 Application failed", "argocd/demo", "diagnostic project")

	diag := assertRenderedResourcePolicyDiagnostic(t, result.Diagnostics)
	if diag.Severity != diagnostic.SeverityError {
		t.Fatalf("resource policy diagnostic severity = %s, want error", diag.Severity)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusFail},
	})
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want no manifests retained for failed application", len(result.Manifests))
	}
}

func TestOrchestratorBuildStatusOnlyReportsRenderedResourcePolicyDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeRenderedResourcePolicyDeniedFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, StatusOnly: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	diag := assertRenderedResourcePolicyDiagnostic(t, result.Diagnostics)
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("resource policy diagnostic severity = %s, want warning", diag.Severity)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0 for status-only build", len(result.Manifests))
	}
	if len(result.ApplicationManifests) != 0 {
		t.Fatalf("len(ApplicationManifests) = %d, want 0 for status-only build", len(result.ApplicationManifests))
	}
}

func TestOrchestratorBuildStatusOnlyStrictFailsRenderedResourcePolicyDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeRenderedResourcePolicyDeniedFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true, StatusOnly: true})
	assertBuildErrorContains(t, err, "1 Application failed", "argocd/demo", "diagnostic project")

	diag := assertRenderedResourcePolicyDiagnostic(t, result.Diagnostics)
	if diag.Severity != diagnostic.SeverityError {
		t.Fatalf("resource policy diagnostic severity = %s, want error", diag.Severity)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusFail},
	})
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0 for status-only build", len(result.Manifests))
	}
	if len(result.ApplicationManifests) != 0 {
		t.Fatalf("len(ApplicationManifests) = %d, want 0 for status-only build", len(result.ApplicationManifests))
	}
}

func TestOrchestratorBuildResourcePolicyDiagnosticsIgnoreOutputFilters(t *testing.T) {
	tests := []struct {
		name    string
		filters FilterOptions
	}{
		{
			name: "skip secrets",
			filters: FilterOptions{
				SkipSecrets: true,
			},
		},
		{
			name: "skip kind",
			filters: FilterOptions{
				SkipKinds: []string{"Secret"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeRenderedSecretResourcePolicyDeniedFixture(t, root)

			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
				Path:          root,
				Strict:        true,
				FilterOptions: tt.filters,
			})
			assertBuildErrorContains(t, err, "1 Application failed", "argocd/demo", "diagnostic project")

			diag := assertRenderedResourcePolicyDiagnostic(t, result.Diagnostics)
			if diag.Severity != diagnostic.SeverityError {
				t.Fatalf("resource policy diagnostic severity = %s, want error", diag.Severity)
			}
			if len(result.Manifests) != 0 {
				t.Fatalf("len(Manifests) = %d, want no manifests retained for failed application", len(result.Manifests))
			}
		})
	}
}

func TestOrchestratorListApplicationsDoesNotReportRenderedResourcePolicyDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeRenderedResourcePolicyDeniedFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	if hasDiagnosticCode(result.Diagnostics, renderedResourceDeniedCode) {
		t.Fatalf("Diagnostics = %#v, want no rendered resource policy diagnostic during discovery", result.Diagnostics)
	}
}

func TestOrchestratorBuildDefersUnknownCRScopeBeforeNamespaceNormalization(t *testing.T) {
	root := t.TempDir()
	writeRenderedUnknownCRScopeFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:                   root,
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeAll,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if !hasDiagnosticCode(result.Diagnostics, renderedResourceScopeDeferredCode) {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, renderedResourceScopeDeferredCode)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if got := result.Manifests[0].Object.GetNamespace(); got != "workloads" {
		t.Fatalf("rendered custom resource namespace = %q, want workloads", got)
	}
}

func writeRenderedResourcePolicyDeniedFixture(t *testing.T, root string) {
	t.Helper()
	writeBuildApplicationWithProject(t, root, "demo", "denied-cm", "platform", "https://github.com/example/repo", "workloads")
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
  namespaceResourceWhitelist:
    - group: apps
      kind: Deployment
`)
}

func writeRenderedSecretResourcePolicyDeniedFixture(t *testing.T, root string) {
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
  destination:
    server: https://kubernetes.default.svc
    namespace: workloads
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: credentials
stringData:
  token: redacted
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
      namespace: workloads
  namespaceResourceWhitelist:
    - group: ""
      kind: ConfigMap
`)
}

func writeRenderedUnknownCRScopeFixture(t *testing.T, root string) {
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
  destination:
    server: https://kubernetes.default.svc
    namespace: workloads
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "widget.yaml"), `apiVersion: example.com/v1
kind: Widget
metadata:
  name: custom
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
      namespace: workloads
`)
}

func assertRenderedResourcePolicyDiagnostic(t *testing.T, diags []diagnostic.Diagnostic) diagnostic.Diagnostic {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == renderedResourceDeniedCode {
			return diag
		}
	}
	t.Fatalf("Diagnostics = %#v, want %s", diags, renderedResourceDeniedCode)
	return diagnostic.Diagnostic{}
}
