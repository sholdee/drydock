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
	// resource.ParseQuantity rejects spaces around the value, so the
	// repo-server stops at startup on them.
	for _, value := range []string{"ten megabytes", " 10M", "10M ", "  "} {
		settings := config.DefaultSettings()
		settings.MaxCombinedDirectoryManifestsSize.Value = value
		if _, err := maxCombinedDirectoryManifestsSize(settings); err == nil || !strings.Contains(err.Error(), "is not a Kubernetes quantity") {
			t.Fatalf("maxCombinedDirectoryManifestsSize(%q) error = %v, want a quantity error", value, err)
		}
	}
}

// TestCommandParametersSizeLimitWithSpacesFailsTheBuild reads the setting
// from an argocd-cmd-params-cm file, as discovery does. A value with spaces
// around it must fail, as it stops the Argo CD repo-server at startup.
func TestCommandParametersSizeLimitWithSpacesFailsTheBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cmd-params-cm\ndata:\n  reposerver.max.combined.directory.manifests.size: \" 20M \"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, _, err := config.LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if _, err := maxCombinedDirectoryManifestsSize(settings); err == nil {
		t.Fatal("maxCombinedDirectoryManifestsSize() error = nil, want a quantity error")
	}
}

// TestRenderSettingsSignatureSignsTheSizeLimitInBytes pins that the render
// cache key and the changed-only settings signature see the size limit as a
// byte count. The same limit written another way gives the same key, and a
// different limit gives a different key.
func TestRenderSettingsSignatureSignsTheSizeLimitInBytes(t *testing.T) {
	signatures := func(value string) (string, string) {
		t.Helper()
		settings := config.DefaultSettings()
		settings.MaxCombinedDirectoryManifestsSize = config.Value[string]{Value: value}
		if value != "" {
			settings.MaxCombinedDirectoryManifestsSize.Provenance = config.Provenance{Path: "argocd-cmd-params-cm.yaml", Pointer: "data." + value}
		}
		render, err := renderSettingsSignature(settings)
		if err != nil {
			t.Fatalf("renderSettingsSignature() error = %v", err)
		}
		changed, err := changedOnlySettingsSignature(settings)
		if err != nil {
			t.Fatalf("changedOnlySettingsSignature() error = %v", err)
		}
		return render, changed
	}
	defaultRender, defaultChanged := signatures("")
	for _, value := range []string{"10M", "10000000", "10000k"} {
		render, changed := signatures(value)
		if render != defaultRender || changed != defaultChanged {
			t.Fatalf("signatures for %q differ from the default 10M", value)
		}
	}
	for _, value := range []string{"20M", "0", "not a quantity"} {
		render, changed := signatures(value)
		if render == defaultRender || changed == defaultChanged {
			t.Fatalf("signatures for %q equal the default 10M", value)
		}
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
