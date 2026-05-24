package source

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	cachepkg "github.com/sholdee/drydock/internal/cache"
	cryptossh "golang.org/x/crypto/ssh"
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

func TestDefaultGitCacheDirUsesUserCacheRoot(t *testing.T) {
	dir, err := DefaultGitCacheDir()
	if err != nil {
		t.Fatalf("DefaultGitCacheDir() error = %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), "/drydock/git") {
		t.Fatalf("DefaultGitCacheDir() = %q, want drydock/git suffix", dir)
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

func TestDefaultGitAcquirerWritesMetadata(t *testing.T) {
	remote := createGitFixture(t)
	commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: main\n")
	repoURL := "file://" + filepath.ToSlash(remote.path)

	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{
		URL:      repoURL,
		Revision: "HEAD",
	}, GitOptions{
		AllowNetwork: true,
		CacheDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	metadata, err := cachepkg.ReadMetadata(result.Path, cachepkg.SourceGit, "git", GitCacheKey(repoURL, "HEAD"))
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	if metadata == nil {
		t.Fatal("metadata = nil, want metadata")
	}
	if metadata.SchemaVersion != 1 || metadata.Source != cachepkg.SourceGit || metadata.Kind != "git" {
		t.Fatalf("metadata identity = %#v, want git metadata", metadata)
	}
	if metadata.Target != repoURL {
		t.Fatalf("Target = %q, want %q", metadata.Target, repoURL)
	}
	if metadata.Revision != result.Revision {
		t.Fatalf("Revision = %q, want %q", metadata.Revision, result.Revision)
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

func TestDefaultGitAcquirerReportsCacheHit(t *testing.T) {
	remote := createGitFixture(t)
	firstHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: first\n")
	cacheDir := t.TempDir()
	repoURL := "file://" + filepath.ToSlash(remote.path)
	request := GitRequest{URL: repoURL, Revision: firstHash.String()}

	first, err := DefaultGitAcquirer{}.Acquire(context.Background(), request, GitOptions{AllowNetwork: true, CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.FromCache {
		t.Fatal("first FromCache = true, want false")
	}
	second, err := DefaultGitAcquirer{}.Acquire(context.Background(), request, GitOptions{AllowNetwork: true, CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if !second.FromCache {
		t.Fatal("second FromCache = false, want true")
	}
	if second.Network {
		t.Fatal("second Network = true, want false")
	}
}

func TestDefaultGitAcquirerReportsRefreshAsNetwork(t *testing.T) {
	remote := createGitFixture(t)
	firstHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: first\n")
	cacheDir := t.TempDir()
	repoURL := "file://" + filepath.ToSlash(remote.path)
	request := GitRequest{URL: repoURL, Revision: firstHash.String()}
	if _, err := (DefaultGitAcquirer{}).Acquire(context.Background(), request, GitOptions{AllowNetwork: true, CacheDir: cacheDir}); err != nil {
		t.Fatalf("seed Acquire() error = %v", err)
	}

	refreshed, err := DefaultGitAcquirer{}.Acquire(context.Background(), request, GitOptions{AllowNetwork: true, CacheDir: cacheDir, Refresh: true})
	if err != nil {
		t.Fatalf("refresh Acquire() error = %v", err)
	}
	if refreshed.FromCache {
		t.Fatal("refresh FromCache = true, want false")
	}
	if !refreshed.Network {
		t.Fatal("refresh Network = false, want true")
	}
}

func TestDefaultGitAcquirerReportsRevisionMissFetchAsNetwork(t *testing.T) {
	remote := createGitFixture(t)
	firstHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: first\n")
	cacheDir := t.TempDir()
	repoURL := "file://" + filepath.ToSlash(remote.path)
	request := GitRequest{URL: repoURL, Revision: firstHash.String()}
	if _, err := (DefaultGitAcquirer{}).Acquire(context.Background(), request, GitOptions{AllowNetwork: true, CacheDir: cacheDir}); err != nil {
		t.Fatalf("seed Acquire() error = %v", err)
	}
	secondHash := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: second\n")

	fetched, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repoURL, Revision: secondHash.String()}, GitOptions{AllowNetwork: true, CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("revision-miss Acquire() error = %v", err)
	}
	if fetched.FromCache {
		t.Fatal("revision-miss FromCache = true, want false")
	}
	if !fetched.Network {
		t.Fatal("revision-miss Network = false, want true")
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

func TestGitCredentialsHTTPBasicAuth(t *testing.T) {
	auth, hasAuth, err := gitAuthMethod(GitCredentials{Username: "user", Password: "pass"}, "https://github.com/example/private")
	if err != nil {
		t.Fatalf("gitAuthMethod() error = %v", err)
	}
	if !hasAuth {
		t.Fatal("hasAuth = false, want true")
	}
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("auth = %T, want *http.BasicAuth", auth)
	}
	if basic.Username != "user" || basic.Password != "pass" {
		t.Fatalf("basic auth = %#v", basic)
	}
}

func TestGitCredentialsBearerToken(t *testing.T) {
	auth, hasAuth, err := gitAuthMethod(GitCredentials{BearerToken: "token"}, "https://github.com/example/private")
	if err != nil {
		t.Fatalf("gitAuthMethod() error = %v", err)
	}
	if !hasAuth {
		t.Fatal("hasAuth = false, want true")
	}
	token, ok := auth.(*githttp.TokenAuth)
	if !ok {
		t.Fatalf("auth = %T, want *http.TokenAuth", auth)
	}
	if token.Token != "token" {
		t.Fatalf("token = %q", token.Token)
	}
}

func TestGitCredentialsBearerTokenPrecedence(t *testing.T) {
	auth, hasAuth, err := gitAuthMethod(GitCredentials{Username: "user", Password: "pass", BearerToken: "token"}, "https://github.com/example/private")
	if err != nil {
		t.Fatalf("gitAuthMethod() error = %v", err)
	}
	if !hasAuth {
		t.Fatal("hasAuth = false, want true")
	}
	if _, ok := auth.(*githttp.TokenAuth); !ok {
		t.Fatalf("auth = %T, want *http.TokenAuth", auth)
	}
}

func TestGitCredentialsSSHAuthUsesSupportedURLsAndDefaultsUser(t *testing.T) {
	keyFile := writeSSHPrivateKey(t, "")
	knownHostsFile := writeKnownHostsFile(t)

	tests := []struct {
		name string
		url  string
		user string
	}{
		{name: "ssh url with user", url: "ssh://deploy@example.com/org/repo.git", user: "deploy"},
		{name: "scp url", url: "git@example.com:org/repo.git", user: "git"},
		{name: "ssh url without user", url: "ssh://example.com/org/repo.git", user: "git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, hasAuth, err := gitAuthMethod(GitCredentials{
				SSHPrivateKeyPath: keyFile,
				SSHKnownHostsPath: knownHostsFile,
			}, tt.url)
			if err != nil {
				t.Fatalf("gitAuthMethod() error = %v", err)
			}
			if !hasAuth {
				t.Fatal("hasAuth = false, want true")
			}
			publicKeys, ok := auth.(*gitssh.PublicKeys)
			if !ok {
				t.Fatalf("auth = %T, want *ssh.PublicKeys", auth)
			}
			if publicKeys.User != tt.user {
				t.Fatalf("User = %q, want %q", publicKeys.User, tt.user)
			}
			if publicKeys.HostKeyCallback == nil {
				t.Fatal("HostKeyCallback = nil, want known_hosts callback")
			}
		})
	}
}

func TestGitCredentialsSSHAuthRequiresKeyFile(t *testing.T) {
	_, _, err := gitAuthMethod(GitCredentials{SSHKnownHostsPath: writeKnownHostsFile(t)}, "ssh://git@example.com/org/repo.git")
	if err == nil {
		t.Fatal("gitAuthMethod() error = nil, want missing key error")
	}
	if !strings.Contains(err.Error(), "git SSH private key file is required") {
		t.Fatalf("gitAuthMethod() error = %q, want missing key message", err)
	}
}

func TestGitCredentialsSSHAuthRequiresKnownHostsFile(t *testing.T) {
	_, _, err := gitAuthMethod(GitCredentials{SSHPrivateKeyPath: writeSSHPrivateKey(t, "")}, "ssh://git@example.com/org/repo.git")
	if err == nil {
		t.Fatal("gitAuthMethod() error = nil, want missing known_hosts error")
	}
	if !strings.Contains(err.Error(), "git SSH known_hosts file is required") {
		t.Fatalf("gitAuthMethod() error = %q, want missing known_hosts message", err)
	}
}

func TestGitCredentialsSSHAuthRejectsBadPassphraseWithoutLeakingSecrets(t *testing.T) {
	const (
		correctPassphrase = "correct-passphrase"
		wrongPassphrase   = "wrong-passphrase"
	)
	keyFile := writeSSHPrivateKey(t, correctPassphrase)
	_, _, err := gitAuthMethod(GitCredentials{
		SSHPrivateKeyPath: keyFile,
		SSHPassphrase:     wrongPassphrase,
		SSHKnownHostsPath: writeKnownHostsFile(t),
	}, "ssh://git@example.com/org/repo.git")
	if err == nil {
		t.Fatal("gitAuthMethod() error = nil, want passphrase error")
	}
	for _, leaked := range []string{correctPassphrase, wrongPassphrase, "OPENSSH PRIVATE KEY"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("gitAuthMethod() error = %q, leaked %q", err, leaked)
		}
	}
}

func TestGitCredentialsRedactsCredentialValuesFromErrors(t *testing.T) {
	creds := GitCredentials{
		Password:      "secret-password",
		BearerToken:   "secret-token",
		SSHPrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key\n-----END OPENSSH PRIVATE KEY-----",
		SSHPassphrase: "secret-passphrase",
	}
	message := redactGitCredentialError("secret-password secret-token secret-key secret-passphrase", creds)
	for _, leaked := range []string{"secret-password", "secret-token", "secret-key", "secret-passphrase"} {
		if strings.Contains(message, leaked) {
			t.Fatalf("redacted message = %q, leaked %q", message, leaked)
		}
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

func writeSSHPrivateKey(t *testing.T, passphrase string) string {
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
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	return path
}

func writeKnownHostsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}
