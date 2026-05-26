package gitref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestSnapshotLocalRefMaterializesBranchTagAndSHA(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	mainHash := commitSnapshotFile(t, repo, wt, "app.yaml", "value: main\n")
	createSnapshotBranch(t, wt, "feature")
	featureHash := commitSnapshotFile(t, repo, wt, "app.yaml", "value: feature\n")
	createSnapshotTag(t, repo, featureHash, "v1.0.0")

	tests := []struct {
		name    string
		ref     string
		want    string
		wantSHA plumbing.Hash
	}{
		{name: "main branch", ref: "master", want: "value: main\n", wantSHA: mainHash},
		{name: "feature branch", ref: "feature", want: "value: feature\n", wantSHA: featureHash},
		{name: "tag", ref: "v1.0.0", want: "value: feature\n", wantSHA: featureHash},
		{name: "sha", ref: featureHash.String(), want: "value: feature\n", wantSHA: featureHash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: tt.ref, ForbiddenRoots: []string{repoPath}})
			if err != nil {
				t.Fatalf("Snapshot() error = %v", err)
			}
			defer result.Cleanup()

			body, err := os.ReadFile(filepath.Join(result.Path, "app.yaml"))
			if err != nil {
				t.Fatalf("ReadFile(snapshot app.yaml) error = %v", err)
			}
			if string(body) != tt.want {
				t.Fatalf("snapshot body = %q, want %q", string(body), tt.want)
			}
			if result.Revision != tt.wantSHA.String() {
				t.Fatalf("Revision = %q, want %q", result.Revision, tt.wantSHA.String())
			}
			assertSnapshotOutsideRepo(t, result.Path, repoPath)
		})
	}
}

func TestSnapshotCleanupRemovesTemporaryDirectory(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "app.yaml", "value: main\n")

	result, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "HEAD", ForbiddenRoots: []string{repoPath}})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("snapshot path stat before cleanup error = %v", err)
	}
	if err := result.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("snapshot path still exists after cleanup: err=%v", err)
	}
}

func TestSnapshotLocalRefResolvesRemoteTrackingBranch(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	hash := commitSnapshotFile(t, repo, wt, "app.yaml", "value: remote\n")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewRemoteReferenceName("origin", "main"), hash)); err != nil {
		t.Fatalf("SetReference(origin/main) error = %v", err)
	}

	result, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "origin/main", ForbiddenRoots: []string{repoPath}})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	defer result.Cleanup()

	body, err := os.ReadFile(filepath.Join(result.Path, "app.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(snapshot app.yaml) error = %v", err)
	}
	if string(body) != "value: remote\n" {
		t.Fatalf("snapshot body = %q, want remote branch content", string(body))
	}
}

func TestSnapshotLocalRefPreservesExecutableMode(t *testing.T) {
	repoPath, _, wt := newSnapshotRepo(t)
	root := wt.Filesystem.Root()
	if err := os.WriteFile(filepath.Join(root, "hook.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(hook) error = %v", err)
	}
	if _, err := wt.Add("hook.sh"); err != nil {
		t.Fatalf("Add(hook) error = %v", err)
	}
	if _, err := wt.Commit("add executable", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	result, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "HEAD", ForbiddenRoots: []string{repoPath}})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	defer result.Cleanup()

	info, err := os.Stat(filepath.Join(result.Path, "hook.sh"))
	if err != nil {
		t.Fatalf("Stat(snapshot hook) error = %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("snapshot hook mode = %v, want executable bits", info.Mode().Perm())
	}
}

