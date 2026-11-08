package gitref

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// newGitlinkRepo commits a repository with a file and two gitlinks
// (submodules) that are only in the index, as a clone without submodule
// content holds them.
func newGitlinkRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	isolateGitUserConfig(t)
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	writeChangeSetFile(t, filepath.Join(root, "params", "a.yaml"), "a: 1\n")
	run("add", "-A")
	for _, link := range []string{"params/sub", "apps/mod"} {
		run("update-index", "--add", "--cacheinfo", "160000,1111111111111111111111111111111111111111,"+link)
	}
	run("commit", "-q", "-m", "gitlinks")
	return root
}

// TestSnapshotMaterializesGitlinksAsEmptyDirectories pins that a snapshot
// of a Git ref holds each gitlink as an empty directory, as an Argo CD
// checkout does before it fetches submodule content, and that
// SnapshotGitlinks finds them. tree.Files skips gitlinks, so snapshots
// used to leave them out.
func TestSnapshotMaterializesGitlinksAsEmptyDirectories(t *testing.T) {
	root := newGitlinkRepo(t)
	result, err := Snapshot(t.Context(), Request{Repo: root, Ref: "HEAD"})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	t.Cleanup(func() { _ = result.Cleanup() })
	for _, link := range []string{"params/sub", "apps/mod"} {
		info, err := os.Lstat(filepath.Join(result.Path, filepath.FromSlash(link)))
		if err != nil || !info.IsDir() {
			t.Fatalf("snapshot %s: %v, want an empty directory", link, err)
		}
	}
	got := SnapshotGitlinks(result.Path)
	slices.Sort(got)
	if want := []string{"apps/mod", "params/sub"}; !slices.Equal(got, want) {
		t.Fatalf("SnapshotGitlinks() = %q, want %q", got, want)
	}
}

// TestIndexGitlinksReadsTheIndex pins that IndexGitlinks lists the gitlinks
// of the index, relative to the root it is given, with the git CLI and
// with go-git.
func TestIndexGitlinksReadsTheIndex(t *testing.T) {
	root := newGitlinkRepo(t)
	filterModes(t, func(t *testing.T, _ bool) {
		got := IndexGitlinks(t.Context(), root)
		slices.Sort(got)
		if want := []string{"apps/mod", "params/sub"}; !slices.Equal(got, want) {
			t.Fatalf("IndexGitlinks(root) = %q, want %q", got, want)
		}
		if got := IndexGitlinks(t.Context(), filepath.Join(root, "params")); !slices.Equal(got, []string{"sub"}) {
			t.Fatalf("IndexGitlinks(params) = %q, want sub", got)
		}
	})
}
