package gitref

import (
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
