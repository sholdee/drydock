package source

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	cachepkg "github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

func originTestConfigMap(name string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
}

// listedAndAttackerHosts starts two smart HTTP Git servers with different
// content. The listed server serves its repository at the root, so its
// repository URL is the bare server URL, which is the cache key of
// "<listed>?@<attacker>/repo.git" for net/url. The attacker server serves
// its repository at /repo.git.
func listedAndAttackerHosts(t *testing.T) (listed *submoduletest.HTTPServer, listedURL string, attacker *submoduletest.HTTPServer, poisonURL string) {
	t.Helper()
	listedDir := t.TempDir()
	submoduletest.NewRepo(t, listedDir, "repo", map[string]string{"app/cm.yaml": originTestConfigMap("listed")})
	listed = submoduletest.ServeSmartHTTP(t, filepath.Join(listedDir, "repo.git"))
	attackerDir := t.TempDir()
	submoduletest.NewRepo(t, attackerDir, "repo", map[string]string{"app/cm.yaml": originTestConfigMap("attacker")})
	attacker = submoduletest.ServeSmartHTTP(t, attackerDir)
	return listed, listed.URL, attacker, listed.URL + "?@" + strings.TrimPrefix(attacker.URL, "http://") + "/repo.git"
}

func requireListedContent(t *testing.T, result GitResult) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(result.Path, "app", "cm.yaml"))
	if err != nil {
		t.Fatalf("read the checkout: %v", err)
	}
	if !strings.Contains(string(data), "name: listed") {
		t.Fatalf("checkout holds %q, want the content of the listed host", data)
	}
}

func requireNoTokenAt(t *testing.T, server *submoduletest.HTTPServer) {
	t.Helper()
	for _, header := range server.AuthorizationHeaders() {
		if strings.Contains(header, gitHostTestToken) {
			t.Fatalf("server %s received the --git-bearer-token", server.URL)
		}
	}
}

// TestDefaultGitAcquirerRefusesAURLWithTwoHosts checks that a repository
// URL with "?" or "#" before its path fails before any request. net/url
// reads the host before "?", and code that cuts at "/" reads the host after
// "@". Before the fix, drydock keyed the cache entry by the first host and
// cloned from the second.
func TestDefaultGitAcquirerRefusesAURLWithTwoHosts(t *testing.T) {
	listed, _, attacker, poisonURL := listedAndAttackerHosts(t)
	for _, repoURL := range []string{poisonURL, strings.Replace(poisonURL, "?@", "#@", 1)} {
		_, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repoURL, Revision: "main"}, GitOptions{
			AllowNetwork: true,
			CacheDir:     t.TempDir(),
			Credentials:  GitCredentials{BearerToken: gitHostTestToken, Hosts: []string{listed.URL}},
		})
		if err == nil || !strings.Contains(err.Error(), "host part of the URL") {
			t.Fatalf("Acquire(%q) error = %v, want the host part refusal", repoURL, err)
		}
	}
	if got := len(listed.Requests()) + len(attacker.Requests()); got != 0 {
		t.Fatalf("servers received %d requests, want none", got)
	}
}

// TestDefaultGitAcquirerNeverSendsTheTokenThroughACacheKeyCollision runs
// the attack of the security review against one cache directory: an
// Application with the two-host URL, and an Application with the listed
// URL and --refresh-git, in both orders. The attacker host never gets the
// token, and the listed Application always reads the listed content.
// Before the fix, the second Application fetched the attacker origin of
// the shared entry with the token of the listed host.
func TestDefaultGitAcquirerNeverSendsTheTokenThroughACacheKeyCollision(t *testing.T) {
	for _, poisonFirst := range []bool{true, false} {
		name := "listed first"
		if poisonFirst {
			name = "poison first"
		}
		t.Run(name, func(t *testing.T) {
			listed, listedURL, attacker, poisonURL := listedAndAttackerHosts(t)
			cacheDir := t.TempDir()
			acquire := func(repoURL string) (GitResult, error) {
				return DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repoURL, Revision: "main"}, GitOptions{
					AllowNetwork: true,
					CacheDir:     cacheDir,
					Refresh:      true,
					Credentials:  GitCredentials{BearerToken: gitHostTestToken, Hosts: []string{listed.URL}},
				})
			}
			order := []string{listedURL, poisonURL, listedURL}
			if poisonFirst {
				order = []string{poisonURL, listedURL}
			}
			for _, repoURL := range order {
				result, err := acquire(repoURL)
				if repoURL == poisonURL {
					if err == nil {
						t.Fatal("Acquire() of the two-host URL succeeded, want a refusal")
					}
					continue
				}
				if err != nil {
					t.Fatalf("Acquire(listed) error = %v", err)
				}
				requireListedContent(t, result)
			}
			requireNoTokenAt(t, attacker)
		})
	}
}

