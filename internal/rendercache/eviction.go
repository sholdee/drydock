package rendercache

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// putTempPrefix matches Store.Put's os.CreateTemp pattern. A crash between
// CreateTemp and the final rename strands these files; they never match the
// .json.gz entry filter, so the sweep removes them once they are old enough
// to be provably abandoned rather than a concurrent Put in flight.
const putTempPrefix = ".put-"

const orphanTempMaxAge = time.Hour

// SweepResult reports what one eviction sweep did.
type SweepResult struct {
	// TotalBytes is the post-sweep sum of entry sizes.
	TotalBytes int64 `json:"totalBytes" yaml:"totalBytes"`
	// EvictedBytes is the sum of deleted entry sizes.
	EvictedBytes int64 `json:"evictedBytes" yaml:"evictedBytes"`
	// EvictedKeys lists deleted entry keys in eviction order.
	EvictedKeys []string `json:"evictedKeys" yaml:"evictedKeys"`
}

type storeEntry struct {
	path    string
	key     string
	size    int64
	modTime time.Time
}

// Sweep enforces the size cap. When the summed entry size exceeds the cap, it
// deletes oldest-mtime entries until at or below 90% of cap. Missing files are
// tolerated so concurrent sweeps and concurrent corrupt-entry deletes race
// benignly. Sweep also removes stale orphaned .put-* temp files regardless of
// the cap.
func (s *Store) Sweep() (SweepResult, error) {
	entries, totalBytes, err := s.listEntries()
	if err != nil {
		return SweepResult{}, err
	}
	result := SweepResult{TotalBytes: totalBytes}
	if totalBytes <= s.maxSizeBytes {
		return result, nil
	}
	target := s.maxSizeBytes / 10 * 9
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})
	for _, entry := range entries {
		if result.TotalBytes <= target {
			break
		}
		if err := deleteEntryFile(entry.path); err != nil {
			return result, err
		}
		result.TotalBytes -= entry.size
		result.EvictedBytes += entry.size
		result.EvictedKeys = append(result.EvictedKeys, entry.key)
	}
	return result, nil
}

func (s *Store) listEntries() ([]storeEntry, int64, error) {
	var entries []storeEntry
	var total int64
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, putTempPrefix) {
			info, err := d.Info()
			if err != nil {
				return ignoreMissingEntry(err)
			}
			if time.Since(info.ModTime()) > orphanTempMaxAge {
				_ = deleteEntryFile(path)
			}
			return nil
		}
		if !strings.HasSuffix(name, ".json.gz") {
			return nil
		}
		key := strings.TrimSuffix(name, ".json.gz")
		if !validEntryKey(key) {
			return nil
		}
		entryPath, err := s.entryPath(key)
		if err != nil {
			return err
		}
		if path != entryPath {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return ignoreMissingEntry(err)
		}
		entries = append(entries, storeEntry{
			path:    path,
			key:     key,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

func ignoreMissingEntry(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func validEntryKey(key string) bool {
	return validateKey(key) == nil
}
