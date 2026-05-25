package cli

import (
	"path/filepath"
	"testing"
)

func TestPortableSmokeFixture(t *testing.T) {
	fixtureRoot := portableSmokeFixtureRoot()
	baseline := filepath.Join(fixtureRoot, "baseline")
	current := filepath.Join(fixtureRoot, "current")
	cacheRoot := t.TempDir()
	chartCacheDir := filepath.Join(cacheRoot, "charts")
	remoteCacheDir := filepath.Join(cacheRoot, "remotes")

	testResult := runCLI(t,
		"test", "apps",
		"--path", current,
		"--offline",
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
	)
	assertStdoutContainsAll(t, testResult,
		"PASS argocd/direct-kustomize",
		"PASS argocd/git-file-generated",
		"PASS argocd/list-generated",
		"PASS argocd/local-helm",
		"PASS argocd/multi-source",
	)
	assertStderrEmpty(t, testResult)

	diffResult := runCLI(t,
		"diff", "apps",
		"--path-orig", baseline,
		"--path", current,
		"--changed-only=false",
		"--exit-code=false",
		"--offline",
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
	)
	assertStdoutContainsAll(t, diffResult,
		"Application: argocd/direct-kustomize",
		"baseline-direct",
		"current-direct",
		"Application: argocd/list-generated",
		"baseline-list",
		"current-list",
		"Application: argocd/git-file-generated",
		"baseline-git-file",
		"current-git-file",
		"Application: argocd/local-helm",
		"baseline-helm",
		"current-helm",
		"Application: argocd/multi-source",
		"baseline-multi-source",
		"current-multi-source",
	)
	assertStderrEmpty(t, diffResult)
}
