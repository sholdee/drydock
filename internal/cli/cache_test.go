package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/rendercache"
)

const cacheCLITestKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func cliRenderTestKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func seedCLIRenderEntry(t *testing.T, dir string, keys ...string) {
	t.Helper()
	store, err := rendercache.Open(dir, 0)
	if err != nil {
		t.Fatalf("rendercache.Open() error = %v", err)
	}
	for _, key := range keys {
		if err := store.Put(key, []byte(`{"manifests":[]}`), rendercache.EntryMeta{Version: "v", Commit: "c"}); err != nil {
			t.Fatalf("rendercache.Put(%s) error = %v", key, err)
		}
	}
}

func TestCachePathPrintsRoots(t *testing.T) {
	renderCacheDir := t.TempDir()
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "path",
		"--git-cache-dir", "/tmp/git",
		"--chart-cache-dir", "/tmp/charts",
		"--remote-cache-dir", "/tmp/remotes",
		"--render-cache-dir", renderCacheDir,
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"SOURCE", "PATH", "git", "/tmp/git", "chart", "/tmp/charts", "remote", "/tmp/remotes", "render", renderCacheDir} {
		if !strings.Contains(got, want) {
			t.Fatalf("cache path output missing %q:\n%s", want, got)
		}
	}
}

func TestCacheListJSON(t *testing.T) {
	root := t.TempDir()
	gitCacheDir := filepath.Join(root, "git")
	chartCacheDir := filepath.Join(root, "charts")
	remoteCacheDir := filepath.Join(root, "remotes")
	renderCacheDir := filepath.Join(root, "render")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "list", "-o", "json",
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
	})
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
	chartCacheDir := filepath.Join(root, "charts")
	remoteCacheDir := filepath.Join(root, "remotes")
	renderCacheDir := filepath.Join(root, "render")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "list",
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
	})
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
	chartCacheDir := filepath.Join(root, "charts")
	remoteCacheDir := filepath.Join(root, "remotes")
	renderCacheDir := filepath.Join(root, "render")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(entryRoot, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "prune",
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
		"--older-than", "24h",
		"--dry-run",
	})
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
	chartCacheDir := filepath.Join(root, "charts")
	remoteCacheDir := filepath.Join(root, "remotes")
	renderCacheDir := filepath.Join(root, "render")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "prune",
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
		"--older-than", "24h",
		"--dry-run",
	})
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
	chartCacheDir := filepath.Join(root, "charts")
	remoteCacheDir := filepath.Join(root, "remotes")
	renderCacheDir := filepath.Join(root, "render")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "prune",
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
		"--older-than", "24h",
	})
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
	renderCacheDir := filepath.Join(root, "render")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--source", "git", "--key", cacheCLITestKey, "--git-cache-dir", gitCacheDir, "--render-cache-dir", renderCacheDir})
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
	renderCacheDir := filepath.Join(root, "render")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--source", "git", "--key", cacheCLITestKey, "--git-cache-dir", gitCacheDir, "--render-cache-dir", renderCacheDir, "--dry-run"})
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
	chartCacheDir := filepath.Join(root, "charts")
	remoteCacheDir := filepath.Join(root, "remotes")
	renderCacheDir := filepath.Join(root, "render")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "delete",
		"--all",
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
	})
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
	renderCacheDir := filepath.Join(root, "render")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"cache", "delete", "--source", "git", "--key", cacheCLITestKey, "--git-cache-dir", gitCacheDir, "--render-cache-dir", renderCacheDir, "--yes"})
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
			renderCacheDir := filepath.Join(root, "render")
			entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
			writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

			cmd := NewRootCommand(VersionInfo{})
			args := make([]string, 0, 13+len(tc.extra))
			args = append(args,
				"cache", "delete",
				"--all",
				"--key", cacheCLITestKey,
				"--git-cache-dir", gitCacheDir,
				"--chart-cache-dir", chartCacheDir,
				"--remote-cache-dir", remoteCacheDir,
				"--render-cache-dir", renderCacheDir,
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
	chartCacheDir := filepath.Join(t.TempDir(), "charts")
	remoteCacheDir := filepath.Join(t.TempDir(), "remotes")
	renderCacheDir := filepath.Join(t.TempDir(), "render")
	writeCacheEntry(t, filepath.Join(gitCacheDir, cacheCLITestKey, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "list",
		"--path", filepath.Join(t.TempDir(), "current"),
		"--path-orig", repoRoot,
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
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
		"--render-cache-dir", filepath.Join(t.TempDir(), "render"),
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
	chartCacheDir := filepath.Join(root, "charts")
	remoteCacheDir := filepath.Join(root, "remotes")
	renderCacheDir := filepath.Join(root, "render")
	entryRoot := filepath.Join(gitCacheDir, cacheCLITestKey)
	writeCacheEntry(t, filepath.Join(entryRoot, ".git", "HEAD"))

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "delete",
		"--all",
		"--git-cache-dir", gitCacheDir,
		"--chart-cache-dir", chartCacheDir,
		"--remote-cache-dir", remoteCacheDir,
		"--render-cache-dir", renderCacheDir,
		"--yes",
		"-o", "name",
	})
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

// --- New tests for Task 3 behaviors ---

// TestParseCacheSourcesRender verifies that parseCacheSources accepts "render"
// and that the error message for unknown sources names "render".
func TestParseCacheSourcesRender(t *testing.T) {
	sources, err := parseCacheSources("render")
	if err != nil {
		t.Fatalf("parseCacheSources(render) error = %v", err)
	}
	if len(sources) != 1 || sources[0] != cache.SourceRender {
		t.Fatalf("parseCacheSources(render) = %v, want [render]", sources)
	}

	// Error message must include "render" so operators know it is a valid source.
	_, err = parseCacheSources("bogus")
	if err == nil || !strings.Contains(err.Error(), "render") {
		t.Fatalf("parseCacheSources(bogus) error = %v, want message naming render", err)
	}
}

// TestCacheRootsRenderDefault verifies that cacheRoots populates the render key
// from rendercache.DefaultDir() when --render-cache-dir is not set, and that
// --render-cache-dir overrides it.
func TestCacheRootsRenderDefault(t *testing.T) {
	wantDefault, err := rendercache.DefaultDir()
	if err != nil {
		t.Fatalf("rendercache.DefaultDir() error = %v", err)
	}

	flags := defaultCacheFlags()
	roots, err := cacheRoots(flags)
	if err != nil {
		t.Fatalf("cacheRoots() error = %v", err)
	}
	if roots[cache.SourceRender] != wantDefault {
		t.Fatalf("cacheRoots()[render] = %q, want %q", roots[cache.SourceRender], wantDefault)
	}

	override := t.TempDir()
	flags.renderCacheDir = override
	roots, err = cacheRoots(flags)
	if err != nil {
		t.Fatalf("cacheRoots(override) error = %v", err)
	}
	if roots[cache.SourceRender] != override {
		t.Fatalf("cacheRoots(override)[render] = %q, want %q", roots[cache.SourceRender], override)
	}
}

// TestCachePathIncludesRenderRow verifies that "cache path" emits a render row.
func TestCachePathIncludesRenderRow(t *testing.T) {
	renderCacheDir := t.TempDir()
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "path",
		"--git-cache-dir", t.TempDir(),
		"--chart-cache-dir", t.TempDir(),
		"--remote-cache-dir", t.TempDir(),
		"--render-cache-dir", renderCacheDir,
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "render") {
		t.Fatalf("cache path output missing render row:\n%s", got)
	}
	if !strings.Contains(got, renderCacheDir) {
		t.Fatalf("cache path output missing render dir %q:\n%s", renderCacheDir, got)
	}
}

