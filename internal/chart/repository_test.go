package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	cachepkg "github.com/sholdee/drydock/internal/cache"
)

func TestDefaultAcquirerFetchesHTTPChartAndCachesIt(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml":  "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"values.yaml": "replicaCount: 1\n",
	})
	archiveRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			fmt.Fprintf(w, `apiVersion: v1
entries:
  demo:
    - version: 1.2.3
      urls:
        - demo-1.2.3.tgz
`)
		case "/demo-1.2.3.tgz":
			archiveRequests++
			w.Header().Set("Content-Type", "application/gzip")
			if _, err := w.Write(archive); err != nil {
				t.Fatalf("write archive response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	request := Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}
	opts := Options{CacheDir: t.TempDir()}
	acquirer := DefaultAcquirer{Client: server.Client()}

	first, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.FromCache {
		t.Fatal("first Acquire() FromCache = true, want false")
	}
	if got, want := first.ChartDir, filepath.Join(opts.CacheDir, string(request.Kind), mustCacheKey(t, request), request.Name); got != want {
		t.Fatalf("first ChartDir = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(first.ChartDir, "Chart.yaml")); err != nil {
		t.Fatalf("stat extracted Chart.yaml: %v", err)
	}

	second, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if !second.FromCache {
		t.Fatal("second Acquire() FromCache = false, want true")
	}
	if second.ChartDir != first.ChartDir {
		t.Fatalf("second ChartDir = %q, want %q", second.ChartDir, first.ChartDir)
	}
	if archiveRequests != 1 {
		t.Fatalf("archive requests = %d, want 1", archiveRequests)
	}
}

func TestDefaultAcquirerWritesChartMetadata(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			fmt.Fprintf(w, `apiVersion: v1
entries:
  demo:
    - version: 1.2.3
      urls:
        - demo-1.2.3.tgz
`)
		case "/demo-1.2.3.tgz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	request := Request{Repository: server.URL, Name: "demo", Version: "1.2.3", Kind: RepositoryHTTP}
	result, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	key := mustCacheKey(t, request)
	metadata, err := cachepkg.ReadMetadata(filepath.Dir(result.ChartDir), cachepkg.SourceChart, string(request.Kind), key)
	if err != nil {
		t.Fatalf("ReadMetadata() error = %v", err)
	}
	if metadata == nil {
		t.Fatal("metadata = nil, want metadata")
		return
	}
	if strings.Contains(metadata.Target, "secret") || strings.Contains(metadata.Target, "?") || strings.Contains(metadata.Target, "#") {
		t.Fatalf("metadata Target = %q, want redacted target", metadata.Target)
	}
	if metadata.Name != request.Name || metadata.Version != request.Version {
		t.Fatalf("metadata = %#v, want chart name/version", metadata)
	}
}

func TestDefaultAcquirerWritesChartMetadataOnCacheHit(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			fmt.Fprintf(w, `apiVersion: v1
entries:
  demo:
    - version: 1.2.3
      urls:
        - demo-1.2.3.tgz
`)
		case "/demo-1.2.3.tgz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	request := Request{Repository: server.URL, Name: "demo", Version: "1.2.3", Kind: RepositoryHTTP}
	acquirer := DefaultAcquirer{Client: server.Client()}
	first, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	metadataPath := cachepkg.MetadataPath(filepath.Dir(first.ChartDir))
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove(metadata) error = %v", err)
	}

	second, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: filepath.Dir(filepath.Dir(filepath.Dir(first.ChartDir))), Offline: true})
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

func TestDefaultAcquirerFetchesAuthenticatedHTTPChart(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/index.yaml":
			writeIndex(t, w, "demo-1.2.3.tgz")
		case "/demo-1.2.3.tgz":
			if _, err := w.Write(archive); err != nil {
				t.Fatalf("write archive response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	result, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: ChartCredentials{
			Username: "user",
			Password: "pass",
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.ChartDir, "Chart.yaml")); err != nil {
		t.Fatalf("stat extracted Chart.yaml: %v", err)
	}
}

