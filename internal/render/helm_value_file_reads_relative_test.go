package render

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The default --path . is a relative repository root. The value files that
// Argo CD resolves are absolute, and they are still reported relative to
// the root.
func TestHelmValueFileReadsWithARelativeRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "install"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Chart.yaml", "values-argocd.yaml"} {
		if err := os.WriteFile(filepath.Join(root, "install", name), []byte("apiVersion: v2\nname: install\nversion: 0.1.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)

	reads := HelmValueFileReads(".", ResolvedSource{RepoRoot: ".", Path: "install"}, RenderOptions{ValueFiles: []string{"values-argocd.yaml"}}, false)
	if !slices.Contains(reads, "install/values-argocd.yaml") {
		t.Fatalf("HelmValueFileReads() = %v, want install/values-argocd.yaml", reads)
	}
}
