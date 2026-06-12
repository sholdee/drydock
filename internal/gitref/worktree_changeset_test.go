package gitref

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestWorktreeChangeSetCleanIsEmpty(t *testing.T) {
	dir, revision := changeSetFixtureRepo(t)
	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorktreeChangeSet() error = %v", err)
	}
	if result.State != WorktreeStateClean || result.Revision != revision || len(result.DirtyPaths) != 0 {
		t.Fatalf("result = %+v, want clean/%s with no dirty paths", result, revision)
	}
}

func TestWorktreeChangeSetEnumeratesEveryDirtCategory(t *testing.T) {
	dir, revision := changeSetFixtureRepo(t)

	// same-size tracked edit
	writeChangeSetFile(t, filepath.Join(dir, "apps", "alpha", "cm.yaml"), "a: 2\n")
	// deleted tracked file
	if err := os.Remove(filepath.Join(dir, "apps", "beta", "cm.yaml")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// untracked file in a tracked dir
	writeChangeSetFile(t, filepath.Join(dir, "apps", "alpha", "untracked.yaml"), "u: 1\n")
	// file under an entirely untracked dir
	writeChangeSetFile(t, filepath.Join(dir, "newdir", "deep", "x.yaml"), "x: 1\n")
	// ignored file (fixture commits a .gitignore for *.tmp)
	writeChangeSetFile(t, filepath.Join(dir, "apps", "alpha", "scratch.tmp"), "t\n")
	// nested .git directory content
	writeChangeSetFile(t, filepath.Join(dir, "apps", "beta", ".git", "config"), "g\n")

	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorktreeChangeSet() error = %v", err)
	}
	if result.State != WorktreeStateDirty || result.Revision != revision {
		t.Fatalf("result = %+v, want dirty/%s", result, revision)
	}
	want := []string{
		"apps/alpha/cm.yaml",
		"apps/alpha/scratch.tmp",
		"apps/alpha/untracked.yaml",
		"apps/beta/.git",
		"apps/beta/cm.yaml",
		"newdir/deep/x.yaml",
	}
	if !reflect.DeepEqual(result.DirtyPaths, want) {
		t.Fatalf("DirtyPaths = %#v, want %#v (sorted, complete)", result.DirtyPaths, want)
	}
}

func TestWorktreeChangeSetDetectsSymlinkTargetChange(t *testing.T) {
	if isWindowsChangeSetTest() {
		t.Skip("symlinks are unreliable on windows")
	}
	dir, _ := changeSetFixtureRepo(t)
	link := filepath.Join(dir, "apps", "alpha", "link.yaml")
	if err := os.Symlink("cm.yaml", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	commitChangeSetRepo(t, dir) // re-commit so the symlink is tracked
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.Symlink("untracked.yaml", link); err != nil {
		t.Fatalf("relink: %v", err)
	}
	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorktreeChangeSet() error = %v", err)
	}
	if result.State != WorktreeStateDirty {
		t.Fatalf("result = %+v, want dirty (symlink target changed)", result)
	}
}

func TestWorktreeChangeSetDetectsTypeChange(t *testing.T) {
	if isWindowsChangeSetTest() {
		t.Skip("symlinks are unreliable on windows")
	}
	dir, _ := changeSetFixtureRepo(t)
	target := filepath.Join(dir, "apps", "alpha", "cm.yaml")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink("../beta/cm.yaml", target); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorktreeChangeSet() error = %v", err)
	}
	if result.State != WorktreeStateDirty || len(result.DirtyPaths) != 1 || result.DirtyPaths[0] != "apps/alpha/cm.yaml" {
		t.Fatalf("result = %+v, want dirty with exactly apps/alpha/cm.yaml", result)
	}
}

func TestWorktreeChangeSetParentDirReplacedByFile(t *testing.T) {
	dir, _ := changeSetFixtureRepo(t)
	parent := filepath.Join(dir, "apps", "alpha")
	if err := os.RemoveAll(parent); err != nil {
		t.Fatalf("remove parent: %v", err)
	}
	if err := os.WriteFile(parent, []byte("now a file\n"), 0o600); err != nil {
		t.Fatalf("replace with file: %v", err)
	}
	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorktreeChangeSet() error = %v, want dirty enumeration (ENOTDIR must read as changed)", err)
	}
	if result.State != WorktreeStateDirty {
		t.Fatalf("result = %+v, want dirty", result)
	}
	// The vanished tracked file and the replacing file must both be present.
	want := map[string]bool{"apps/alpha/cm.yaml": false, "apps/alpha": false}
	for _, p := range result.DirtyPaths {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, found := range want {
		if !found {
			t.Fatalf("DirtyPaths = %#v, missing %q", result.DirtyPaths, p)
		}
	}
}

