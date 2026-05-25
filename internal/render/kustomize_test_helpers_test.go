package render

import (
	"context"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/remote"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeChartAcquirer struct {
	chartDir  string
	fromCache bool
	requests  []chart.Request
	options   []chart.Options
}

func (acquirer *fakeChartAcquirer) Acquire(_ context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	return chart.Result{
		ChartDir:   acquirer.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  acquirer.fromCache,
	}, nil
}

type fakeRemoteAcquirer struct {
	requests  []remote.Request
	options   []remote.Options
	path      string
	paths     map[string]string
	fromCache bool
	release   func()
	err       error
}

func (acquirer *fakeRemoteAcquirer) Acquire(_ context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return remote.Result{}, acquirer.err
	}
	acquiredPath := acquirer.path
	if acquirer.paths != nil {
		for _, key := range []string{request.RepoURL, request.URL} {
			if key == "" {
				continue
			}
			if path, ok := acquirer.paths[key]; ok {
				acquiredPath = path
				break
			}
		}
	}
	return remote.Result{Path: acquiredPath, URL: request.URL, Revision: request.Revision, FromCache: acquirer.fromCache, Release: acquirer.release}, nil
}
func writeNamedTestChart(t *testing.T, chartDir, name, version, template string) {
	t.Helper()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), `
apiVersion: v2
name: `+name+`
version: `+version+`
`)
	writeFile(t, filepath.Join(chartDir, "templates", "manifest.yaml"), template)
}
func writeTestChart(t *testing.T, chartDir, template string) {
	t.Helper()
	writeNamedTestChart(t, chartDir, "demo", "1.2.3", template)
}
func filterObjects(manifests []Manifest, kind string) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, manifest := range manifests {
		if manifest.Object != nil && manifest.Object.GetKind() == kind {
			out = append(out, manifest.Object)
		}
	}
	return out
}
func containsManifest(manifests []Manifest, kind, name string) bool {
	return findManifest(manifests, kind, name) != nil
}
func hasRenderCacheEvent(events []cacheevent.Event, source, action, targetFragment string) bool {
	for _, event := range events {
		if string(event.Source) == source && string(event.Action) == action && strings.Contains(event.Target, targetFragment) {
			return true
		}
	}
	return false
}
func findManifest(manifests []Manifest, kind, name string) *unstructured.Unstructured {
	for _, manifest := range manifests {
		if manifest.Object != nil && manifest.Object.GetKind() == kind && manifest.Object.GetName() == name {
			return manifest.Object
		}
	}
	return nil
}
func containsConfigMapData(manifests []Manifest, key, want string) bool {
	for _, configMap := range filterObjects(manifests, "ConfigMap") {
		got, _, _ := unstructured.NestedString(configMap.Object, "data", key)
		if got == want {
			return true
		}
	}
	return false
}
func assertConfigMapData(t *testing.T, manifests []Manifest, key, want string) {
	t.Helper()
	configMaps := filterObjects(manifests, "ConfigMap")
	if len(configMaps) != 1 {
		t.Fatalf("len(configMaps) = %d, want 1", len(configMaps))
	}
	got, _, _ := unstructured.NestedString(configMaps[0].Object, "data", key)
	if got != want {
		t.Fatalf("ConfigMap data[%q] = %q, want %q", key, got, want)
	}
}
func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", path, err)
	}
}
func assertKustomizeRenderErrorContains(t *testing.T, kustomization, want string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), kustomization)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{})
	if err == nil {
		t.Fatalf("Render() error = nil, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Render() error = %v, want error containing %q", err, want)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}
