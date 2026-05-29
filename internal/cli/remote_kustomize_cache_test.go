package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiffAppsRemoteKustomizeCacheSemanticFixture(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "semantic-remediation", "remote-kustomize-cache", "seeded-diff")
	cacheDir := t.TempDir()
	seedRemoteResourceCacheFromFile(t, cacheDir,
		"https://example.invalid/kustomize/baseline/base.yaml",
		filepath.Join(fixtureRoot, "baseline", "cache", "base.yaml"),
	)
	seedRemoteResourceCacheFromFile(t, cacheDir,
		"https://example.invalid/kustomize/current/base.yaml",
		filepath.Join(fixtureRoot, "current", "cache", "base.yaml"),
	)

	result := runCLI(t,
		"diff", "apps",
		"--path-orig", filepath.Join(fixtureRoot, "baseline"),
		"--path", filepath.Join(fixtureRoot, "current"),
		"--offline",
		"--remote-cache-dir", cacheDir,
		"--changed-only=false",
		"--exit-code=false",
	)

	assertStdoutContainsAll(t, result,
		"Application: argocd/remote-kustomize-cache",
		"ConfigMap: remote-kustomize/remote-base",
		"-  value: baseline",
		"+  value: current",
	)
	assertStderrEmpty(t, result)
}

func seedRemoteResourceCacheFromFile(t *testing.T, cacheDir, rawURL, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read remote cache fixture %s: %v", path, err)
	}
	seedRemoteResourceCache(t, cacheDir, rawURL, string(body))
}