func TestDefaultAcquirerPassCredentialsWithholdsCredentialsForCrossHostChartArchive(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("archive Authorization = %q, want empty for cross-host archive", got)
		}
		if r.URL.Path != "/archives/demo-1.2.3.tgz" {
			t.Fatalf("archive request path = %q, want /archives/demo-1.2.3.tgz", r.URL.Path)
		}
		if _, err := w.Write(archive); err != nil {
			t.Fatalf("write archive response: %v", err)
		}
	}))
	t.Cleanup(archiveServer.Close)
	indexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("index Authorization = %q, want %q", got, wantAuth)
		}
		writeIndex(t, w, archiveServer.URL+"/archives/demo-1.2.3.tgz")
	}))
	t.Cleanup(indexServer.Close)

	result, err := (DefaultAcquirer{Client: indexServer.Client()}).Acquire(context.Background(), Request{
		Repository: indexServer.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: ChartCredentials{
			Username: "user",
			Password: "pass",
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.ChartDir, "Chart.yaml")); err != nil {
		t.Fatalf("stat extracted Chart.yaml: %v", err)
	}
}

func TestDefaultAcquirerPassCredentialsForwardsCredentialsForCrossHostChartArchive(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("archive Authorization = %q, want %q", got, wantAuth)
		}
		if _, err := w.Write(archive); err != nil {
			t.Fatalf("write archive response: %v", err)
		}
	}))
	t.Cleanup(archiveServer.Close)
	indexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("index Authorization = %q, want %q", got, wantAuth)
		}
		writeIndex(t, w, archiveServer.URL+"/demo-1.2.3.tgz")
	}))
	t.Cleanup(indexServer.Close)

	result, err := (DefaultAcquirer{Client: indexServer.Client()}).Acquire(context.Background(), Request{
		Repository: indexServer.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{
		CacheDir:        t.TempDir(),
		PassCredentials: true,
		Credentials: ChartCredentials{
			Username: "user",
			Password: "pass",
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.ChartDir, "Chart.yaml")); err != nil {
		t.Fatalf("stat extracted Chart.yaml: %v", err)
	}
}

func TestDefaultAcquirerUsesExactSemverChartVersion(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			fmt.Fprint(w, `apiVersion: v1
entries:
  demo:
    - version: 1.2.10
      urls:
        - demo-1.2.10.tgz
    - version: 1.2.3
      urls:
        - demo-1.2.3.tgz
`)
		case "/demo-1.2.3.tgz":
			if _, err := w.Write(archive); err != nil {
				t.Fatalf("write archive response: %v", err)
			}
		case "/demo-1.2.10.tgz":
			t.Fatal("Acquire() fetched 1.2.10, want exact 1.2.3")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
}

func TestDefaultAcquirerMissingVersionDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			t.Fatalf("request path = %q, want /index.yaml", r.URL.Path)
		}
		writeIndex(t, w, "demo-1.2.3.tgz")
	}))
	t.Cleanup(server.Close)

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "9.9.9",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want missing version error")
	}
	if !strings.Contains(err.Error(), "chart demo version 9.9.9 not found") {
		t.Fatalf("Acquire() error = %q, want missing version diagnostic", err)
	}
}

func TestDefaultAcquirerBearerTokenTakesPrecedenceOverBasicAuth(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	const wantAuth = "Bearer token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("Authorization = %q, want %q", got, wantAuth)
		}
		switch r.URL.Path {
		case "/index.yaml":
			writeIndex(t, w, "demo-1.2.3.tgz")
		case "/demo-1.2.3.tgz":
			if _, err := w.Write(archive); err != nil {
				t.Fatalf("write archive response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: ChartCredentials{
			Username:    "user",
			Password:    "pass",
			BearerToken: "token",
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
}

func TestDefaultAcquirerRedactsChartCredentials(t *testing.T) {
	_, err := (DefaultAcquirer{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("boom chart-user chart-pass chart-token")
	})}}).Acquire(context.Background(), Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: ChartCredentials{
			Username:    "chart-user",
			Password:    "chart-pass",
			BearerToken: "chart-token",
		},
	})
	if err == nil {
		t.Fatal("Acquire() error = nil, want fetch error")
	}
	for _, leaked := range []string{"chart-pass", "chart-token"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Acquire() error = %q, leaked %q", err, leaked)
		}
	}
}

func TestDefaultAcquirerRefreshBypassesCache(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	archiveRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			writeIndex(t, w, "demo-1.2.3.tgz")
		case "/demo-1.2.3.tgz":
			archiveRequests++
			if _, err := w.Write(archive); err != nil {
				t.Fatalf("write archive response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	request := Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}
	acquirer := DefaultAcquirer{Client: server.Client()}
	opts := Options{CacheDir: t.TempDir()}
	if _, err := acquirer.Acquire(context.Background(), request, opts); err != nil {
		t.Fatalf("initial Acquire() error = %v", err)
	}
	refreshed, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: opts.CacheDir, Refresh: true})
	if err != nil {
		t.Fatalf("refresh Acquire() error = %v", err)
	}
	if refreshed.FromCache {
		t.Fatal("refresh Acquire() FromCache = true, want false")
	}
	if archiveRequests != 2 {
		t.Fatalf("archive requests = %d, want 2", archiveRequests)
	}
}

