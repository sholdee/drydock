package render

import (
	"context"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"path/filepath"
)

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
func TestKustomizeRendererRecordsHelmChartCacheEvents(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeTestChart(t, chartDir, `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	writeFile(t, filepath.Join(root, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
`)
	recorder := cacheevent.NewRecorder(true)
	_, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "."}, RenderOptions{
		ChartAcquirer:      &fakeChartAcquirer{chartDir: chartDir, fromCache: true},
		CacheEventRecorder: recorder,
	})
	if err != nil {
		t.Fatalf("Render() error = %v, diagnostics = %#v", err, diags)
	}
	if !hasRenderCacheEvent(recorder.Events(), "chart", "hit", "https://charts.example.test") {
		t.Fatalf("Cache events = %#v, want chart hit", recorder.Events())
	}
}
func TestKustomizeRendererRecordsHelmChartRefreshCacheEvent(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeTestChart(t, chartDir, `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	writeFile(t, filepath.Join(root, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
`)
	recorder := cacheevent.NewRecorder(true)
	_, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "."}, RenderOptions{
		ChartAcquirer:      &fakeChartAcquirer{chartDir: chartDir, fromCache: false},
		RefreshCharts:      true,
		CacheEventRecorder: recorder,
	})
	if err != nil {
		t.Fatalf("Render() error = %v, diagnostics = %#v", err, diags)
	}
	if !hasRenderCacheEvent(recorder.Events(), "chart", "refresh", "https://charts.example.test") {
		t.Fatalf("Cache events = %#v, want chart refresh", recorder.Events())
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
		ChartCredentials: chart.ChartCredentials{
			Username:       "helm-user",
			Password:       "helm-pass",
			BearerToken:    "helm-token",
			RegistryConfig: filepath.Join(root, "registry.json"),
		},
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
		Credentials: chart.ChartCredentials{
			Username:       "helm-user",
			Password:       "helm-pass",
			BearerToken:    "helm-token",
			RegistryConfig: filepath.Join(root, "registry.json"),
		},
	}) {
		t.Fatalf("acquirer.options[0] = %#v", acquirer.options[0])
	}
}
func TestKustomizeRendererUsesDiscoveredBareOCIChartRepository(t *testing.T) {
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
    repo: ghcr.io/example/charts/
    version: 1.2.3
    releaseName: demo
`)

	acquirer := &fakeChartAcquirer{chartDir: chartDir}
	_, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		ChartAcquirer: acquirer,
		OCIChartRepositories: map[string]bool{
			"oci://ghcr.io/example/charts": true,
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("len(acquirer.requests) = %d, want 1", len(acquirer.requests))
	}
	if acquirer.requests[0].Kind != chart.RepositoryOCI {
		t.Fatalf("request kind = %q, want %q", acquirer.requests[0].Kind, chart.RepositoryOCI)
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
func TestKustomizeRendererAppliesAdditionalValuesAfterInlineValues(t *testing.T) {
	for _, tt := range []struct {
		name        string
		mergeLine   string
		valuesFile  string
		inlineValue string
	}{
		{
			name:        "default override",
			valuesFile:  "image: from-primary\n",
			inlineValue: "from-inline",
		},
		{
			name:        "merge",
			mergeLine:   "    valuesMerge: merge\n",
			valuesFile:  "image: from-primary\n",
			inlineValue: "from-inline-default",
		},
		{
			name:        "replace",
			mergeLine:   "    valuesMerge: replace\n",
			valuesFile:  "image: from-primary\n",
			inlineValue: "from-inline-replacement",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
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
			writeFile(t, filepath.Join(root, "apps", "demo", "values.yaml"), tt.valuesFile)
			writeFile(t, filepath.Join(root, "apps", "demo", "additional.yaml"), "image: from-additional\n")
			writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
    valuesFile: values.yaml
    additionalValuesFiles:
      - additional.yaml
    valuesInline:
      image: `+tt.inlineValue+`
`+tt.mergeLine)

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
			assertConfigMapData(t, result, "image", "from-additional")
		})
	}
}
func TestKustomizeRendererMergesChartDefaultValuesBeforeAdditionalValues(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "charts", "demo")
	writeTestChart(t, chartDir, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  base: {{ .Values.base }}
  image: {{ .Values.image }}
`)
	writeFile(t, filepath.Join(chartDir, "values.yaml"), `
base: from-chart-default
image: from-chart-default
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "additional.yaml"), `
image: from-additional
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    repo: https://charts.example.test
    version: 1.2.3
    releaseName: demo
    additionalValuesFiles:
      - additional.yaml
    valuesInline:
      base: from-inline
      image: from-inline
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
	assertConfigMapData(t, result, "base", "from-chart-default")
	assertConfigMapData(t, result, "image", "from-additional")
}

func TestKustomizeRendererAllowsRepoRootBoundedHelmValuesWithLoadRestrictionsNone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "values", "demo.yaml"), "nameOverride: shared\n")
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    valuesFile: ../../values/demo.yaml
`)
	writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.nameOverride }}
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{BuildOptions: []string{"--load-restrictor=LoadRestrictionsNone"}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 || result[0].Object.GetName() != "shared" {
		t.Fatalf("result = %#v, want shared ConfigMap", result)
	}
}

func TestKustomizeRendererAllowsRepoRootBoundedAdditionalHelmValues(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "values", "extra.yaml"), "extraName: from-extra\n")
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    valuesInline:
      nameOverride: base
    additionalValuesFiles:
      - ../../values/extra.yaml
`)
	writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.extraName }}
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{BuildOptions: []string{"--load-restrictor=LoadRestrictionsNone"}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 || result[0].Object.GetName() != "from-extra" {
		t.Fatalf("result = %#v, want from-extra ConfigMap", result)
	}
}

func TestKustomizeRendererMergesInlineValuesWithRepoRootBoundedPrimaryValuesFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "values", "base.yaml"), "baseName: from-file\n")
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    valuesFile: ../../values/base.yaml
    valuesInline:
      inlineName: from-inline
`)
	writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.baseName }}-{{ .Values.inlineName }}
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{BuildOptions: []string{"--load-restrictor=LoadRestrictionsNone"}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 || result[0].Object.GetName() != "from-file-from-inline" {
		t.Fatalf("result = %#v, want merged ConfigMap", result)
	}
}

func TestKustomizeRendererRejectsHelmValuesOutsideRepoRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(outside, "values.yaml"), "nameOverride: outside\n")
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    valuesFile: ../../../outside/values.yaml
`)
	writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.nameOverride }}
`)

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{BuildOptions: []string{"--load-restrictor=LoadRestrictionsNone"}})
	if err == nil {
		t.Fatal("Render() error = nil, want repo escape error")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want escape error", err)
	}
}

func TestKustomizeRendererRejectsAdditionalHelmValuesOutsideRepoRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(outside, "extra.yaml"), "extraName: outside\n")
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    additionalValuesFiles:
      - ../../../outside/extra.yaml
`)
	writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.extraName }}
`)

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{BuildOptions: []string{"--load-restrictor=LoadRestrictionsNone"}})
	if err == nil {
		t.Fatal("Render() error = nil, want repo escape error")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want escape error", err)
	}
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
