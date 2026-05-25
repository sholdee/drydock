package app

import (
	"context"
	"errors"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

type blockingInternalPluginRenderer struct{}

func (blockingInternalPluginRenderer) RenderPlugin(ctx context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

type internalPluginRendererFunc func(context.Context, render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error)

func (f internalPluginRendererFunc) RenderPlugin(ctx context.Context, request render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return f(ctx, request)
}

type failingInternalPluginRenderer struct{}

func (failingInternalPluginRenderer) RenderPlugin(_ context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return nil, []diagnostic.Diagnostic{{
		Code:     "plugin.custom",
		Severity: diagnostic.SeverityError,
		Category: "plugin",
		Message:  "renderer supplied diagnostic",
	}}, errors.New("renderer failed")
}
func manifestByName(manifests []render.Manifest, name string) (render.Manifest, bool) {
	for _, manifest := range manifests {
		if manifest.Object.GetName() == name {
			return manifest, true
		}
	}
	return render.Manifest{}, false
}
func diagnosticByCategory(diags []diagnostic.Diagnostic, category string) (diagnostic.Diagnostic, bool) {
	for _, diag := range diags {
		if diag.Category == category {
			return diag, true
		}
	}
	return diagnostic.Diagnostic{}, false
}
func hasDiagnosticMessage(diags []diagnostic.Diagnostic, fragment string) bool {
	for _, diag := range diags {
		if strings.Contains(diag.Message, fragment) {
			return true
		}
	}
	return false
}
func hasDiagnosticCode(diags []diagnostic.Diagnostic, code string) bool {
	for _, diag := range diags {
		if diag.Code == code {
			return true
		}
	}
	return false
}
func hasCacheEvent(events []cacheevent.Event, source, action, targetFragment string) bool {
	for _, event := range events {
		if string(event.Source) == source && string(event.Action) == action && strings.Contains(event.Target, targetFragment) {
			return true
		}
	}
	return false
}
func assertApplicationStatuses(t *testing.T, got, want []ApplicationStatus) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(Statuses) = %d, want %d: %#v", len(got), len(want), got)
	}
	byName := map[string]ApplicationStatus{}
	for _, status := range got {
		byName[applicationStatusDisplayName(status)] = status
	}
	for _, expected := range want {
		status, ok := byName[applicationStatusDisplayName(expected)]
		if !ok {
			t.Fatalf("Statuses = %#v, missing %s", got, applicationStatusDisplayName(expected))
		}
		if status.Status != expected.Status {
			t.Fatalf("Status for %s = %q, want %q: %#v", applicationStatusDisplayName(expected), status.Status, expected.Status, got)
		}
		if status.Status != ApplicationStatusPass && status.Message == "" {
			t.Fatalf("Status message for %s is empty, want failure/skipped message: %#v", applicationStatusDisplayName(expected), status)
		}
	}
}
func assertApplicationStatusOrder(t *testing.T, got []ApplicationStatus, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(Statuses) = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, status := range got {
		key := applicationStatusDisplayName(status) + ":" + status.Status
		if key != want[i] {
			t.Fatalf("Statuses[%d] = %q, want %q: %#v", i, key, want[i], got)
		}
	}
}
func assertManifestNames(t *testing.T, manifests []render.Manifest, want []string) {
	t.Helper()
	if len(manifests) != len(want) {
		t.Fatalf("len(Manifests) = %d, want %d: %#v", len(manifests), len(want), manifests)
	}
	for i, manifest := range manifests {
		if got := manifest.Object.GetName(); got != want[i] {
			t.Fatalf("Manifests[%d].Name = %q, want %q", i, got, want[i])
		}
	}
}

type controlledChartAcquirer struct {
	root     string
	started  chan string
	releases map[string]chan struct{}
	fail     map[string]error
	mu       sync.Mutex
	active   int
	max      int
}