func TestDefaultAcquirerSupportsAbsoluteChartURLs(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/absolute/demo-1.2.3.tgz" {
			t.Fatalf("archive request path = %q, want /absolute/demo-1.2.3.tgz", r.URL.Path)
		}
		if _, err := w.Write(archive); err != nil {
			t.Fatalf("write archive response: %v", err)
		}
	}))
	t.Cleanup(archiveServer.Close)
	indexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			t.Fatalf("index request path = %q, want /index.yaml", r.URL.Path)
		}
		writeIndex(t, w, archiveServer.URL+"/absolute/demo-1.2.3.tgz")
	}))
	t.Cleanup(indexServer.Close)

	result, err := (DefaultAcquirer{Client: indexServer.Client()}).Acquire(context.Background(), Request{
		Repository: indexServer.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.FromCache {
		t.Fatal("Acquire() FromCache = true, want false")
	}
	if _, err := os.Stat(filepath.Join(result.ChartDir, "Chart.yaml")); err != nil {
		t.Fatalf("stat extracted Chart.yaml: %v", err)
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
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir(), Offline: true})
	if err == nil {
		t.Fatal("Acquire() error = nil, want offline cache miss")
	}
	if !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("Acquire() error = %q, want offline cache miss", err)
	}
}

func TestDefaultAcquirerRejectsHTTPChartCacheInsideForbiddenRootBeforeCacheRead(t *testing.T) {
	repoRoot := t.TempDir()
	request := Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}
	cacheDir := filepath.Join(repoRoot, ".drydock", "charts")
	chartDir := filepath.Join(cacheDir, string(request.Kind), mustCacheKey(t, request), request.Name)
	if err := os.MkdirAll(chartDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\nversion: 1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	acquirer := DefaultAcquirer{Client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("Acquire() made a network request for forbidden cache root")
			return nil, errors.New("unexpected network request")
		}),
	}}
	_, err := acquirer.Acquire(context.Background(), request, Options{
		CacheDir:       cacheDir,
		Offline:        true,
		ForbiddenRoots: []string{repoRoot},
	})
	if err == nil || !strings.Contains(err.Error(), "chart cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Acquire() error = %v, want chart cache containment error", err)
	}
}

func TestDefaultAcquirerRejectsOCIChartCacheInsideForbiddenRootBeforePull(t *testing.T) {
	repoRoot := t.TempDir()
	puller := &fakeOCIPuller{archive: chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})}
	_, err := (DefaultAcquirer{OCIPuller: puller}).Acquire(context.Background(), Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{
		CacheDir:       filepath.Join(repoRoot, ".drydock", "charts"),
		ForbiddenRoots: []string{repoRoot},
	})
	if err == nil || !strings.Contains(err.Error(), "chart cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Acquire() error = %v, want chart cache containment error", err)
	}
	if puller.pulls != 0 {
		t.Fatalf("pull count = %d, want 0", puller.pulls)
	}
}

func TestResolveCacheDirRejectsSymlinkIntoForbiddenRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges are not guaranteed on Windows")
	}
	repoRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(outside, "charts-link")
	if err := os.Symlink(repoRoot, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	_, err := ResolveCacheDir(filepath.Join(link, "charts"), []string{repoRoot})
	if err == nil || !strings.Contains(err.Error(), "chart cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("ResolveCacheDir() error = %v, want symlink containment error", err)
	}
}

func TestDefaultAcquirerMapsIndexAuthFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/index.yaml" {
					t.Fatalf("request path = %q, want /index.yaml", r.URL.Path)
				}
				http.Error(w, "authentication required", status)
			}))
			t.Cleanup(server.Close)

			acquirer := DefaultAcquirer{Client: server.Client()}
			_, err := acquirer.Acquire(context.Background(), Request{
				Repository: server.URL,
				Name:       "demo",
				Version:    "1.2.3",
				Kind:       RepositoryHTTP,
			}, Options{CacheDir: t.TempDir()})
			if err == nil {
				t.Fatal("Acquire() error = nil, want auth unsupported error")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("Acquire() error = %q, want HTTP %d", err, status)
			}
		})
	}
}

