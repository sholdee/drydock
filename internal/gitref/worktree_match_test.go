package gitref

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertWorktreeMatch(t *testing.T, input PathDigestInput, want bool, why string) {
	t.Helper()
	match, err := WorktreePathsMatchRevision(context.Background(), input)
	if err != nil || match != want {
		t.Fatalf("match = %v, %v, want %v (%s)", match, err, want, why)
	}
}

func TestWorktreePathsMatchRevision(t *testing.T) {
	dir, _, wt := newSnapshotRepo(t)
	writeChangedPathFileForTest(t, filepath.Join(dir, "manifests", "demo", "cm.yaml"), "a: 1\n")
	writeChangedPathFileForTest(t, filepath.Join(dir, "other", "x.yaml"), "x: 1\n")
	revision := commitAllSnapshotFiles(t, wt, "fixture")
	paths := []PathDigestPath{{Path: "manifests/demo"}, {Path: "manifests/demo/.argocd-source.yaml", Optional: true}}
	input := func() PathDigestInput {
		return PathDigestInput{RepoPath: dir, Revision: revision, Paths: paths}
	}

	t.Run("clean worktree matches", func(t *testing.T) {
		assertWorktreeMatch(t, input(), true, "untouched worktree")
	})

	t.Run("mtime-only touch still matches", func(t *testing.T) {
		now := time.Now()
		if err := os.Chtimes(filepath.Join(dir, "manifests", "demo", "cm.yaml"), now, now); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		assertWorktreeMatch(t, input(), true, "content identical")
	})

	t.Run("edit outside requested paths still matches", func(t *testing.T) {
		writeChangedPathFileForTest(t, filepath.Join(dir, "other", "x.yaml"), "x: 2\n")
		defer writeChangedPathFileForTest(t, filepath.Join(dir, "other", "x.yaml"), "x: 1\n")
		assertWorktreeMatch(t, input(), true, "edit is outside the requested paths")
	})

	t.Run("same-size content edit mismatches", func(t *testing.T) {
		writeChangedPathFileForTest(t, filepath.Join(dir, "manifests", "demo", "cm.yaml"), "a: 2\n")
		defer writeChangedPathFileForTest(t, filepath.Join(dir, "manifests", "demo", "cm.yaml"), "a: 1\n")
		assertWorktreeMatch(t, input(), false, "content differs at identical size")
	})

	t.Run("extra untracked file under requested dir mismatches", func(t *testing.T) {
		extra := filepath.Join(dir, "manifests", "demo", "untracked.yaml")
		writeChangedPathFileForTest(t, extra, "u: 1\n")
		defer func() { _ = os.Remove(extra) }()
		assertWorktreeMatch(t, input(), false, "untracked file inside requested dir")
	})

	t.Run("optional path appearing in worktree mismatches", func(t *testing.T) {
		override := filepath.Join(dir, "manifests", "demo", ".argocd-source.yaml")
		writeChangedPathFileForTest(t, override, "kustomize:\n  namePrefix: p-\n")
		defer func() { _ = os.Remove(override) }()
		assertWorktreeMatch(t, input(), false, "optional path absent from commit but present in worktree")
	})

	t.Run("nested git dir under requested dir mismatches", func(t *testing.T) {
		nested := filepath.Join(dir, "manifests", "demo", ".git", "extra.yaml")
		writeChangedPathFileForTest(t, nested, "u: 1\n")
		defer func() { _ = os.RemoveAll(filepath.Join(dir, "manifests", "demo", ".git")) }()
		assertWorktreeMatch(t, input(), false, "nested .git content is unverifiable")
	})

	t.Run("deleted tracked file mismatches", func(t *testing.T) {
		target := filepath.Join(dir, "manifests", "demo", "cm.yaml")
		if err := os.Remove(target); err != nil {
			t.Fatalf("remove: %v", err)
		}
		defer writeChangedPathFileForTest(t, target, "a: 1\n")
		assertWorktreeMatch(t, input(), false, "tracked file missing from worktree")
	})
}

func TestWorktreePathsMatchRevisionSymlinkFailsClosed(t *testing.T) {
	dir, _, wt := newSnapshotRepo(t)
	writeChangedPathFileForTest(t, filepath.Join(dir, "manifests", "demo", "cm.yaml"), "a: 1\n")
	if err := os.Symlink("cm.yaml", filepath.Join(dir, "manifests", "demo", "link.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	revision := commitAllSnapshotFiles(t, wt, "fixture")
	assertWorktreeMatch(t, PathDigestInput{
		RepoPath: dir, Revision: revision,
		Paths: []PathDigestPath{{Path: "manifests/demo"}},
	}, false, "symlink targets are invisible to the committed digest")
}

func TestWorktreePathsMatchRevisionSubmoduleFailsClosed(t *testing.T) {
	// Fixture: a commit whose tree contains a gitlink (submodule) at
	// "vendor/dep", with the submodule uninitialized in the worktree (the
	// "vendor/dep" directory is present but empty — the typical state of an
	// uninitialized submodule). go-git's FileIter.Next() skips gitlink entries
	// entirely, so the committed-file iteration guard for filemode.Submodule is
	// unreachable; without the fix, committedTreeFilesMatchWorktree sees an
	// empty committed set and match=true, then worktreeDirHasExtraFiles sees no
	// extra files in the empty dep dir and returns extra=false — overall
	// match=true, violating the fail-closed contract.
	//
	// The fix must intercept gitlinks before iteration via
	// rejectSubmodulesInDigestTree so the primitive honors its own contract.
	//
	// Reuses commitSubmoduleEntryForTest from path_digest_test.go (same package).
	dir, repo, wt := newSnapshotRepo(t)
	revision := commitSubmoduleEntryForTest(t, repo, wt, "vendor/dep")

	// Simulate an uninitialized submodule: the directory exists but is empty.
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "dep"), 0o755); err != nil {
		t.Fatalf("MkdirAll(vendor/dep) error = %v", err)
	}

	match, err := WorktreePathsMatchRevision(context.Background(), PathDigestInput{
		RepoPath: dir, Revision: revision.String(),
		Paths: []PathDigestPath{{Path: "vendor"}},
	})
	if err == nil && match {
		t.Fatalf("match = true for a tree containing a gitlink; submodule content is invisible to the committed digest and must fail closed")
	}
}
