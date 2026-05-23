package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestDefaultGitAcquirerRejectsNetworkWhenNotAllowed(t *testing.T) {
	_, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      "file:///tmp/repo",
		Revision: "main",
	}, GitOptions{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want allow-network error")
	}
	if !strings.Contains(err.Error(), "--allow-network") {
		t.Fatalf("Acquire() error = %q, want --allow-network", err)
	}
}

func TestDefaultGitAcquirerClonesLocalFileRepository(t *testing.T) {
	remote := createGitFixture(t)
	mainHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")

	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      "file://" + filepath.ToSlash(remote.path),
		Revision: "HEAD",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Revision != mainHash.String() {
		t.Fatalf("Revision = %s, want %s", result.Revision, mainHash)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if string(data) != "version: main\n" {
		t.Fatalf("cloned file = %q", data)
	}
}

func TestDefaultGitAcquirerChecksOutBranch(t *testing.T) {
	remote := createGitFixture(t)
	commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")
	checkoutFixtureBranch(t, remote.worktree, "feature")
	featureHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: feature\n")

	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      "file://" + filepath.ToSlash(remote.path),
		Revision: "feature",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Revision != featureHash.String() {
		t.Fatalf("Revision = %s, want %s", result.Revision, featureHash)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if string(data) != "version: feature\n" {
		t.Fatalf("cloned file = %q", data)
	}
}

func TestDefaultGitAcquirerChecksOutFullBranchRef(t *testing.T) {
	remote := createGitFixture(t)
	commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")
	checkoutFixtureBranch(t, remote.worktree, "feature")
	featureHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: full-ref\n")
	if err := remote.worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("master")}); err != nil {
		t.Fatalf("Worktree.Checkout(master) error = %v", err)
	}

	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      "file://" + filepath.ToSlash(remote.path),
		Revision: "refs/heads/feature",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Revision != featureHash.String() {
		t.Fatalf("Revision = %s, want %s", result.Revision, featureHash)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if string(data) != "version: full-ref\n" {
		t.Fatalf("cloned file = %q", data)
	}
}

func TestDefaultGitAcquirerRefreshesCachedHead(t *testing.T) {
	remote := createGitFixture(t)
	firstHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: first\n")
	cacheDir := t.TempDir()
	repoURL := "file://" + filepath.ToSlash(remote.path)

	first, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      repoURL,
		Revision: "HEAD",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     cacheDir,
	})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.Revision != firstHash.String() {
		t.Fatalf("first Revision = %s, want %s", first.Revision, firstHash)
	}

	secondHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: second\n")
	second, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      repoURL,
		Revision: "HEAD",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     cacheDir,
		Refresh:      true,
	})
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if second.Revision != secondHash.String() {
		t.Fatalf("second Revision = %s, want %s", second.Revision, secondHash)
	}
	data, err := os.ReadFile(filepath.Join(second.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read refreshed file: %v", err)
	}
	if string(data) != "version: second\n" {
		t.Fatalf("refreshed file = %q", data)
	}
}

func TestDefaultGitAcquirerChecksOutTag(t *testing.T) {
	remote := createGitFixture(t)
	tagHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: tag\n")
	if _, err := remote.repo.CreateTag("v1.0.0", tagHash, nil); err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}
	commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: later\n")

	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      "file://" + filepath.ToSlash(remote.path),
		Revision: "v1.0.0",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Revision != tagHash.String() {
		t.Fatalf("Revision = %s, want %s", result.Revision, tagHash)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if string(data) != "version: tag\n" {
		t.Fatalf("cloned file = %q", data)
	}
}

func TestDefaultGitAcquirerRedactsURLOnCloneFailure(t *testing.T) {
	_, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      "https://user:secret@example.invalid/repo.git?token=abc#frag",
		Revision: "main",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     t.TempDir(),
	})
	if err == nil {
		t.Fatal("Acquire() error = nil, want clone error")
	}
	for _, leaked := range []string{"user", "secret", "token", "abc", "frag"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Acquire() error = %q, leaked %q", err, leaked)
		}
	}
	if !strings.Contains(err.Error(), "https://example.invalid/repo.git") {
		t.Fatalf("Acquire() error = %q, want redacted URL", err)
	}
}

type gitFixture struct {
	path     string
	repo     *git.Repository
	worktree *git.Worktree
}

func createGitFixture(t *testing.T) gitFixture {
	t.Helper()
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	return gitFixture{path: root, repo: repo, worktree: worktree}
}

func commitFixtureFile(t *testing.T, repo *git.Repository, worktree *git.Worktree, name, content string) plumbing.Hash {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree.Filesystem.Root(), name), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatalf("Worktree.Add() error = %v", err)
	}
	hash, err := worktree.Commit("update "+name, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Unix(1, 0),
		},
	})
	if err != nil {
		t.Fatalf("Worktree.Commit() error = %v", err)
	}
	if _, err := repo.CommitObject(hash); err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	return hash
}

func checkoutFixtureBranch(t *testing.T, worktree *git.Worktree, name string) {
	t.Helper()
	if err := worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	}); err != nil {
		t.Fatalf("Worktree.Checkout(%s) error = %v", name, err)
	}
}
