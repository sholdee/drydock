package gitref

import (
	"path/filepath"
	"testing"
)

func TestHeadRevision(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	if got := HeadRevision(root); got != "" {
		t.Fatalf("HeadRevision(no commit yet) = %q, want empty", got)
	}
	hash := commitSnapshotFile(t, repo, wt, "dir/file.txt", "content\n")
	if got := HeadRevision(root); got != hash.String() {
		t.Fatalf("HeadRevision(top) = %q, want %q", got, hash.String())
	}
	if got := HeadRevision(filepath.Join(root, "dir")); got != "" {
		t.Fatalf("HeadRevision(subdirectory) = %q, want empty", got)
	}
	if got := HeadRevision(t.TempDir()); got != "" {
		t.Fatalf("HeadRevision(no repository) = %q, want empty", got)
	}
	linked := createLinkedSnapshotWorktree(t, root, "master")
	if got := HeadRevision(linked); got != hash.String() {
		t.Fatalf("HeadRevision(linked worktree) = %q, want %q", got, hash.String())
	}
}

func TestPinnedRevisionReturnsBeforeOpeningForNonHexRevisions(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, ok, _ := PinnedRevision(missing, "main", ""); ok {
		t.Fatal("PinnedRevision(main) ok = true, want false for a branch name")
	}
	const upper = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	single, multi, ok, err := PinnedRevision(missing, upper, "")
	if err != nil || !ok || single != "abcdef0123456789abcdef0123456789abcdef01" || multi != upper {
		t.Fatalf("PinnedRevision(full SHA, no repository) = %q, %q, %t", single, multi, ok)
	}
}
