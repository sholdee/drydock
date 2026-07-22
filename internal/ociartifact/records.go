package ociartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
)

// recordsDirName holds per-repository tag records captured on successful
// online resolves so tags and semver constraints resolve under --offline.
// "tags" is not a 64-hex cache key, so the cache entry listers skip the
// directory (internal/cache/cache.go:384,581-591 isCacheKey) and prune never
// selects it.
const recordsDirName = "tags"

// tagRecord is one repository's offline resolution record: the tag list from
// the last online tag-list fetch plus the revision→digest pairs resolved
// online. Digest entries merge across resolves; the tag list is replaced
// whole on each fresh fetch (see updateTagRecord).
type tagRecord struct {
	Tags    []string          `json:"tags,omitempty"`
	Digests map[string]string `json:"digests,omitempty"`
}

func tagRecordPath(cacheDir, repoURL string) string {
	sum := sha256.Sum256([]byte(NormalizeURL(repoURL)))
	return filepath.Join(cacheDir, recordsDirName, hex.EncodeToString(sum[:])+".json")
}

func readTagRecord(cacheDir, repoURL string) (tagRecord, bool) {
	data, err := os.ReadFile(tagRecordPath(cacheDir, repoURL))
	if err != nil {
		return tagRecord{}, false
	}
	var record tagRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return tagRecord{}, false
	}
	return record, true
}

// updateTagRecord read-merge-writes one repository's record under the package
// key lock: digest entries accumulate across resolves (two Applications
// pinning different tags of one repository must both resolve offline after
// one online run), while the tag list is replaced only when this resolve
// fetched a fresh list. The lock serializes in-process resolves of one
// repository; cross-process merges stay best-effort like every record write.
func updateTagRecord(cacheDir, repoURL string, freshTags []string, replaceTags bool, digests map[string]string) {
	path := tagRecordPath(cacheDir, repoURL)
	ociKeyLock.Lock(path)
	defer ociKeyLock.Unlock(path)
	record, _ := readTagRecord(cacheDir, repoURL)
	if record.Digests == nil {
		record.Digests = make(map[string]string, len(digests))
	}
	maps.Copy(record.Digests, digests)
	if replaceTags {
		record.Tags = freshTags
	}
	writeTagRecord(cacheDir, repoURL, record)
}

// writeTagRecord is best-effort: a failed record write only degrades a later
// offline run to the offline-cache-miss error. The write is atomic
// (temp+rename) so concurrent resolves never expose partial records.
func writeTagRecord(cacheDir, repoURL string, record tagRecord) {
	path := tagRecordPath(cacheDir, repoURL)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, path)
}
