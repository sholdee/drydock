package cache

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// makeGitEntry creates a git cache entry at root/git/<key> with a .git/HEAD
// file and optional metadata with a controlled UpdatedAt timestamp.
// If updatedAt is zero, no metadata file is written (legacy entry).
func makeGitEntry(t *testing.T, root, key string, updatedAt time.Time) {
	t.Helper()
	entryRoot := filepath.Join(root, "git", key)
	writeCacheFile(t, filepath.Join(entryRoot, ".git", "HEAD"), "ref: refs/heads/main\n")
	if !updatedAt.IsZero() {
		if err := WriteMetadata(entryRoot, Metadata{
			SchemaVersion: 1,
			Source:        SourceGit,
			Kind:          "git",
			Key:           key,
			UpdatedAt:     updatedAt,
			CreatedAt:     updatedAt,
		}); err != nil {
			t.Fatalf("WriteMetadata(%s) error = %v", key, err)
		}
	}
}

// makeRemoteHTTPEntry creates a remote/http-file cache entry at root/remotes/<key>
// with optional metadata.
func makeRemoteHTTPEntry(t *testing.T, root, key string, updatedAt time.Time) {
	t.Helper()
	entryRoot := filepath.Join(root, "remotes", key)
	writeCacheFile(t, filepath.Join(entryRoot, "resource.yaml"), "kind: ConfigMap\n")
	if !updatedAt.IsZero() {
		if err := WriteMetadata(entryRoot, Metadata{
			SchemaVersion: 1,
			Source:        SourceRemote,
			Kind:          "http-file",
			Key:           key,
			UpdatedAt:     updatedAt,
			CreatedAt:     updatedAt,
		}); err != nil {
			t.Fatalf("WriteMetadata(%s) error = %v", key, err)
		}
	}
}

var (
	keyA = strings.Repeat("a", 64)
	keyB = strings.Repeat("b", 64)
	keyC = strings.Repeat("c", 64)
)

