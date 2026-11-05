package chart

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cachepkg "github.com/sholdee/drydock/internal/cache"
)

// maxAgeChartServer serves demo 1.2.3 and counts archive downloads; content
// changes what the next download returns, as a republished chart version.
type maxAgeChartServer struct {
	server    *httptest.Server
	downloads int
	content   string
}

func newMaxAgeChartServer(t *testing.T) *maxAgeChartServer {
	t.Helper()
	s := &maxAgeChartServer{content: "first"}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			writeIndex(t, w, "demo-1.2.3.tgz")
		case "/demo-1.2.3.tgz":
			s.downloads++
			archive := chartArchive(t, "demo", map[string]string{
				"Chart.yaml":  "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
				"values.yaml": "content: " + s.content + "\n",
			})
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *maxAgeChartServer) request() Request {
	return Request{Repository: s.server.URL, Name: "demo", Version: "1.2.3", Kind: RepositoryHTTP}
}

func readChartValues(t *testing.T, result Result) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(result.ChartDir, "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// ageChartEntry rewrites the cache entry's fetch time to age ago.
func ageChartEntry(t *testing.T, cacheDir string, request Request, age time.Duration) time.Time {
	t.Helper()
	key := mustCacheKey(t, request)
	entry := cachepkg.ChartEntryPath(cacheDir, string(request.Kind), key)
	metadata, err := cachepkg.ReadMetadata(entry, cachepkg.SourceChart, string(request.Kind), key)
	if err != nil || metadata == nil {
		t.Fatalf("ReadMetadata() = %#v, %v", metadata, err)
	}
	fetchedAt := time.Now().Add(-age).UTC().Truncate(time.Second)
	metadata.CreatedAt = fetchedAt
	if err := cachepkg.WriteMetadata(entry, *metadata); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}
	return fetchedAt
}

func TestDefaultAcquirerRecordsChartFetchTime(t *testing.T) {
	server := newMaxAgeChartServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.server.Client()}
	request := server.request()

	before := time.Now().Add(-time.Second)
	fetched, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if fetched.FromCache || fetched.CacheKey != mustCacheKey(t, request) || fetched.FetchedAt.Before(before) || fetched.Stale {
		t.Fatalf("fetched result = %#v", fetched)
	}
	stored, ok := EntryFetchedAt(cacheDir, request.Kind, fetched.CacheKey)
	if !ok || !stored.Equal(fetched.FetchedAt) {
		t.Fatalf("EntryFetchedAt() = %v, %v; want %v", stored, ok, fetched.FetchedAt)
	}

	// A cache hit reports the original fetch time, not the access time.
	time.Sleep(10 * time.Millisecond)
	hit, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("cached Acquire() error = %v", err)
	}
	if !hit.FromCache || !hit.FetchedAt.Equal(fetched.FetchedAt) || hit.Stale {
		t.Fatalf("cached result = %#v, want fetch time %v", hit, fetched.FetchedAt)
	}
	if server.downloads != 1 {
		t.Fatalf("downloads = %d, want 1", server.downloads)
	}
}

// acquireChecked acquires request and checks whether it came from the cache,
// the download count so far, and the values the chart carries.
func acquireChecked(t *testing.T, server *maxAgeChartServer, opts Options, fromCache bool, downloads int, values string) Result {
	t.Helper()
	acquirer := DefaultAcquirer{Client: server.server.Client()}
	result, err := acquirer.Acquire(context.Background(), server.request(), opts)
	if err != nil {
		t.Fatalf("Acquire(%#v) error = %v", opts, err)
	}
	if result.FromCache != fromCache || server.downloads != downloads || readChartValues(t, result) != values {
		t.Fatalf("Acquire(%#v) = %#v (%s), downloads = %d; want FromCache %v, %d downloads, %q", opts, result, readChartValues(t, result), server.downloads, fromCache, downloads, values)
	}
	return result
}

func TestDefaultAcquirerRefetchesChartOlderThanMaxAge(t *testing.T) {
	server := newMaxAgeChartServer(t)
	cacheDir := t.TempDir()
	request := server.request()

	acquireChecked(t, server, Options{CacheDir: cacheDir}, false, 1, "content: first")
	old := ageChartEntry(t, cacheDir, request, 48*time.Hour)
	server.content = "republished"

	// Without a max age the entry never expires.
	kept := acquireChecked(t, server, Options{CacheDir: cacheDir}, true, 1, "content: first")
	if !kept.FetchedAt.Equal(old) || kept.Stale {
		t.Fatalf("max-age 0 result = %#v, want fetch time %v", kept, old)
	}
	// Within the max age the entry is still served.
	acquireChecked(t, server, Options{CacheDir: cacheDir, MaxAge: 72 * time.Hour}, true, 1, "content: first")

	refetched := acquireChecked(t, server, Options{CacheDir: cacheDir, MaxAge: 24 * time.Hour}, false, 2, "content: republished")
	if refetched.Stale || !refetched.FetchedAt.After(old) {
		t.Fatalf("refetched result = %#v, want a fresh fetch time after %v", refetched, old)
	}
	stored, ok := EntryFetchedAt(cacheDir, request.Kind, refetched.CacheKey)
	if !ok || !stored.Equal(refetched.FetchedAt) {
		t.Fatalf("EntryFetchedAt() = %v, %v; want %v", stored, ok, refetched.FetchedAt)
	}
}

