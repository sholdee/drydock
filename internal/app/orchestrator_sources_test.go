package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCleanLocalSourcePathPreservesRootPaths(t *testing.T) {
	for _, input := range []string{"", "."} {
		got, err := cleanLocalSourcePath(input)
		if err != nil {
			t.Fatalf("cleanLocalSourcePath(%q) error = %v", input, err)
		}
		if got != "." {
			t.Fatalf("cleanLocalSourcePath(%q) = %q, want .", input, got)
		}
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
func TestOrchestratorBuildOfflineErrorsForMissingUnmappedPathSource(t *testing.T) {
	root := t.TempDir()
	cacheDir := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:        root,
		GitCacheDir: cacheDir,
		Offline:     true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want offline cache miss")
	}
	for _, want := range []string{"offline cache miss", "https://github.com/example/external"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
}
func TestOrchestratorBuildFetchesMissingPathSourceByDefault(t *testing.T) {
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
		Path:        root,
		GitCacheDir: cacheDir,
		RefreshGit:  true,
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
func TestOrchestratorBuildRejectsDefaultGitCacheInsideRepoRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
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
		Path:        root,
		GitCacheDir: filepath.Join(external, ".drydock", "git"),
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

func TestOrchestratorBuildRejectsChartCacheInsideRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeChartOnlyBuildApplication(t, root, "chart-only")
	acquirer := &recordingChartAcquirer{chartDir: t.TempDir()}

	_, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:          root,
		ChartCacheDir: filepath.Join(root, ".drydock", "charts"),
	})
	if err == nil {
		t.Fatal("Build() error = nil, want chart cache location error")
	}
	if !strings.Contains(err.Error(), "chart cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want chart cache location error", err.Error())
	}
	if len(acquirer.requests) != 0 {
		t.Fatalf("chart acquire calls = %d, want 0", len(acquirer.requests))
	}
}

func TestOrchestratorBuildStatusOnlyRejectsChartCacheInsideRepoMapRoot(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeChartOnlyBuildApplication(t, root, "chart-only")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:          root,
		StatusOnly:    true,
		ChartCacheDir: filepath.Join(external, ".drydock", "charts"),
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/external.git",
			Path: external,
		}},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want chart cache location error")
	}
	if !strings.Contains(err.Error(), "chart cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want chart cache location error", err.Error())
	}
}

func TestOrchestratorBuildRejectsRemoteCacheInsideRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "plain", "plain")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:                   root,
		RemoteResourceCacheDir: filepath.Join(root, ".drydock", "remotes"),
	})
	if err == nil {
		t.Fatal("Build() error = nil, want remote cache location error")
	}
	if !strings.Contains(err.Error(), "remote resource cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want remote cache location error", err.Error())
	}
}

func TestOrchestratorBuildRejectsRenderCacheInsideRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "plain", "plain")
	cacheDir := filepath.Join(root, ".drydock", "render")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:               root,
		RenderCacheEnabled: true,
		RenderCacheDir:     cacheDir,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want render cache location error")
	}
	if !strings.Contains(err.Error(), "render cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want render cache location error", err.Error())
	}
	if _, statErr := os.Stat(cacheDir); !os.IsNotExist(statErr) {
		t.Fatalf("render cache dir stat error = %v, want not created", statErr)
	}
}

func TestOrchestratorBuildRejectsRenderCacheInsideRepoMapRoot(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeBuildApplication(t, root, "plain", "plain")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/external.git",
			Path: external,
		}},
		RenderCacheEnabled: true,
		RenderCacheDir:     filepath.Join(external, ".drydock", "render"),
	})
	if err == nil {
		t.Fatal("Build() error = nil, want render cache location error")
	}
	if !strings.Contains(err.Error(), "render cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want render cache location error", err.Error())
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

