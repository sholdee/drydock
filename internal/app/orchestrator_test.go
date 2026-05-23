package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/chart"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/render"
	sourcepkg "github.com/home-operations/argocd-local/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOrchestratorDiscoversGeneratesAndRenders(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "applications", "e2e")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Name != "demo" {
		t.Fatalf("Application name = %q, want demo", result.Applications[0].Name)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "demo" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/demo", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	version, found, err := unstructured.NestedString(manifest.Object.Object, "data", "version")
	if err != nil || !found || version != "v1" {
		t.Fatalf("data.version = %q, found %v, err %v; want v1", version, found, err)
	}
}

func TestOrchestratorBuildRendersPlainDirectorySource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "plain-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plain
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: manifests/plain
  destination:
    name: in-cluster
    namespace: plain
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plain", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: plain
data:
  source: directory
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "plain" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/plain", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", "source")
	if err != nil || !found || value != "directory" {
		t.Fatalf("data.source = %q, found %v, err %v; want directory", value, found, err)
	}
}

func TestOrchestratorBuildRendersLocalHelmChartSource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "helm-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-local
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: charts/demo
    helm:
      values: |
        value: from-values
  destination:
    name: in-cluster
    namespace: helm-ns
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), `apiVersion: v2
name: demo
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  value: {{ .Values.value | quote }}
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "helm-local" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/helm-local", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-values" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-values", value, found, err)
	}
	if namespace := manifest.Object.GetNamespace(); namespace != "helm-ns" {
		t.Fatalf("namespace = %q, want helm-ns", namespace)
	}
}

func TestOrchestratorBuildRendersRepoMappedPathSource(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")
	writeTestFile(t, filepath.Join(external, "manifests", "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: repo-map
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/external.git",
			Path: external,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "source")
	if err != nil || !found || value != "repo-map" {
		t.Fatalf("data.source = %q, found %v, err %v; want repo-map", value, found, err)
	}
}

func TestOrchestratorBuildErrorsForMissingUnmappedPathSource(t *testing.T) {
	root := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want missing unmapped source error")
	}
	for _, want := range []string{"manifests/external", "--repo-map", "--allow-network"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
}

func TestOrchestratorBuildFetchesMissingPathSourceWhenNetworkAllowed(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	cacheDir := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")
	writeTestFile(t, filepath.Join(external, "manifests", "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: fetched
`)
	acquirer := &recordingGitAcquirer{path: external, revision: "abc123"}

	result, err := (Orchestrator{GitAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
		GitCacheDir:  cacheDir,
		RefreshGit:   true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("git acquire calls = %d, want 1", len(acquirer.requests))
	}
	if acquirer.requests[0] != (sourcepkg.GitRequest{URL: "https://github.com/example/external", Revision: "main"}) {
		t.Fatalf("git request = %#v", acquirer.requests[0])
	}
	if acquirer.options[0] != (sourcepkg.GitOptions{AllowNetwork: true, CacheDir: cacheDir, Refresh: true}) {
		t.Fatalf("git options = %#v", acquirer.options[0])
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "source")
	if err != nil || !found || value != "fetched" {
		t.Fatalf("data.source = %q, found %v, err %v; want fetched", value, found, err)
	}
}

func TestOrchestratorBuildRejectsOfflineWithGitNetwork(t *testing.T) {
	root := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:         root,
		Offline:      true,
		AllowNetwork: true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want offline allow-network error")
	}
	if !strings.Contains(err.Error(), "--offline cannot be combined with --allow-network") {
		t.Fatalf("Build() error = %q, want offline allow-network message", err.Error())
	}
}

func TestOrchestratorBuildRejectsDefaultGitCacheInsideRepoRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want git cache location error")
	}
	if !strings.Contains(err.Error(), "git cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want git cache location error", err.Error())
	}
}

func TestOrchestratorBuildRejectsGitCacheInsideRepoMapRoot(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
		GitCacheDir:  filepath.Join(external, ".argocd-local", "git"),
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/external.git",
			Path: external,
		}},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want git cache location error")
	}
	if !strings.Contains(err.Error(), "git cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want git cache location error", err.Error())
	}
}