// poisonCacheEntry writes, at the cache entry of repoURL and revision, a
// clone of fromURL: an entry whose origin names another repository, as a
// key collision or a change on disk leaves it.
func poisonCacheEntry(t *testing.T, cacheDir, repoURL, revision, fromURL string) {
	t.Helper()
	path := cachepkg.GitEntryPath(cacheDir, GitCacheKey(repoURL, revision))
	if _, err := git.PlainClone(path, false, &git.CloneOptions{URL: fromURL}); err != nil {
		t.Fatalf("write the poisoned entry: %v", err)
	}
}

// TestDefaultGitAcquirerRefusesACacheEntryOfAnotherRepository checks a
// cache entry whose origin names another host. Offline, drydock fails with
// a cache miss and never serves the other content, which is cache
// poisoning without any credential. Online, it clones the listed URL into
// the entry again: the other host gets no request and no token, with or
// without --refresh-git and for a revision that the entry lacks. Before the
// fix, drydock served the other content and fetched the other origin with
// the token of the listed host.
func TestDefaultGitAcquirerRefusesACacheEntryOfAnotherRepository(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revision string
		refresh  bool
	}{
		{name: "cached revision", revision: "main"},
		{name: "refresh", revision: "main", refresh: true},
		{name: "revision the entry lacks", revision: "release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed, listedURL, attacker, _ := listedAndAttackerHosts(t)
			cacheDir := t.TempDir()
			poisonCacheEntry(t, cacheDir, listedURL, tc.revision, attacker.URL+"/repo.git")
			before := len(attacker.Requests())
			if _, err := (DefaultGitAcquirer{}).Acquire(context.Background(), GitRequest{URL: listedURL, Revision: tc.revision}, GitOptions{CacheDir: cacheDir}); err == nil || !strings.Contains(err.Error(), "clone of another repository") {
				t.Fatalf("offline Acquire() error = %v, want a cache miss for the other repository", err)
			}
			result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: listedURL, Revision: tc.revision}, GitOptions{
				AllowNetwork: true,
				CacheDir:     cacheDir,
				Refresh:      tc.refresh,
				Credentials:  GitCredentials{BearerToken: gitHostTestToken, Hosts: []string{listed.URL}},
			})
			if tc.revision == "release" {
				// The listed repository has no such branch either.
				if err == nil {
					t.Fatal("Acquire() of a missing branch succeeded")
				}
			} else {
				if err != nil {
					t.Fatalf("Acquire() error = %v", err)
				}
				requireListedContent(t, result)
			}
			if got := attacker.Requests()[before:]; len(got) != 0 {
				t.Fatalf("the other host received %d requests after the poisoning, want none", len(got))
			}
			requireNoTokenAt(t, attacker)
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