func TestSnapshotLocalRefMaterializesSafeSymlink(t *testing.T) {
	repoPath, _, wt := newSnapshotRepo(t)
	root := wt.Filesystem.Root()
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := wt.Add("target.txt"); err != nil {
		t.Fatalf("Add(target) error = %v", err)
	}
	if _, err := wt.Add("link.txt"); err != nil {
		t.Fatalf("Add(link) error = %v", err)
	}
	if _, err := wt.Commit("add symlink", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	result, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "HEAD", ForbiddenRoots: []string{repoPath}})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	defer result.Cleanup()

	target, err := os.Readlink(filepath.Join(result.Path, "link.txt"))
	if err != nil {
		t.Fatalf("Readlink(snapshot link) error = %v", err)
	}
	if target != "target.txt" {
		t.Fatalf("symlink target = %q, want target.txt", target)
	}
}

func TestSnapshotLocalRefMaterializesSafeParentTraversalSymlink(t *testing.T) {
	repoPath, _, wt := newSnapshotRepo(t)
	root := wt.Filesystem.Root()
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll(nested) error = %v", err)
	}
	if err := os.Symlink("../target.txt", filepath.Join(root, "nested", "link.txt")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := wt.Add("target.txt"); err != nil {
		t.Fatalf("Add(target) error = %v", err)
	}
	if _, err := wt.Add("nested/link.txt"); err != nil {
		t.Fatalf("Add(link) error = %v", err)
	}
	if _, err := wt.Commit("add nested symlink", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	result, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "HEAD", ForbiddenRoots: []string{repoPath}})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	defer result.Cleanup()

	target, err := os.Readlink(filepath.Join(result.Path, "nested", "link.txt"))
	if err != nil {
		t.Fatalf("Readlink(snapshot nested link) error = %v", err)
	}
	if target != "../target.txt" {
		t.Fatalf("symlink target = %q, want ../target.txt", target)
	}
	body, err := os.ReadFile(filepath.Join(result.Path, "nested", "link.txt"))
	if err != nil {
		t.Fatalf("ReadFile(snapshot nested link) error = %v", err)
	}
	if string(body) != "target\n" {
		t.Fatalf("symlink body = %q, want target", string(body))
	}
}

func TestSnapshotRejectsEscapingSymlink(t *testing.T) {
	repoPath, _, wt := newSnapshotRepo(t)
	root := wt.Filesystem.Root()
	if err := os.Symlink("../outside", filepath.Join(root, "escape")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := wt.Add("escape"); err != nil {
		t.Fatalf("Add(escape) error = %v", err)
	}
	if _, err := wt.Commit("add escaping symlink", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	_, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "HEAD", ForbiddenRoots: []string{repoPath}})
	if err == nil {
		t.Fatal("Snapshot() error = nil, want escaping symlink error")
	}
	if !strings.Contains(err.Error(), "symlink target") {
		t.Fatalf("Snapshot() error = %q, want symlink target error", err)
	}
}

func TestSnapshotRejectsAbsoluteSymlink(t *testing.T) {
	repoPath, _, wt := newSnapshotRepo(t)
	root := wt.Filesystem.Root()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(target, filepath.Join(root, "absolute")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := wt.Add("absolute"); err != nil {
		t.Fatalf("Add(absolute) error = %v", err)
	}
	if _, err := wt.Commit("add absolute symlink", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	_, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "HEAD", ForbiddenRoots: []string{repoPath}})
	if err == nil {
		t.Fatal("Snapshot() error = nil, want absolute symlink error")
	}
	if !strings.Contains(err.Error(), "symlink target") {
		t.Fatalf("Snapshot() error = %q, want symlink target error", err)
	}
}