func TestDefaultAcquirerMapsArchiveAuthFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/index.yaml":
					writeIndex(t, w, "demo-1.2.3.tgz")
				case "/demo-1.2.3.tgz":
					http.Error(w, "authentication required", status)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
				Repository: server.URL,
				Name:       "demo",
				Version:    "1.2.3",
				Kind:       RepositoryHTTP,
			}, Options{CacheDir: t.TempDir()})
			if err == nil {
				t.Fatal("Acquire() error = nil, want auth unsupported error")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("Acquire() error = %q, want HTTP %d", err, status)
			}
		})
	}
}

func TestDefaultAcquirerIncludesHTTPStatusForIndexErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			t.Fatalf("request path = %q, want /index.yaml", r.URL.Path)
		}
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want HTTP status error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Acquire() error = %q, want HTTP 500", err)
	}
}

func TestDefaultAcquirerIncludesHTTPStatusForArchiveErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			writeIndex(t, w, "demo-1.2.3.tgz")
		case "/demo-1.2.3.tgz":
			http.Error(w, "server error", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want HTTP status error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Acquire() error = %q, want HTTP 500", err)
	}
}

func TestDefaultAcquirerRejectsUnsafeChartNameBeforeCacheMutation(t *testing.T) {
	for _, name := range []string{"../demo", "demo/evil"} {
		t.Run(name, func(t *testing.T) {
			archive := rawChartArchive(t, map[string]string{
				filepath.ToSlash(filepath.Join(name, "Chart.yaml")): "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/index.yaml":
					writeIndexFor(t, w, name, "demo-1.2.3.tgz")
				case "/demo-1.2.3.tgz":
					if _, err := w.Write(archive); err != nil {
						t.Fatalf("write archive response: %v", err)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			request := Request{
				Repository: server.URL,
				Name:       name,
				Version:    "1.2.3",
				Kind:       RepositoryHTTP,
			}
			cacheDir := t.TempDir()
			key := mustCacheKey(t, request)
			sentinelDir := filepath.Join(cacheDir, string(request.Kind), key, "demo")
			if name == "../demo" {
				sentinelDir = filepath.Join(cacheDir, string(request.Kind))
			}
			if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
				t.Fatalf("create sentinel dir: %v", err)
			}
			sentinelPath := filepath.Join(sentinelDir, "sentinel")
			if err := os.WriteFile(sentinelPath, []byte("keep"), 0o600); err != nil {
				t.Fatalf("write sentinel: %v", err)
			}

			_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), request, Options{CacheDir: cacheDir})
			if err == nil {
				t.Fatal("Acquire() error = nil, want invalid chart name error")
			}
			if !strings.Contains(err.Error(), "chart name") {
				t.Fatalf("Acquire() error = %q, want chart name validation error", err)
			}
			if _, err := os.Stat(sentinelPath); err != nil {
				t.Fatalf("sentinel was removed before validation completed: %v", err)
			}
		})
	}
}

func TestDefaultAcquirerRefreshPreservesCacheWhenExtractionFails(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\ndescription: keep\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			writeIndex(t, w, "demo-1.2.3.tgz")
		case "/demo-1.2.3.tgz":
			if _, err := w.Write(archive); err != nil {
				t.Fatalf("write archive response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	request := Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}
	opts := Options{CacheDir: t.TempDir()}
	acquirer := DefaultAcquirer{Client: server.Client()}
	result, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("initial Acquire() error = %v", err)
	}
	chartYAML := filepath.Join(result.ChartDir, "Chart.yaml")
	before, err := os.ReadFile(chartYAML)
	if err != nil {
		t.Fatalf("read cached Chart.yaml: %v", err)
	}

	archive = rawChartArchive(t, map[string]string{
		"demo/Chart.yaml":               "apiVersion: v2\nname: demo\nversion: 1.2.3\ndescription: replace\n",
		"demo/../demo/templates/x.yaml": "bad\n",
	})
	if _, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: opts.CacheDir, Refresh: true}); err == nil {
		t.Fatal("refresh Acquire() error = nil, want unsafe archive error")
	}
	after, err := os.ReadFile(chartYAML)
	if err != nil {
		t.Fatalf("read cached Chart.yaml after failed refresh: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("cached Chart.yaml changed after failed refresh:\n%s", after)
	}
}

