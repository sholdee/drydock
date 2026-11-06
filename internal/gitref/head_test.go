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