func TestOrchestratorBuildUsesLocalSameRepoPathSourceForRefOnlyHelmValueRef(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-chart-only-same-repo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-chart-only-same-repo
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: manifests/anchor
    - repoURL: https://charts.example.test
      targetRevision: 1.2.3
      chart: demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "root-values.yaml"), `value: from-current-repo-root
`)
	writeTestFile(t, filepath.Join(root, "manifests", "anchor", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: anchor
data:
  source: local
`)
	writeAppTestValueChart(t, chartRoot)

	gitAcquirer := &recordingGitAcquirer{err: errors.New("unexpected git acquire")}
	chartAcquirer := &recordingChartAcquirer{chartDir: chartRoot}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: chartAcquirer,
	}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := len(gitAcquirer.requests); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
	helmManifest, ok := manifestByName(result.Manifests, "demo")
	if !ok {
		t.Fatalf("missing Helm manifest demo: %#v", result.Manifests)
	}
	value, found, err := unstructured.NestedString(helmManifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-current-repo-root" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-current-repo-root", value, found, err)
	}
}

func TestOrchestratorBuildUsesFetchedSameRepoPathSourceForRefOnlyHelmValueRef(t *testing.T) {
	root := t.TempDir()
	fetchedRoot := t.TempDir()
	chartRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-chart-only-fetched-same-repo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-chart-only-fetched-same-repo
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: manifests/anchor
    - repoURL: https://charts.example.test
      targetRevision: 1.2.3
      chart: demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "root-values.yaml"), `value: from-current-root-wrong
`)
	writeTestFile(t, filepath.Join(fetchedRoot, "root-values.yaml"), `value: from-fetched-root
`)
	writeTestFile(t, filepath.Join(fetchedRoot, "manifests", "anchor", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: anchor
data:
  source: fetched
`)
	writeAppTestValueChart(t, chartRoot)

	gitAcquirer := &recordingGitAcquirer{path: fetchedRoot, revision: "abc123"}
	chartAcquirer := &recordingChartAcquirer{chartDir: chartRoot}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: chartAcquirer,
	}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := len(gitAcquirer.requests); got != 1 {
		t.Fatalf("git acquire calls = %d, want 1: %#v", got, gitAcquirer.requests)
	}
	helmManifest, ok := manifestByName(result.Manifests, "demo")
	if !ok {
		t.Fatalf("missing Helm manifest demo: %#v", result.Manifests)
	}
	value, found, err := unstructured.NestedString(helmManifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-fetched-root" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-fetched-root", value, found, err)
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
		Path: root,
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
		Path: root,
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

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:    root,
		Offline: true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want unmapped ref repository error")
	}
	for _, want := range []string{"ref root $values", "offline cache miss"} {
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
	cacheDir := t.TempDir()
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
		CacheDir:       cacheDir,
		Offline:        true,
		Refresh:        true,
		ForbiddenRoots: []string{root},
	}); !reflect.DeepEqual(got, want) {
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
func TestOrchestratorRecordsChartCacheEvents(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "charted.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: charted
`)
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)

	result, err := Orchestrator{ChartAcquirer: &recordingChartAcquirer{chartDir: chartDir, fromCache: true}}.Build(context.Background(), BuildRequest{
		Path:              root,
		RecordCacheEvents: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !hasCacheEvent(result.CacheEvents, "chart", "hit", "https://charts.example.test") {
		t.Fatalf("CacheEvents = %#v, want chart cache hit", result.CacheEvents)
	}
}
func TestOrchestratorRecordsRedactedGitCacheErrors(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: https://user:secret@example.test/repo.git?token=abc#frag
    path: missing
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)

	result, err := Orchestrator{GitAcquirer: &recordingGitAcquirer{err: errors.New("offline cache miss for https://user:secret@example.test/repo.git?token=abc#frag")}}.Build(context.Background(), BuildRequest{
		Path:              root,
		RecordCacheEvents: true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want failing Git acquire")
	}
	if len(result.CacheEvents) == 0 {
		t.Fatalf("CacheEvents = %#v, want Git cache event", result.CacheEvents)
	}
	text := fmt.Sprintf("err=%v diagnostics=%#v statuses=%#v events=%#v", err, result.Diagnostics, result.Statuses, result.CacheEvents)
	for _, leaked := range []string{"user", "secret", "token", "abc", "frag"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("result leaked %q: %s", leaked, text)
		}
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

func TestLocalProviderClassifiesBareChartOnlyRepositoryAsOCI(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	writeAppTestValueChart(t, chartDir)
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	_, _, err := (localProvider{
		repoRoot:      root,
		chartAcquirer: acquirer,
	}).RenderSource(context.Background(), render.ResolvedSource{
		Chart:          "demo",
		RepoURL:        "ghcr.io/grafana/helm-charts",
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

func TestOrchestratorPassesChartCredentialsToKustomizeHelmCharts(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	writeAppTestValueChart(t, chartDir)
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: chart
    repo: https://charts.example.test
    version: 0.1.0
    releaseName: demo
`)
	credentials := chart.ChartCredentials{
		Username:       "helm-user",
		Password:       "helm-pass",
		BearerToken:    "helm-token",
		RegistryConfig: filepath.Join(root, "registry.json"),
	}
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	if _, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:             root,
		ChartCredentials: credentials,
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("chart options = %d, want 1", len(acquirer.options))
	}
	if got := acquirer.options[0].Credentials; got != credentials {
		t.Fatalf("chart credentials = %#v, want %#v", got, credentials)
	}
}
func TestOrchestratorPassesRemoteCredentialsToKustomizeRenderer(t *testing.T) {
	root := t.TempDir()
	remoteFile := filepath.Join(t.TempDir(), "remote.yaml")
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/remote.yaml
`)
	writeTestFile(t, remoteFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	remoteCredentials := remote.Credentials{
		Username:    "remote-user",
		Password:    "remote-pass",
		BearerToken: "remote-token",
	}
	remoteGitCredentials := remote.GitCredentials{
		Username:          "git-user",
		Password:          "git-pass",
		BearerToken:       "git-token",
		SSHPrivateKeyPath: filepath.Join(root, "id_ed25519"),
		SSHPassphrase:     "git-phrase",
		SSHKnownHostsPath: filepath.Join(root, "known_hosts"),
	}
	acquirer := &recordingRemoteAcquirer{path: remoteFile}

	if _, err := (Orchestrator{RemoteResourceAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:                         root,
		Offline:                      true,
		RefreshRemoteResources:       true,
		RemoteResourceCacheDir:       t.TempDir(),
		RemoteResourceCredentials:    remoteCredentials,
		RemoteResourceGitCredentials: remoteGitCredentials,
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("remote options = %d, want 1", len(acquirer.options))
	}
	if got := acquirer.options[0].Credentials; got != remoteCredentials {
		t.Fatalf("remote credentials = %#v, want %#v", got, remoteCredentials)
	}
	if got := acquirer.options[0].GitCredentials; got != remoteGitCredentials {
		t.Fatalf("remote git credentials = %#v, want %#v", got, remoteGitCredentials)
	}
}