func newControlledChartAcquirer(root string, names []string) *controlledChartAcquirer {
	releases := make(map[string]chan struct{}, len(names))
	for _, name := range names {
		if _, ok := releases[name]; !ok {
			releases[name] = make(chan struct{})
		}
	}
	return &controlledChartAcquirer{
		root:     root,
		started:  make(chan string, len(names)+4),
		releases: releases,
		fail:     map[string]error{},
	}
}
func (acquirer *controlledChartAcquirer) Acquire(ctx context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	acquirer.mu.Lock()
	acquirer.active++
	if acquirer.active > acquirer.max {
		acquirer.max = acquirer.active
	}
	acquirer.mu.Unlock()
	defer func() {
		acquirer.mu.Lock()
		acquirer.active--
		acquirer.mu.Unlock()
	}()

	select {
	case acquirer.started <- request.Name:
	case <-ctx.Done():
		return chart.Result{}, ctx.Err()
	}
	release := acquirer.releases[request.Name]
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return chart.Result{}, ctx.Err()
		}
	}
	if err := acquirer.fail[request.Name]; err != nil {
		return chart.Result{}, err
	}
	return chart.Result{
		ChartDir:   filepath.Join(acquirer.root, request.Name),
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  true,
	}, nil
}
func (acquirer *controlledChartAcquirer) waitStarted(t *testing.T, want ...string) {
	t.Helper()
	remaining := map[string]int{}
	for _, name := range want {
		remaining[name]++
	}
	for _, expected := range want {
		select {
		case got := <-acquirer.started:
			if remaining[got] == 0 {
				t.Fatalf("started chart = %q, want one of %#v", got, want)
			}
			remaining[got]--
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for chart acquisition %q", expected)
		}
	}
}
func (acquirer *controlledChartAcquirer) release(name string) {
	if release := acquirer.releases[name]; release != nil {
		close(release)
	}
}
func (acquirer *controlledChartAcquirer) maxActive() int {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return acquirer.max
}

type staticGitAcquirer struct {
	path string
}

func (acquirer staticGitAcquirer) Acquire(_ context.Context, request sourcepkg.GitRequest, _ sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	return sourcepkg.GitResult{
		Path:      acquirer.path,
		Revision:  request.Revision,
		FromCache: true,
	}, nil
}
func chartOnlyApplication(appName, chartName, version string) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Project:     "default",
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://charts.example.com", Chart: chartName, TargetRevision: version},
			Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "default"},
		},
	}
}
func pluginApplication(name string) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Project: "default",
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://github.com/example/repo",
				TargetRevision: "main",
				Path:           "manifests/" + name,
				Plugin:         &argoappv1.ApplicationSourcePlugin{Name: "cue"},
			},
			Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "default"},
		},
	}
}
func writeTestChart(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, name, "Chart.yaml"), `apiVersion: v2
name: `+name+`
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, name, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  value: rendered
`)
}
func writeBuildApplication(t *testing.T, root, appName, configMapName string) {
	t.Helper()
	writeBuildApplicationWithDestination(t, root, appName, configMapName, "in-cluster")
}
func writeBuildApplicationWithDestination(t *testing.T, root, appName, configMapName, destinationName string) {
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
    name: `+destinationName+`
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
func writePluginBuildApplication(t *testing.T, root, appName, pluginName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
    plugin:
      name: `+pluginName+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, ".keep"), "")
}
func writeBuildApplicationWithProject(t *testing.T, root, appName, configMapName, projectName, repoURL, destinationNamespace string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  project: `+projectName+`
  source:
    repoURL: `+repoURL+`
    path: manifests/`+appName+`
  destination:
    server: https://kubernetes.default.svc
    namespace: `+destinationNamespace+`
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
data:
  value: demo
`)
}
func writeExternalPathApplicationNamed(t *testing.T, root, appName, repoURL, sourcePath string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
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
    - scmProvider: {}
  template:
    metadata:
      name: generated
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
	fromCache  bool
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
type recordingRemoteAcquirer struct {
	path     string
	err      error
	requests []remote.Request
	options  []remote.Options
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
func (acquirer *recordingRemoteAcquirer) Acquire(_ context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return remote.Result{}, acquirer.err
	}
	return remote.Result{Path: acquirer.path, URL: request.URL}, nil
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
		FromCache:  acquirer.fromCache,
	}, nil
}