func TestOrchestratorBuildUsesRepoMappedHelmValueRef(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/values
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(root, "charts", "demo"))
	writeTestFile(t, filepath.Join(valuesRoot, "values.yaml"), `value: from-mapped-ref
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/values.git",
			Path: valuesRoot,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "value")
	if err != nil || !found || value != "from-mapped-ref" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-mapped-ref", value, found, err)
	}
}

func TestOrchestratorBuildUsesRepoMappedHelmValueRefFromRepoRootWhenRefHasPath(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-path.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-path
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/values
      targetRevision: main
      ref: values
      path: value-manifests
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(root, "charts", "demo"))
	writeTestFile(t, filepath.Join(valuesRoot, "root-values.yaml"), `value: from-root-ref
`)
	writeTestFile(t, filepath.Join(valuesRoot, "value-manifests", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: ref-source
data:
  source: ref-path
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/values.git",
			Path: valuesRoot,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 2 {
		t.Fatalf("len(Manifests) = %d, want 2", len(result.Manifests))
	}

	helmManifest, ok := manifestByName(result.Manifests, "demo")
	if !ok {
		t.Fatalf("missing Helm manifest demo: %#v", result.Manifests)
	}
	value, found, err := unstructured.NestedString(helmManifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-root-ref" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-root-ref", value, found, err)
	}

	refManifest, ok := manifestByName(result.Manifests, "ref-source")
	if !ok {
		t.Fatalf("missing ref source manifest ref-source: %#v", result.Manifests)
	}
	source, found, err := unstructured.NestedString(refManifest.Object.Object, "data", "source")
	if err != nil || !found || source != "ref-path" {
		t.Fatalf("data.source = %q, found %v, err %v; want ref-path", source, found, err)
	}
}

func TestOrchestratorBuildUsesFetchedSourceRootForSameRepoHelmValueRef(t *testing.T) {
	root := t.TempDir()
	fetchedRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-same-repo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-same-repo
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(fetchedRoot, "charts", "demo"))
	writeTestFile(t, filepath.Join(fetchedRoot, "root-values.yaml"), `value: from-fetched-root
`)
	acquirer := &recordingGitAcquirer{path: fetchedRoot, revision: "abc123"}

	result, err := (Orchestrator{GitAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("git acquire calls = %d, want 1", len(acquirer.requests))
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "value")
	if err != nil || !found || value != "from-fetched-root" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-fetched-root", value, found, err)
	}
}

func TestOrchestratorBuildResolvesSameRepoHelmValueRefWithDifferentRevision(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	valuesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-same-repo-revision.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-same-repo-revision
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: values-revision
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: chart-revision
      path: charts/demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(chartRoot, "charts", "demo"))
	writeTestFile(t, filepath.Join(valuesRoot, "root-values.yaml"), `value: from-values-revision
`)
	acquirer := &recordingGitAcquirer{
		paths: map[string]string{
			"chart-revision":  chartRoot,
			"values-revision": valuesRoot,
		},
		revisions: map[string]string{
			"chart-revision":  "chart-sha",
			"values-revision": "values-sha",
		},
	}

	result, err := (Orchestrator{GitAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.requests) != 2 {
		t.Fatalf("git acquire calls = %d, want 2: %#v", len(acquirer.requests), acquirer.requests)
	}
	wantRevisions := []string{"chart-revision", "values-revision"}
	for i, want := range wantRevisions {
		if got := acquirer.requests[i].Revision; got != want {
			t.Fatalf("git request[%d].Revision = %q, want %q; requests %#v", i, got, want, acquirer.requests)
		}
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "value")
	if err != nil || !found || value != "from-values-revision" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-values-revision", value, found, err)
	}
}

func TestOrchestratorBuildRejectsUnmappedCrossRepoHelmValueRefEvenWhenLocalValueFileExists(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-unmapped.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-unmapped
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/values
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/leaked-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(root, "charts", "demo"))
	writeTestFile(t, filepath.Join(root, "leaked-values.yaml"), `value: from-current-repo
`)

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unmapped ref repository error")
	}
	for _, want := range []string{"ref root $values", "--repo-map", "--allow-network"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
}

func TestOrchestratorBuildReturnsChartAcquireErrorForChartOnlySource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "chart-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart-only
  namespace: argocd
spec:
  source:
    repoURL: https://repo-user:repo-secret@charts.example.test?token=repo-token#repo-frag
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: chart-only
`)

	acquirer := &recordingChartAcquirer{
		acquireErr: errors.New("fetch https://repo-user:repo-secret@charts.example.test?token=repo-token#repo-frag failed"),
	}
	result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatalf("Build() error = nil, want chart acquire error")
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
	}
	for _, want := range []string{`chart="demo"`, "acquire chart demo", "https://charts.example.test"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
	for _, leaked := range []string{"repo-user", "repo-secret", "repo-token", "repo-frag"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Build() error = %q, leaked %q", err.Error(), leaked)
		}
	}
}

