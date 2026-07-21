package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
)

func runCacheCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs(args)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	err := cmd.Execute()
	return stdout.String(), err
}

func seedOCICacheEntry(t *testing.T, ociCacheDir, repoURL, digest string, updatedAt time.Time) string {
	t.Helper()
	entry := ociartifact.EntryPath(ociCacheDir, repoURL, digest)
	writeCacheEntry(t, filepath.Join(entry, "image.tar"))
	if err := cache.WriteMetadata(entry, cache.Metadata{
		Source:    cache.SourceOCI,
		Kind:      ociartifact.EntryKind,
		Key:       filepath.Base(entry),
		Target:    ociartifact.RedactURL(repoURL),
		Revision:  digest,
		CreatedAt: updatedAt,
		UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}
	return entry
}

func TestParseCacheSourcesOCI(t *testing.T) {
	sources, err := parseCacheSources("oci")
	if err != nil {
		t.Fatalf("parseCacheSources(oci) error = %v", err)
	}
	if len(sources) != 1 || sources[0] != cache.SourceOCI {
		t.Fatalf("parseCacheSources(oci) = %v, want [oci]", sources)
	}
	_, err = parseCacheSources("bogus")
	if err == nil || !strings.Contains(err.Error(), "oci") {
		t.Fatalf("parseCacheSources(bogus) error = %v, want message naming oci", err)
	}
}

func TestCacheRootsOCIDefault(t *testing.T) {
	wantDefault, err := ociartifact.DefaultCacheDir()
	if err != nil {
		t.Fatalf("ociartifact.DefaultCacheDir() error = %v", err)
	}
	flags := defaultCacheFlags()
	roots, err := cacheRoots(flags)
	if err != nil {
		t.Fatalf("cacheRoots() error = %v", err)
	}
	if roots[cache.SourceOCI] != wantDefault {
		t.Fatalf("cacheRoots()[oci] = %q, want %q", roots[cache.SourceOCI], wantDefault)
	}

	override := t.TempDir()
	flags.ociCacheDir = override
	roots, err = cacheRoots(flags)
	if err != nil {
		t.Fatalf("cacheRoots(override) error = %v", err)
	}
	if roots[cache.SourceOCI] != override {
		t.Fatalf("cacheRoots(override)[oci] = %q, want %q", roots[cache.SourceOCI], override)
	}
}

func TestCachePathIncludesOCIRow(t *testing.T) {
	ociCacheDir := t.TempDir()
	got, err := runCacheCLI(t,
		"cache", "path",
		"--git-cache-dir", t.TempDir(),
		"--chart-cache-dir", t.TempDir(),
		"--remote-cache-dir", t.TempDir(),
		"--render-cache-dir", t.TempDir(),
		"--oci-cache-dir", ociCacheDir,
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(got, "oci") || !strings.Contains(got, ociCacheDir) {
		t.Fatalf("cache path output missing oci row %q:\n%s", ociCacheDir, got)
	}
}

// TestCacheListOCIEntriesFromRealAcquirer lists entries written by the
// production acquirer against the hermetic registry.
func TestCacheListOCIEntriesFromRealAcquirer(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	digest := ocitest.PushPlainManifestsArtifact(t, reg, "manifests/app", "v1", map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: listed\ndata: {}\n",
	})
	repoURL := reg.RepoURL("manifests/app")
	ociCacheDir := t.TempDir()
	_, release, err := (ociartifact.DefaultAcquirer{}).Extract(t.Context(), repoURL, digest, ociartifact.Options{CacheDir: ociCacheDir})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	release()

	got, err := runCacheCLI(t,
		"cache", "list",
		"--source", "oci",
		"--git-cache-dir", t.TempDir(),
		"--chart-cache-dir", t.TempDir(),
		"--remote-cache-dir", t.TempDir(),
		"--render-cache-dir", t.TempDir(),
		"--oci-cache-dir", ociCacheDir,
		"-o", "json",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var entries []cache.Entry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("unmarshal cache list output: %v\n%s", err, got)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: %s", len(entries), got)
	}
	entry := entries[0]
	if entry.Source != cache.SourceOCI || entry.Kind != ociartifact.EntryKind {
		t.Fatalf("entry = %+v, want source oci kind image", entry)
	}
	if entry.Legacy {
		t.Fatalf("entry = %+v, want metadata-backed (not legacy)", entry)
	}
	if entry.Metadata == nil || entry.Metadata.Revision != digest {
		t.Fatalf("entry metadata = %+v, want revision %q", entry.Metadata, digest)
	}
}

// TestCachePruneDefaultSourcesIncludesOCI pins the enabledSources default:
// a prune without --source must select stale OCI entries.
func TestCachePruneDefaultSourcesIncludesOCI(t *testing.T) {
	ociCacheDir := t.TempDir()
	digest := "sha256:" + strings.Repeat("ab", 32)
	entry := seedOCICacheEntry(t, ociCacheDir, "oci://registry.example/org/app", digest, time.Now().Add(-48*time.Hour))

	_, err := runCacheCLI(t,
		"cache", "prune",
		"--git-cache-dir", t.TempDir(),
		"--chart-cache-dir", t.TempDir(),
		"--remote-cache-dir", t.TempDir(),
		"--render-cache-dir", t.TempDir(),
		"--oci-cache-dir", ociCacheDir,
		"--older-than", "24h",
		"--yes",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatalf("stale oci entry survived default-source prune: %v", err)
	}
}

// TestCachePruneExplicitOCISource prunes through --source oci and pins that
// the offline tag records directory is not treated as a prunable entry.
func TestCachePruneExplicitOCISource(t *testing.T) {
	ociCacheDir := t.TempDir()
	oldDigest := "sha256:" + strings.Repeat("ab", 32)
	freshDigest := "sha256:" + strings.Repeat("cd", 32)
	oldEntry := seedOCICacheEntry(t, ociCacheDir, "oci://registry.example/org/app", oldDigest, time.Now().Add(-48*time.Hour))
	freshEntry := seedOCICacheEntry(t, ociCacheDir, "oci://registry.example/org/app", freshDigest, time.Now())
	recordsDir := filepath.Join(ociCacheDir, "tags")
	writeCLITestFile(t, filepath.Join(recordsDir, "record.json"), `{"digests":{"v1":"`+oldDigest+`"}}`)

	got, err := runCacheCLI(t,
		"cache", "prune",
		"--source", "oci",
		"--git-cache-dir", t.TempDir(),
		"--chart-cache-dir", t.TempDir(),
		"--remote-cache-dir", t.TempDir(),
		"--render-cache-dir", t.TempDir(),
		"--oci-cache-dir", ociCacheDir,
		"--older-than", "24h",
		"--yes",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(got, "removed 1 cache entries") {
		t.Fatalf("stdout = %q, want one removal", got)
	}
	if _, err := os.Stat(oldEntry); !os.IsNotExist(err) {
		t.Fatalf("stale oci entry survived --source oci prune: %v", err)
	}
	if _, err := os.Stat(freshEntry); err != nil {
		t.Fatalf("fresh oci entry removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recordsDir, "record.json")); err != nil {
		t.Fatalf("offline tag records removed by prune: %v", err)
	}
}

// TestCachePruneMaxSizeEvictsLRUOCIEntries drives the size phase over OCI
// entries: the least-recently-used entry goes first.
func TestCachePruneMaxSizeEvictsLRUOCIEntries(t *testing.T) {
	ociCacheDir := t.TempDir()
	oldDigest := "sha256:" + strings.Repeat("ab", 32)
	freshDigest := "sha256:" + strings.Repeat("cd", 32)
	oldEntry := seedOCICacheEntry(t, ociCacheDir, "oci://registry.example/org/app", oldDigest, time.Now().Add(-48*time.Hour))
	freshEntry := seedOCICacheEntry(t, ociCacheDir, "oci://registry.example/org/app", freshDigest, time.Now())
	// Make the LRU entry dominate the total so a 2000-byte cap evicts exactly
	// it (entry sizes include metadata.json).
	if err := os.WriteFile(filepath.Join(oldEntry, "image.tar"), bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runCacheCLI(t,
		"cache", "prune",
		"--source", "oci",
		"--git-cache-dir", t.TempDir(),
		"--chart-cache-dir", t.TempDir(),
		"--remote-cache-dir", t.TempDir(),
		"--render-cache-dir", t.TempDir(),
		"--oci-cache-dir", ociCacheDir,
		"--max-size", "2000",
		"--yes",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(oldEntry); !os.IsNotExist(err) {
		t.Fatalf("LRU oci entry survived --max-size prune: %v", err)
	}
	if _, err := os.Stat(freshEntry); err != nil {
		t.Fatalf("most-recent oci entry evicted before LRU: %v", err)
	}
}

func TestCacheDeleteOCIEntryByKey(t *testing.T) {
	ociCacheDir := t.TempDir()
	digest := "sha256:" + strings.Repeat("ab", 32)
	entry := seedOCICacheEntry(t, ociCacheDir, "oci://registry.example/org/app", digest, time.Now())

	_, err := runCacheCLI(t,
		"cache", "delete",
		"--source", "oci",
		"--key", filepath.Base(entry),
		"--git-cache-dir", t.TempDir(),
		"--chart-cache-dir", t.TempDir(),
		"--remote-cache-dir", t.TempDir(),
		"--render-cache-dir", t.TempDir(),
		"--oci-cache-dir", ociCacheDir,
		"--yes",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatalf("oci entry survived delete: %v", err)
	}
}
