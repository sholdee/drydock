package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableSmokeFixture(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "fixtures", "portable-smoke")
	baseline := filepath.Join(fixtureRoot, "baseline")
	current := filepath.Join(fixtureRoot, "current")
	cacheRoot := t.TempDir()
	chartCacheDir := filepath.Join(cacheRoot, "charts")
	remoteCacheDir := filepath.Join(cacheRoot, "remotes")

	testCmd := NewRootCommand(VersionInfo{})
	testCmd.SetArgs([]string{
		"test", "apps",
		"--path", current,
		"--offline",
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
	})
	var testStdout bytes.Buffer
	var testStderr bytes.Buffer
	testCmd.SetOut(&testStdout)
	testCmd.SetErr(&testStderr)

	if err := testCmd.Execute(); err != nil {
		t.Fatalf("test apps Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, testStdout.String(), testStderr.String())
	}
	for _, want := range []string{
		"PASS argocd/direct-kustomize",
		"PASS argocd/git-file-generated",
		"PASS argocd/list-generated",
		"PASS argocd/local-helm",
		"PASS argocd/multi-source",
	} {
		if !strings.Contains(testStdout.String(), want) {
			t.Fatalf("test apps stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, testStdout.String(), testStderr.String())
		}
	}
	if testStderr.String() != "" {
		t.Fatalf("test apps stderr = %q, want empty", testStderr.String())
	}

	diffCmd := NewRootCommand(VersionInfo{})
	diffCmd.SetArgs([]string{
		"diff", "apps",
		"--path-orig", baseline,
		"--path", current,
		"--changed-only=false",
		"--exit-code=false",
		"--offline",
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
	})
	var diffStdout bytes.Buffer
	var diffStderr bytes.Buffer
	diffCmd.SetOut(&diffStdout)
	diffCmd.SetErr(&diffStderr)

	if err := diffCmd.Execute(); err != nil {
		t.Fatalf("diff apps Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, diffStdout.String(), diffStderr.String())
	}
	for _, want := range []string{
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
	} {
		if !strings.Contains(diffStdout.String(), want) {
			t.Fatalf("diff apps stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, diffStdout.String(), diffStderr.String())
		}
	}
	if diffStderr.String() != "" {
		t.Fatalf("diff apps stderr = %q, want empty", diffStderr.String())
	}
}