func TestDefaultAcquirerRejectsArchiveEntriesOutsideChartRoot(t *testing.T) {
	archive := rawChartArchive(t, map[string]string{
		"demo/Chart.yaml":         "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"other/templates/cm.yaml": "bad\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			writeIndex(t, w, "demo-1.2.3.tgz")
		case "/demo-1.2.3.tgz":
			if _, err := w.Write(archive); err != nil {
				t.Fatalf("write archive response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want chart root mismatch error")
	}
	if !strings.Contains(err.Error(), "outside chart root") {
		t.Fatalf("Acquire() error = %q, want chart root mismatch error", err)
	}
}

func TestDefaultAcquirerRedactsFetchURLs(t *testing.T) {
	t.Run("repository userinfo", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server error", http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)
		repository := strings.Replace(server.URL, "http://", "http://user:pass@", 1)

		_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
			Repository: repository,
			Name:       "demo",
			Version:    "1.2.3",
			Kind:       RepositoryHTTP,
		}, Options{CacheDir: t.TempDir()})
		if err == nil {
			t.Fatal("Acquire() error = nil, want HTTP status error")
		}
		if strings.Contains(err.Error(), "user:") || strings.Contains(err.Error(), "pass") {
			t.Fatalf("Acquire() error leaked userinfo: %q", err)
		}
	})

	t.Run("repository userinfo from transport error", func(t *testing.T) {
		repository := "https://user:pass@charts.example.test"
		acquirer := DefaultAcquirer{Client: &http.Client{
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("blocked %s", request.URL.String())
			}),
		}}

		_, err := acquirer.Acquire(context.Background(), Request{
			Repository: repository,
			Name:       "demo",
			Version:    "1.2.3",
			Kind:       RepositoryHTTP,
		}, Options{CacheDir: t.TempDir()})
		if err == nil {
			t.Fatal("Acquire() error = nil, want fetch error")
		}
		if strings.Contains(err.Error(), "user:") || strings.Contains(err.Error(), "pass") {
			t.Fatalf("Acquire() error leaked userinfo: %q", err)
		}
	})

	t.Run("archive query and fragment", func(t *testing.T) {
		archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server error", http.StatusInternalServerError)
		}))
		t.Cleanup(archiveServer.Close)
		indexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeIndex(t, w, archiveServer.URL+"/demo-1.2.3.tgz?token=secret#frag")
		}))
		t.Cleanup(indexServer.Close)

		_, err := (DefaultAcquirer{Client: indexServer.Client()}).Acquire(context.Background(), Request{
			Repository: indexServer.URL,
			Name:       "demo",
			Version:    "1.2.3",
			Kind:       RepositoryHTTP,
		}, Options{CacheDir: t.TempDir()})
		if err == nil {
			t.Fatal("Acquire() error = nil, want HTTP status error")
		}
		if strings.Contains(err.Error(), "token=secret") || strings.Contains(err.Error(), "frag") {
			t.Fatalf("Acquire() error leaked signed URL details: %q", err)
		}
	})

	t.Run("archive query from transport error", func(t *testing.T) {
		acquirer := DefaultAcquirer{Client: &http.Client{
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/index.yaml":
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body: io.NopCloser(strings.NewReader(`apiVersion: v1
entries:
  demo:
    - version: 1.2.3
      urls:
        - https://charts.example.test/demo-1.2.3.tgz?token=secret#frag
`)),
					}, nil
				default:
					return nil, fmt.Errorf("blocked %s", request.URL.String())
				}
			}),
		}}

		_, err := acquirer.Acquire(context.Background(), Request{
			Repository: "https://charts.example.test",
			Name:       "demo",
			Version:    "1.2.3",
			Kind:       RepositoryHTTP,
		}, Options{CacheDir: t.TempDir()})
		if err == nil {
			t.Fatal("Acquire() error = nil, want fetch error")
		}
		if strings.Contains(err.Error(), "token=secret") || strings.Contains(err.Error(), "frag") {
			t.Fatalf("Acquire() error leaked signed URL details: %q", err)
		}
	})
}

func TestDefaultAcquirerRejectsNonHTTPAbsoluteChartURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			t.Fatalf("request path = %q, want /index.yaml", r.URL.Path)
		}
		writeIndex(t, w, "ftp://charts.example.test/demo-1.2.3.tgz")
	}))
	t.Cleanup(server.Close)

	_, err := (DefaultAcquirer{Client: server.Client()}).Acquire(context.Background(), Request{
		Repository: server.URL,
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryHTTP,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want unsupported scheme error")
	}
	if !strings.Contains(err.Error(), "must use http or https") {
		t.Fatalf("Acquire() error = %q, want unsupported scheme error", err)
	}
}

