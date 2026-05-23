package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/chart"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeChartAcquirer struct {
	chartDir string
	requests []chart.Request
	options  []chart.Options
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
	}, nil
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

func TestKustomizeRendererRendersResources(t *testing.T) {
	renderer := KustomizeRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications"),
		Path:     "kustomize",
	}

	result, diags, err := renderer.Render(context.Background(), source, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Object.GetKind() != "ConfigMap" || result[0].Object.GetName() != "kustomized" {
		t.Fatalf("unexpected object: %#v", result[0].Object)
	}
	if result[0].Path != filepath.Join("kustomize", "kustomization.yaml") {
		t.Fatalf("Path = %q, want kustomize/kustomization.yaml", result[0].Path)
	}
}

func writeTestChart(t *testing.T, chartDir, template string) {
	t.Helper()
	writeNamedTestChart(t, chartDir, "demo", "1.2.3", template)
}

func TestKustomizeRendererAllowsRepoRootLocalComponents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "namespace", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - serviceaccount.yaml
`)
	writeFile(t, filepath.Join(root, "components", "namespace", "serviceaccount.yaml"), `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: demo
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: demo
components:
  - ../../components/namespace
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
}

func TestKustomizeRendererRendersHelmChartsWithoutShellout(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "charts", "demo")
	writeTestChart(t, chartDir, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
  labels:
    app.kubernetes.io/name: {{ .Chart.Name }}
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: {{ .Chart.Name }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: {{ .Chart.Name }}
    spec:
      containers:
        - name: app
          image: {{ .Values.image }}
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: demo
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
    valuesFile: values.yaml
patches:
  - target:
      group: apps
      version: v1
      kind: Deployment
      name: demo
    patch: |-
      - op: add
        path: /metadata/labels/patched
        value: "true"
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "values.yaml"), `
image: example/app:v1
`)

	acquirer := &fakeChartAcquirer{chartDir: chartDir}
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: acquirer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("len(acquirer.requests) = %d, want 1", len(acquirer.requests))
	}
	if acquirer.requests[0] != (chart.Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       chart.RepositoryHTTP,
	}) {
		t.Fatalf("acquirer.requests[0] = %#v", acquirer.requests[0])
	}

	deployments := filterObjects(result, "Deployment")
	if len(deployments) != 1 {
		t.Fatalf("len(deployments) = %d, want 1", len(deployments))
	}
	if got := deployments[0].GetLabels()["patched"]; got != "true" {
		t.Fatalf("patched label = %q, want true", got)
	}
	containers, found, err := unstructured.NestedSlice(deployments[0].Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("deployment containers lookup found=%v err=%v", found, err)
	}
	if len(containers) != 1 {
		t.Fatalf("len(containers) = %d, want 1", len(containers))
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("container = %#v, want map", containers[0])
	}
	image, _ := container["image"].(string)
	if image != "example/app:v1" {
		t.Fatalf("deployment image = %q, want example/app:v1", image)
	}
}

func TestKustomizeRendererRendersHelmChartsInReferencedKustomizationWithNamespaceFallback(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "charts", "demo")
	writeTestChart(t, chartDir, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  namespace: {{ .Release.Namespace }}
`)
	writeFile(t, filepath.Join(root, "bases", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: demo
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../bases/demo
`)

	acquirer := &fakeChartAcquirer{chartDir: chartDir}
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: acquirer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("len(acquirer.requests) = %d, want 1", len(acquirer.requests))
	}
	configMaps := filterObjects(result, "ConfigMap")
	if len(configMaps) != 1 {
		t.Fatalf("len(configMaps) = %d, want 1", len(configMaps))
	}
	namespace, _, _ := unstructured.NestedString(configMaps[0].Object, "data", "namespace")
	if namespace != "demo" {
		t.Fatalf("rendered namespace = %q, want demo", namespace)
	}
}

