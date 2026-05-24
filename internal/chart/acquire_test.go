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
	if !strings.HasSuffix(filepath.ToSlash(dir), "/drydock/charts") {
		t.Fatalf("DefaultCacheDir() = %q, want drydock/charts suffix", dir)
	}
}

func TestNormalizeRepositoryRejectsUnsupportedKind(t *testing.T) {
	if _, err := NormalizeRepository("https://example.com/charts", RepositoryKind("git")); err == nil {
		t.Fatal("NormalizeRepository() error = nil, want unsupported kind error")
	}
}

func TestNormalizeRepositoryRejectsMissingHTTPHost(t *testing.T) {
	if _, err := NormalizeRepository("https:///charts", RepositoryHTTP); err == nil {
		t.Fatal("NormalizeRepository() error = nil, want missing host error")
	}
}

func TestNormalizeRepositoryRejectsMissingOCIHost(t *testing.T) {
	if _, err := NormalizeRepository("oci:///charts", RepositoryOCI); err == nil {
		t.Fatal("NormalizeRepository() error = nil, want missing host error")
	}
}

func TestNormalizeRepositoryRejectsInvalidOCIScheme(t *testing.T) {
	if _, err := NormalizeRepository("https://example.com/charts", RepositoryOCI); err == nil {
		t.Fatal("NormalizeRepository() error = nil, want invalid OCI scheme error")
	}
}

func TestNormalizeRepositoryTrimsOCITrailingSlash(t *testing.T) {
	normalized, err := NormalizeRepository(" oci://example.com/charts/ ", RepositoryOCI)
	if err != nil {
		t.Fatalf("NormalizeRepository() error = %v", err)
	}
	if normalized != "oci://example.com/charts" {
		t.Fatalf("NormalizeRepository() = %q, want oci://example.com/charts", normalized)
	}
}
