package app

import (
	"context"
	"path/filepath"
	"testing"
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
	if hasDiagnosticCode(result.Diagnostics, removedExtensionFilterCode) {
		t.Fatalf("Diagnostics = %#v, want no extension-filter warning when include is set", result.Diagnostics)
	}
}

// removedExtensionFilterCode is the code of the warning that drydock gave
// before for disableExtensionFilter without include or exclude.
const removedExtensionFilterCode = "directory.extension-filter-disabled"

// TestOrchestratorBuildDirectoryDisableExtensionFilterWithoutFiltersIsQuiet
// pins that disableExtensionFilter without include or exclude renders every
// file and gives no diagnostic. Argo CD only logs it and renders every file,
// and the setting is valid, so drydock reports nothing, with or without
// --strict.
func TestOrchestratorBuildDirectoryDisableExtensionFilterWithoutFiltersIsQuiet(t *testing.T) {
	root := t.TempDir()
	writeDisableExtensionFilterApplication(t, root, "unfiltered", "      disableExtensionFilter: true")

	for _, strict := range []bool{false, true} {
		result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, Strict: strict})
		if err != nil {
			t.Fatalf("Build(strict=%t) error = %v", strict, err)
		}
		for _, name := range []string{"unfiltered-yaml", "unfiltered-sealed", "unfiltered-upper"} {
			if _, ok := manifestByName(result.Manifests, name); !ok {
				t.Errorf("Manifests missing %q", name)
			}
		}
		assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
			{Namespace: "argocd", Name: "unfiltered", Status: ApplicationStatusPass},
		})
		for _, diag := range result.Diagnostics {
			if diag.Code == removedExtensionFilterCode || diag.Category == "directory" {
				t.Fatalf("Diagnostics = %#v, want no directory diagnostic", result.Diagnostics)
			}
		}
	}
}
