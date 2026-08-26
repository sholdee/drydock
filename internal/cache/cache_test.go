package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/rendercache"
)

const (
	testGitKey    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testChartKey  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testRemoteKey = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestListDiscoversCacheLayouts(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, "git")
	chartDir := filepath.Join(root, "charts")
	remoteDir := filepath.Join(root, "remotes")

	writeCacheFile(t, filepath.Join(gitDir, testGitKey, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeCacheFile(t, filepath.Join(chartDir, "http", testChartKey, "renovate", "Chart.yaml"), "name: renovate\n")
	writeCacheFile(t, filepath.Join(chartDir, "oci", strings.ReplaceAll(testChartKey, "b", "d"), "operator", "Chart.yaml"), "name: operator\n")
	writeCacheFile(t, filepath.Join(remoteDir, testRemoteKey, "resource.yaml"), "kind: ConfigMap\n")
	writeCacheFile(t, filepath.Join(remoteDir, strings.ReplaceAll(testRemoteKey, "c", "e"), "repo", ".git", "HEAD"), "ref: refs/heads/main\n")

	entries, err := List(Options{
		GitCacheDir:    gitDir,
		ChartCacheDir:  chartDir,
		RemoteCacheDir: remoteDir,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, string(entry.Source)+"/"+entry.Kind+"/"+entry.Key)
		if entry.SizeBytes <= 0 {
			t.Fatalf("entry %s SizeBytes = %d, want > 0", entry.Path, entry.SizeBytes)
		}
		if !entry.Legacy {
			t.Fatalf("entry %s Legacy = false, want true for missing metadata", entry.Path)
		}
	}
	want := []string{
		"chart/http/" + testChartKey,
		"chart/oci/" + strings.ReplaceAll(testChartKey, "b", "d"),
		"git/git/" + testGitKey,
		"remote/git-repo/" + strings.ReplaceAll(testRemoteKey, "c", "e"),
		"remote/http-file/" + testRemoteKey,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %#v, want %#v", got, want)
	}
}

func TestListMissingRootsReturnsEmpty(t *testing.T) {
	entries, err := List(Options{
		GitCacheDir:    filepath.Join(t.TempDir(), "git"),
		ChartCacheDir:  filepath.Join(t.TempDir(), "charts"),
		RemoteCacheDir: filepath.Join(t.TempDir(), "remotes"),
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want empty", entries)
	}
}

func TestMetadataRoundTripAndValidation(t *testing.T) {
	entryRoot := filepath.Join(t.TempDir(), testGitKey)
	created := time.Now().UTC().Truncate(time.Second)
	meta := Metadata{
		SchemaVersion: 1,
		Source:        SourceGit,
		Kind:          "git",
		Key:           testGitKey,
		Target:        RedactedTarget("https://user:secret@example.test/org/repo.git?token=secret#frag"),
		Revision:      "main",
		CreatedAt:     created,
		UpdatedAt:     created,
	}
	if err := WriteMetadata(entryRoot, meta); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}
	got, err := ReadMetadata(entryRoot, SourceGit, "git", testGitKey)
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	if got.Target != "https://example.test/org/repo.git" {
		t.Fatalf("Target = %q, want redacted canonical URL", got.Target)
	}
	if _, err := ReadMetadata(entryRoot, SourceRemote, "git", testGitKey); err == nil {
		t.Fatal("ReadMetadata() error = nil, want source mismatch")
	}
}

func TestMetadataPathUsesHiddenDirectory(t *testing.T) {
	entryRoot := filepath.Join(t.TempDir(), testGitKey)
	if err := WriteMetadata(entryRoot, Metadata{
		Source: SourceGit,
		Kind:   "git",
		Key:    testGitKey,
		Target: "https://example.test/org/repo.git",
	}); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}

	metadataPath := MetadataPath(entryRoot)
	if !strings.Contains(filepath.ToSlash(metadataPath), "/.drydock-cache/metadata.json") {
		t.Fatalf("MetadataPath() = %q, want hidden cache metadata path", metadataPath)
	}
	assertExists(t, metadataPath)
	assertNotExists(t, filepath.Join(entryRoot, "metadata.json"))
}

func TestRedactedTargetStripsSecretsQueryAndFragment(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{
			raw:  "https://user:secret@example.test/org/repo.git?token=secret#frag",
			want: "https://example.test/org/repo.git",
		},
		{
			raw:  "git@example.test:org/repo.git?token=secret#frag",
			want: "example.test:org/repo.git",
		},
		{
			raw:  "git::https://user:secret@example.test/org/repo.git?token=secret#frag",
			want: "git::https://example.test/org/repo.git",
		},
		{
			raw:  "git::ssh://user:secret@example.test/org/repo.git?token=secret#frag",
			want: "git::ssh://example.test/org/repo.git",
		},
	} {
		if got := RedactedTarget(tc.raw); got != tc.want {
			t.Fatalf("RedactedTarget(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestPruneUsesMetadataUpdatedAtThenMTime(t *testing.T) {
	root := t.TempDir()
	oldKey := testGitKey
	recentKey := strings.ReplaceAll(testGitKey, "a", "f")
	oldRoot := filepath.Join(root, "git", oldKey)
	recentRoot := filepath.Join(root, "git", recentKey)
	writeCacheFile(t, filepath.Join(oldRoot, ".git", "HEAD"), "old")
	writeCacheFile(t, filepath.Join(recentRoot, ".git", "HEAD"), "recent")
	oldTime := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	recentTime := time.Now().UTC().Truncate(time.Second)
	if err := WriteMetadata(oldRoot, Metadata{SchemaVersion: 1, Source: SourceGit, Kind: "git", Key: oldKey, UpdatedAt: oldTime}); err != nil {
		t.Fatalf("WriteMetadata(old) error = %v", err)
	}
	if err := WriteMetadata(recentRoot, Metadata{SchemaVersion: 1, Source: SourceGit, Kind: "git", Key: recentKey, UpdatedAt: recentTime}); err != nil {
		t.Fatalf("WriteMetadata(recent) error = %v", err)
	}

	result, err := Prune(OperationOptions{
		GitCacheDir: filepath.Join(root, "git"),
		OlderThan:   24 * time.Hour,
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if result.RemovedCount != 1 || len(result.Entries) != 1 || result.Entries[0].Key != oldKey {
		t.Fatalf("Prune() result = %#v, want one old entry", result)
	}
	assertNotExists(t, oldRoot)
	assertExists(t, recentRoot)
	assertExists(t, filepath.Join(root, "git"))
}

func TestPruneDryRunDoesNotRemoveEntries(t *testing.T) {
	root := t.TempDir()
	entryRoot := filepath.Join(root, "git", testGitKey)
	writeCacheFile(t, filepath.Join(entryRoot, ".git", "HEAD"), "old")
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(entryRoot, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	result, err := Prune(OperationOptions{
		GitCacheDir: filepath.Join(root, "git"),
		OlderThan:   24 * time.Hour,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if !result.DryRun || result.RemovedCount != 0 || len(result.Entries) != 1 {
		t.Fatalf("Prune() result = %#v, want dry-run selected entry", result)
	}
	assertExists(t, entryRoot)
}

func TestDeleteRemovesOnlyMatchingSourceAndKey(t *testing.T) {
	root := t.TempDir()
	gitRoot := filepath.Join(root, "git", testGitKey)
	remoteRoot := filepath.Join(root, "remotes", testGitKey)
	writeCacheFile(t, filepath.Join(gitRoot, ".git", "HEAD"), "git")
	writeCacheFile(t, filepath.Join(remoteRoot, "resource.yaml"), "remote")

	result, err := Delete(OperationOptions{
		GitCacheDir:    filepath.Join(root, "git"),
		RemoteCacheDir: filepath.Join(root, "remotes"),
		Source:         SourceGit,
		Key:            testGitKey,
		Yes:            true,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if result.RemovedCount != 1 || len(result.Entries) != 1 || result.Entries[0].Source != SourceGit {
		t.Fatalf("Delete() result = %#v, want one git entry", result)
	}
	assertNotExists(t, gitRoot)
	assertExists(t, remoteRoot)
}

func TestDeleteAllRequiresConfirmation(t *testing.T) {
	root := t.TempDir()
	entryRoot := filepath.Join(root, "git", testGitKey)
	writeCacheFile(t, filepath.Join(entryRoot, ".git", "HEAD"), "git")

	_, err := Delete(OperationOptions{
		GitCacheDir: filepath.Join(root, "git"),
		All:         true,
	})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("Delete() error = %v, want --yes confirmation error", err)
	}
	assertExists(t, entryRoot)
}

func TestOperationsRejectCacheRootInsideForbiddenRoot(t *testing.T) {
	repoRoot := t.TempDir()
	cacheRoot := filepath.Join(repoRoot, ".drydock", "git")
	writeCacheFile(t, filepath.Join(cacheRoot, testGitKey, ".git", "HEAD"), "git")

	_, err := List(Options{GitCacheDir: cacheRoot, ForbiddenRoots: []string{repoRoot}})
	if err == nil || !strings.Contains(err.Error(), "must not be inside protected root") {
		t.Fatalf("List() error = %v, want protected root error", err)
	}
}

func TestOperationsRejectSymlinkedForbiddenCacheRoot(t *testing.T) {
	repoRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(outside, "cache-link")
	if err := os.Symlink(repoRoot, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	_, err := List(Options{GitCacheDir: filepath.Join(link, "git"), ForbiddenRoots: []string{repoRoot}})
	if err == nil || !strings.Contains(err.Error(), "must not be inside protected root") {
		t.Fatalf("List() error = %v, want protected root error", err)
	}
}

func TestDeleteRemovesRemoteEntryRootNotNestedResult(t *testing.T) {
	root := t.TempDir()
	entryRoot := filepath.Join(root, "remotes", testRemoteKey)
	writeCacheFile(t, filepath.Join(entryRoot, "resource.yaml"), "remote")
	if err := WriteMetadata(entryRoot, Metadata{SchemaVersion: 1, Source: SourceRemote, Kind: "http-file", Key: testRemoteKey, UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}

	result, err := Delete(OperationOptions{
		RemoteCacheDir: filepath.Join(root, "remotes"),
		Source:         SourceRemote,
		Key:            testRemoteKey,
		Yes:            true,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if result.RemovedCount != 1 {
		t.Fatalf("RemovedCount = %d, want 1", result.RemovedCount)
	}
	assertNotExists(t, entryRoot)
	assertExists(t, filepath.Join(root, "remotes"))
}

func TestListSkipsMalformedEntryNames(t *testing.T) {
	root := t.TempDir()
	writeCacheFile(t, filepath.Join(root, "git", "not-a-cache-key", ".git", "HEAD"), "git")

	entries, err := List(Options{GitCacheDir: filepath.Join(root, "git")})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want malformed entry skipped", entries)
	}
}

func TestListDoesNotTrustRemoteMetadataWithoutRecognizedLayout(t *testing.T) {
	root := t.TempDir()
	entryRoot := filepath.Join(root, "remotes", testRemoteKey)
	if err := WriteMetadata(entryRoot, Metadata{
		SchemaVersion: 1,
		Source:        SourceRemote,
		Kind:          "http-file",
		Key:           testRemoteKey,
		UpdatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}

	entries, err := List(Options{RemoteCacheDir: filepath.Join(root, "remotes")})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want metadata-only remote entry skipped", entries)
	}
}

func TestListDoesNotTrustUnsupportedRemoteMetadataWithoutRecognizedLayout(t *testing.T) {
	root := t.TempDir()
	entryRoot := filepath.Join(root, "remotes", testRemoteKey)
	if err := os.MkdirAll(entryRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeCacheFile(t, MetadataPath(entryRoot), `{
  "schemaVersion": 99,
  "source": "remote",
  "kind": "http-file",
  "key": "`+testRemoteKey+`"
}
`)

	entries, err := List(Options{RemoteCacheDir: filepath.Join(root, "remotes")})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want unsupported metadata-only remote entry skipped", entries)
	}
}

func TestListRejectsCacheRootInsideGitRepository(t *testing.T) {
	repoRoot := t.TempDir()
	writeCacheFile(t, filepath.Join(repoRoot, ".git", "HEAD"), "ref: refs/heads/main\n")
	cacheRoot := filepath.Join(repoRoot, ".cache", "git")
	writeCacheFile(t, filepath.Join(cacheRoot, testGitKey, ".git", "HEAD"), "git")

	_, err := List(Options{GitCacheDir: cacheRoot})
	if err == nil || !strings.Contains(err.Error(), "must not be inside Git repository") {
		t.Fatalf("List() error = %v, want Git repository containment error", err)
	}
}

func writeCacheFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(%s) error = %v, want exists", path, err)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%s) error = %v, want not exist", path, err)
	}
}

func seedRenderEntries(t *testing.T, dir string, keys ...string) {
	t.Helper()
	store, err := rendercache.Open(dir, 0)
	if err != nil {
		t.Fatalf("rendercache.Open() error = %v", err)
	}
	for _, key := range keys {
		if err := store.Put(key, []byte(`{"manifests":[]}`), rendercache.EntryMeta{Version: "v", Commit: "c"}); err != nil {
			t.Fatalf("Put(%s) error = %v", key, err)
		}
	}
}

func renderTestKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func TestListIncludesRenderEntries(t *testing.T) {
	dir := t.TempDir() + "/render"
	keyA := renderTestKey("list-a")
	seedRenderEntries(t, dir, keyA)

	entries, err := List(Options{RenderCacheDir: dir, Sources: []Source{SourceRender}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List() = %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Source != SourceRender || entry.Kind != "output" || entry.Key != keyA {
		t.Fatalf("entry = %+v, want render/output/%s", entry, keyA)
	}
	if entry.Legacy || entry.Metadata != nil || entry.MetadataPath != "" {
		t.Fatalf("render entries are self-describing, not legacy: %+v", entry)
	}
	if entry.SizeBytes <= 0 {
		t.Fatalf("entry size = %d, want > 0", entry.SizeBytes)
	}
}

func TestListDefaultSourcesIncludeRender(t *testing.T) {
	dir := t.TempDir() + "/render"
	seedRenderEntries(t, dir, renderTestKey("default-a"))
	entries, err := List(Options{RenderCacheDir: dir}) // no Sources filter
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("default sources must include render, got %#v", entries)
	}
}

func TestDeleteRenderEntryByKey(t *testing.T) {
	dir := t.TempDir() + "/render"
	keep := renderTestKey("delete-keep")
	remove := renderTestKey("delete-remove")
	seedRenderEntries(t, dir, keep, remove)

	result, err := Delete(OperationOptions{
		RenderCacheDir: dir, Sources: []Source{SourceRender},
		Source: SourceRender,
		Key:    remove,
		Yes:    true,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if result.RemovedCount != 1 {
		t.Fatalf("RemovedCount = %d, want 1", result.RemovedCount)
	}
	remaining, err := List(Options{RenderCacheDir: dir, Sources: []Source{SourceRender}})
	if err != nil || len(remaining) != 1 || remaining[0].Key != keep {
		t.Fatalf("remaining = %#v, %v, want only %s", remaining, err, keep)
	}
}

func TestPruneRenderEntriesByAgeAndSweep(t *testing.T) {
	dir := t.TempDir() + "/render"
	old := renderTestKey("prune-old")
	fresh := renderTestKey("prune-fresh")
	seedRenderEntries(t, dir, old, fresh)
	entries, err := rendercache.Entries(dir)
	if err != nil {
		t.Fatalf("Entries() error = %v", err)
	}
	for _, entry := range entries {
		if entry.Key == old {
			stale := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(entry.Path, stale, stale); err != nil {
				t.Fatalf("Chtimes() error = %v", err)
			}
		}
	}

	result, err := Prune(OperationOptions{
		RenderCacheDir: dir, Sources: []Source{SourceRender},
		OlderThan: 24 * time.Hour,
		Yes:       true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if result.RemovedCount != 1 || len(result.Entries) != 1 || result.Entries[0].Key != old {
		t.Fatalf("prune result = %+v, want exactly the old entry", result)
	}
	if result.RenderSweep == nil {
		t.Fatalf("prune with render enabled must run the size-cap sweep, got nil RenderSweep")
	}

	// Dry-run: no removal, no sweep.
	dry, err := Prune(OperationOptions{
		RenderCacheDir: dir, Sources: []Source{SourceRender},
		OlderThan: time.Hour,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Prune(dry) error = %v", err)
	}
	if dry.RemovedCount != 0 || dry.RenderSweep != nil {
		t.Fatalf("dry-run must not remove or sweep: %+v", dry)
	}
}
