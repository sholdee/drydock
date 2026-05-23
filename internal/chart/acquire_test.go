package chart

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheKeyNormalizesRepositoryURL(t *testing.T) {
	left, err := NewCacheKey(Request{
		Repository: " https://example.com/charts/ ",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	})
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	right, err := NewCacheKey(Request{
		Repository: "https://example.com/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	})
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	if left != right {
		t.Fatalf("cache keys differ:\nleft:  %s\nright: %s", left, right)
	}
}

func TestCacheKeySeparatesOCIAndHTTP(t *testing.T) {
	httpKey, err := NewCacheKey(Request{
		Repository: "https://example.com/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	})
	if err != nil {
		t.Fatalf("NewCacheKey(http) error = %v", err)
	}
	ociKey, err := NewCacheKey(Request{
		Repository: "oci://example.com/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	})
	if err != nil {
		t.Fatalf("NewCacheKey(oci) error = %v", err)
	}
	if httpKey == ociKey {
		t.Fatalf("cache key did not separate repository kinds: %s", httpKey)
	}
}

func TestCacheKeyRejectsMissingFields(t *testing.T) {
	for _, request := range []Request{
		{Name: "demo", Version: "1.2.3", Kind: RepositoryHTTP},
		{Repository: "https://example.com/charts", Version: "1.2.3", Kind: RepositoryHTTP},
		{Repository: "https://example.com/charts", Name: "demo", Kind: RepositoryHTTP},
	} {
		if _, err := NewCacheKey(request); err == nil {
			t.Fatalf("NewCacheKey(%#v) error = nil, want validation error", request)
		}
	}
}

func TestDefaultCacheDirUsesUserCacheRoot(t *testing.T) {
	dir, err := DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir() error = %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), "/argocd-local/charts") {
		t.Fatalf("DefaultCacheDir() = %q, want argocd-local/charts suffix", dir)
	}
}
