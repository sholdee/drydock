package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

func writeDisableExtensionFilterApplication(t *testing.T, root, name, directory string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+name+`
    targetRevision: main
    directory:
`+directory+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"-yaml\n")
	writeTestFile(t, filepath.Join(root, "manifests", name, "secret.yaml.sealed"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"-sealed\n")
	writeTestFile(t, filepath.Join(root, "manifests", name, "upper.YAML"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"-upper\n")
}

// TestOrchestratorBuildDirectoryDisableExtensionFilter pins that
// spec.source.directory.disableExtensionFilter reaches the directory
// renderer: include/exclude become the only filters, and the default filter
// stays case-sensitive.
func TestOrchestratorBuildDirectoryDisableExtensionFilter(t *testing.T) {
	root := t.TempDir()
	writeDisableExtensionFilterApplication(t, root, "filtered", "      include: '{*.yaml.sealed,*.yaml}'\n      disableExtensionFilter: true")
	writeDisableExtensionFilterApplication(t, root, "defaulted", "      recurse: false")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, name := range []string{"filtered-yaml", "filtered-sealed", "defaulted-yaml"} {
		if _, ok := manifestByName(result.Manifests, name); !ok {
			t.Errorf("Manifests missing %q", name)
		}
	}
	for _, name := range []string{"filtered-upper", "defaulted-sealed", "defaulted-upper"} {
		if _, ok := manifestByName(result.Manifests, name); ok {
			t.Errorf("Manifests include %q, want it filtered out", name)
		}
	}
	if hasDiagnosticCode(result.Diagnostics, render.DirectoryExtensionFilterDisabledCode) {
		t.Fatalf("Diagnostics = %#v, want no extension-filter warning when include is set", result.Diagnostics)
	}
}

// TestOrchestratorBuildDirectoryDisableExtensionFilterWithoutFiltersWarns
// pins the warning for disableExtensionFilter without include or exclude
// (Argo CD logs it and renders every file) and its --strict promotion.
func TestOrchestratorBuildDirectoryDisableExtensionFilterWithoutFiltersWarns(t *testing.T) {
	root := t.TempDir()
	writeDisableExtensionFilterApplication(t, root, "unfiltered", "      disableExtensionFilter: true")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, name := range []string{"unfiltered-yaml", "unfiltered-sealed", "unfiltered-upper"} {
		if _, ok := manifestByName(result.Manifests, name); !ok {
			t.Errorf("Manifests missing %q", name)
		}
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "unfiltered", Status: ApplicationStatusPass},
	})
	assertExtensionFilterDiagnostic(t, result.Diagnostics, diagnostic.SeverityWarning)

	result, err = (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatal("strict Build() error = nil, want extension-filter warning promoted to failure")
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "unfiltered", Status: ApplicationStatusFail},
	})
	assertExtensionFilterDiagnostic(t, result.Diagnostics, diagnostic.SeverityError)
}

func assertExtensionFilterDiagnostic(t *testing.T, diags []diagnostic.Diagnostic, severity diagnostic.Severity) {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == render.DirectoryExtensionFilterDisabledCode {
			if diag.Severity != severity {
				t.Fatalf("diagnostic = %#v, want severity %s", diag, severity)
			}
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want %s", diags, render.DirectoryExtensionFilterDisabledCode)
}
