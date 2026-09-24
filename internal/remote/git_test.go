package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	cachepkg "github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/gitauth"
	cryptossh "golang.org/x/crypto/ssh"
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

func TestRemoteGitSSHAuthReportsNoUsableIdentity(t *testing.T) {
	useSSHEnvironment(t, syntheticSSHEnvironment(t, t.TempDir()))

	_, _, err := gitAuthMethod(GitCredentials{SSHKnownHostsPath: writeEmptyKnownHostsFile(t)}, "ssh://git@example.com/org/repo.git")
	if err == nil {
		t.Fatal("gitAuthMethod() error = nil, want no-credentials error")
	}
	for _, want := range []string{
		"git SSH",
		"no SSH credentials: --git-ssh-key-file is not set",
		"no ssh-agent",
		"ssh://example.com/org/repo.git",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("gitAuthMethod() error = %q, want %q", err, want)
		}
	}
}

func TestRemoteGitSSHAuthRequiresKnownHostsFile(t *testing.T) {
	useSSHEnvironment(t, syntheticSSHEnvironment(t, t.TempDir()))

	_, _, err := gitAuthMethod(GitCredentials{SSHPrivateKeyPath: writeSSHPrivateKey(t, "")}, "ssh://git@example.com/org/repo.git")
	if err == nil {
		t.Fatal("gitAuthMethod() error = nil, want missing known_hosts error")
	}
	if !strings.Contains(err.Error(), "ssh-keyscan example.com >> ~/.ssh/known_hosts") {
		t.Fatalf("gitAuthMethod() error = %q, want ssh-keyscan hint", err)
	}
}

func TestRemoteGitSSHAuthUsesAmbientIdentityWithoutKeyFile(t *testing.T) {
	home := writeSSHHome(t, "example.com")
	useSSHEnvironment(t, syntheticSSHEnvironment(t, home))

	auth, hasAuth, err := gitAuthMethod(GitCredentials{}, "ssh://git@example.com/org/repo.git")
	if err != nil {
		t.Fatalf("gitAuthMethod() error = %v", err)
	}
	if !hasAuth {
		t.Fatal("hasAuth = false, want true")
	}
	if auth.Name() != "ssh-ambient-identities" {
		t.Fatalf("auth.Name() = %q, want ssh-ambient-identities", auth.Name())
	}
	assertVerifiesHostKeys(t, auth)
}

func TestRemoteGitSSHAuthExplicitKeyVerifiesHostKeys(t *testing.T) {
	home := writeSSHHome(t, "example.com")
	useSSHEnvironment(t, syntheticSSHEnvironment(t, home))

	auth, _, err := gitAuthMethod(GitCredentials{
		SSHPrivateKeyPath: writeSSHPrivateKey(t, ""),
		SSHKnownHostsPath: filepath.Join(home, ".ssh", "known_hosts"),
	}, "ssh://git@example.com/org/repo.git")
	if err != nil {
		t.Fatalf("gitAuthMethod() error = %v", err)
	}
	if _, ok := auth.(*gitssh.PublicKeys); !ok {
		t.Fatalf("auth = %T, want *ssh.PublicKeys", auth)
	}
	assertVerifiesHostKeys(t, auth)
}

func writeSSHPrivateKey(t *testing.T, passphrase string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_ed25519")
	writeSSHPrivateKeyAt(t, path, passphrase)
	return path
}

func writeSSHPrivateKeyAt(t *testing.T, path, passphrase string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = cryptossh.MarshalPrivateKey(privateKey, "test@example.com")
	} else {
		block, err = cryptossh.MarshalPrivateKeyWithPassphrase(privateKey, "test@example.com", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("MarshalPrivateKey() error = %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
}

func writeEmptyKnownHostsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// writeSSHHome builds a synthetic home directory holding an unencrypted
// ~/.ssh/id_ed25519 and a ~/.ssh/known_hosts entry for host.
func writeSSHHome(t *testing.T, host string) string {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("create .ssh: %v", err)
	}
	writeSSHPrivateKeyAt(t, filepath.Join(sshDir, "id_ed25519"), "")
	writeKnownHostsEntry(t, filepath.Join(sshDir, "known_hosts"), host)
	return home
}

func writeKnownHostsEntry(t *testing.T, path, host string) {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	hostKey, err := cryptossh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("NewPublicKey() error = %v", err)
	}
	line := host + " " + strings.TrimSpace(string(cryptossh.MarshalAuthorizedKey(hostKey))) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}

// syntheticSSHEnvironment isolates SSH identity resolution from the developer's
// real ~/.ssh, /etc/ssh, and ssh-agent.
func syntheticSSHEnvironment(t *testing.T, home string) gitauth.Environment {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	return gitauth.Environment{
		LookupEnv:           func(string) (string, bool) { return "", false },
		HomeDir:             home,
		SystemSSHConfigPath: missing,
		SystemKnownHosts:    missing,
		AgentDial:           func(string) (net.Conn, error) { return nil, errors.New("no agent") },
	}
}

func useSSHEnvironment(t *testing.T, env gitauth.Environment) {
	t.Helper()
	previous := newSSHEnvironment
	newSSHEnvironment = func() gitauth.Environment { return env }
	t.Cleanup(func() { newSSHEnvironment = previous })
}

func assertVerifiesHostKeys(t *testing.T, auth transport.AuthMethod) {
	t.Helper()
	configurer, ok := auth.(interface {
		ClientConfig() (*cryptossh.ClientConfig, error)
	})
	if !ok {
		t.Fatalf("auth = %T, want an ssh client config builder", auth)
	}
	config, err := configurer.ClientConfig()
	if err != nil {
		t.Fatalf("ClientConfig() error = %v", err)
	}
	if config.HostKeyCallback == nil {
		t.Fatal("HostKeyCallback = nil, want known_hosts callback")
	}
	if len(config.HostKeyAlgorithms) == 0 {
		t.Fatal("HostKeyAlgorithms is empty, want algorithms from known_hosts")
	}
}
