package remote

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cachepkg "github.com/sholdee/drydock/internal/cache"
)

func TestNewCacheKeyNormalizesURL(t *testing.T) {
	left, err := NewCacheKey(Request{URL: " https://raw.githubusercontent.com/org/repo/main/file.yaml "})
	if err != nil {
		t.Fatalf("NewCacheKey(left) error = %v", err)
	}
	right, err := NewCacheKey(Request{URL: "https://raw.githubusercontent.com/org/repo/main/file.yaml"})
	if err != nil {
		t.Fatalf("NewCacheKey(right) error = %v", err)
	}
	if left != right {
		t.Fatalf("cache keys differ: %s != %s", left, right)
	}
}

func TestNewCacheKeyRejectsUnsafeURLs(t *testing.T) {
	for _, raw := range []string{
		"",
		"ftp://example.test/file.yaml",
		"https://user:secret@example.test/file.yaml",
		"https://example.test/file.yaml?token=secret",
		"https://example.test/file.yaml#token",
		"https://github.com/org/repo//base?ref=main",
		"https://example.test/file.txt",
		"git::https://example.test/file.yaml",
		"git@github.com:org/repo.git//base",
		"github.com/org/repo//base",
	} {
		if _, err := NewCacheKey(Request{URL: raw}); err == nil {
			t.Fatalf("NewCacheKey(%q) error = nil, want validation error", raw)
		}
	}
}

func TestNewCacheKeyForGitRepoIncludesRevisionAndOmitsSecrets(t *testing.T) {
	mainKey, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		URL:      "https://example.test/org/repo.git",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(main) error = %v", err)
	}
	devKey, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "https://example.test/org/repo.git",
		Revision: "dev",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(dev) error = %v", err)
	}
	if mainKey == devKey {
		t.Fatalf("cache keys match for different revisions: %s", mainKey)
	}

	secretBearingKey, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "https://user:secret@example.test/org/repo.git?token=secret#fragment",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(secret-bearing) error = %v", err)
	}
	if secretBearingKey != mainKey {
		t.Fatalf("secret-bearing key = %s, want clean repo key %s", secretBearingKey, mainKey)
	}
}

func TestNewCacheKeyForGitRepoCanonicalizesEquivalentURLs(t *testing.T) {
	left, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "https://example.test/org/repo.git/",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(left) error = %v", err)
	}
	right, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "https://example.test/org/repo",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(right) error = %v", err)
	}
	if left != right {
		t.Fatalf("URL keys differ: %s != %s", left, right)
	}

	scpLeft, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "git@example.test:org/repo.git/",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(scpLeft) error = %v", err)
	}
	scpRight, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "git@example.test:org/repo",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(scpRight) error = %v", err)
	}
	if scpLeft != scpRight {
		t.Fatalf("SCP keys differ: %s != %s", scpLeft, scpRight)
	}
}

func TestNewCacheKeyForGitRepoRedactsSCPUser(t *testing.T) {
	gitUserKey, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "git@example.test:org/repo",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(git user) error = %v", err)
	}
	deployUserKey, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "deploy@example.test:org/repo.git?token=secret#frag",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(deploy user) error = %v", err)
	}
	if gitUserKey != deployUserKey {
		t.Fatalf("SCP cache keys differ: git=%s deploy=%s", gitUserKey, deployUserKey)
	}

	redactedKey, err := NewCacheKey(Request{
		Kind:     RequestGitRepo,
		RepoURL:  "example.test:org/repo",
		Revision: "main",
	})
	if err != nil {
		t.Fatalf("NewCacheKey(redacted) error = %v", err)
	}
	if gitUserKey != redactedKey {
		t.Fatalf("SCP cache key is not idempotent after redaction: git=%s redacted=%s", gitUserKey, redactedKey)
	}
}

func TestNormalizeURLParseErrorDoesNotLeakSecretBearingURL(t *testing.T) {
	_, err := NormalizeURL("https://user:secret@example.test/%zz.yaml?token=secret#fragment")
	if err == nil {
		t.Fatal("NormalizeURL() error = nil, want parse error")
	}
	for _, leaked := range []string{"secret", "token=", "user:", "@example.test", "?token", "#fragment"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("NormalizeURL() error = %q, leaked %q", err, leaked)
		}
	}
}

func TestDefaultCacheDirUsesUserCacheRoot(t *testing.T) {
	dir, err := DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir() error = %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), "/drydock/remotes") {
		t.Fatalf("DefaultCacheDir() = %q, want drydock/remotes suffix", dir)
	}
}

func TestRedactURLRemovesSensitiveParts(t *testing.T) {
	got := RedactURL(" https://user:secret@example.test/file.yaml?token=secret#fragment ")
	want := "https://example.test/file.yaml"
	if got != want {
		t.Fatalf("RedactURL() = %q, want %q", got, want)
	}
}