func TestKustomizeRendererInheritsParentNamespaceForNestedHelmCharts(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "charts", "demo")
	writeTestChart(t, chartDir, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  namespace: {{ .Release.Namespace }}
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: overlay
resources:
  - ../../bases/mid
`)
	writeFile(t, filepath.Join(root, "bases", "mid", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../leaf
`)
	writeFile(t, filepath.Join(root, "bases", "leaf", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: &fakeChartAcquirer{chartDir: chartDir}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	assertConfigMapData(t, result, "namespace", "overlay")
}

func TestKustomizeRendererUsesLocalHelmChartByDefaultWithoutAcquisition(t *testing.T) {
	root := t.TempDir()
	writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  source: local
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
`)

	acquirer := &fakeChartAcquirer{chartDir: filepath.Join(root, "unused")}
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: acquirer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 0 {
		t.Fatalf("len(acquirer.requests) = %d, want 0", len(acquirer.requests))
	}
	assertConfigMapData(t, result, "source", "local")
}

func TestKustomizeRendererUsesCustomHelmChartHome(t *testing.T) {
	root := t.TempDir()
	writeTestChart(t, filepath.Join(root, "apps", "demo", "vendor", "demo"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  source: custom
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmGlobals:
  chartHome: vendor
helmCharts:
  - name: demo
    releaseName: demo
`)

	acquirer := &fakeChartAcquirer{chartDir: filepath.Join(root, "unused")}
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: acquirer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 0 {
		t.Fatalf("len(acquirer.requests) = %d, want 0", len(acquirer.requests))
	}
	assertConfigMapData(t, result, "source", "custom")
}

func TestKustomizeRendererPrefersVersionedLocalHelmChartOverRepo(t *testing.T) {
	root := t.TempDir()
	writeNamedTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo-1.2.3", "demo"), "demo", "1.2.3", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  source: versioned-local
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
`)

	acquirer := &fakeChartAcquirer{chartDir: filepath.Join(root, "unused")}
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: acquirer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 0 {
		t.Fatalf("len(acquirer.requests) = %d, want 0", len(acquirer.requests))
	}
	assertConfigMapData(t, result, "source", "versioned-local")
}

func TestKustomizeRendererPropagatesOCIChartAcquisitionOptions(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "charts", "demo")
	writeTestChart(t, chartDir, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: oci://registry.example.test/charts
    version: 1.2.3
    releaseName: demo
`)

	acquirer := &fakeChartAcquirer{chartDir: chartDir}
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		ChartAcquirer: acquirer,
		ChartCacheDir: filepath.Join(root, "cache"),
		OfflineCharts: true,
		RefreshCharts: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("len(acquirer.requests) = %d, want 1", len(acquirer.requests))
	}
	if acquirer.requests[0].Kind != chart.RepositoryOCI {
		t.Fatalf("request kind = %q, want %q", acquirer.requests[0].Kind, chart.RepositoryOCI)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("len(acquirer.options) = %d, want 1", len(acquirer.options))
	}
	if acquirer.options[0] != (chart.Options{
		CacheDir: filepath.Join(root, "cache"),
		Offline:  true,
		Refresh:  true,
	}) {
		t.Fatalf("acquirer.options[0] = %#v", acquirer.options[0])
	}
}

