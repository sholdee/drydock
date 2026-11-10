package gitlfs

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// keyedLocks hands out one mutex for each key. Entries are never removed:
// a process fills a bounded number of objects.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// objectLocks makes the Fills of a process download each object once.
// Processes that share a cache need no lock: each writes its own
// temporary file, and a rename publishes only a verified object.
var objectLocks keyedLocks

func (l *keyedLocks) lock(dir, oid string) func() {
	key := filepath.Clean(dir) + "\x00" + oid
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*sync.Mutex{}
	}
	entry, ok := l.locks[key]
	if !ok {
		entry = &sync.Mutex{}
		l.locks[key] = entry
	}
	l.mu.Unlock()
	entry.Lock()
	return entry.Unlock
}

// has reports whether the store holds a regular file for want with its
// size and SHA-256. It removes a file that does not match, so a download
// replaces it.
func (s store) has(want objectRef) bool {
	path := s.objectPath(want.OID)
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode().IsRegular() && info.Size() == want.Size && fileMatches(path, want.OID) {
		return true
	}
	_ = os.Remove(path)
	return false
}

func fileMatches(path, oid string) bool {
	in, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = in.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, in); err != nil {
		return false
	}
	return hex.EncodeToString(sum.Sum(nil)) == oid
}
