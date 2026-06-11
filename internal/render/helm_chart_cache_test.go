package render

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	helmchart "helm.sh/helm/v4/pkg/chart"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
)

func TestHelmChartLoadCacheReturnsIndependentCharts(t *testing.T) {
	chartDir := writeCacheTestChart(t)
	cache := NewHelmChartLoadCache()
	first, err := cache.Load(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.Load(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("Load must return a fresh chart per call")
	}

	firstChart := mustV2Chart(t, first)
	secondChart := mustV2Chart(t, second)
	firstChart.Metadata.Name = "mutated"
	if secondChart.Metadata.Name == "mutated" {
		t.Fatal("Load returned charts sharing mutable metadata")
	}
	firstChart.Raw[0].Data[0] = 'X'
	if secondChart.Raw[0].Data[0] == 'X' {
		t.Fatal("Load returned charts sharing mutable raw file bytes")
	}
}

func TestHelmChartLoadCacheMatchesDirectLoad(t *testing.T) {
	chartDir := writeCacheTestChart(t)
	cached, err := NewHelmChartLoadCache().Load(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := loadValidatedHelmChart(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	assertChartsEquivalent(t, cached, direct)
}

func BenchmarkHelmRendererSharedChart(b *testing.B) {
	chartDir := writeCacheTestChart(b)
	source := ResolvedSource{RepoRoot: chartDir, Path: "."}
	renderer := HelmRenderer{}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		cache := NewHelmChartLoadCache()
		for app := range 25 {
			manifests, diags, err := renderer.Render(context.Background(), source, RenderOptions{
				AppName:            "demo",
				ReleaseName:        "demo",
				HelmChartLoadCache: cache,
			})
			if err != nil {
				b.Fatal(err)
			}
			if len(diags) != 0 {
				b.Fatalf("diagnostics = %#v, want none", diags)
			}
			if len(manifests) != 2 {
				b.Fatalf("render %d manifests = %d, want 2", app, len(manifests))
			}
		}
	}
}

func writeCacheTestChart(t testing.TB) string {
	t.Helper()
	chartDir := t.TempDir()
	writeCacheTestChartFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeCacheTestChartFile(t, filepath.Join(chartDir, "templates", "manifest.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
`)
	subDir := filepath.Join(chartDir, "charts", "sub")
	writeCacheTestChartFile(t, filepath.Join(subDir, "Chart.yaml"), `apiVersion: v2
name: sub
version: 0.1.0
`)
	writeCacheTestChartFile(t, filepath.Join(subDir, "templates", "manifest.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: sub-cm
`)
	return chartDir
}

func writeCacheTestChartFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertChartsEquivalent(t *testing.T, got, want helmchart.Charter) {
	t.Helper()
	gotChart := mustV2Chart(t, got)
	wantChart := mustV2Chart(t, want)
	if gotChart.Name() != wantChart.Name() {
		t.Fatalf("Name() = %q, want %q", gotChart.Name(), wantChart.Name())
	}
	if gotChart.Metadata.Version != wantChart.Metadata.Version {
		t.Fatalf("Metadata.Version = %q, want %q", gotChart.Metadata.Version, wantChart.Metadata.Version)
	}
	if len(gotChart.Templates) != len(wantChart.Templates) {
		t.Fatalf("len(Templates) = %d, want %d", len(gotChart.Templates), len(wantChart.Templates))
	}
	if len(gotChart.Dependencies()) != len(wantChart.Dependencies()) {
		t.Fatalf("len(Dependencies()) = %d, want %d", len(gotChart.Dependencies()), len(wantChart.Dependencies()))
	}
	if len(gotChart.Raw) != len(wantChart.Raw) {
		t.Fatalf("len(Raw) = %d, want %d", len(gotChart.Raw), len(wantChart.Raw))
	}
}

func mustV2Chart(t *testing.T, chart helmchart.Charter) *chartv2.Chart {
	t.Helper()
	v2chart, ok := chart.(*chartv2.Chart)
	if !ok {
		t.Fatalf("chart = %T, want *chartv2.Chart", chart)
	}
	return v2chart
}
