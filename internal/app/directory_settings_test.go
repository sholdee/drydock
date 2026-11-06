package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/render"
)

// TestMaxCombinedDirectoryManifestsSize pins how drydock reads
// reposerver.max.combined.directory.manifests.size. The Argo CD v3.6.1
// repo-server defaults the flag to 10M and parses it with resource.MustParse
// (cmd/argocd-repo-server/commands/argocd_repo_server.go:264), so 10M is
// 10000000 bytes and 0 turns the limit off.
func TestMaxCombinedDirectoryManifestsSize(t *testing.T) {
	for value, want := range map[string]int64{
		"":     10000000,
		"10M":  10000000,
		"1Mi":  1048576,
		"0":    0,
		"1.5k": 1500,
	} {
		settings := config.DefaultSettings()
		settings.MaxCombinedDirectoryManifestsSize.Value = value
		got, err := maxCombinedDirectoryManifestsSize(settings)
		if err != nil || got != want {
			t.Fatalf("maxCombinedDirectoryManifestsSize(%q) = %d, %v, want %d", value, got, err, want)
		}
	}
	settings := config.DefaultSettings()
	settings.MaxCombinedDirectoryManifestsSize.Value = "ten megabytes"
	if _, err := maxCombinedDirectoryManifestsSize(settings); err == nil || !strings.Contains(err.Error(), "is not a Kubernetes quantity") {
		t.Fatalf("maxCombinedDirectoryManifestsSize() error = %v, want a quantity error", err)
	}
}

// TestLocalProviderAppliesDirectorySizeLimit pins that the local provider
// passes its size limit to the directory renderer.
func TestLocalProviderAppliesDirectorySizeLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "apps", "cm.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := render.ResolvedSource{RepoRoot: root, Path: "apps"}
	if _, _, err := (localProvider{maxCombinedDirectoryManifestsSize: 1000}).RenderSource(context.Background(), source, render.RenderOptions{}); err != nil {
		t.Fatalf("RenderSource() error = %v", err)
	}
	_, _, err := (localProvider{maxCombinedDirectoryManifestsSize: 10}).RenderSource(context.Background(), source, render.RenderOptions{})
	if err == nil || !strings.Contains(err.Error(), "exceeded max combined manifest file size") {
		t.Fatalf("RenderSource() error = %v, want the size limit", err)
	}
}
