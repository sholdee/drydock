package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/diagnostic"
)

func TestOrchestratorDiffAppsReportsManifestChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %s, want modified", result.Results[0].Change)
	}
}

func TestOrchestratorDiffAppsChangedOnlyFallsBackOnUnownedCurrentPath(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeSimpleApp(t, right, "new")
	writeTestFile(t, filepath.Join(left, "README.md"), "left\n")
	writeTestFile(t, filepath.Join(right, "README.md"), "right\n")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		ChangedOnly: true,
		Unified:     3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	diag := result.Diagnostics[0]
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
	}
	if diag.Category != "changed-only" || !strings.Contains(diag.Message, "README.md") {
		t.Fatalf("diagnostic = %#v, want changed-only warning for README.md", diag)
	}

	strictResult, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want strict changed-only error")
	}
	if len(strictResult.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(strictResult.Diagnostics), strictResult.Diagnostics)
	}
	if strictResult.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("strict diagnostic severity = %s, want error", strictResult.Diagnostics[0].Severity)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsApplicationManifestChanges(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppWithDestination(t, left, "old")
	writeSimpleAppWithDestination(t, right, "new")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) == 0 {
		t.Fatal("len(Results) = 0, want rendered diff from Application manifest change")
	}
	var sawNewNamespace bool
	for _, item := range result.Results {
		if item.Resource.Namespace == "new" {
			sawNewNamespace = true
		}
	}
	if !sawNewNamespace {
		t.Fatalf("Results = %#v, want diff for new namespace", result.Results)
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlyOwnsDeletedApplicationManifestFromLeft(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleApp(t, left, "old")
	writeTestFile(t, filepath.Join(right, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: old
`)

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("len(Diagnostics) = %d, want 0: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "removed" {
		t.Fatalf("Change = %s, want removed", result.Results[0].Change)
	}
}

func writeSimpleApp(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: `+value+`
`)
}

func writeSimpleAppWithDestination(t *testing.T, root, namespace string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: `+namespace+`
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: same
`)
}