// TestPruneSizePhaseEvictsOldestFirst verifies that the size phase evicts
// least-recently-used (oldest entryAgeTime) entries first and stops at exactly
// ≤ cap. Sabotage-validated during implementation: inverting the LRU sort
// makes this test fail (wrong entries evicted).
func TestPruneSizePhaseEvictsOldestFirst(t *testing.T) {
	root := t.TempDir()

	// Three entries with controlled UpdatedAt; all have the same .git/HEAD content
	// so their SizeBytes differ only by the content written. We use metadata to
	// control their timestamps precisely, avoiding sleeps.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	oldest := base                    // evicted first
	middle := base.Add(time.Hour)     // evicted second
	newest := base.Add(2 * time.Hour) // survives

	makeGitEntry(t, root, keyA, oldest)
	makeGitEntry(t, root, keyB, middle)
	makeGitEntry(t, root, keyC, newest)

	// List to learn actual SizeBytes values.
	entries, err := List(Options{GitCacheDir: filepath.Join(root, "git")})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Compute the per-entry sizes by key.
	sizeByKey := map[string]int64{}
	for _, e := range entries {
		sizeByKey[e.Key] = e.SizeBytes
	}

	// Cap = sizeByKey[keyC] (one entry's worth). This forces eviction of at least
	// two entries (oldest + middle) since all entries have similar sizes due to
	// metadata files. We want the cap to be: total - (size_oldest + size_middle),
	// i.e. just the newest entry's size. Use that as the cap.
	capBytes := sizeByKey[keyC]

	result, err := Prune(OperationOptions{
		Options:  Options{GitCacheDir: filepath.Join(root, "git")},
		MaxBytes: capBytes,
		Yes:      true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	// Expect oldest (keyA) and middle (keyB) evicted, newest (keyC) survives.
	if len(result.Entries) != 2 {
		t.Fatalf("Prune() removed %d entries, want 2; entries=%v", len(result.Entries), result.Entries)
	}
	removedKeys := map[string]bool{}
	for _, e := range result.Entries {
		removedKeys[e.Key] = true
		if e.PruneReason != "size" {
			t.Errorf("entry %s PruneReason = %q, want \"size\"", e.Key, e.PruneReason)
		}
	}
	if !removedKeys[keyA] || !removedKeys[keyB] {
		t.Errorf("expected keyA and keyB removed; got %v", removedKeys)
	}
	if removedKeys[keyC] {
		t.Errorf("keyC (newest) should survive, but was removed")
	}

	// RemovedCount matches selected length.
	if result.RemovedCount != 2 {
		t.Errorf("RemovedCount = %d, want 2", result.RemovedCount)
	}

	// SizeEvictedBytes = sum of removed entries' sizes.
	wantEvicted := sizeByKey[keyA] + sizeByKey[keyB]
	if result.SizeEvictedBytes != wantEvicted {
		t.Errorf("SizeEvictedBytes = %d, want %d", result.SizeEvictedBytes, wantEvicted)
	}

	// TotalSizeBytes = size of surviving entry (keyC).
	if result.TotalSizeBytes != sizeByKey[keyC] {
		t.Errorf("TotalSizeBytes = %d, want %d", result.TotalSizeBytes, sizeByKey[keyC])
	}

	// File system: keyA and keyB dirs are gone; keyC dir exists.
	assertNotExists(t, filepath.Join(root, "git", keyA))
	assertNotExists(t, filepath.Join(root, "git", keyB))
	assertExists(t, filepath.Join(root, "git", keyC))
}

// TestPruneSizePhaseStopsAtCapExactly verifies that when removing one more entry
// than necessary to reach cap, the function stops immediately at ≤ cap.
func TestPruneSizePhaseStopsAtCapExactly(t *testing.T) {
	root := t.TempDir()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	makeGitEntry(t, root, keyA, base)
	makeGitEntry(t, root, keyB, base.Add(time.Hour))
	makeGitEntry(t, root, keyC, base.Add(2*time.Hour))

	entries, err := List(Options{GitCacheDir: filepath.Join(root, "git")})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var totalSize int64
	for _, e := range entries {
		totalSize += e.SizeBytes
	}

	// Cap = totalSize - size(keyA): removing only keyA is sufficient.
	sizeA := entries[0].SizeBytes // entries sorted by key; keyA is first
	capBytes := totalSize - sizeA

	result, err := Prune(OperationOptions{
		Options:  Options{GitCacheDir: filepath.Join(root, "git")},
		MaxBytes: capBytes,
		Yes:      true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	// Only keyA (oldest) should be removed.
	if len(result.Entries) != 1 || result.Entries[0].Key != keyA {
		t.Fatalf("expected only keyA removed; got %v", result.Entries)
	}
	if result.TotalSizeBytes > capBytes {
		t.Errorf("TotalSizeBytes %d exceeds cap %d", result.TotalSizeBytes, capBytes)
	}
}

// TestPruneDryRunParity verifies that dry-run Entries exactly matches the set
// of entries a subsequent real run would remove, for age-only, size-only, and
// combined runs. Sabotage-validated during implementation: making dry-run skip
// the size phase makes this test fail. (Inverting the LRU sort does NOT fail
// this test — both runs invert identically, so parity trivially holds; that
// sabotage is caught by TestPruneSizePhaseEvictsOldestFirst instead.)
func TestPruneDryRunParity(t *testing.T) {
	t.Run("age-only", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now().UTC()
		oldTime := now.Add(-48 * time.Hour)   // > 24h ago: age-evicted
		recentTime := now.Add(-1 * time.Hour) // < 24h ago: survives

		makeGitEntry(t, root, keyA, oldTime)
		makeGitEntry(t, root, keyB, recentTime)

		gitDir := filepath.Join(root, "git")
		opts := OperationOptions{
			Options:   Options{GitCacheDir: gitDir},
			OlderThan: 24 * time.Hour,
		}

		dry, err := Prune(OperationOptions{
			Options:   opts.Options,
			OlderThan: opts.OlderThan,
			DryRun:    true,
		})
		if err != nil {
			t.Fatalf("dry-run Prune() error = %v", err)
		}

		realRun, err := Prune(OperationOptions{
			Options:   opts.Options,
			OlderThan: opts.OlderThan,
			Yes:       true,
		})
		if err != nil {
			t.Fatalf("real Prune() error = %v", err)
		}

		assertEntrySetEqual(t, dry.Entries, realRun.Entries)
		if dry.RemovedCount != 0 {
			t.Errorf("dry-run RemovedCount = %d, want 0", dry.RemovedCount)
		}
	})

	t.Run("size-only", func(t *testing.T) {
		root := t.TempDir()
		base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

		makeGitEntry(t, root, keyA, base)
		makeGitEntry(t, root, keyB, base.Add(time.Hour))
		makeGitEntry(t, root, keyC, base.Add(2*time.Hour))

		gitDir := filepath.Join(root, "git")
		entries, err := List(Options{GitCacheDir: gitDir})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		// Cap to newest entry's size only — forces 2 evictions.
		// Build a size-by-key map to look up keyC directly.
		sizeByKey := map[string]int64{}
		for _, e := range entries {
			sizeByKey[e.Key] = e.SizeBytes
		}
		capBytes := sizeByKey[keyC]

		opts := OperationOptions{
			Options:  Options{GitCacheDir: gitDir},
			MaxBytes: capBytes,
		}

		dry, err := Prune(OperationOptions{
			Options:  opts.Options,
			MaxBytes: opts.MaxBytes,
			DryRun:   true,
		})
		if err != nil {
			t.Fatalf("dry-run Prune() error = %v", err)
		}

		realRun, err := Prune(OperationOptions{
			Options:  opts.Options,
			MaxBytes: opts.MaxBytes,
			Yes:      true,
		})
		if err != nil {
			t.Fatalf("real Prune() error = %v", err)
		}

		assertEntrySetEqual(t, dry.Entries, realRun.Entries)
		if dry.RemovedCount != 0 {
			t.Errorf("dry-run RemovedCount = %d, want 0", dry.RemovedCount)
		}
		if dry.SizeEvictedBytes != realRun.SizeEvictedBytes {
			t.Errorf("dry SizeEvictedBytes %d != real %d", dry.SizeEvictedBytes, realRun.SizeEvictedBytes)
		}
	})

	t.Run("combined", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now().UTC()
		// keyA: old enough for age phase (>24h).
		makeGitEntry(t, root, keyA, now.Add(-48*time.Hour))
		// keyB: not old enough for age (1h), but gets size-evicted.
		makeGitEntry(t, root, keyB, now.Add(-1*time.Hour))
		// keyC: newest; survives both phases.
		makeGitEntry(t, root, keyC, now)

		gitDir := filepath.Join(root, "git")
		entries, err := List(Options{GitCacheDir: gitDir})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		sizeByKey := map[string]int64{}
		for _, e := range entries {
			sizeByKey[e.Key] = e.SizeBytes
		}
		// Cap to only keyC's size: after age removes keyA, size phase removes keyB.
		capBytes := sizeByKey[keyC]

		opts := OperationOptions{
			Options:   Options{GitCacheDir: gitDir},
			OlderThan: 24 * time.Hour,
			MaxBytes:  capBytes,
		}

		dry, err := Prune(OperationOptions{
			Options:   opts.Options,
			OlderThan: opts.OlderThan,
			MaxBytes:  opts.MaxBytes,
			DryRun:    true,
		})
		if err != nil {
			t.Fatalf("dry-run Prune() error = %v", err)
		}

		realRun, err := Prune(OperationOptions{
			Options:   opts.Options,
			OlderThan: opts.OlderThan,
			MaxBytes:  opts.MaxBytes,
			Yes:       true,
		})
		if err != nil {
			t.Fatalf("real Prune() error = %v", err)
		}

		assertEntrySetEqual(t, dry.Entries, realRun.Entries)
		if dry.RemovedCount != 0 {
			t.Errorf("dry-run RemovedCount = %d, want 0", dry.RemovedCount)
		}
	})
}

// TestPruneCombinedPhasesNoCrossCount verifies that age-selected entries are
// excluded from size accounting and that PruneReason is set correctly.
func TestPruneCombinedPhasesNoCrossCount(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()

	// keyA: age-selected (>48h ago, well past the 24h cutoff).
	makeGitEntry(t, root, keyA, now.Add(-48*time.Hour))
	// keyB: not old enough for age phase (1h ago), but will be size-evicted.
	makeGitEntry(t, root, keyB, now.Add(-1*time.Hour))
	// keyC: newest (just now); survives both phases.
	makeGitEntry(t, root, keyC, now)

	gitDir := filepath.Join(root, "git")
	entries, err := List(Options{GitCacheDir: gitDir})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	sizeByKey := map[string]int64{}
	for _, e := range entries {
		sizeByKey[e.Key] = e.SizeBytes
	}

	// Cap: only keyC should remain after both phases.
	capBytes := sizeByKey[keyC]

	result, err := Prune(OperationOptions{
		Options:   Options{GitCacheDir: gitDir},
		OlderThan: 24 * time.Hour,
		MaxBytes:  capBytes,
		Yes:       true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	if result.RemovedCount != 2 {
		t.Fatalf("RemovedCount = %d, want 2", result.RemovedCount)
	}

	reasonByKey := map[string]string{}
	for _, e := range result.Entries {
		reasonByKey[e.Key] = e.PruneReason
	}

	if reasonByKey[keyA] != "age" {
		t.Errorf("keyA PruneReason = %q, want \"age\"", reasonByKey[keyA])
	}
	if reasonByKey[keyB] != "size" {
		t.Errorf("keyB PruneReason = %q, want \"size\"", reasonByKey[keyB])
	}

	// SizeEvictedBytes counts only the size phase (keyB).
	if result.SizeEvictedBytes != sizeByKey[keyB] {
		t.Errorf("SizeEvictedBytes = %d, want %d (keyB only)", result.SizeEvictedBytes, sizeByKey[keyB])
	}

	// TotalSizeBytes = keyC only (survivors after both phases).
	if result.TotalSizeBytes != sizeByKey[keyC] {
		t.Errorf("TotalSizeBytes = %d, want %d (keyC only)", result.TotalSizeBytes, sizeByKey[keyC])
	}
}

// TestPruneSourceFilterScopesCapToFilteredSource verifies that --source=git
// applies the cap only to git entries, leaving remote entries untouched.
func TestPruneSourceFilterScopesCapToFilteredSource(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Two git entries.
	makeGitEntry(t, root, keyA, base)
	makeGitEntry(t, root, keyB, base.Add(time.Hour))

	// One remote entry.
	makeRemoteHTTPEntry(t, root, keyC, base.Add(2*time.Hour))

	gitDir := filepath.Join(root, "git")
	remoteDir := filepath.Join(root, "remotes")

	// List git only to get sizes.
	gitEntries, err := List(Options{GitCacheDir: gitDir, Sources: []Source{SourceGit}})
	if err != nil {
		t.Fatalf("List(git) error = %v", err)
	}
	sizeByKey := map[string]int64{}
	for _, e := range gitEntries {
		sizeByKey[e.Key] = e.SizeBytes
	}

	// Cap to only keyB (newest git) size: should evict keyA from git only.
	capBytes := sizeByKey[keyB]

	result, err := Prune(OperationOptions{
		Options: Options{
			GitCacheDir:    gitDir,
			RemoteCacheDir: remoteDir,
		},
		Source:   SourceGit,
		MaxBytes: capBytes,
		Yes:      true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	// Only keyA (oldest git) should be removed.
	if len(result.Entries) != 1 || result.Entries[0].Key != keyA {
		t.Fatalf("expected only keyA removed; got %v", result.Entries)
	}

	// Remote entry keyC must still exist.
	assertExists(t, filepath.Join(root, "remotes", keyC))
	// keyB git entry survives.
	assertExists(t, filepath.Join(root, "git", keyB))
}

// TestPruneLegacyEntriesFallBackToMtime verifies that entries without metadata
// files are sorted by directory mtime (ModifiedAt) as fallback.
func TestPruneLegacyEntriesFallBackToMtime(t *testing.T) {
	root := t.TempDir()

	// Create entries without metadata (legacy).
	writeCacheFile(t, filepath.Join(root, "git", keyA, ".git", "HEAD"), "legacy-a")
	writeCacheFile(t, filepath.Join(root, "git", keyB, ".git", "HEAD"), "legacy-b")

	// Assign distinct mtimes so the eviction order is deterministic:
	// keyA is older (evicted first), keyB is newer (survives).
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mtimeA := base
	mtimeB := base.Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "git", keyA), mtimeA, mtimeA); err != nil {
		t.Fatalf("Chtimes(keyA) error = %v", err)
	}
	if err := os.Chtimes(filepath.Join(root, "git", keyB), mtimeB, mtimeB); err != nil {
		t.Fatalf("Chtimes(keyB) error = %v", err)
	}

	gitDir := filepath.Join(root, "git")
	entries, err := List(Options{GitCacheDir: gitDir})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if !e.Legacy {
			t.Errorf("entry %s Legacy = false, want true", e.Key)
		}
	}

	// Both entries have no metadata; entryAgeTime uses ModifiedAt.
	// keyA has the older mtime and must be evicted first.
	sizeByKey := map[string]int64{}
	for _, e := range entries {
		sizeByKey[e.Key] = e.SizeBytes
	}

	// Cap to keyB's size: keyA (oldest mtime) gets evicted, keyB survives.
	capBytes := sizeByKey[keyB]

	result, err := Prune(OperationOptions{
		Options:  Options{GitCacheDir: gitDir},
		MaxBytes: capBytes,
		Yes:      true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 entry removed; got %v", result.Entries)
	}
	if result.Entries[0].Key != keyA {
		t.Errorf("evicted entry key = %q, want keyA (oldest mtime)", result.Entries[0].Key)
	}
	if result.TotalSizeBytes != sizeByKey[keyB] {
		t.Errorf("TotalSizeBytes = %d, want %d (keyB only)", result.TotalSizeBytes, sizeByKey[keyB])
	}
	assertNotExists(t, filepath.Join(root, "git", keyA))
	assertExists(t, filepath.Join(root, "git", keyB))
}

// TestPruneNeitherConstraintErrors verifies that calling Prune without either
// OlderThan or MaxBytes returns the expected error.
func TestPruneNeitherConstraintErrors(t *testing.T) {
	root := t.TempDir()
	makeGitEntry(t, root, keyA, time.Now())

	_, err := Prune(OperationOptions{
		Options: Options{GitCacheDir: filepath.Join(root, "git")},
		Yes:     true,
	})
	if err == nil {
		t.Fatal("Prune() error = nil, want error for missing constraints")
	}
	if !strings.Contains(err.Error(), "at least one of older-than or max-size is required") {
		t.Errorf("error = %q, want message about older-than or max-size", err.Error())
	}
}

// TestPruneSizeOnlyUnderCapIsNoop verifies that when all entries are already
// within the cap, no entries are evicted and TotalSizeBytes is still populated.
// The cap is set to exactly the total (not total+1) to pin the total <= maxBytes
// boundary. Sabotage-validated during implementation: changing both <= guards in
// selectSizeEntries to < causes this test to fail with 1 entry incorrectly evicted.
func TestPruneSizeOnlyUnderCapIsNoop(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	makeGitEntry(t, root, keyA, base)
	makeGitEntry(t, root, keyB, base.Add(time.Hour))

	gitDir := filepath.Join(root, "git")
	entries, err := List(Options{GitCacheDir: gitDir})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var totalSize int64
	for _, e := range entries {
		totalSize += e.SizeBytes
	}

	// Cap equals exact total: nothing should be evicted (tests the total <= cap
	// early-return boundary — would fail if the condition were total < cap).
	capBytes := totalSize

	result, err := Prune(OperationOptions{
		Options:  Options{GitCacheDir: gitDir},
		MaxBytes: capBytes,
		Yes:      true,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	if len(result.Entries) != 0 {
		t.Errorf("expected no entries removed; got %v", result.Entries)
	}
	if result.RemovedCount != 0 {
		t.Errorf("RemovedCount = %d, want 0", result.RemovedCount)
	}
	if result.TotalSizeBytes != totalSize {
		t.Errorf("TotalSizeBytes = %d, want %d", result.TotalSizeBytes, totalSize)
	}
	if result.SizeEvictedBytes != 0 {
		t.Errorf("SizeEvictedBytes = %d, want 0", result.SizeEvictedBytes)
	}

	// Both entries still on disk.
	assertExists(t, filepath.Join(root, "git", keyA))
	assertExists(t, filepath.Join(root, "git", keyB))
}

// assertEntrySetEqual asserts that two entry slices contain the same Keys,
// regardless of order or slice position.
func assertEntrySetEqual(t *testing.T, a, b []Entry) {
	t.Helper()
	keysA := entryKeySet(a)
	keysB := entryKeySet(b)
	if !reflect.DeepEqual(keysA, keysB) {
		t.Errorf("entry key sets differ:\n  dry-run: %v\n  real:    %v", keysA, keysB)
	}
}

func entryKeySet(entries []Entry) map[string]bool {
	m := make(map[string]bool, len(entries))
	for _, e := range entries {
		m[e.Key] = true
	}
	return m
}

// makeChartHTTPEntry creates a chart cache entry at root/charts/http/<key>
// with metadata (Source chart, Kind "http") and a controlled UpdatedAt.
func makeChartHTTPEntry(t *testing.T, root, key string, updatedAt time.Time) {
	t.Helper()
	entryRoot := filepath.Join(root, "charts", "http", key)
	writeCacheFile(t, filepath.Join(entryRoot, "mychart", "Chart.yaml"), "name: mychart\n")
	if err := WriteMetadata(entryRoot, Metadata{
		SchemaVersion: 1,
		Source:        SourceChart,
		Kind:          "http",
		Key:           key,
		UpdatedAt:     updatedAt,
		CreatedAt:     updatedAt,
	}); err != nil {
		t.Fatalf("WriteMetadata(%s) error = %v", key, err)
	}
}

// makeRemoteGitRepoEntry creates a remote/git-repo cache entry at
// root/remotes/<key> (the repo/ dir marks the git-repo kind) with metadata.
func makeRemoteGitRepoEntry(t *testing.T, root, key string, updatedAt time.Time) {
	t.Helper()
	entryRoot := filepath.Join(root, "remotes", key)
	writeCacheFile(t, filepath.Join(entryRoot, "repo", "HEAD"), "ref: refs/heads/main\n")
	if err := WriteMetadata(entryRoot, Metadata{
		SchemaVersion: 1,
		Source:        SourceRemote,
		Kind:          "git-repo",
		Key:           key,
		UpdatedAt:     updatedAt,
		CreatedAt:     updatedAt,
	}); err != nil {
		t.Fatalf("WriteMetadata(%s) error = %v", key, err)
	}
}

// TestPruneSizeEqualTimestampTieBreak verifies that when entries share an
// identical UpdatedAt timestamp the eviction sort falls back to
// (Source, Kind, Key) ascending. Each comparator is pinned by a pair whose
// lower-priority orderings OPPOSE it, so deleting that comparator flips the
// outcome:
//
//   - source: chart/http (Source "chart", key "bbb…") vs git/git (Source
//     "git", key "aaa…"). Source orders the chart entry first; both fallbacks
//     — Kind ("git" < "http") and Key ("aaa…" < "bbb…") — order the git entry
//     first. Only the Source comparator evicts the chart entry.
//   - kind: remote git-repo (key "bbb…") vs remote http-file (key "aaa…").
//     Sources are equal; Kind ("git-repo" < "http-file") orders the git-repo
//     entry first while Key orders the http-file entry first. Only the Kind
//     comparator evicts the git-repo entry.
//   - key: two git entries with equal timestamps; Key orders "aaa…" first.
//     List() pre-sorts candidates by key in the same direction, so deleting
//     the Key comparator alone is masked by arrival order — this case pins
//     the deterministic outcome, not the comparator line itself.
//
// Sabotage-validated: deleting the Source comparator fails the "source"
// case; deleting the Kind comparator fails the "kind" case.
func TestPruneSizeEqualTimestampTieBreak(t *testing.T) {
	ts := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	// evictOne runs Prune with the given options capped to the expected
	// survivor's size and asserts exactly one entry was evicted.
	evictOne := func(t *testing.T, opts Options, capBytes int64) Entry {
		t.Helper()
		result, err := Prune(OperationOptions{Options: opts, MaxBytes: capBytes, Yes: true})
		if err != nil {
			t.Fatalf("Prune() error = %v", err)
		}
		if len(result.Entries) != 1 {
			t.Fatalf("expected 1 entry evicted; got %v", result.Entries)
		}
		return result.Entries[0]
	}

	sizesByKey := func(t *testing.T, opts Options, want int) map[string]int64 {
		t.Helper()
		entries, err := List(opts)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(entries) != want {
			t.Fatalf("expected %d entries, got %d", want, len(entries))
		}
		sizes := map[string]int64{}
		for _, e := range entries {
			sizes[e.Key] = e.SizeBytes
		}
		return sizes
	}

	t.Run("source", func(t *testing.T) {
		root := t.TempDir()
		makeChartHTTPEntry(t, root, keyB, ts) // Source "chart" — must be evicted
		makeGitEntry(t, root, keyA, ts)       // Source "git" — must survive
		opts := Options{
			GitCacheDir:   filepath.Join(root, "git"),
			ChartCacheDir: filepath.Join(root, "charts"),
		}
		sizes := sizesByKey(t, opts, 2)
		evicted := evictOne(t, opts, sizes[keyA])
		if evicted.Source != SourceChart || evicted.Key != keyB {
			t.Errorf("tie-break evicted source=%q key=%q, want source=chart key=%s (Source \"chart\" sorts before \"git\")", evicted.Source, evicted.Key, keyB)
		}
		assertNotExists(t, filepath.Join(root, "charts", "http", keyB))
		assertExists(t, filepath.Join(root, "git", keyA))
	})

	t.Run("kind", func(t *testing.T) {
		root := t.TempDir()
		makeRemoteGitRepoEntry(t, root, keyB, ts) // Kind "git-repo" — must be evicted
		makeRemoteHTTPEntry(t, root, keyA, ts)    // Kind "http-file" — must survive
		opts := Options{RemoteCacheDir: filepath.Join(root, "remotes")}
		sizes := sizesByKey(t, opts, 2)
		evicted := evictOne(t, opts, sizes[keyA])
		if evicted.Kind != "git-repo" || evicted.Key != keyB {
			t.Errorf("tie-break evicted kind=%q key=%q, want kind=git-repo key=%s (Kind \"git-repo\" sorts before \"http-file\")", evicted.Kind, evicted.Key, keyB)
		}
		assertNotExists(t, filepath.Join(root, "remotes", keyB))
		assertExists(t, filepath.Join(root, "remotes", keyA))
	})

	t.Run("key", func(t *testing.T) {
		root := t.TempDir()
		makeGitEntry(t, root, keyA, ts)
		makeGitEntry(t, root, keyB, ts)
		opts := Options{GitCacheDir: filepath.Join(root, "git")}
		sizes := sizesByKey(t, opts, 2)
		evicted := evictOne(t, opts, sizes[keyB])
		if evicted.Key != keyA {
			t.Errorf("tie-break evicted key=%q, want %s (Key ascending)", evicted.Key, keyA)
		}
		assertNotExists(t, filepath.Join(root, "git", keyA))
		assertExists(t, filepath.Join(root, "git", keyB))
	})
}