func TestOrchestratorBuildRendersChartOnlyApplication(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	cacheDir := filepath.Join(root, "chart-cache")
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "chart-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart-only
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
    helm:
      valueFiles:
        - values-extra.yaml
      values: |
        value: from-inline
  destination:
    name: in-cluster
    namespace: chart-ns
`)
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeTestFile(t, filepath.Join(chartDir, "values-extra.yaml"), `value: from-file
`)
	writeTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  value: {{ .Values.value | quote }}
`)
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:          root,
		ChartCacheDir: cacheDir,
		Offline:       true,
		RefreshCharts: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got, want := acquirer.requests[0], (chart.Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       chart.RepositoryHTTP,
	}); got != want {
		t.Fatalf("chart request = %#v, want %#v", got, want)
	}
	if got, want := acquirer.options[0], (chart.Options{
		CacheDir: cacheDir,
		Offline:  true,
		Refresh:  true,
	}); got != want {
		t.Fatalf("chart options = %#v, want %#v", got, want)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "chart-only" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/chart-only", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	if namespace := manifest.Object.GetNamespace(); namespace != "chart-ns" {
		t.Fatalf("namespace = %q, want chart-ns", namespace)
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-inline" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-inline", value, found, err)
	}
}

func TestLocalProviderClassifiesChartOnlyOCIRepository(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	writeAppTestValueChart(t, chartDir)
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	_, _, err := (localProvider{
		repoRoot:      root,
		chartAcquirer: acquirer,
	}).RenderSource(context.Background(), render.ResolvedSource{
		Chart:          "demo",
		RepoURL:        " oci://registry.example.test/charts ",
		TargetRevision: "2.0.0",
	}, render.RenderOptions{AppName: "demo"})
	if err != nil {
		t.Fatalf("RenderSource() error = %v", err)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got := acquirer.requests[0].Kind; got != chart.RepositoryOCI {
		t.Fatalf("request kind = %q, want %q", got, chart.RepositoryOCI)
	}
}

func TestOrchestratorBuildPreservesListAndRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want 2: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	categories := map[string]bool{}
	for _, diag := range result.Diagnostics {
		if diag.Severity != diagnostic.SeverityWarning {
			t.Fatalf("diagnostic severity = %s, want warning: %#v", diag.Severity, diag)
		}
		categories[diag.Category] = true
	}
	for _, want := range []string{"appset", "repeated-resource"} {
		if !categories[want] {
			t.Fatalf("diagnostic categories = %#v, want %q", categories, want)
		}
	}
}

func TestOrchestratorBuildStrictFailsOnRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "direct.yaml"), directApplicationYAML())
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatalf("Build() error = nil, want strict diagnostic error")
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("diagnostic severity = %s, want error", result.Diagnostics[0].Severity)
	}
	if result.Diagnostics[0].Category != "repeated-resource" {
		t.Fatalf("diagnostic category = %q, want repeated-resource", result.Diagnostics[0].Category)
	}
	if !strings.Contains(err.Error(), "repeated-resource") {
		t.Fatalf("Build() error = %q, want repeated-resource", err.Error())
	}
}

func TestOrchestratorBuildAppRendersOnlyNamedApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "first", "one")
	writeBuildApplication(t, root, "second", "two")

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name: "second",
		BuildRequest: BuildRequest{
			Path: root,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "second" {
		t.Fatalf("Applications = %#v, want only second", result.Applications)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if result.Manifests[0].Object.GetName() != "two" {
		t.Fatalf("rendered ConfigMap name = %q, want two", result.Manifests[0].Object.GetName())
	}
}

func TestOrchestratorBuildAppPreservesSelectedApplicationInputs(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "first", "one")
	writeBuildApplication(t, root, "second", "two")

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name: "second",
		BuildRequest: BuildRequest{
			Path: root,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}

	if len(result.ApplicationInputs) != 1 {
		t.Fatalf("len(ApplicationInputs) = %d, want 1: %#v", len(result.ApplicationInputs), result.ApplicationInputs)
	}
	input := result.ApplicationInputs[0]
	if input.Application.Name != "second" {
		t.Fatalf("ApplicationInputs[0].Application.Name = %q, want second", input.Application.Name)
	}
	wantPath := filepath.ToSlash(filepath.Join("apps", "second.yaml"))
	if len(input.Paths) != 1 || input.Paths[0] != wantPath {
		t.Fatalf("ApplicationInputs[0].Paths = %#v, want [%q]", input.Paths, wantPath)
	}
	if strings.Contains(strings.Join(input.Paths, ","), "first") {
		t.Fatalf("ApplicationInputs[0].Paths = %#v, want no first app paths", input.Paths)
	}
}

func TestOrchestratorBuildAppReportsMissingApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")

	_, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "missing",
		BuildRequest: BuildRequest{Path: root},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want missing application error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found`) {
		t.Fatalf("BuildApp() error = %v, want missing application message", err)
	}
}

func TestOrchestratorBuildAppRequiresName(t *testing.T) {
	_, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         " ",
		BuildRequest: BuildRequest{Path: t.TempDir()},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want required name error")
	}
	if !strings.Contains(err.Error(), "application name is required") {
		t.Fatalf("BuildApp() error = %v, want required name message", err)
	}
}

func TestOrchestratorBuildAppPreservesBuildOptionsForSelectedApplication(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	cacheDir := filepath.Join(root, "chart-cache")
	writeAppTestValueChart(t, chartDir)
	writeBuildApplication(t, root, "plain", "plain")
	writeChartOnlyBuildApplication(t, root, "chart-only")
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	result, err := (Orchestrator{ChartAcquirer: acquirer}).BuildApp(context.Background(), BuildAppRequest{
		Name: "chart-only",
		BuildRequest: BuildRequest{
			Path:          root,
			ChartCacheDir: cacheDir,
			Offline:       true,
			RefreshCharts: true,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "chart-only" {
		t.Fatalf("Applications = %#v, want only chart-only", result.Applications)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got, want := acquirer.options[0], (chart.Options{
		CacheDir: cacheDir,
		Offline:  true,
		Refresh:  true,
	}); got != want {
		t.Fatalf("chart options = %#v, want %#v", got, want)
	}
}

func TestOrchestratorBuildAppPreservesListAndRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "direct",
		BuildRequest: BuildRequest{Path: root},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want 2: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	categories := map[string]bool{}
	for _, diag := range result.Diagnostics {
		categories[diag.Category] = true
	}
	for _, want := range []string{"appset", "repeated-resource"} {
		if !categories[want] {
			t.Fatalf("diagnostic categories = %#v, want %q", categories, want)
		}
	}
}

func TestOrchestratorListApplicationsSkipsUnsupportedApplicationSetInNonStrictMode(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Name != "direct" {
		t.Fatalf("Application name = %q, want direct", result.Applications[0].Name)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	diag := result.Diagnostics[0]
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
	}
	if diag.Category != "appset" {
		t.Fatalf("diagnostic category = %q, want appset", diag.Category)
	}
}

func TestOrchestratorListApplicationsFailsUnsupportedApplicationSetInStrictMode(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatalf("ListApplications() error = nil, want unsupported ApplicationSet error")
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("diagnostic severity = %s, want error", result.Diagnostics[0].Severity)
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("ListApplications() error = %q, want unsupported ApplicationSet generator", err.Error())
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func manifestByName(manifests []render.Manifest, name string) (render.Manifest, bool) {
	for _, manifest := range manifests {
		if manifest.Object.GetName() == name {
			return manifest, true
		}
	}
	return render.Manifest{}, false
}

func writeBuildApplication(t *testing.T, root, appName, configMapName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
data:
  key: value
`)
}

func writeChartOnlyBuildApplication(t *testing.T, root, appName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: default
`)
}

func writeExternalPathApplication(t *testing.T, root, repoURL, sourcePath string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: main
    path: `+sourcePath+`
  destination:
    name: in-cluster
    namespace: default
`)
}

func writeUnsupportedApplicationSetFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "direct.yaml"), directApplicationYAML())
	writeTestFile(t, filepath.Join(root, "apps", "unsupported-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - name: generated
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: manifests/generated
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

func directApplicationYAML() string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: direct
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/direct
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`
}

func writeDuplicateConfigMaps(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "first.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: direct
data:
  value: first
`)
	writeTestFile(t, filepath.Join(dir, "second.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: direct
data:
  value: second
`)
}

type recordingChartAcquirer struct {
	chartDir   string
	acquireErr error
	requests   []chart.Request
	options    []chart.Options
}

type recordingGitAcquirer struct {
	path      string
	paths     map[string]string
	revision  string
	revisions map[string]string
	err       error
	requests  []sourcepkg.GitRequest
	options   []sourcepkg.GitOptions
}

func (acquirer *recordingGitAcquirer) Acquire(_ context.Context, request sourcepkg.GitRequest, opts sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return sourcepkg.GitResult{}, acquirer.err
	}
	path := acquirer.path
	if acquirer.paths != nil {
		path = acquirer.paths[request.Revision]
	}
	revision := acquirer.revision
	if acquirer.revisions != nil {
		revision = acquirer.revisions[request.Revision]
	}
	return sourcepkg.GitResult{Path: path, Revision: revision}, nil
}

func (acquirer *recordingChartAcquirer) Acquire(_ context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.acquireErr != nil {
		return chart.Result{}, acquirer.acquireErr
	}
	return chart.Result{
		ChartDir:   acquirer.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
	}, nil
}
