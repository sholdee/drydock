package gitref

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestChangedPathsBetweenRefsReportsTrackedAddsModifiesAndDeletes(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "value: old\n")
	commitSnapshotFile(t, repo, wt, "apps/deleted/cm.yaml", "value: deleted\n")
	base := commitSnapshotFile(t, repo, wt, "apps/same/cm.yaml", "value: same\n")

	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "demo", "cm.yaml"), "value: new\n")
	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "added", "cm.yaml"), "value: added\n")
	if err := os.Remove(filepath.Join(repoPath, "apps", "deleted", "cm.yaml")); err != nil {
		t.Fatalf("Remove(deleted) error = %v", err)
	}
	head := commitAllSnapshotFiles(t, wt, "feature")

	got, err := ChangedPathsBetweenRefs(t.Context(), repoPath, base.String(), head)
	if err != nil {
		t.Fatalf("ChangedPathsBetweenRefs() error = %v", err)
	}

	want := []string{
		"apps/added/cm.yaml",
		"apps/deleted/cm.yaml",
		"apps/demo/cm.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedPathsBetweenRefs() = %#v, want %#v", got, want)
	}
}

func TestChangedPathsFromRefToWorktreeIgnoresUntrackedFiles(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	writeChangedPathFileForTest(t, filepath.Join(repoPath, ".gitignore"), "ignored/\n")
	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "value: old\n")
	base := commitAllSnapshotFiles(t, wt, "baseline")

	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "demo", "cm.yaml"), "value: new\n")
	if _, err := wt.Add("apps/demo/cm.yaml"); err != nil {
		t.Fatalf("Add(apps/demo/cm.yaml) error = %v", err)
	}
	head := commitAllSnapshotFiles(t, wt, "committed change")

	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "tracked-worktree", "cm.yaml"), "value: tracked\n")
	if _, err := wt.Add("apps/tracked-worktree/cm.yaml"); err != nil {
		t.Fatalf("Add(tracked-worktree) error = %v", err)
	}
	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "tracked-worktree", "cm.yaml"), "value: tracked working\n")
	writeChangedPathFileForTest(t, filepath.Join(repoPath, "ignored", "noise.yaml"), "ignored\n")
	writeChangedPathFileForTest(t, filepath.Join(repoPath, "scratch.yaml"), "untracked\n")

	got, err := ChangedPathsFromRefToWorktree(t.Context(), repoPath, base)
	if err != nil {
		t.Fatalf("ChangedPathsFromRefToWorktree() error = %v", err)
	}

	want := []string{
		"apps/demo/cm.yaml",
		"apps/tracked-worktree/cm.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedPathsFromRefToWorktree() = %#v, want %#v (head=%s)", got, want, head)
	}
}

func TestChangedPathsFromRefToWorktreeReportsStagedOnlyChanges(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	base := commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "value: old\n")

	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "demo", "cm.yaml"), "value: staged\n")
	if _, err := wt.Add("apps/demo/cm.yaml"); err != nil {
		t.Fatalf("Add(apps/demo/cm.yaml) error = %v", err)
	}
	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "demo", "cm.yaml"), "value: old\n")

	got, err := ChangedPathsFromRefToWorktree(t.Context(), repoPath, base.String())
	if err != nil {
		t.Fatalf("ChangedPathsFromRefToWorktree() error = %v", err)
	}

	want := []string{"apps/demo/cm.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedPathsFromRefToWorktree() = %#v, want %#v", got, want)
	}
}

func TestChangedPathsFromRefToWorktreeReportsStagedDeletionWithRestoredFile(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	base := commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "value: old\n")

	if _, err := wt.Remove("apps/demo/cm.yaml"); err != nil {
		t.Fatalf("Remove(apps/demo/cm.yaml) error = %v", err)
	}
	writeChangedPathFileForTest(t, filepath.Join(repoPath, "apps", "demo", "cm.yaml"), "value: old\n")

	got, err := ChangedPathsFromRefToWorktree(t.Context(), repoPath, base.String())
	if err != nil {
		t.Fatalf("ChangedPathsFromRefToWorktree() error = %v", err)
	}

	want := []string{"apps/demo/cm.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedPathsFromRefToWorktree() = %#v, want %#v", got, want)
	}
}

func TestChangedPathsFromRefToWorktreeReportsManyTrackedWorktreeChanges(t *testing.T) {
	repoPath, _, wt := newSnapshotRepo(t)
	for i := range maxMaterializeTreeWorkers * 2 {
		rel := filepath.Join("apps", "many", fmt.Sprintf("cm-%02d.yaml", i))
		writeChangedPathFileForTest(t, filepath.Join(repoPath, rel), fmt.Sprintf("value: old-%02d\n", i))
	}
	base := commitAllSnapshotFiles(t, wt, "baseline")

	want := make([]string, 0, maxMaterializeTreeWorkers*2)
	for i := range maxMaterializeTreeWorkers * 2 {
		rel := filepath.Join("apps", "many", fmt.Sprintf("cm-%02d.yaml", i))
		writeChangedPathFileForTest(t, filepath.Join(repoPath, rel), fmt.Sprintf("value: new-%02d\n", i))
		want = append(want, filepath.ToSlash(rel))
	}

	got, err := ChangedPathsFromRefToWorktree(t.Context(), repoPath, base)
	if err != nil {
		t.Fatalf("ChangedPathsFromRefToWorktree() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedPathsFromRefToWorktree() = %#v, want %#v", got, want)
	}
}

func writeChangedPathFileForTest(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	writeSnapshotFileForTest(t, path, body)
}

func commitAllSnapshotFiles(t *testing.T, wt *git.Worktree, message string) string {
	t.Helper()
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("AddWithOptions() error = %v", err)
	}
	hash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Commit(%s) error = %v", message, err)
	}
	return hash.String()
}
