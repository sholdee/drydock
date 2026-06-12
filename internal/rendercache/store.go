package rendercache

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sholdee/drydock/internal/pathsafety"
)

// FormatVersion is the entry schema version. It rotates together with the
// "v1" path segment on any entry-schema change and participates in the
// persistent render cache key.
const FormatVersion = 1

// DefaultMaxSizeBytes is the default eviction cap: 512 MiB.
const DefaultMaxSizeBytes int64 = 512 * 1024 * 1024

const storeVersionSegment = "v1"

// Store is a handle on one cache directory. It is safe for concurrent use;
// concurrent processes sharing the directory are safe by construction through
// atomic rename writes and ENOENT-tolerant deletes.
type Store struct {
	root         string
	maxSizeBytes int64
	writes       atomic.Int64
}

// EntryMeta describes the drydock build that produced an entry. It is stored
// in the envelope for debuggability and never read back into results.
type EntryMeta struct {
	Version string
	Commit  string
}

type envelopeOrigin struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type envelope struct {
	FormatVersion int             `json:"formatVersion"`
	Key           string          `json:"key"`
	CreatedAt     string          `json:"createdAt"`
	Drydock       envelopeOrigin  `json:"drydock"`
	Result        json.RawMessage `json:"result"`
}

// DefaultDir returns <os.UserCacheDir()>/drydock/render, consistent with the
// existing git/chart/remote cache roots.
func DefaultDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "drydock", "render"), nil
}

// Open prepares the versioned cache root under dir. An empty dir uses
// DefaultDir; a non-positive maxSizeBytes uses DefaultMaxSizeBytes.
func Open(dir string, maxSizeBytes int64) (*Store, error) {
	dir, err := ResolveDir(dir, nil)
	if err != nil {
		return nil, err
	}
	if maxSizeBytes <= 0 {
		maxSizeBytes = DefaultMaxSizeBytes
	}
	root := filepath.Join(dir, storeVersionSegment)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("prepare render cache directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure render cache directory: %w", err)
	}
	return &Store{root: root, maxSizeBytes: maxSizeBytes}, nil
}

// ResolveDir resolves the configured render cache root and rejects locations
// inside repository roots or other protected roots.
func ResolveDir(dir string, forbiddenRoots []string) (string, error) {
	if dir == "" {
		resolved, err := DefaultDir()
		if err != nil {
			return "", err
		}
		dir = resolved
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absDir = filepath.Clean(absDir)
	inside, matchedRoot, err := pathsafety.IsInsideAny(absDir, forbiddenRoots)
	if err != nil {
		return "", err
	}
	if inside {
		return "", fmt.Errorf("render cache dir %q must not be inside repository root %q", absDir, matchedRoot)
	}
	return absDir, nil
}

// Writes reports how many entries this handle has written. The post-run
// eviction sweep only runs when at least one entry was written.
func (s *Store) Writes() int64 {
	return s.writes.Load()
}

func validateKey(key string) error {
	if len(key) != 64 {
		return fmt.Errorf("render cache key must be 64 lowercase hex characters")
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("render cache key must be 64 lowercase hex characters")
		}
	}
	return nil
}

func (s *Store) entryPath(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return filepath.Join(s.root, key[:2], key+".json.gz"), nil
}

// Get returns the opaque payload for key. A missing entry is (nil, false,
// nil). A corrupt, unparseable, or wrong-key entry is deleted
// (ENOENT-tolerant) and reported as an error so callers can emit an
// error-action cache event and treat it as a miss. A hit refreshes the entry
// mtime best-effort for LRU eviction.
func (s *Store) Get(key string) ([]byte, bool, error) {
	path, err := s.entryPath(key)
	if err != nil {
		return nil, false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	payload, readErr := readEntry(file, key)
	closeErr := file.Close()
	if readErr != nil {
		_ = deleteEntryFile(path)
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return payload, true, nil
}

func readEntry(reader io.Reader, key string) ([]byte, error) {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("render cache entry %s: decompress: %w", key[:12], err)
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("render cache entry %s: decompress: %w", key[:12], err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("render cache entry %s: decompress: %w", key[:12], err)
	}
	var entry envelope
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("render cache entry %s: decode: %w", key[:12], err)
	}
	if entry.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("render cache entry %s: format version %d, want %d", key[:12], entry.FormatVersion, FormatVersion)
	}
	if entry.Key != key {
		return nil, fmt.Errorf("render cache entry %s: key mismatch", key[:12])
	}
	if len(entry.Result) == 0 {
		return nil, fmt.Errorf("render cache entry %s: empty result payload", key[:12])
	}
	return entry.Result, nil
}

// Put writes the entry atomically: serialize to a temp file in the destination
// directory, then rename. A lost write race with a concurrent process costs
// one redundant render; torn reads are avoided by never writing in place.
func (s *Store) Put(key string, payload []byte, meta EntryMeta) error {
	path, err := s.entryPath(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("prepare render cache shard directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure render cache shard directory: %w", err)
	}
	entry := envelope{
		FormatVersion: FormatVersion,
		Key:           key,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Drydock:       envelopeOrigin(meta),
		Result:        json.RawMessage(payload),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode render cache entry: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return fmt.Errorf("write render cache entry: %w", err)
	}
	tempPath := temp.Name()
	cleanupTemp := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanupTemp()
		return fmt.Errorf("write render cache entry: %w", err)
	}
	if err := writeGzip(temp, data); err != nil {
		cleanupTemp()
		return fmt.Errorf("write render cache entry: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("write render cache entry: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("write render cache entry: %w", err)
	}
	s.writes.Add(1)
	return nil
}

func writeGzip(w io.Writer, data []byte) error {
	gz := gzip.NewWriter(w)
	if _, err := gz.Write(data); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// Delete removes the entry for key, tolerating a missing file.
func (s *Store) Delete(key string) error {
	path, err := s.entryPath(key)
	if err != nil {
		return err
	}
	return deleteEntryFile(path)
}

func deleteEntryFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
