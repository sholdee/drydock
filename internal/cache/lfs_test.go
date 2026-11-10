package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeLFSObject writes content into the Git LFS store of the Git cache at
// gitRoot with the modification time used, and returns its oid.
func writeLFSObject(t *testing.T, gitRoot, content string, used time.Time) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(sum[:])
	path := filepath.Join(gitRoot, "lfs", "objects", oid[0:2], oid[2:4], oid)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, used, used); err != nil {
		t.Fatal(err)
	}
	return oid
}

func lfsKeys(entries []Entry) map[string]bool {
	out := map[string]bool{}
	for _, entry := range entries {
		if entry.Source == SourceGit && entry.Kind == KindLFS {
			out[entry.Key] = true
		}
	}
	return out
}

// TestListShowsGitLFSObjects pins that cache list shows each object of the
// Git LFS store as a git entry of kind lfs, with its size and last use,
// and skips files that are no objects.
func TestListShowsGitLFSObjects(t *testing.T) {
	gitRoot := filepath.Join(t.TempDir(), "git")
	used := time.Now().Add(-time.Hour).Truncate(time.Second)
	oid := writeLFSObject(t, gitRoot, "object one\n", used)
	misplaced := filepath.Join(gitRoot, "lfs", "objects", "00", "00", oid)
	if err := os.MkdirAll(filepath.Dir(misplaced), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(misplaced, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := List(Options{GitCacheDir: gitRoot, Sources: []Source{SourceGit}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Key != oid || entries[0].Kind != KindLFS || entries[0].SizeBytes != int64(len("object one\n")) || !entries[0].ModifiedAt.Equal(used) {
		t.Fatalf("List() = %+v, want the one object", entries)
	}
}

// TestPruneEvictsGitLFSObjects pins that prune --older-than removes old
// objects, that --max-size counts their bytes and evicts the least
// recently used first, and that prune removes stale temporary downloads.
func TestPruneEvictsGitLFSObjects(t *testing.T) {
	gitRoot := filepath.Join(t.TempDir(), "git")
	now := time.Now()
	old := writeLFSObject(t, gitRoot, "old object\n", now.Add(-72*time.Hour))
	older := writeLFSObject(t, gitRoot, "less recent\n", now.Add(-2*time.Hour))
	recent := writeLFSObject(t, gitRoot, "most recent\n", now.Add(-time.Minute))
	tmp := filepath.Join(gitRoot, "lfs", "tmp", recent+".123.tmp")
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-2 * staleCloneAge)
	if err := os.Chtimes(tmp, stale, stale); err != nil {
		t.Fatal(err)
	}
	options := Options{GitCacheDir: gitRoot, Sources: []Source{SourceGit}}
	result, err := Prune(OperationOptions{Options: options, OlderThan: 24 * time.Hour, Yes: true})
	if err != nil || !lfsKeys(result.Entries)[old] || len(result.Entries) != 1 || result.StaleLFSTempsRemoved != 1 {
		t.Fatalf("Prune(older-than) = %+v, %v; want the old object and the stale download removed", result, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("the stale temporary download is still there")
	}
	result, err = Prune(OperationOptions{Options: options, MaxBytes: int64(len("most recent\n")), Yes: true})
	if err != nil || len(result.Entries) != 1 || !lfsKeys(result.Entries)[older] {
		t.Fatalf("Prune(max-size) = %+v, %v; want the less recently used object evicted", result, err)
	}
	entries, _ := List(options)
	if keys := lfsKeys(entries); len(keys) != 1 || !keys[recent] {
		t.Fatalf("List() after prune = %+v, want the most recent object", entries)
	}
}

// TestDeleteRemovesGitLFSObjects pins that delete --source git --all
// removes the Git LFS store objects too, and --key removes one.
func TestDeleteRemovesGitLFSObjects(t *testing.T) {
	gitRoot := filepath.Join(t.TempDir(), "git")
	first := writeLFSObject(t, gitRoot, "first\n", time.Now())
	writeLFSObject(t, gitRoot, "second\n", time.Now())
	options := Options{GitCacheDir: gitRoot, Sources: []Source{SourceGit}}
	result, err := Delete(OperationOptions{Options: options, Source: SourceGit, Key: first, Yes: true})
	if err != nil || result.RemovedCount != 1 {
		t.Fatalf("Delete(--key) = %+v, %v", result, err)
	}
	result, err = Delete(OperationOptions{Options: options, Source: SourceGit, All: true, Yes: true})
	if err != nil || result.RemovedCount != 1 {
		t.Fatalf("Delete(--all) = %+v, %v", result, err)
	}
	if entries, _ := List(options); len(entries) != 0 {
		t.Fatalf("List() after delete = %+v, want none", entries)
	}
}
