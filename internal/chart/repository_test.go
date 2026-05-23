package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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

func TestDefaultAcquirerOfflineRequiresCacheHit(t *testing.T) {
	acquirer := DefaultAcquirer{Client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("offline Acquire() made a network request")
			return nil, nil
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
			if !strings.Contains(err.Error(), "authenticated chart repositories are not supported yet") {
				t.Fatalf("Acquire() error = %q, want auth unsupported error", err)
			}
		})
	}
}

func TestDefaultAcquirerRejectsUnsupportedKindBeforeCacheHit(t *testing.T) {
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
			t.Fatal("unsupported kind Acquire() made a network request")
			return nil, nil
		}),
	}}
	_, err := acquirer.Acquire(context.Background(), request, Options{CacheDir: cacheDir})
	if err == nil {
		t.Fatal("Acquire() error = nil, want unsupported kind error")
	}
	if !strings.Contains(err.Error(), "unsupported chart repository kind") {
		t.Fatalf("Acquire() error = %q, want unsupported kind error", err)
	}
}

func TestExtractChartArchiveRejectsUnsafePathsBeforeCleaning(t *testing.T) {
	for _, name := range []string{"demo/../demo/values.yaml", "../evil"} {
		t.Run(name, func(t *testing.T) {
			err := extractChartArchive(bytes.NewReader(rawChartArchive(t, map[string]string{
				"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
				name:              "bad\n",
			})), t.TempDir())
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
