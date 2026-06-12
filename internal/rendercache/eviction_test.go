package rendercache

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// putRandomEntry writes an entry with an incompressible random payload so gzip
// cannot collapse it to nothing, then pins its mtime. Tests derive caps from
// measured on-disk sizes via listEntries rather than guessing compressed sizes.
func putRandomEntry(t *testing.T, store *Store, dir, seed string, payloadBytes int, modTime time.Time) string {
	t.Helper()
	key := testKey(seed)
	random := rand.New(rand.NewSource(int64(len(seed)) + 7919))
	data := make([]byte, payloadBytes)
	random.Read(data)
	payload := []byte(`{"data":"` + hex.EncodeToString(data) + `"}`)
	if err := store.Put(key, payload, EntryMeta{}); err != nil {
		t.Fatalf("Put(%s) error = %v", seed, err)
	}
	path := filepath.Join(dir, "v1", key[:2], key+".json.gz")
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes(%s) error = %v", path, err)
	}
	return key
}

func entryExists(t *testing.T, dir, key string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "v1", key[:2], key+".json.gz"))
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("Stat() error = %v", err)
	return false
}

func TestSweepUnderCapDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, 1024*1024)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	key := putRandomEntry(t, store, dir, "small", 1024, time.Now())

	result, err := store.Sweep()
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(result.EvictedKeys) != 0 {
		t.Fatalf("EvictedKeys = %v, want none", result.EvictedKeys)
	}
	if !entryExists(t, dir, key) {
		t.Fatalf("entry was deleted under cap")
	}
}

func TestSweepEvictsOldestUntilBelowNinetyPercent(t *testing.T) {
	dir := t.TempDir()
	prime, err := Open(dir, DefaultMaxSizeBytes)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	base := time.Now().Add(-time.Hour)
	oldest := putRandomEntry(t, prime, dir, "entry-oldest", 8*1024, base)
	middle := putRandomEntry(t, prime, dir, "entry-middle", 8*1024, base.Add(time.Minute))
	newest := putRandomEntry(t, prime, dir, "entry-newest", 8*1024, base.Add(2*time.Minute))

	// Measure real on-disk sizes, then reopen with cap = total-1: the
	// directory is over cap by one byte, and deleting the oldest entry
	// (~1/3 of total) lands well under 90% of cap, so the sweep must delete
	// exactly the oldest entry and stop.
	_, total, err := prime.listEntries()
	if err != nil {
		t.Fatalf("listEntries() error = %v", err)
	}
	store, err := Open(dir, total-1)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	result, err := store.Sweep()
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(result.EvictedKeys) != 1 || result.EvictedKeys[0] != oldest {
		t.Fatalf("EvictedKeys = %v, want exactly [%s]", result.EvictedKeys, oldest)
	}
	if entryExists(t, dir, oldest) {
		t.Fatalf("oldest entry survived sweep")
	}
	if !entryExists(t, dir, middle) || !entryExists(t, dir, newest) {
		t.Fatalf("newer entries were evicted")
	}
	if result.TotalBytes > (total-1)/10*9 {
		t.Fatalf("TotalBytes after sweep = %d, want <= %d", result.TotalBytes, (total-1)/10*9)
	}
}

func TestSweepStopsAtNinetyPercentNotZero(t *testing.T) {
	dir := t.TempDir()
	prime, err := Open(dir, DefaultMaxSizeBytes)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	base := time.Now().Add(-time.Hour)
	for i := range 6 {
		putRandomEntry(t, prime, dir, fmt.Sprintf("entry-%d", i), 4*1024, base.Add(time.Duration(i)*time.Minute))
	}
	_, total, err := prime.listEntries()
	if err != nil {
		t.Fatalf("listEntries() error = %v", err)
	}
	store, err := Open(dir, total/2)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	result, err := store.Sweep()
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if result.TotalBytes == 0 {
		t.Fatalf("Sweep() deleted everything; want prune to ~90%% of cap")
	}
	if result.TotalBytes > total/2/10*9 {
		t.Fatalf("TotalBytes = %d, want <= %d", result.TotalBytes, total/2/10*9)
	}
	if len(result.EvictedKeys) == 0 || len(result.EvictedKeys) == 6 {
		t.Fatalf("EvictedKeys = %d entries, want some but not all evicted", len(result.EvictedKeys))
	}
}

func TestSweepToleratesConcurrentlyDeletedEntries(t *testing.T) {
	// deleteEntryFile is the sweep's deletion primitive; it must tolerate
	// ENOENT so two concurrent sweeps race benignly.
	if err := deleteEntryFile(filepath.Join(t.TempDir(), "v1", "ab", testKey("gone")+".json.gz")); err != nil {
		t.Fatalf("deleteEntryFile() error = %v, want nil for missing file", err)
	}
}

func TestSweepIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, 1024)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	foreign := filepath.Join(dir, "v1", "zz", "README.txt")
	if err := os.MkdirAll(filepath.Dir(foreign), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(foreign, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result, err := store.Sweep()
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(result.EvictedKeys) != 0 {
		t.Fatalf("EvictedKeys = %v, want none (foreign files are not entries)", result.EvictedKeys)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign file was touched: %v", err)
	}
}

func TestSweepIgnoresValidKeyOutsideCanonicalShard(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, 1024)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	key := testKey("foreign-valid-key")
	foreign := filepath.Join(dir, "v1", "zz", key+".json.gz")
	if err := os.MkdirAll(filepath.Dir(foreign), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(foreign, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result, err := store.Sweep()
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(result.EvictedKeys) != 0 {
		t.Fatalf("EvictedKeys = %v, want none (wrong-shard files are not entries)", result.EvictedKeys)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("wrong-shard file was touched: %v", err)
	}
}

func TestSweepRemovesStaleOrphanTempFiles(t *testing.T) {
	store, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	shard := filepath.Join(store.root, "ab")
	if err := os.MkdirAll(shard, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	stale := filepath.Join(shard, ".put-stale123")
	fresh := filepath.Join(shard, ".put-fresh456")
	for _, path := range []string{stale, fresh} {
		if err := os.WriteFile(path, []byte("orphan"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	if _, err := store.Sweep(); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale orphan %q still present after sweep", stale)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp file %q was removed; it may belong to a concurrent Put: %v", fresh, err)
	}
}
