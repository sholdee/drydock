package gitref

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestUntrackedIgnoredFilterSkipsOnlyIgnoredUntrackedPaths pins which
// worktree paths the out-of-bounds symlink check leaves out: a path that Git
// ignores and does not track. A tracked path is kept even when an ignore
// rule matches it, and so is a directory that holds one.
func TestUntrackedIgnoredFilterSkipsOnlyIgnoredUntrackedPaths(t *testing.T) {
	root, _, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "ignored/\n*.tmp\nforced/\n")
	writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
	writeChangeSetFile(t, filepath.Join(root, "forced", "kept.tmp"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	if _, err := wt.Add("forced/kept.tmp"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	filter, err := OpenUntrackedIgnoredFilter(root)
	if err != nil || filter == nil {
		t.Fatalf("OpenUntrackedIgnoredFilter() = %v, %v", filter, err)
	}
	for _, tc := range []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{rel: "apps", isDir: true},
		{rel: "apps/cm.yaml"},
		{rel: "apps/new.yaml"},
		{rel: "ignored", isDir: true, want: true},
		{rel: "apps/scratch.tmp", want: true},
		{rel: "forced", isDir: true},
		{rel: "forced/kept.tmp"},
		{rel: "forced/other.tmp", want: true},
	} {
		if got := filter.Skip(tc.rel, tc.isDir); got != tc.want {
			t.Errorf("Skip(%q, %t) = %t, want %t", tc.rel, tc.isDir, got, tc.want)
		}
	}
}

func TestUntrackedIgnoredFilterSkipsNothingOutsideAWorktree(t *testing.T) {
	root := t.TempDir()
	writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "*\n")
	filter, err := OpenUntrackedIgnoredFilter(root)
	if err != nil {
		t.Fatalf("OpenUntrackedIgnoredFilter() error = %v", err)
	}
	if filter.Skip("anything", false) || filter.Skip("dir", true) {
		t.Fatal("the filter of a directory that is not a worktree skipped a path")
	}
}

// TestUntrackedIgnoredFilterNeverSkipsSubmodulePaths pins that paths at or
// under a gitlink are kept. The parent index does not list the files of a
// submodule, but Argo CD checks out submodules, so a tracked submodule file
// that a parent ignore rule matches is in every checkout.
func TestUntrackedIgnoredFilterNeverSkipsSubmodulePaths(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "result\nsub/\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	commitSubmoduleEntryForTest(t, repo, wt, "sub")
	filter, err := OpenUntrackedIgnoredFilter(root)
	if err != nil {
		t.Fatalf("OpenUntrackedIgnoredFilter() error = %v", err)
	}
	for _, tc := range []struct {
		rel   string
		isDir bool
	}{
		{rel: "sub", isDir: true},
		{rel: "sub/result"},
		{rel: "sub/deep/result"},
	} {
		if filter.Skip(tc.rel, tc.isDir) {
			t.Errorf("Skip(%q, %t) = true, want a submodule path kept", tc.rel, tc.isDir)
		}
	}
	if !filter.Skip("result", false) {
		t.Error("Skip(result) = false, want the ignored parent path skipped")
	}
}

// TestUntrackedIgnoredFilterReadsTheDefaultUserIgnoreFile pins that Git's
// default user ignore file applies when core.excludesFile is not set, and
// that a configured core.excludesFile replaces it.
func TestUntrackedIgnoredFilterReadsTheDefaultUserIgnoreFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeChangeSetFile(t, filepath.Join(xdg, "git", "ignore"), "result\n")
	root, _, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(root, "apps", "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")

	filter, err := OpenUntrackedIgnoredFilter(root)
	if err != nil {
		t.Fatal(err)
	}
	if !filter.Skip("result", false) {
		t.Fatal("Skip(result) = false, want the default user ignore file read")
	}

	writeChangeSetFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesFile = ~/custom-ignore\n")
	writeChangeSetFile(t, filepath.Join(home, "custom-ignore"), ".venv/\n")
	filter, err = OpenUntrackedIgnoredFilter(root)
	if err != nil {
		t.Fatal(err)
	}
	if filter.Skip("result", false) || !filter.Skip(".venv", true) {
		t.Fatal("core.excludesFile did not replace the default user ignore file")
	}
}

// TestUntrackedIgnoredFilterReadsInfoExcludeOfALinkedWorktree pins that a
// linked worktree, whose .git is a file, reads info/exclude of the common
// Git directory.
func TestUntrackedIgnoredFilterReadsInfoExcludeOfALinkedWorktree(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	main, _, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(main, "apps", "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	writeChangeSetFile(t, filepath.Join(main, ".git", "info", "exclude"), "local-only/\n")
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command(gitBin, "-C", main, "worktree", "add", "-q", linked).CombinedOutput(); err != nil {
		t.Skipf("git worktree add: %v: %s", err, out)
	}
	filter, err := OpenUntrackedIgnoredFilter(linked)
	if err != nil {
		t.Fatalf("OpenUntrackedIgnoredFilter() error = %v", err)
	}
	if !filter.Skip("local-only", true) {
		t.Fatal("Skip(local-only) = false, want info/exclude of the common Git directory read")
	}
}
