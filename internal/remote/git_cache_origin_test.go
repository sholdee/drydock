package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// remoteListedAndAttackerHosts starts two smart HTTP Git servers with
// different content. The listed server serves its repository at the root,
// so its repository URL is the bare server URL, which is the cache key of
// "<listed>?@<attacker>/repo.git" for net/url.
func remoteListedAndAttackerHosts(t *testing.T) (listed *submoduletest.HTTPServer, attacker *submoduletest.HTTPServer, poisonURL string) {
	t.Helper()
	listedDir := t.TempDir()
	submoduletest.NewRepo(t, listedDir, "repo", map[string]string{"base/cm.yaml": "name: listed\n"})
	listed = submoduletest.ServeSmartHTTP(t, filepath.Join(listedDir, "repo.git"))
	attackerDir := t.TempDir()
	submoduletest.NewRepo(t, attackerDir, "repo", map[string]string{"base/cm.yaml": "name: attacker\n"})
	attacker = submoduletest.ServeSmartHTTP(t, attackerDir)
	return listed, attacker, listed.URL + "?@" + strings.TrimPrefix(attacker.URL, "http://") + "/repo.git"
}

func remoteAcquireGit(cacheDir, repoURL, revision string, offline, refresh bool, credentials GitCredentials) (Result, error) {
	return DefaultAcquirer{}.Acquire(context.Background(), Request{Kind: RequestGitRepo, RepoURL: repoURL, Revision: revision}, Options{
		CacheDir:       cacheDir,
		Offline:        offline,
		Refresh:        refresh,
		GitCredentials: credentials,
	})
}

func requireRemoteListedContent(t *testing.T, result Result) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(result.Path, "base", "cm.yaml"))
	if err != nil {
		t.Fatalf("read the checkout: %v", err)
	}
	if !strings.Contains(string(data), "name: listed") {
		t.Fatalf("checkout holds %q, want the content of the listed host", data)
	}
}

func requireRemoteNoToken(t *testing.T, server *submoduletest.HTTPServer) {
	t.Helper()
	for _, header := range server.AuthorizationHeaders() {
		if strings.Contains(header, remoteGitHostTestToken) {
			t.Fatalf("server %s received the --git-bearer-token", server.URL)
		}
	}
}

// TestDefaultAcquirerRefusesARemoteBaseURLWithTwoHosts checks that a
// remote Git base whose URL has "?" or "#" before its path fails before
// any request, and that a later base on the listed URL, which net/url
// gives the same cache key, gets the listed content and never sends the
// token to the other host. Before the fix, the first base filled the
// entry of the listed URL from the other host, and the second fetched
// that origin with the token.
func TestDefaultAcquirerRefusesARemoteBaseURLWithTwoHosts(t *testing.T) {
	listed, attacker, poisonURL := remoteListedAndAttackerHosts(t)
	cacheDir := t.TempDir()
	credentials := GitCredentials{BearerToken: remoteGitHostTestToken, Hosts: []string{listed.URL}}
	if _, err := remoteAcquireGit(cacheDir, poisonURL, "main", false, false, credentials); err == nil || !strings.Contains(err.Error(), "host part of the URL") {
		t.Fatalf("Acquire() of the two-host URL error = %v, want the host part refusal", err)
	}
	if got := len(listed.Requests()) + len(attacker.Requests()); got != 0 {
		t.Fatalf("servers received %d requests, want none", got)
	}
	result, err := remoteAcquireGit(cacheDir, listed.URL, "main", false, true, credentials)
	if err != nil {
		t.Fatalf("Acquire() of the listed URL error = %v", err)
	}
	requireRemoteListedContent(t, result)
	requireRemoteNoToken(t, attacker)
}

// TestDefaultAcquirerRefusesARemoteCacheEntryOfAnotherRepository checks a
// remote Git cache entry whose origin names another host. Offline, drydock
// fails with a cache miss and never serves the other content. Online, it
// clones the listed URL into the entry again: the other host gets no
// request and no token, with or without --refresh-remotes. Before the fix,
// drydock served the other content, and with --refresh-remotes fetched the
// other origin with the token of the listed host.
func TestDefaultAcquirerRefusesARemoteCacheEntryOfAnotherRepository(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "cached"
		if refresh {
			name = "refresh"
		}
		t.Run(name, func(t *testing.T) {
			listed, attacker, _ := remoteListedAndAttackerHosts(t)
			cacheDir := t.TempDir()
			normalized, err := NormalizeGitRepoCacheURL(listed.URL)
			if err != nil {
				t.Fatal(err)
			}
			key, err := NewCacheKey(Request{Kind: RequestGitRepo, RepoURL: normalized, Revision: "main"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := git.PlainClone(cache.RemoteGitRepoPath(cacheDir, key), false, &git.CloneOptions{URL: attacker.URL + "/repo.git"}); err != nil {
				t.Fatalf("write the poisoned entry: %v", err)
			}
			before := len(attacker.Requests())
			credentials := GitCredentials{BearerToken: remoteGitHostTestToken, Hosts: []string{listed.URL}}
			if _, err := remoteAcquireGit(cacheDir, listed.URL, "main", true, false, credentials); err == nil || !strings.Contains(err.Error(), "offline cache miss") {
				t.Fatalf("offline Acquire() error = %v, want a cache miss", err)
			}
			result, err := remoteAcquireGit(cacheDir, listed.URL, "main", false, refresh, credentials)
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			requireRemoteListedContent(t, result)
			if got := attacker.Requests()[before:]; len(got) != 0 {
				t.Fatalf("the other host received %d requests after the poisoning, want none", len(got))
			}
			requireRemoteNoToken(t, attacker)
		})
	}
}

// setCacheEntryOrigin sets the origin URLs of the cache entry at path to
// originURLs. One URL is what drydock writes when it clones that URL. Tests that
// build an entry from a local clone use it, because drydock does not use
// an entry whose origin names another repository.
func setCacheEntryOrigin(t *testing.T, path string, originURLs ...string) {
	t.Helper()
	repo, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteRemote("origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: originURLs}); err != nil {
		t.Fatal(err)
	}
}
