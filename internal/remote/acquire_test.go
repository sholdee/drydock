package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestDefaultCacheDirUsesUserCacheRoot(t *testing.T) {
	dir, err := DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir() error = %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), "/argocd-local/remotes") {
		t.Fatalf("DefaultCacheDir() = %q, want argocd-local/remotes suffix", dir)
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
	cacheDir := filepath.Join(repoRoot, ".argocd-local", "remote-cache")
	_, err := (DefaultAcquirer{}).Acquire(context.Background(), Request{
		URL: "https://raw.githubusercontent.com/org/repo/main/file.yaml",
	}, Options{CacheDir: cacheDir, ForbiddenRoots: []string{repoRoot}})
	if err == nil || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Acquire() error = %v, want cache containment error", err)
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
			if err == nil || !strings.Contains(err.Error(), "authenticated remote Kustomize resources are not supported") {
				t.Fatalf("Acquire() error = %v, want auth unsupported error", err)
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
