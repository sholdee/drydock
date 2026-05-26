package remote

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
	cachepkg "github.com/sholdee/drydock/internal/cache"
)

func TestNormalizeGitRepoCacheURLDoesNotDoubleAppendGitSuffix(t *testing.T) {
	got, err := NormalizeGitRepoCacheURL("https://github.com/example/repo.git")
	if err != nil {
		t.Fatalf("NormalizeGitRepoCacheURL() error = %v", err)
	}
	if strings.Contains(got, ".git.git") {
		t.Fatalf("NormalizeGitRepoCacheURL() = %q, must not contain .git.git", got)
	}
}

func TestDefaultAcquirerClonesGitRemoteIntoRemoteCache(t *testing.T) {
	remote := createRemoteGitFixture(t)
	hash := commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")
	cacheDir := t.TempDir()

	result, err := DefaultAcquirer{}.Acquire(context.Background(), Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(remote.path),
		Revision: "HEAD",
	}, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.FromCache {
		t.Fatal("FromCache = true, want false")
	}
	if result.Revision != hash.String() {
		t.Fatalf("Revision = %s, want %s", result.Revision, hash)
	}
	if filepath.Base(result.Path) != "repo" {
		t.Fatalf("Path = %q, want repo directory", result.Path)
	}
	if got, want := filepath.Dir(filepath.Dir(result.Path)), cacheDir; got != want {
		t.Fatalf("cache root = %q, want %q", got, want)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if string(data) != "version: main\n" {
		t.Fatalf("cloned file = %q", data)
	}
}

func TestDefaultRemoteAcquirerWritesGitMetadata(t *testing.T) {
	remote := createRemoteGitFixture(t)
	commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")
	request := Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(remote.path),
		Revision: "HEAD",
	}
	result, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	key, err := NewCacheKey(request)
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	metadata, err := cachepkg.ReadMetadata(filepath.Dir(result.Path), cachepkg.SourceRemote, "git-repo", key)
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	if metadata == nil {
		t.Fatal("metadata = nil, want metadata")
	}
	if metadata.Target != request.RepoURL {
		t.Fatalf("Target = %q, want %q", metadata.Target, request.RepoURL)
	}
	if metadata.Revision != result.Revision {
		t.Fatalf("Revision = %q, want %q", metadata.Revision, result.Revision)
	}
}

func TestDefaultRemoteAcquirerWritesGitMetadataOnCacheHit(t *testing.T) {
	remote := createRemoteGitFixture(t)
	hash := commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")
	request := Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(remote.path),
		Revision: hash.String(),
	}
	cacheDir := t.TempDir()
	first, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	metadataPath := cachepkg.MetadataPath(filepath.Dir(first.Path))
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove(metadata) error = %v", err)
	}

	second, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if !second.FromCache {
		t.Fatal("second FromCache = false, want true")
	}
	if _, err := os.Stat(metadataPath); err != nil {
		t.Fatalf("metadata was not rewritten on cache hit: %v", err)
	}
}

func TestDefaultAcquirerUsesCachedGitRemoteWhenOffline(t *testing.T) {
	remote := createRemoteGitFixture(t)
	hash := commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: cached\n")
	cacheDir := t.TempDir()
	request := Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(remote.path),
		Revision: "HEAD",
	}

	first, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if err := os.Rename(remote.path, remote.path+"-removed"); err != nil {
		t.Fatalf("rename remote fixture out of the way: %v", err)
	}

	second, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true})
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	if !second.FromCache {
		t.Fatal("offline FromCache = false, want true")
	}
	if second.Path != first.Path {
		t.Fatalf("offline Path = %q, want %q", second.Path, first.Path)
	}
	if second.Revision != hash.String() {
		t.Fatalf("offline Revision = %s, want %s", second.Revision, hash)
	}

	_, err = DefaultAcquirer{}.Acquire(context.Background(), Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "missing")),
		Revision: "HEAD",
	}, Options{CacheDir: cacheDir, Offline: true})
	if err == nil || !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("offline miss error = %v, want offline cache miss", err)
	}
}

func TestDefaultAcquirerDoesNotFetchCachedGitRemoteWithoutRefresh(t *testing.T) {
	remote := createRemoteGitFixture(t)
	commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")
	cacheDir := t.TempDir()
	request := Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(remote.path),
		Revision: "missing-revision",
	}
	key, err := NewCacheKey(request)
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	cachePath := filepath.Join(cacheDir, key, "repo")
	if _, err := git.PlainClone(cachePath, false, &git.CloneOptions{
		URL: "file://" + filepath.ToSlash(remote.path),
	}); err != nil {
		t.Fatalf("PlainClone() error = %v", err)
	}
	if err := os.Rename(remote.path, remote.path+"-removed"); err != nil {
		t.Fatalf("rename remote fixture out of the way: %v", err)
	}

	_, err = DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err == nil {
		t.Fatal("Acquire() error = nil, want cached checkout error without fetch")
	}
	if !strings.Contains(err.Error(), "checkout cached remote Git repository") {
		t.Fatalf("Acquire() error = %v, want cached checkout error", err)
	}
}

