package rendercache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func testKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func TestStorePutGetRoundTrip(t *testing.T) {
	store, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	key := testKey("round-trip")
	payload := []byte(`{"manifests":[{"sourceIndex":0}]}`)

	if err := store.Put(key, payload, EntryMeta{Version: "1.2.3", Commit: "abc"}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, hit, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !hit {
		t.Fatalf("Get() hit = false, want true")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("Get() payload = %s, want %s", got, payload)
	}
	if got := store.Writes(); got != 1 {
		t.Fatalf("Writes() = %d, want 1", got)
	}
}

func TestStoreGetMissingKeyIsMiss(t *testing.T) {
	store, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	payload, hit, err := store.Get(testKey("missing"))
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if hit || payload != nil {
		t.Fatalf("Get() = (%v, %t), want (nil, false)", payload, hit)
	}
}

func TestDefaultDirUsesUserCacheDir(t *testing.T) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("os.UserCacheDir() unavailable: %v", err)
	}
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir() error = %v", err)
	}
	want := filepath.Join(userCacheDir, "drydock", "render")
	if got != want {
		t.Fatalf("DefaultDir() = %s, want %s", got, want)
	}
}

func TestResolveDirRejectsSymlinkIntoForbiddenRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges are not guaranteed on Windows")
	}
	repoRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(outside, "render-link")
	if err := os.Symlink(repoRoot, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	_, err := ResolveDir(filepath.Join(link, "render"), []string{repoRoot})
	if err == nil || !strings.Contains(err.Error(), "render cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("ResolveDir() error = %v, want symlink containment error", err)
	}
}

func TestStoreEntryLayoutAndPermissions(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	key := testKey("layout")
	if err := store.Put(key, []byte(`{}`), EntryMeta{}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	entryPath := filepath.Join(dir, "v1", key[:2], key+".json.gz")
	info, err := os.Stat(entryPath)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", entryPath, err)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("entry mode = %v, want 0600", got)
		}
		dirInfo, err := os.Stat(filepath.Join(dir, "v1", key[:2]))
		if err != nil {
			t.Fatalf("Stat(shard dir) error = %v", err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Fatalf("shard dir mode = %v, want 0700", got)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "v1", key[:2]))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("shard dir entries = %d, want 1 (no leftover temp files)", len(entries))
	}
}

func TestStoreEnvelopeFields(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	key := testKey("envelope")
	if err := store.Put(key, []byte(`{"ok":true}`), EntryMeta{Version: "9.9.9", Commit: "feedface"}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "v1", key[:2], key+".json.gz"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	var entry struct {
		FormatVersion int    `json:"formatVersion"`
		Key           string `json:"key"`
		CreatedAt     string `json:"createdAt"`
		Drydock       struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"drydock"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(gz).Decode(&entry); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if entry.FormatVersion != FormatVersion {
		t.Fatalf("formatVersion = %d, want %d", entry.FormatVersion, FormatVersion)
	}
	if entry.Key != key {
		t.Fatalf("key = %s, want %s", entry.Key, key)
	}
	if entry.CreatedAt == "" {
		t.Fatalf("createdAt is empty")
	}
	if entry.Drydock.Version != "9.9.9" || entry.Drydock.Commit != "feedface" {
		t.Fatalf("drydock = %+v, want {9.9.9 feedface}", entry.Drydock)
	}
	if string(entry.Result) != `{"ok":true}` {
		t.Fatalf("result = %s, want {\"ok\":true}", entry.Result)
	}
}

func TestStoreCorruptEntryDeletedAndErrors(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	cases := []struct {
		name string
		body func(t *testing.T, key, path string)
	}{
		{name: "truncated gzip", body: func(t *testing.T, key, path string) {
			writeTruncatedGzipEntry(t, store, key, path)
		}},
		{name: "invalid json", body: writeInvalidJSONEntry},
		{name: "key mismatch", body: func(t *testing.T, key, path string) {
			writeMismatchedKeyEntry(t, store, dir, path)
		}},
	}
	for i, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			key := testKey(fmt.Sprintf("corrupt-%d", i))
			path := filepath.Join(dir, "v1", key[:2], key+".json.gz")
			testCase.body(t, key, path)

			payload, hit, err := store.Get(key)
			if err == nil {
				t.Fatalf("Get() error = nil, want corrupt-entry error")
			}
			if hit || payload != nil {
				t.Fatalf("Get() = (%v, %t), want (nil, false)", payload, hit)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("corrupt entry still exists: stat err = %v", statErr)
			}
		})
	}
}

func writeTruncatedGzipEntry(t *testing.T, store *Store, key, path string) {
	t.Helper()
	if err := store.Put(key, []byte(`{}`), EntryMeta{}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := os.WriteFile(path, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeInvalidJSONEntry(t *testing.T, _ string, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	if _, err := gz.Write([]byte("not-json")); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeMismatchedKeyEntry(t *testing.T, store *Store, dir, path string) {
	t.Helper()
	other := testKey("some-other-key")
	if err := store.Put(other, []byte(`{}`), EntryMeta{}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	otherPath := filepath.Join(dir, "v1", other[:2], other+".json.gz")
	raw, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func TestStoreRejectsInvalidKeys(t *testing.T) {
	store, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for _, key := range []string{"", "short", strings.Repeat("z", 64), "../" + strings.Repeat("a", 61)} {
		if err := store.Put(key, []byte(`{}`), EntryMeta{}); err == nil {
			t.Fatalf("Put(%q) error = nil, want invalid-key error", key)
		}
		if _, _, err := store.Get(key); err == nil {
			t.Fatalf("Get(%q) error = nil, want invalid-key error", key)
		}
	}
}

func TestStoreDeleteToleratesMissing(t *testing.T) {
	store, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Delete(testKey("never-written")); err != nil {
		t.Fatalf("Delete() error = %v, want nil for missing entry", err)
	}
}

func TestStoreDeleteRemovesEntry(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	key := testKey("delete")
	if err := store.Put(key, []byte(`{"ok":true}`), EntryMeta{}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := store.Delete(key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	got, hit, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if hit || got != nil {
		t.Fatalf("Get() = (%v, %t), want (nil, false)", got, hit)
	}
	path := filepath.Join(dir, "v1", key[:2], key+".json.gz")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted entry still exists: stat err = %v", err)
	}
}

// Atomicity property: while one goroutine overwrites an entry repeatedly,
// concurrent readers must never observe a partial entry; every hit parses.
func TestStoreConcurrentGetNeverSeesPartialEntry(t *testing.T) {
	// Same OS gate pattern as the permission checks above: Windows cannot
	// reliably rename over an entry a concurrent reader holds open.
	if runtime.GOOS == "windows" {
		t.Skip("rename-over-existing with a concurrent reader is unreliable on Windows")
	}
	store, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	key := testKey("atomic")
	payload := []byte(`{"data":"` + strings.Repeat("x", 64*1024) + `"}`)
	if err := store.Put(key, payload, EntryMeta{}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 100 {
			if err := store.Put(key, payload, EntryMeta{}); err != nil {
				t.Errorf("Put() error = %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			got, hit, err := store.Get(key)
			if err != nil {
				t.Errorf("Get() error = %v", err)
				return
			}
			if hit && !bytes.Equal(got, payload) {
				t.Errorf("Get() returned partial/foreign payload (%d bytes)", len(got))
				return
			}
		}
	}()
	wg.Wait()
}