// TestDefaultGitAcquirerRefusesAnEntryWithOneForeignOriginURL checks a
// cache entry whose origin remote has two URLs: the listed URL and another
// host. drydock uses the entry only when every origin URL names the
// repository of the request. Offline, the request is a cache miss and the
// other content is never served. Online, drydock clones the listed URL into
// the entry again and does not contact the other host. A check that
// accepts the entry when any one URL matches fails this test.
func TestDefaultGitAcquirerRefusesAnEntryWithOneForeignOriginURL(t *testing.T) {
	listed, listedURL, attacker, _ := listedAndAttackerHosts(t)
	cacheDir := t.TempDir()
	poisonCacheEntry(t, cacheDir, listedURL, "main", attacker.URL+"/repo.git")
	setCacheEntryOrigin(t, cachepkg.GitEntryPath(cacheDir, GitCacheKey(listedURL, "main")), listedURL, attacker.URL+"/repo.git")
	before := len(attacker.Requests())
	if _, err := (DefaultGitAcquirer{}).Acquire(context.Background(), GitRequest{URL: listedURL, Revision: "main"}, GitOptions{CacheDir: cacheDir}); err == nil || !strings.Contains(err.Error(), "clone of another repository") {
		t.Fatalf("offline Acquire() error = %v, want a cache miss for the other repository", err)
	}
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: listedURL, Revision: "main"}, GitOptions{
		AllowNetwork: true,
		CacheDir:     cacheDir,
		Credentials:  GitCredentials{BearerToken: gitHostTestToken, Hosts: []string{listed.URL}},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	requireListedContent(t, result)
	if got := attacker.Requests()[before:]; len(got) != 0 {
		t.Fatalf("the other host received %d requests, want none", len(got))
	}
}

// userinfoRecorder is the HTTP transport of go-git in a test. It records
// whether a request URL held userinfo. go-git builds each request URL from
// the URL that it fetches, so a fetch of a URL with userinfo shows up
// here, while the server never sees it.
type userinfoRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (recorder *userinfoRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder.mu.Lock()
	if request.URL.User != nil {
		recorder.seen = append(recorder.seen, request.Method+" "+request.URL.Path)
	}
	recorder.mu.Unlock()
	return http.DefaultTransport.RoundTrip(request)
}

func (recorder *userinfoRecorder) withUserinfo() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.seen...)
}

// recordGoGitHTTPUserinfo installs a userinfoRecorder as the go-git http
// client until the test ends. No test of this package runs in parallel.
func recordGoGitHTTPUserinfo(t *testing.T) *userinfoRecorder {
	t.Helper()
	recorder := &userinfoRecorder{}
	gitclient.InstallProtocol("http", githttp.NewClient(&http.Client{Transport: recorder}))
	t.Cleanup(func() { gitclient.InstallProtocol("http", githttp.DefaultClient) })
	return recorder
}

// TestDefaultGitAcquirerFetchesTheURLWithoutUserinfo checks that the
// clone, and the fetch of the existing entry with --refresh-git, go to the
// repository URL without its userinfo. The userinfo becomes the auth
// method instead. A fetch of the raw request URL puts the user and
// password back into the URL that go-git requests.
func TestDefaultGitAcquirerFetchesTheURLWithoutUserinfo(t *testing.T) {
	recorder := recordGoGitHTTPUserinfo(t)
	listed, listedURL, _, _ := listedAndAttackerHosts(t)
	withUserinfo := strings.Replace(listedURL, "http://", "http://ci-user:url-secret@", 1)
	cacheDir := t.TempDir()
	for _, refresh := range []bool{false, true} {
		result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: withUserinfo, Revision: "main"}, GitOptions{
			AllowNetwork: true,
			CacheDir:     cacheDir,
			Refresh:      refresh,
		})
		if err != nil {
			t.Fatalf("Acquire(refresh %t) error = %v", refresh, err)
		}
		requireListedContent(t, result)
	}
	if got := recorder.withUserinfo(); len(got) != 0 {
		t.Fatalf("go-git requested URLs with userinfo: %q", got)
	}
	if len(listed.Requests()) < 2 {
		t.Fatalf("listed host requests = %d, want the clone and the refresh fetch", len(listed.Requests()))
	}
	requireAuthOnEveryRequest(t, listed, "Basic "+base64.StdEncoding.EncodeToString([]byte("ci-user:url-secret")))
}