// TestCacheListRenderSource verifies that "cache list --source render" lists a
// seeded render entry.
func TestCacheListRenderSource(t *testing.T) {
	renderCacheDir := t.TempDir()
	key := cliRenderTestKey("cli-list-a")
	seedCLIRenderEntry(t, renderCacheDir, key)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "list", "-o", "json",
		"--source", "render",
		"--render-cache-dir", renderCacheDir,
	})
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
	if len(entries) != 1 {
		t.Fatalf("entries = %#v, want exactly 1 render entry", entries)
	}
	if entries[0].Source != "render" || entries[0].Kind != "output" || entries[0].Key != key {
		t.Fatalf("entry = %+v, want render/output/%s", entries[0], key)
	}
}

// TestCachePruneRenderJSON verifies that "cache prune --source render --yes"
// removes a backdated render entry and that -o json output contains the
// renderSweep field.
func TestCachePruneRenderJSON(t *testing.T) {
	renderCacheDir := t.TempDir()
	old := cliRenderTestKey("cli-prune-old")
	fresh := cliRenderTestKey("cli-prune-fresh")
	seedCLIRenderEntry(t, renderCacheDir, old, fresh)

	// Backdate the old entry.
	entries, err := rendercache.Entries(renderCacheDir)
	if err != nil {
		t.Fatalf("rendercache.Entries() error = %v", err)
	}
	for _, entry := range entries {
		if entry.Key == old {
			stale := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(entry.Path, stale, stale); err != nil {
				t.Fatalf("Chtimes() error = %v", err)
			}
		}
	}

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"cache", "prune",
		"-o", "json",
		"--source", "render",
		"--older-than", "24h",
		"--yes",
		"--render-cache-dir", renderCacheDir,
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result struct {
		RemovedCount int `json:"removedCount"`
		RenderSweep  *struct {
			TotalBytes  int64    `json:"totalBytes"`
			EvictedKeys []string `json:"evictedKeys"`
		} `json:"renderSweep"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if result.RemovedCount != 1 {
		t.Fatalf("removedCount = %d, want 1", result.RemovedCount)
	}
	if result.RenderSweep == nil {
		t.Fatalf("renderSweep = nil, want non-nil (sweep must run when render is in scope)")
	}
}

func writeCacheEntry(t *testing.T, path string) {
	t.Helper()
	writeCLITestFile(t, path, "cache")
}