func TestKustomizeRendererPropagatesValuesInlineMergeMode(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "charts", "demo")
	writeTestChart(t, chartDir, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  image: {{ .Values.image }}
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "values.yaml"), `
image: from-file
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
    valuesFile: values.yaml
    valuesInline:
      image: inline-default
    valuesMerge: merge
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{ChartAcquirer: &fakeChartAcquirer{chartDir: chartDir}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	assertConfigMapData(t, result, "image", "from-file")
}

func TestKustomizeRendererRejectsUnsupportedHelmFields(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		want          string
	}{
		{
			name: "nameTemplate",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    nameTemplate: demo-{{ randAlpha 5 }}
`,
			want: "nameTemplate",
		},
		{
			name: "devel",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    devel: true
`,
			want: "devel",
		},
		{
			name: "debug",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    debug: true
`,
			want: "debug",
		},
		{
			name: "configHome",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmGlobals:
  configHome: helm-config
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
`,
			want: "configHome",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertKustomizeRenderErrorContains(t, tt.kustomization, tt.want)
		})
	}
}

func TestKustomizeRendererRejectsDeprecatedHelmChartInflationGenerator(t *testing.T) {
	assertKustomizeRenderErrorContains(t, `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmChartInflationGenerator:
  - chartName: demo
    chartRepoUrl: https://charts.example.test
    chartVersion: 1.2.3
`, "helmChartInflationGenerator")
}

func TestCopyRegularTreeSkipsGitFilesAndDirectories(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")
	writeFile(t, filepath.Join(src, ".git"), "gitdir: ../main/.git/worktrees/demo\n")
	writeFile(t, filepath.Join(src, "nested", ".git", "config"), "[core]\n")
	writeFile(t, filepath.Join(src, "nested", "manifest.yaml"), "apiVersion: v1\nkind: ConfigMap\n")

	if err := copyRegularTree(src, dst); err != nil {
		t.Fatalf("copyRegularTree() error = %v", err)
	}
	assertPathMissing(t, filepath.Join(dst, ".git"))
	assertPathMissing(t, filepath.Join(dst, "nested", ".git"))
	if _, err := os.Stat(filepath.Join(dst, "nested", "manifest.yaml")); err != nil {
		t.Fatalf("copied manifest Stat() error = %v", err)
	}
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

func TestKustomizeRendererRejectsSourcePathEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)

	renderer := KustomizeRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     ".." + string(filepath.Separator) + "outside",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want path escape error")
	}
	if !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("Render() error = %v, want escape error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestKustomizeRendererRejectsKustomizationGraphEscapes(t *testing.T) {
	for _, tt := range []struct {
		name           string
		kustomization  string
		outsideRelPath string
	}{
		{
			name: "resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../../outside/cm.yaml
`,
			outsideRelPath: filepath.Join("outside", "cm.yaml"),
		},
		{
			name: "base",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - ../../../outside
`,
			outsideRelPath: filepath.Join("outside", "kustomization.yaml"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "repo")
			outside := filepath.Join(parent, tt.outsideRelPath)
			writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), tt.kustomization)
			writeFile(t, outside, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)

			result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     filepath.Join("apps", "demo"),
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want graph escape error")
			}
			if !strings.Contains(err.Error(), "escapes repository root") {
				t.Fatalf("Render() error = %v, want repository root escape error", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if len(result) != 0 {
				t.Fatalf("result = %#v, want no manifests", result)
			}
		})
	}
}

func TestKustomizeRendererRejectsSymlinkedSourcePathComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)
	symlink(t, outside, filepath.Join(root, "apps"))

	renderer := KustomizeRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestKustomizeRendererRejectsSymlinkedGraphEntries(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		linkName      string
		targetFile    string
		targetDir     bool
	}{
		{
			name: "resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - linked.yaml
`,
			linkName:   "linked.yaml",
			targetFile: "cm.yaml",
		},
		{
			name: "base",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - linked-base
`,
			linkName:  "linked-base",
			targetDir: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), tt.kustomization)
			target := outside
			if !tt.targetDir {
				target = filepath.Join(outside, tt.targetFile)
			}
			writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)
			if tt.targetFile != "" {
				writeFile(t, filepath.Join(outside, tt.targetFile), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)
			}
			symlink(t, target, filepath.Join(root, "app", tt.linkName))

			result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want symlink error")
			}
			if !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("Render() error = %v, want symlink error", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if len(result) != 0 {
				t.Fatalf("result = %#v, want no manifests", result)
			}
		})
	}
}

func TestKustomizeRendererRejectsRemoteRefs(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
	}{
		{
			name: "https resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo//base?ref=main
`,
		},
		{
			name: "scp-like git resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - alice@example.com:org/repo.git//base
`,
		},
		{
			name: "scp-like git resource without git suffix",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - alice@example.com:org/repo/base?ref=main
`,
		},
		{
			name: "github host colon resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - github.com:org/repo//base
`,
		},
		{
			name: "file URL resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - file:///tmp/repo//base
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertKustomizeRenderErrorContains(t, tt.kustomization, "remote")
		})
	}
}

func TestKustomizeRendererRejectsRemotePathBearingFields(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
	}{
		{
			name: "openapi path",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
openapi:
  path: https://github.com/example/repo//schema.json?ref=main
resources: []
`,
		},
		{
			name: "configurations",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
configurations:
  - https://github.com/example/repo//name-reference.yaml?ref=main
resources: []
`,
		},
		{
			name: "replacements path",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
replacements:
  - path: https://github.com/example/repo//replacement.yaml?ref=main
resources: []
`,
		},
		{
			name: "generators",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - https://github.com/example/repo//generator.yaml?ref=main
resources: []
`,
		},
		{
			name: "transformers",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
transformers:
  - https://github.com/example/repo//transformer.yaml?ref=main
resources: []
`,
		},
		{
			name: "validators",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
validators:
  - https://github.com/example/repo//validator.yaml?ref=main
resources: []
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertKustomizeRenderErrorContains(t, tt.kustomization, "remote")
		})
	}
}

func TestKustomizeRendererRejectsBuildOptions(t *testing.T) {
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications"),
		Path:     "kustomize",
	}, RenderOptions{
		BuildOptions: []string{"--enable-helm"},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want build options error")
	}
	if !strings.Contains(err.Error(), "build options") {
		t.Fatalf("Render() error = %v, want build options error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
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