func TestIsPathInsideAny(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "nested", "cache")
	outside := filepath.Join(t.TempDir(), "cache")

	ok, matched, err := IsPathInsideAny(inside, []string{"", root})
	if err != nil {
		t.Fatalf("IsPathInsideAny(inside) error = %v", err)
	}
	if !ok || matched != root {
		t.Fatalf("IsPathInsideAny(inside) = %v, %q; want true, %q", ok, matched, root)
	}

	ok, matched, err = IsPathInsideAny(outside, []string{root})
	if err != nil {
		t.Fatalf("IsPathInsideAny(outside) error = %v", err)
	}
	if ok || matched != "" {
		t.Fatalf("IsPathInsideAny(outside) = %v, %q; want false, empty root", ok, matched)
	}
}

func TestDefaultAcquirerFetchesAndCachesResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: remote\n"))
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	request := Request{URL: server.URL + "/resource.yaml"}
	acquirer := DefaultAcquirer{Client: server.Client()}

	first, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.FromCache {
		t.Fatal("first FromCache = true, want false")
	}
	data, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read cached resource: %v", err)
	}
	if !strings.Contains(string(data), "name: remote") {
		t.Fatalf("cached data = %q, want remote manifest", string(data))
	}

	second, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true})
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	if !second.FromCache {
		t.Fatal("offline FromCache = false, want true")
	}
	if second.Path != first.Path {
		t.Fatalf("offline Path = %q, want %q", second.Path, first.Path)
	}
}

func TestDefaultRemoteAcquirerWritesHTTPMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: remote\n"))
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	request := Request{URL: server.URL + "/resource.yaml"}
	result, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	key, err := NewCacheKey(request)
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	metadata, err := cachepkg.ReadMetadata(filepath.Dir(result.Path), cachepkg.SourceRemote, "http-file", key)
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	if metadata == nil {
		t.Fatal("metadata = nil, want metadata")
	}
	if metadata.Target != request.URL {
		t.Fatalf("Target = %q, want %q", metadata.Target, request.URL)
	}
}

func TestDefaultRemoteAcquirerWritesHTTPMetadataOnCacheHit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: remote\n"))
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	request := Request{URL: server.URL + "/resource.yaml"}
	acquirer := DefaultAcquirer{Client: server.Client()}
	first, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	metadataPath := cachepkg.MetadataPath(filepath.Dir(first.Path))
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove(metadata) error = %v", err)
	}

	second, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true})
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

func TestDefaultAcquirerUsesBearerToken(t *testing.T) {
	const token = "secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+token; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bearer\n"))
	}))
	defer server.Close()

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		URL: server.URL + "/resource.yaml",
	}, Options{
		CacheDir:    t.TempDir(),
		Credentials: Credentials{BearerToken: token},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
}

func TestDefaultAcquirerUsesBasicAuth(t *testing.T) {
	const username = "user"
	const password = "secret-password"
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("Authorization = %q, want %q", got, wantAuth)
		}
		_, _ = w.Write([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: basic\n"))
	}))
	defer server.Close()

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		URL: server.URL + "/resource.yaml",
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: Credentials{
			Username: username,
			Password: password,
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
}

func TestDefaultAcquirerBearerWinsOverBasic(t *testing.T) {
	const token = "secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+token; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bearer\n"))
	}))
	defer server.Close()

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		URL: server.URL + "/resource.yaml",
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: Credentials{
			Username:    "user",
			Password:    "secret-password",
			BearerToken: token,
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
}

func TestDefaultAcquirerRedactsCredentialFetchErrors(t *testing.T) {
	creds := Credentials{
		Username:    "user",
		Password:    "secret-password",
		BearerToken: "secret-token",
	}
	_, err := (DefaultAcquirer{Client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("failed with %s, %s, and %s", creds.Username, creds.Password, creds.BearerToken)
		}),
	}}).Acquire(context.Background(), Request{
		URL: "https://example.test/resource.yaml",
	}, Options{CacheDir: t.TempDir(), Credentials: creds})
	if err == nil {
		t.Fatal("Acquire() error = nil, want fetch error")
	}
	for _, leaked := range []string{creds.Username, creds.Password, creds.BearerToken} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Acquire() error = %q, leaked %q", err, leaked)
		}
	}
}