func TestDefaultAcquirerOfflineReusesExpiredChartAsStale(t *testing.T) {
	server := newMaxAgeChartServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.server.Client()}
	request := server.request()

	if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir}); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	old := ageChartEntry(t, cacheDir, request, 48*time.Hour)

	stale, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true, MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	if !stale.FromCache || !stale.Stale || !stale.FetchedAt.Equal(old) || server.downloads != 1 {
		t.Fatalf("offline result = %#v, downloads = %d; want the stale cached chart", stale, server.downloads)
	}
	// Reuse keeps the original fetch time.
	if stored, ok := EntryFetchedAt(cacheDir, request.Kind, stale.CacheKey); !ok || !stored.Equal(old) {
		t.Fatalf("EntryFetchedAt() = %v, %v; want %v", stored, ok, old)
	}
}

func TestDefaultAcquirerChartWithoutMetadataIsExpired(t *testing.T) {
	server := newMaxAgeChartServer(t)
	cacheDir := t.TempDir()
	acquirer := DefaultAcquirer{Client: server.server.Client()}
	request := server.request()

	first, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	entry := cachepkg.ChartEntryPath(cacheDir, string(request.Kind), first.CacheKey)
	if err := os.Remove(cachepkg.MetadataPath(entry)); err != nil {
		t.Fatalf("remove metadata: %v", err)
	}

	stale, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, Offline: true, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	if !stale.Stale || !stale.FetchedAt.IsZero() {
		t.Fatalf("offline result = %#v, want stale with unknown fetch time", stale)
	}
	if _, ok := EntryFetchedAt(cacheDir, request.Kind, first.CacheKey); ok {
		t.Fatal("offline reuse of an unknown-age chart wrote a fetch time")
	}

	refetched, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if refetched.FromCache || refetched.FetchedAt.IsZero() || server.downloads != 2 {
		t.Fatalf("result = %#v, downloads = %d; want a refetch of the unknown-age chart", refetched, server.downloads)
	}
}

// A run without a max age that reuses an entry of unknown age backfills its
// metadata for last-use tracking, but must not date the old content as
// freshly fetched: a later max-age run still refetches it.
func TestDefaultAcquirerDefaultReuseKeepsUnknownFetchTime(t *testing.T) {
	server := newMaxAgeChartServer(t)
	cacheDir := t.TempDir()
	request := server.request()

	first := acquireChecked(t, server, Options{CacheDir: cacheDir}, false, 1, "content: first")
	entry := cachepkg.ChartEntryPath(cacheDir, string(request.Kind), first.CacheKey)
	if err := os.Remove(cachepkg.MetadataPath(entry)); err != nil {
		t.Fatalf("remove metadata: %v", err)
	}

	reused := acquireChecked(t, server, Options{CacheDir: cacheDir}, true, 1, "content: first")
	if !reused.FetchedAt.IsZero() || reused.Stale {
		t.Fatalf("default reuse = %#v, want unknown fetch time and not stale", reused)
	}
	metadata, err := cachepkg.ReadMetadata(entry, cachepkg.SourceChart, string(request.Kind), first.CacheKey)
	if err != nil || metadata == nil || !metadata.FetchTimeUnknown || metadata.UpdatedAt.IsZero() {
		t.Fatalf("backfilled metadata = %#v, %v; want last use recorded with an unknown fetch time", metadata, err)
	}
	if _, ok := EntryFetchedAt(cacheDir, request.Kind, first.CacheKey); ok {
		t.Fatal("default reuse of an unknown-age chart recorded a fetch time")
	}

	server.content = "republished"
	refetched := acquireChecked(t, server, Options{CacheDir: cacheDir, MaxAge: time.Hour}, false, 2, "content: republished")
	stored, ok := EntryFetchedAt(cacheDir, request.Kind, refetched.CacheKey)
	if !ok || !stored.Equal(refetched.FetchedAt) {
		t.Fatalf("EntryFetchedAt() after refetch = %v, %v; want %v", stored, ok, refetched.FetchedAt)
	}
	acquireChecked(t, server, Options{CacheDir: cacheDir, MaxAge: time.Hour}, true, 2, "content: republished")
}

func TestChartExpired(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		fetchedAt time.Time
		known     bool
		maxAge    time.Duration
		want      bool
	}{
		{fetchedAt: now.Add(-48 * time.Hour), known: true, maxAge: 0, want: false},
		{fetchedAt: time.Time{}, known: false, maxAge: 0, want: false},
		{fetchedAt: time.Time{}, known: false, maxAge: time.Hour, want: true},
		{fetchedAt: now.Add(-time.Hour), known: true, maxAge: time.Hour, want: false},
		{fetchedAt: now.Add(-time.Hour - time.Second), known: true, maxAge: time.Hour, want: true},
	} {
		t.Run(fmt.Sprintf("%v-%v-%v", tc.fetchedAt, tc.known, tc.maxAge), func(t *testing.T) {
			if got := Expired(tc.fetchedAt, tc.known, tc.maxAge, now); got != tc.want {
				t.Fatalf("Expired() = %v, want %v", got, tc.want)
			}
		})
	}
}