func TestDefaultAcquirerUsesOfflineGitCacheWithoutCredentials(t *testing.T) {
	remote := createRemoteGitFixture(t)
	hash := commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: cached\n")
	cacheDir := t.TempDir()
	request := Request{
		Kind:     RequestGitRepo,
		RepoURL:  "ssh://example.test/org/repo.git",
		Revision: "HEAD",
	}
	key, err := NewCacheKey(request)
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	cachePath := filepath.Join(cacheDir, key, "repo")
	if _, err := git.PlainClone(cachePath, false, &git.CloneOptions{
		URL: "file://" + filepath.ToSlash(remote.path),
	}); err != nil {
		t.Fatalf("PlainClone() error = %v", err)
	}

	result, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true})
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	if !result.FromCache {
		t.Fatal("FromCache = false, want true")
	}
	if result.Revision != hash.String() {
		t.Fatalf("Revision = %s, want %s", result.Revision, hash)
	}
}

func TestDefaultAcquirerRefreshesGitRemoteWhenRequested(t *testing.T) {
	remote := createRemoteGitFixture(t)
	firstHash := commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: first\n")
	cacheDir := t.TempDir()
	request := Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(remote.path),
		Revision: "HEAD",
	}

	first, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.Revision != firstHash.String() {
		t.Fatalf("first Revision = %s, want %s", first.Revision, firstHash)
	}

	secondHash := commitRemoteGitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: second\n")
	cached, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("cached Acquire() error = %v", err)
	}
	if cached.Revision != firstHash.String() {
		t.Fatalf("cached Revision = %s, want %s", cached.Revision, firstHash)
	}
	if !cached.FromCache {
		t.Fatal("cached FromCache = false, want true")
	}

	refreshed, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Refresh: true})
	if err != nil {
		t.Fatalf("refresh Acquire() error = %v", err)
	}
	if refreshed.FromCache {
		t.Fatal("refresh FromCache = true, want false")
	}
	if refreshed.Revision != secondHash.String() {
		t.Fatalf("refresh Revision = %s, want %s", refreshed.Revision, secondHash)
	}
	data, err := os.ReadFile(filepath.Join(refreshed.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read refreshed file: %v", err)
	}
	if string(data) != "version: second\n" {
		t.Fatalf("refreshed file = %q", data)
	}
}

func TestDefaultAcquirerRejectsGitRemoteCacheInsideForbiddenRoot(t *testing.T) {
	repoRoot := t.TempDir()
	cacheDir := filepath.Join(repoRoot, ".drydock", "remote-cache")

	_, err := DefaultAcquirer{}.Acquire(context.Background(), Request{
		Kind:     RequestGitRepo,
		RepoURL:  "file://" + filepath.ToSlash(t.TempDir()),
		Revision: "HEAD",
	}, Options{CacheDir: cacheDir, ForbiddenRoots: []string{repoRoot}})
	if err == nil || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Acquire() error = %v, want cache containment error", err)
	}
}

func TestDefaultAcquirerRedactsGitRemoteCredentialErrors(t *testing.T) {
	creds := GitCredentials{
		Username:      "user",
		Password:      "secret-password",
		BearerToken:   "secret-token",
		SSHPrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key\n-----END OPENSSH PRIVATE KEY-----",
		SSHPassphrase: "secret-passphrase",
	}
	_, err := DefaultAcquirer{}.Acquire(context.Background(), Request{
		Kind:     RequestGitRepo,
		RepoURL:  "https://user:url-secret@example.invalid/repo.git?token=query-secret#frag-secret",
		Revision: "main",
	}, Options{CacheDir: t.TempDir(), GitCredentials: creds})
	if err == nil {
		t.Fatal("Acquire() error = nil, want clone error")
	}
	for _, leaked := range []string{
		"user",
		"url-secret",
		"query-secret",
		"frag-secret",
		creds.Password,
		creds.BearerToken,
		"secret-key",
		creds.SSHPassphrase,
		"OPENSSH PRIVATE KEY",
	} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Acquire() error = %q, leaked %q", err, leaked)
		}
	}
}

func TestRedactGitRepoURLStripsSCPQueryAndFragment(t *testing.T) {
	got := RedactGitRepoURL("git@github.com:org/repo.git?token=secret#frag")
	if strings.Contains(got, "token") || strings.Contains(got, "secret") || strings.Contains(got, "frag") {
		t.Fatalf("RedactGitRepoURL() = %q, leaked query or fragment", got)
	}
	if got != "github.com:org/repo.git" {
		t.Fatalf("RedactGitRepoURL() = %q, want github.com:org/repo.git", got)
	}
}

type remoteGitFixture struct {
	path     string
	repo     *git.Repository
	worktree *git.Worktree
}

func createRemoteGitFixture(t *testing.T) remoteGitFixture {
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
	return remoteGitFixture{path: root, repo: repo, worktree: worktree}
}

func commitRemoteGitFixtureFile(t *testing.T, repo *git.Repository, worktree *git.Worktree, name, content string) plumbing.Hash {
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