// TestWorktreeChangeSetNestedGitDirIsDirt pins the fail-closed divergence:
// nested .git directories are recorded as dirty paths and not descended into,
// because their content is unverifiable repository metadata. WorktreeChangeSet
// must mark the worktree dirty even when standard status tooling would skip
// nested .git directories silently.
func TestWorktreeChangeSetNestedGitDirIsDirt(t *testing.T) {
	dir, _ := changeSetFixtureRepo(t)
	writeChangeSetFile(t, filepath.Join(dir, "apps", "alpha", ".git", "config"), "g\n")
	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil || result.State != WorktreeStateDirty {
		t.Fatalf("WorktreeChangeSet = %+v, %v, want dirty (nested .git is fail-closed dirt)", result, err)
	}
}

func TestWorktreeChangeSetDetectsModeFlip(t *testing.T) {
	if isWindowsChangeSetTest() {
		t.Skip("executable bits are not tracked on windows")
	}
	dir, _ := changeSetFixtureRepo(t)
	target := filepath.Join(dir, "apps", "alpha", "cm.yaml")
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil {
		t.Fatalf("WorktreeChangeSet() error = %v", err)
	}
	if result.State != WorktreeStateDirty || len(result.DirtyPaths) != 1 || result.DirtyPaths[0] != "apps/alpha/cm.yaml" {
		t.Fatalf("result = %+v, want exactly the mode-flipped file", result)
	}
}

func TestWorktreeChangeSetCleanAfterMtimeTouch(t *testing.T) {
	dir, revision := changeSetFixtureRepo(t)
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "apps", "alpha", "cm.yaml"), now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	result, err := WorktreeChangeSet(context.Background(), dir)
	if err != nil || result.State != WorktreeStateClean || result.Revision != revision {
		t.Fatalf("result = %+v, %v, want clean (content identical; no stat-cache heuristics)", result, err)
	}
}

func TestWorktreeChangeSetNonRepositoryIsUnknown(t *testing.T) {
	result, err := WorktreeChangeSet(context.Background(), t.TempDir())
	if err != nil || result.State != WorktreeStateUnknown {
		t.Fatalf("result = %+v, %v, want unknown without error", result, err)
	}
}

func TestWorktreeChangeSetCanceledContext(t *testing.T) {
	dir, _ := changeSetFixtureRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WorktreeChangeSet(ctx, dir); err == nil {
		t.Fatalf("WorktreeChangeSet(canceled) error = nil, want context error")
	}
}

// changeSetFixtureRepo creates a fresh git repository with a baseline commit
// containing apps/alpha/cm.yaml, apps/beta/cm.yaml, and .gitignore (*.tmp).
// Returns the repo root directory and the HEAD revision string.
func changeSetFixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	root, _, wt := newSnapshotRepo(t)
	writeChangeSetFile(t, filepath.Join(root, "apps", "alpha", "cm.yaml"), "a: 1\n")
	writeChangeSetFile(t, filepath.Join(root, "apps", "beta", "cm.yaml"), "b: 1\n")
	writeChangeSetFile(t, filepath.Join(root, ".gitignore"), "*.tmp\n")
	revision := commitAllSnapshotFiles(t, wt, "fixture")
	return root, revision
}

// writeChangeSetFile writes body to path, creating parent directories as needed.
func writeChangeSetFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

// commitChangeSetRepo stages all worktree changes and commits them.
// Used by the symlink-target-change subtest to make the symlink a tracked file.
func commitChangeSetRepo(t *testing.T, dir string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen(%s) error = %v", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("AddWithOptions() error = %v", err)
	}
	if _, err := wt.Commit("re-commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

// isWindowsChangeSetTest reports whether the current platform is Windows, used
// to skip tests that rely on symlinks or executable mode bits.
func isWindowsChangeSetTest() bool {
	return runtime.GOOS == "windows"
}
