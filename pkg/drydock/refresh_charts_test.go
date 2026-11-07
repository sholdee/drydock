package drydock

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func refreshChartsArchive(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(n, d string) {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(d)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(d))
	}
	add("demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.2.3\n")
	add("demo/templates/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"\n")
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// TestRefreshChartsRefetchesOnEveryRender pins that RefreshCharts refreshes
// a chart cache entry once per Render call, not once per process: the
// second Render in the same process fetches the chart again and renders
// the new content.
func TestRefreshChartsRefetchesOnEveryRender(t *testing.T) {
	var fetches atomic.Int32
	var gen atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.yaml" {
			_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  demo:\n    - name: demo\n      version: 1.2.3\n      urls: [demo-1.2.3.tgz]\n"))
			return
		}
		fetches.Add(1)
		if gen.Load() == 0 {
			_, _ = w.Write(refreshChartsArchive(t, "first"))
		} else {
			_, _ = w.Write(refreshChartsArchive(t, "second"))
		}
	}))
	defer server.Close()
	root := t.TempDir()
	writeAPIFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: `+server.URL+`
    chart: demo
    targetRevision: 1.2.3
  destination:
    name: in-cluster
    namespace: default
`)
	off := false
	cacheDir := t.TempDir()
	for i, want := range []string{"first", "second"} {
		gen.Store(int32(i))
		res, err := Render(context.Background(), Config{Path: root, AllowNetwork: true, ChartCacheDir: cacheDir, RefreshCharts: true, RenderCache: RenderCacheOptions{Enabled: &off}})
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		names := make([]string, 0, len(res.Manifests))
		for _, m := range res.Manifests {
			md, _ := m.Object["metadata"].(map[string]any)
			name, _ := md["name"].(string)
			names = append(names, name)
		}
		if len(names) != 1 || names[0] != want || fetches.Load() != int32(i+1) {
			t.Fatalf("render %d: manifests = %v after %d archive fetches, want %s after %d", i, names, fetches.Load(), want, i+1)
		}
	}
}

// TestDiffRefreshChartsFetchesEachChartOnce pins that both sides of one
// diff share the refreshed chart cache entries: with RefreshCharts, a chart
// that both sides render is fetched once.
func TestDiffRefreshChartsFetchesEachChartOnce(t *testing.T) {
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.yaml" {
			_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  demo:\n    - name: demo\n      version: 1.2.3\n      urls: [demo-1.2.3.tgz]\n"))
			return
		}
		fetches.Add(1)
		_, _ = w.Write(refreshChartsArchive(t, "chart"))
	}))
	defer server.Close()
	app := func(namespace string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: ` + server.URL + `
    chart: demo
    targetRevision: 1.2.3
  destination:
    name: in-cluster
    namespace: ` + namespace + `
`
	}
	orig := t.TempDir()
	head := t.TempDir()
	writeAPIFile(t, filepath.Join(orig, "apps", "chart.yaml"), app("before"))
	writeAPIFile(t, filepath.Join(head, "apps", "chart.yaml"), app("after"))
	off := false
	if _, err := DiffApplications(context.Background(), Config{PathOrig: orig, Path: head, AllowNetwork: true, ChartCacheDir: t.TempDir(), RefreshCharts: true, RenderCache: RenderCacheOptions{Enabled: &off}}); err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("archive fetches = %d, want one for both sides", got)
	}
}