func TestMaterializeTreeRejectsSymlinkPrefixWriteEscape(t *testing.T) {
	_, repo, wt := newSnapshotRepo(t)
	hash := commitSnapshotFile(t, repo, wt, "prefix/nested/file.yaml", "value: unsafe\n")
	commit, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	snapshotRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(snapshotRoot, "prefix")); err != nil {
		t.Fatalf("Symlink(prefix) error = %v", err)
	}

	err = materializeTree(t.Context(), tree, snapshotRoot)
	if err == nil {
		t.Fatal("materializeTree() error = nil, want symlink-prefix escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes snapshot root") {
		t.Fatalf("materializeTree() error = %q, want escape error", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested")); !os.IsNotExist(statErr) {
		t.Fatalf("materialization created directory through symlink prefix: err=%v", statErr)
	}
}

func TestSnapshotRejectsTempDirectoryInsideForbiddenRoot(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "app.yaml", "value: main\n")
	tmpParent := filepath.Join(repoPath, "tmp")
	if err := os.MkdirAll(tmpParent, 0o755); err != nil {
		t.Fatalf("MkdirAll(tmpParent) error = %v", err)
	}
	t.Setenv("TMPDIR", tmpParent)

	_, err := Snapshot(t.Context(), Request{Repo: repoPath, Ref: "HEAD", ForbiddenRoots: []string{repoPath}})
	if err == nil {
		t.Fatal("Snapshot() error = nil, want forbidden temp root error")
	}
	if !strings.Contains(err.Error(), "inside protected root") {
		t.Fatalf("Snapshot() error = %q, want protected root error", err)
	}
	entries, readErr := os.ReadDir(tmpParent)
	if readErr != nil {
		t.Fatalf("ReadDir(tmpParent) error = %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafe temp dir was not cleaned up: %#v", entries)
	}
}

func TestSnapshotRejectsInvalidInputs(t *testing.T) {
	repoPath, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "app.yaml", "value: main\n")

	tests := []struct {
		name string
		req  Request
		want string
	}{
		{name: "missing ref", req: Request{Repo: repoPath, Ref: "missing"}, want: "resolve Git ref"},
		{name: "non repo", req: Request{Repo: t.TempDir(), Ref: "HEAD"}, want: "open Git repository"},
		{name: "remote URL", req: Request{Repo: "https://github.com/example/repo.git", Ref: "main"}, want: "remote repository URLs are not supported"},
		{name: "scp style URL", req: Request{Repo: "git@github.com:example/repo.git", Ref: "main"}, want: "remote repository URLs are not supported"},
		{name: "scp style URL without user", req: Request{Repo: "github.com:example/repo.git", Ref: "main"}, want: "remote repository URLs are not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Snapshot(t.Context(), tt.req)
			if err == nil {
				t.Fatalf("Snapshot() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Snapshot() error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func newSnapshotRepo(t *testing.T) (string, *git.Repository, *git.Worktree) {
	t.Helper()
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	return root, repo, wt
}

func commitSnapshotFile(t *testing.T, repo *git.Repository, wt *git.Worktree, name, body string) plumbing.Hash {
	t.Helper()
	path := filepath.Join(wt.Filesystem.Root(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := wt.Add(name); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	hash, err := wt.Commit("update "+name, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return hash
}

func createSnapshotBranch(t *testing.T, wt *git.Worktree, name string) {
	t.Helper()
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	}); err != nil {
		t.Fatalf("Checkout(create %s) error = %v", name, err)
	}
}

func createSnapshotTag(t *testing.T, repo *git.Repository, hash plumbing.Hash, name string) {
	t.Helper()
	if _, err := repo.CreateTag(name, hash, nil); err != nil {
		t.Fatalf("CreateTag(%s) error = %v", name, err)
	}
}

func assertSnapshotOutsideRepo(t *testing.T, snapshotPath, repoPath string) {
	t.Helper()
	snapshotPath, err := filepath.EvalSymlinks(snapshotPath)
	if err != nil {
		t.Fatalf("EvalSymlinks(snapshot) error = %v", err)
	}
	repoPath, err = filepath.EvalSymlinks(repoPath)
	if err != nil {
		t.Fatalf("EvalSymlinks(repo) error = %v", err)
	}
	if snapshotPath == repoPath || strings.HasPrefix(snapshotPath, repoPath+string(os.PathSeparator)) {
		t.Fatalf("snapshot path %q must not be inside repo %q", snapshotPath, repoPath)
	}
}