func TestDefaultAcquirerRefreshBypassesCache(t *testing.T) {
	responses := []string{"kind: ConfigMap\nmetadata:\n  name: old\n", "kind: ConfigMap\nmetadata:\n  name: new\n"}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := responses[requests]
		requests++
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	request := Request{URL: server.URL + "/resource.yaml"}
	acquirer := DefaultAcquirer{Client: server.Client()}
	if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir}); err != nil {
		t.Fatalf("initial Acquire() error = %v", err)
	}
	refreshed, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Refresh: true})
	if err != nil {
		t.Fatalf("refresh Acquire() error = %v", err)
	}
	if refreshed.FromCache {
		t.Fatal("refresh FromCache = true, want false")
	}
	data, err := os.ReadFile(refreshed.Path)
	if err != nil {
		t.Fatalf("read refreshed cache: %v", err)
	}
	if !strings.Contains(string(data), "name: new") {
		t.Fatalf("refreshed data = %q, want new manifest", string(data))
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestDefaultAcquirerOfflineRequiresCacheHit(t *testing.T) {
	acquirer := DefaultAcquirer{Client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("offline Acquire() made a network request")
			return nil, errors.New("unexpected network request")
		}),
	}}
	_, err := acquirer.Acquire(context.Background(), Request{
		URL: "https://raw.githubusercontent.com/org/repo/main/file.yaml",
	}, Options{CacheDir: t.TempDir(), Offline: true})
	if err == nil || !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("Acquire() error = %v, want offline cache miss", err)
	}
}

func TestDefaultAcquirerRejectsCacheInsideForbiddenRoot(t *testing.T) {
	repoRoot := t.TempDir()
	cacheDir := filepath.Join(repoRoot, ".drydock", "remote-cache")
	_, err := (DefaultAcquirer{}).Acquire(context.Background(), Request{
		URL: "https://raw.githubusercontent.com/org/repo/main/file.yaml",
	}, Options{CacheDir: cacheDir, ForbiddenRoots: []string{repoRoot}})
	if err == nil || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Acquire() error = %v, want cache containment error", err)
	}
}

func TestDefaultAcquirerRejectsCacheSymlinkedIntoForbiddenRoot(t *testing.T) {
	repoRoot := t.TempDir()
	outsideRoot := t.TempDir()
	cacheLink := filepath.Join(outsideRoot, "cache-link")
	if err := os.Symlink(repoRoot, cacheLink); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	cacheDir := filepath.Join(cacheLink, ".drydock", "remote-cache")
	acquirer := DefaultAcquirer{Client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("Acquire() made a network request before rejecting cache dir")
			return nil, errors.New("unexpected network request")
		}),
	}}

	_, err := acquirer.Acquire(context.Background(), Request{
		URL: "https://raw.githubusercontent.com/org/repo/main/file.yaml",
	}, Options{CacheDir: cacheDir, ForbiddenRoots: []string{repoRoot}})
	if err == nil || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Acquire() error = %v, want cache containment error", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".drydock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forbidden root cache directory exists after rejected Acquire(): %v", err)
	}
}

func TestDefaultAcquirerRejectsCacheKeySymlinkedIntoForbiddenRoot(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: forbidden\n"))
	}))
	defer server.Close()

	repoRoot := t.TempDir()
	forbiddenTarget := filepath.Join(repoRoot, "remote-cache-key")
	if err := os.MkdirAll(forbiddenTarget, 0o755); err != nil {
		t.Fatalf("create forbidden target: %v", err)
	}
	cacheDir := t.TempDir()
	request := Request{URL: server.URL + "/resource.yaml"}
	key, err := NewCacheKey(request)
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	if err := os.Symlink(forbiddenTarget, filepath.Join(cacheDir, key)); err != nil {
		t.Skipf("create cache key symlink: %v", err)
	}

	_, err = (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{
		CacheDir:       cacheDir,
		ForbiddenRoots: []string{repoRoot},
	})
	if err == nil || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Acquire() error = %v, want cache containment error", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if _, err := os.Stat(filepath.Join(forbiddenTarget, "resource.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forbidden cache resource exists after rejected Acquire(): %v", err)
	}
}

func TestDefaultAcquirerRejectsOversizedResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, defaultMaxResourceBytes+1))
	}))
	defer server.Close()

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		URL: server.URL + "/resource.yaml",
	}, Options{CacheDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Acquire() error = %v, want size limit error", err)
	}
}

func TestDefaultAcquirerRedactsFetchErrors(t *testing.T) {
	_, err := (DefaultAcquirer{Client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("fetch https://example.test/resource.yaml failed")
		}),
	}}).Acquire(context.Background(), Request{
		URL: "https://example.test/resource.yaml",
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want fetch error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("Acquire() leaked query secret: %q", err)
	}
}

func TestDefaultAcquirerMapsAuthFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "authentication required", status)
			}))
			defer server.Close()

			_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
				URL: server.URL + "/resource.yaml",
			}, Options{CacheDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("Acquire() error = %v, want HTTP status error", err)
			}
		})
	}
}

func TestPublishCacheFileReplacesAtomically(t *testing.T) {
	target := filepath.Join(t.TempDir(), "key", "resource.yaml")
	if err := publishCacheFile(target, []byte("old")); err != nil {
		t.Fatalf("publish old cache file: %v", err)
	}
	if err := publishCacheFile(target, []byte("new")); err != nil {
		t.Fatalf("publish new cache file: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("cache file = %q, want new", string(data))
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary cache file remained: %s", entry.Name())
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
