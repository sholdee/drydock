package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cacheCLITestKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCachePathPrintsRoots(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "path",
		"--git-cache-dir", "/tmp/git",
		"--chart-cache-dir", "/tmp/charts",
		"--remote-cache-dir", "/tmp/remotes",
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"SOURCE", "PATH", "git", "/tmp/git", "chart", "/tmp/charts", "remote", "/tmp/remotes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cache path output missing %q:\n%s", want, got)
		}
	}
}

func TestCacheListJSON(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "list", "-o", "json", "--git-cache-dir", gitCacheDir})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var entries []struct {
		Source string `json:"source"`
		Kind   string `json:"kind"`
		Key    string `json:"key"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(entries) != 1 || entries[0].Source != "git" || entries[0].Kind != "git" || entries[0].Key != cacheCLITestKey {
		t.Fatalf("entries = %#v, want git cache entry", entries)
	}
}

func TestCacheListTableReportsLegacyStatus(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "list", "--git-cache-dir", gitCacheDir})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"LEGACY", "true"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cache list table output missing %q:\n%s", want, got)
		}
	}
}

func TestCachePruneDryRun(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(entryRoot, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "prune", "--git-cache-dir", gitCacheDir, "--older-than", "24h", "--dry-run"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "would remove 1 cache entries") {
		t.Fatalf("stdout = %q, want dry-run summary", stdout.String())
	}
	if _, err := os.Stat(entryRoot); err != nil {
		t.Fatalf("cache entry removed during dry-run: %v", err)
	}
}

func TestCachePruneDryRunDoesNotRequireYes(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "prune", "--git-cache-dir", gitCacheDir, "--older-than", "24h", "--dry-run"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestCachePruneRequiresYesBeforeMutation(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "prune", "--git-cache-dir", gitCacheDir, "--older-than", "24h"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("Execute() error = %v, want --yes error", err)
	}
	if _, statErr := os.Stat(entryRoot); statErr != nil {
		t.Fatalf("cache entry removed without --yes: %v", statErr)
	}
}

func TestCacheDeleteRequiresYes(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--source", "git", "--key", cacheCLITestKey, "--git-cache-dir", gitCacheDir})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("Execute() error = %v, want --yes error", err)
	}
	if _, statErr := os.Stat(entryRoot); statErr != nil {
		t.Fatalf("cache entry removed without --yes: %v", statErr)
	}
}

func TestCacheDeleteDryRunDoesNotRequireYes(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--source", "git", "--key", cacheCLITestKey, "--git-cache-dir", gitCacheDir, "--dry-run"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(entryRoot); err != nil {
		t.Fatalf("cache entry removed during dry-run: %v", err)
	}
}

func TestCacheDeleteAllRequiresYes(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--all", "--git-cache-dir", gitCacheDir})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("Execute() error = %v, want --yes error", err)
	}
	if _, statErr := os.Stat(entryRoot); statErr != nil {
		t.Fatalf("cache entry removed without --yes: %v", statErr)
	}
}

func TestCacheDeleteWithYesRemovesEntry(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--source", "git", "--key", cacheCLITestKey, "--git-cache-dir", gitCacheDir, "--yes"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(entryRoot); !os.IsNotExist(err) {
		t.Fatalf("cache entry exists after delete: %v", err)
	}
}

func TestCacheDeleteRejectsAllWithKeyBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{name: "yes", extra: []string{"--yes"}},
		{name: "dry-run", extra: []string{"--dry-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			gitCacheDir := filepath.Join(root, "git")
			chartCacheDir := filepath.Join(root, "charts")
			remoteCacheDir := filepath.Join(root, "remotes")
			entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
			writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

			cmd := NewRootCommand(VersionInfo{})
			args := make([]string, 0, 11+len(tc.extra))
			args = append(args,
				"cache", "delete",
				"--all",
				"--key", cacheCLITestKey,
				"--git-cache-dir", gitCacheDir,
				"--chart-cache-dir", chartCacheDir,
				"--remote-cache-dir", remoteCacheDir,
			)
			args = append(args, tc.extra...)
			cmd.SetArgs(args)
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stdout)

			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "--all cannot be combined with --key") {
				t.Fatalf("Execute() error = %v, want mutually exclusive flags error", err)
			}
			if _, statErr := os.Stat(entryRoot); statErr != nil {
				t.Fatalf("cache entry removed after invalid delete flags: %v", statErr)
			}
		})
	}
}

func TestCacheRejectsRootInsidePathOrig(t *testing.T) {
	repoRoot := t.TempDir()
	gitCacheDir := filepath.Join(repoRoot, ".cache", "git")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "list",
		"--path", filepath.Join(t.TempDir(), "current"),
		"--path-orig", repoRoot,
		"--git-cache-dir", gitCacheDir,
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "must not be inside protected root") {
		t.Fatalf("Execute() error = %v, want path-orig protected root error", err)
	}
}

func TestCachePathRejectsRootInsidePathOrig(t *testing.T) {
	repoRoot := t.TempDir()
	cacheRoot := filepath.Join(repoRoot, ".cache", "git")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "path",
		"--path", filepath.Join(t.TempDir(), "current"),
		"--path-orig", repoRoot,
		"--git-cache-dir", cacheRoot,
		"--chart-cache-dir", filepath.Join(t.TempDir(), "charts"),
		"--remote-cache-dir", filepath.Join(t.TempDir(), "remotes"),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "must not be inside protected root") {
		t.Fatalf("Execute() error = %v, want path-orig protected root error", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no paths for unsafe cache root", stdout.String())
	}
}

func TestCacheRejectsInvalidOutputBeforeMutation(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--all", "--git-cache-dir", gitCacheDir, "--yes", "-o", "name"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cache output supports") {
		t.Fatalf("Execute() error = %v, want output validation error", err)
	}
	if _, statErr := os.Stat(entryRoot); statErr != nil {
		t.Fatalf("cache entry removed after invalid output: %v", statErr)
	}
}

func writeCacheEntry(t *testing.T, path string) {
	t.Helper()
	writeCLITestFile(t, path, "cache")
}