func TestDefaultAcquirerReturnsCachedOCIChartWithClientAndDefaultPuller(t *testing.T) {
	request := Request{
		Repository: "oci://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}
	cacheDir := t.TempDir()
	chartDir := filepath.Join(cacheDir, string(request.Kind), mustCacheKey(t, request), request.Name)
	if err := os.MkdirAll(chartDir, 0o755); err != nil {
		t.Fatalf("create cached chart dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\nversion: 1.2.3\n"), 0o600); err != nil {
		t.Fatalf("write cached Chart.yaml: %v", err)
	}

	acquirer := DefaultAcquirer{Client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("cached OCI Acquire() made a network request")
			return nil, errors.New("unexpected network request")
		}),
	}}
	result, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("Acquire() error = %v, want cached result", err)
	}
	if !result.FromCache {
		t.Fatal("Acquire() FromCache = false, want true")
	}
	if result.ChartDir != chartDir {
		t.Fatalf("Acquire() ChartDir = %q, want %q", result.ChartDir, chartDir)
	}
}

func TestExtractChartArchiveRejectsUnsafePathsBeforeCleaning(t *testing.T) {
	for _, name := range []string{"demo/../demo/values.yaml", "../evil", "/demo/values.yaml"} {
		t.Run(name, func(t *testing.T) {
			err := extractChartArchive(bytes.NewReader(rawChartArchive(t, map[string]string{
				"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
				name:              "bad\n",
			})), t.TempDir(), "demo")
			if err == nil {
				t.Fatal("extractChartArchive() error = nil, want unsafe path error")
			}
			if !strings.Contains(err.Error(), "unsafe") && !strings.Contains(err.Error(), "escape") {
				t.Fatalf("extractChartArchive() error = %q, want unsafe path rejection", err)
			}
		})
	}
}

func TestChartDirReadyRequiresRegularChartYAML(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		chartDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\n"), 0o600); err != nil {
			t.Fatalf("write Chart.yaml: %v", err)
		}
		if !chartDirReady(chartDir) {
			t.Fatal("chartDirReady() = false, want true for regular Chart.yaml")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		chartDir := t.TempDir()
		target := filepath.Join(t.TempDir(), "Chart.yaml")
		if err := os.WriteFile(target, []byte("apiVersion: v2\n"), 0o600); err != nil {
			t.Fatalf("write symlink target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(chartDir, "Chart.yaml")); err != nil {
			t.Skipf("create symlink: %v", err)
		}
		if chartDirReady(chartDir) {
			t.Fatal("chartDirReady() = true, want false for symlink Chart.yaml")
		}
	})

	t.Run("fifo", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fifo not supported on windows")
		}
		chartDir := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(chartDir, "Chart.yaml"), 0o600); err != nil {
			t.Skipf("create fifo: %v", err)
		}
		if chartDirReady(chartDir) {
			t.Fatal("chartDirReady() = true, want false for fifo Chart.yaml")
		}
	})
}

func chartArchive(t *testing.T, name string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for path, body := range files {
		data := []byte(body)
		if err := tw.WriteHeader(&tar.Header{
			Name: filepath.ToSlash(filepath.Join(name, path)),
			Mode: 0o600,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("write tar header %s: %v", path, err)
		}
		if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
			t.Fatalf("write tar body %s: %v", path, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func writeIndex(t *testing.T, w http.ResponseWriter, chartURL string) {
	t.Helper()
	writeIndexFor(t, w, "demo", chartURL)
}

func writeIndexFor(t *testing.T, w http.ResponseWriter, chartName, chartURL string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/yaml")
	fmt.Fprintf(w, `apiVersion: v1
entries:
  %q:
    - version: 1.2.3
      urls:
        - %s
`, chartName, chartURL)
}

func rawChartArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for path, body := range files {
		data := []byte(body)
		if err := tw.WriteHeader(&tar.Header{
			Name: path,
			Mode: 0o600,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("write tar header %s: %v", path, err)
		}
		if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
			t.Fatalf("write tar body %s: %v", path, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func mustCacheKey(t *testing.T, request Request) string {
	t.Helper()
	key, err := NewCacheKey(request)
	if err != nil {
		t.Fatalf("NewCacheKey() error = %v", err)
	}
	return key
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
