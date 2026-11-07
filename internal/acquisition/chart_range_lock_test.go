package acquisition

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/chart"
)

func rangeLockChartArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte("apiVersion: v2\nname: demo\nversion: 1.2.5\n")
	if err := tw.WriteHeader(&tar.Header{Name: "demo/Chart.yaml", Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestChartAcquirerSerializesRangesThatResolveToOneVersion pins that
// requests whose versions resolve to one chart version, such as 1.2.*,
// ~1.2.0, and 1.2.5, share one chart cache entry and never publish it at
// once. The session lock is on the version as written, so the acquirer
// locks the resolved entry: the archive is fetched once, and every request
// succeeds.
func TestChartAcquirerSerializesRangesThatResolveToOneVersion(t *testing.T) {
	archive := rangeLockChartArchive(t)
	var archiveRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.yaml" {
			_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  demo:\n    - version: 1.2.5\n      urls: [demo-1.2.5.tgz]\n"))
			return
		}
		archiveRequests.Add(1)
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	cacheDir := t.TempDir()
	session := Session{Locks: NewTargetLocks()}
	acquirer := session.ChartAcquirer(chart.DefaultAcquirer{Client: server.Client()})
	versions := []string{"1.2.*", "~1.2.0", "1.2.5", ">=1.2.0 <1.3.0"}
	var wg sync.WaitGroup
	errs := make([]error, len(versions))
	for i, version := range versions {
		wg.Add(1)
		go func(i int, version string) {
			defer wg.Done()
			_, errs[i] = acquirer.Acquire(context.Background(), chart.Request{Repository: server.URL, Name: "demo", Version: version, Kind: chart.RepositoryHTTP, VersionPolicy: chart.VersionArgoTargetRevision}, chart.Options{CacheDir: cacheDir})
		}(i, version)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Acquire(%q) error = %v", versions[i], err)
		}
	}
	if got := archiveRequests.Load(); got != 1 {
		t.Fatalf("archive requests = %d, want 1", got)
	}
}

// refreshRaceChartArchive is a chart archive with many files, so that a
// snapshot copy takes long enough to overlap another refresh.
func refreshRaceChartArchive(t *testing.T, files int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add("demo/Chart.yaml", []byte("apiVersion: v2\nname: demo\nversion: 1.2.5\n"))
	for i := range files {
		add(fmt.Sprintf("demo/files/f%05d.txt", i), []byte("x"))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestChartAcquirerRefreshesAResolvedEntryOncePerSession pins
// --refresh-charts with snapshots for requests whose versions resolve to
// one cache entry: the session refreshes the entry once and copies it
// under the entry lock, so no request copies an entry that another one
// replaces. Each run fetches the archive once, and every request succeeds.
func TestChartAcquirerRefreshesAResolvedEntryOncePerSession(t *testing.T) {
	archive := refreshRaceChartArchive(t, 1000)
	var archiveRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.yaml" {
			_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  demo:\n    - version: 1.2.5\n      urls: [demo-1.2.5.tgz]\n"))
			return
		}
		archiveRequests.Add(1)
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	const runs = 5
	for run := range runs {
		cacheDir := t.TempDir()
		session := Session{Locks: NewTargetLocks(), SnapshotRoot: t.TempDir(), SnapshotCacheReads: true, SnapshotCache: NewSnapshotCache()}
		acquirer := session.ChartAcquirer(chart.DefaultAcquirer{Client: server.Client()})
		versions := []string{"1.2.*", "1.2.5", "~1.2.0", "1.2"}
		var wg sync.WaitGroup
		errs := make([]error, len(versions))
		for i, version := range versions {
			wg.Add(1)
			go func(i int, version string) {
				defer wg.Done()
				_, errs[i] = acquirer.Acquire(context.Background(), chart.Request{Repository: server.URL, Name: "demo", Version: version, Kind: chart.RepositoryHTTP, VersionPolicy: chart.VersionArgoTargetRevision}, chart.Options{CacheDir: cacheDir, Refresh: true})
			}(i, version)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("run %d Acquire(%q) error = %v", run, versions[i], err)
			}
		}
	}
	if got := archiveRequests.Load(); got != runs {
		t.Fatalf("archive requests = %d, want one per run (%d)", got, runs)
	}
}
