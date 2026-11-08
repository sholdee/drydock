package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeClone writes a store clone directory with one file and sets every
// time below it to age ago.
func writeClone(t *testing.T, path string, age time.Duration) {
	t.Helper()
	file := filepath.Join(path, "objects", "pack", "pack-1.pack")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("pack"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	for _, p := range []string{file, filepath.Dir(file), filepath.Dir(filepath.Dir(file)), path} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPruneRemovesOnlyStaleStoreClones pins that cache prune removes the
// temporary submodule store clones that a killed process left, in the Git
// and remote caches, once nothing changed in them for staleCloneAge, and
// keeps a clone that another process may still be writing.
func TestPruneRemovesOnlyStaleStoreClones(t *testing.T) {
	gitRoot, remoteRoot := t.TempDir(), t.TempDir()
	key := strings.Repeat("a", 64)
	stale := filepath.Join(gitRoot, key+".clone-111")
	fresh := filepath.Join(gitRoot, key+".clone-222")
	remoteStale := filepath.Join(remoteRoot, key, "repo.clone-333")
	writeClone(t, stale, 2*time.Hour)
	writeClone(t, fresh, time.Minute)
	writeClone(t, remoteStale, 2*time.Hour)
	// A fresh file deep inside an old directory keeps the clone: a live
	// clone writes below its top directory.
	busy := filepath.Join(gitRoot, key+".clone-444")
	writeClone(t, busy, 2*time.Hour)
	if err := os.WriteFile(filepath.Join(busy, "objects", "pack", "pack-2.pack"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := OperationOptions{GitCacheDir: gitRoot, RemoteCacheDir: remoteRoot, Sources: []Source{SourceGit, SourceRemote}, OlderThan: 24 * time.Hour, Yes: true}

	dry := opts
	dry.DryRun = true
	result, err := Prune(dry)
	if err != nil || result.StaleClonesRemoved != 2 {
		t.Fatalf("dry Prune() = %d stale clones, %v; want 2", result.StaleClonesRemoved, err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("dry run removed %s: %v", stale, err)
	}
	result, err = Prune(opts)
	if err != nil || result.StaleClonesRemoved != 2 {
		t.Fatalf("Prune() = %d stale clones, %v; want 2", result.StaleClonesRemoved, err)
	}
	for path, kept := range map[string]bool{stale: false, remoteStale: false, fresh: true, busy: true} {
		if _, err := os.Stat(path); (err == nil) != kept {
			t.Fatalf("stat %s = %v, want kept %t", path, err, kept)
		}
	}
}

// TestDeleteRemovesStaleClonesOfTheDeletedGitEntry pins that deleting a
// Git entry also removes its stale store clones, and no other entry's.
func TestDeleteRemovesStaleClonesOfTheDeletedGitEntry(t *testing.T) {
	gitRoot := t.TempDir()
	key, other := strings.Repeat("b", 64), strings.Repeat("c", 64)
	mine := filepath.Join(gitRoot, key+".clone-1")
	theirs := filepath.Join(gitRoot, other+".clone-2")
	writeClone(t, mine, 2*time.Hour)
	writeClone(t, theirs, 2*time.Hour)
	result, err := Delete(OperationOptions{GitCacheDir: gitRoot, Sources: []Source{SourceGit}, Source: SourceGit, Key: key, Yes: true})
	if err != nil || result.StaleClonesRemoved != 1 {
		t.Fatalf("Delete() = %d stale clones, %v; want 1", result.StaleClonesRemoved, err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want it removed", mine, err)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("stat %s = %v, want it kept", theirs, err)
	}
}
