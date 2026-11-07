package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeComponentGlobApps writes the Application glob-components, whose
// spec.source.kustomize.components glob "../components/[ab]" matches the
// components under workloads/glob/components, one per name in parts, and an
// unrelated Application.
func writeComponentGlobApps(t *testing.T, root string, parts ...string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "glob-components.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: glob-components
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/glob/app
    kustomize:
      components:
        - ../components/[ab]
  destination:
    name: in-cluster
    namespace: glob
`)
	writeTestFile(t, filepath.Join(root, "workloads", "glob", "app", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - configmap.yaml\n")
	writeTestFile(t, filepath.Join(root, "workloads", "glob", "app", "configmap.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")
	for _, part := range parts {
		writeTestFile(t, filepath.Join(root, "workloads", "glob", "components", part, "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - configmap.yaml\n")
		writeTestFile(t, filepath.Join(root, "workloads", "glob", "components", part, "configmap.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: component-"+part+"\n")
	}
	writeDiffApplication(t, root, "other", "other", "same")
}

func diffComponentGlobApps(t *testing.T, left, right string) DiffResult {
	t.Helper()
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:          left,
		RightPath:         right,
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", result.Diagnostics)
	}
	return result
}

// Removing a component that a spec.source.kustomize.components glob
// matched selects the Application in changed-only mode.
func TestChangedOnlySelectsRemovedComponentGlobMatch(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeComponentGlobApps(t, left, "a", "b")
	writeComponentGlobApps(t, right, "a")

	result := diffComponentGlobApps(t, left, right)
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	diff := result.Results[0].Diff
	if !strings.Contains(diff, "argocd/glob-components") || !strings.Contains(diff, "component-b") || strings.Contains(diff, "argocd/other") {
		t.Fatalf("diff = %s, want only glob-components without component-b", diff)
	}
}

// A component glob owns the directory it lists as a path prefix, so a
// symlink elsewhere in that directory does not drop the ownership: a new
// entry that does not match is still owned, and strict changed-only mode
// does not report it as unowned.
func TestChangedOnlyComponentGlobOwnsDirectoryWithSymlink(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, side := range []string{left, right} {
		writeComponentGlobApps(t, side, "a")
		if err := os.Symlink("a", filepath.Join(side, "workloads", "glob", "components", "zlink")); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
	}
	writeTestFile(t, filepath.Join(right, "workloads", "glob", "components", "d", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n")

	result := diffComponentGlobApps(t, left, right)
	for _, diffResult := range result.Results {
		if strings.Contains(diffResult.Diff, "argocd/other") {
			t.Fatalf("diff included unrelated Application:\n%s", diffResult.Diff)
		}
	}
}
