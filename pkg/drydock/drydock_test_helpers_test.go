package drydock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type publicPluginRendererFunc func(context.Context, PluginRequest) (PluginResult, error)

func (f publicPluginRendererFunc) RenderPlugin(ctx context.Context, request PluginRequest) (PluginResult, error) {
	return f(ctx, request)
}

type controlledPublicPluginRenderer struct {
	started  chan string
	releases map[string]chan struct{}
}

func newControlledPublicPluginRenderer(names []string) *controlledPublicPluginRenderer {
	releases := make(map[string]chan struct{}, len(names))
	for _, name := range names {
		releases[name] = make(chan struct{})
	}
	return &controlledPublicPluginRenderer{
		started:  make(chan string, len(names)),
		releases: releases,
	}
}
func (renderer *controlledPublicPluginRenderer) RenderPlugin(ctx context.Context, request PluginRequest) (PluginResult, error) {
	name := request.Application.Name
	select {
	case renderer.started <- name:
	case <-ctx.Done():
		return PluginResult{}, ctx.Err()
	}
	if release := renderer.releases[name]; release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return PluginResult{}, ctx.Err()
		}
	}
	return PluginResult{Manifests: []PluginManifest{{
		Path: "plugin/cm.yaml",
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": name},
		},
	}}}, nil
}
func (renderer *controlledPublicPluginRenderer) waitStarted(t *testing.T, want ...string) {
	t.Helper()
	remaining := map[string]int{}
	for _, name := range want {
		remaining[name]++
	}
	for _, expected := range want {
		select {
		case got := <-renderer.started:
			if remaining[got] == 0 {
				t.Fatalf("started plugin app = %q, want one of %#v", got, want)
			}
			remaining[got]--
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for plugin app %q", expected)
		}
	}
}
func (renderer *controlledPublicPluginRenderer) release(name string) {
	if release := renderer.releases[name]; release != nil {
		close(release)
	}
}

type blockingPublicPluginRenderer struct{}

func (blockingPublicPluginRenderer) RenderPlugin(ctx context.Context, _ PluginRequest) (PluginResult, error) {
	<-ctx.Done()
	return PluginResult{}, ctx.Err()
}

type failingPublicPluginRenderer struct{}

func (failingPublicPluginRenderer) RenderPlugin(_ context.Context, _ PluginRequest) (PluginResult, error) {
	return PluginResult{Diagnostics: []Diagnostic{{
		Code:     "plugin.custom",
		Severity: "error",
		Category: "plugin",
		Message:  "renderer supplied diagnostic",
	}}}, errors.New("renderer failed")
}
func publicPluginParamsByName(params []PluginParameter) map[string]PluginParameter {
	out := make(map[string]PluginParameter, len(params))
	for _, param := range params {
		out[param.Name] = param
	}
	return out
}
func assertPublicPluginRequest(t *testing.T, request PluginRequest) {
	t.Helper()
	if request.Application.Name != "plugin-app" {
		t.Fatalf("Application.Name = %q, want plugin-app", request.Application.Name)
	}
	if request.Application.Namespace != "argocd" {
		t.Fatalf("Application.Namespace = %q, want argocd", request.Application.Namespace)
	}
	if request.Application.Project != "default" {
		t.Fatalf("Application.Project = %q, want default", request.Application.Project)
	}
	if request.DestinationNamespace != "rendered" {
		t.Fatalf("DestinationNamespace = %q, want rendered", request.DestinationNamespace)
	}
	if request.Source.Path != "apps/plugin" {
		t.Fatalf("Source.Path = %q, want apps/plugin", request.Source.Path)
	}
	if request.Source.RepoURL != "https://github.com/example/repo" {
		t.Fatalf("Source.RepoURL = %q, want https://github.com/example/repo", request.Source.RepoURL)
	}
	if request.Source.TargetRevision != "main" {
		t.Fatalf("Source.TargetRevision = %q, want main", request.Source.TargetRevision)
	}
	if request.Source.RepoRoot == "" {
		t.Fatalf("Source.RepoRoot is empty")
	}
	if _, err := os.Stat(request.Source.RepoRoot); err != nil {
		t.Fatalf("Source.RepoRoot %q stat error = %v", request.Source.RepoRoot, err)
	}
	assertPublicPluginConfig(t, request.Plugin)
}
func assertPublicPluginConfig(t *testing.T, config PluginConfig) {
	t.Helper()
	if config.Name != "cue" {
		t.Fatalf("Plugin.Name = %q, want cue", config.Name)
	}
	if len(config.Env) != 1 {
		t.Fatalf("Plugin.Env = %#v, want one env entry", config.Env)
	}
	if config.Env[0].Name != "FEATURE" || config.Env[0].Value != "enabled" {
		t.Fatalf("Plugin.Env = %#v, want FEATURE=enabled", config.Env)
	}
	params := publicPluginParamsByName(config.Parameters)
	assertPublicPluginStringParam(t, params, "mode", "fast")
	assertPublicPluginMapParam(t, params, "labels", map[string]string{"tier": "backend"})
	assertPublicPluginArrayParam(t, params, "args", []string{"--debug"})
	assertPublicPluginMapParam(t, params, "empty-map", map[string]string{})
	assertPublicPluginArrayParam(t, params, "empty-array", []string{})
}
func assertPublicPluginStringParam(t *testing.T, params map[string]PluginParameter, name, value string) {
	t.Helper()
	param := params[name]
	if param.String == nil {
		t.Fatalf("Plugin.Parameters = %#v, want %s string", params, name)
	}
	if *param.String != value {
		t.Fatalf("Plugin.Parameters[%s].String = %q, want %q", name, *param.String, value)
	}
}
func assertPublicPluginMapParam(t *testing.T, params map[string]PluginParameter, name string, values map[string]string) {
	t.Helper()
	param := params[name]
	if param.Map == nil {
		t.Fatalf("Plugin.Parameters = %#v, want %s map", params, name)
	}
	if !stringMapsEqual(param.Map.Values, values) {
		t.Fatalf("Plugin.Parameters[%s].Map = %#v, want %#v", name, param.Map.Values, values)
	}
}
func assertPublicPluginArrayParam(t *testing.T, params map[string]PluginParameter, name string, values []string) {
	t.Helper()
	param := params[name]
	if param.Array == nil {
		t.Fatalf("Plugin.Parameters = %#v, want %s array", params, name)
	}
	if !slices.Equal(param.Array.Values, values) {
		t.Fatalf("Plugin.Parameters[%s].Array = %#v, want %#v", name, param.Array.Values, values)
	}
}
func assertRenderedPluginConfigMap(t *testing.T, result RenderResult) {
	t.Helper()
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if got := result.Manifests[0].Object["kind"]; got != "ConfigMap" {
		t.Fatalf("kind = %#v, want ConfigMap", got)
	}
	metadata, ok := result.Manifests[0].Object["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v, want object", result.Manifests[0].Object["metadata"])
	}
	if metadata["namespace"] != "rendered" {
		t.Fatalf("metadata.namespace = %#v, want rendered", metadata["namespace"])
	}
}
func publicPluginConfigMapResult() PluginResult {
	return PluginResult{Manifests: []PluginManifest{{
		Path: "plugin/cm.yaml",
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name": "from-plugin",
			},
			"data": map[string]any{"value": "rendered"},
		},
	}}}
}
func stringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		rightValue, ok := right[key]
		if !ok || rightValue != leftValue {
			return false
		}
	}
	return true
}

type recordingGitAcquirer struct {
	path      string
	revision  string
	fromCache bool
	network   bool
	err       error
	requests  []GitRequest
	options   []GitOptions
}

func (acquirer *recordingGitAcquirer) Acquire(_ context.Context, request GitRequest, opts GitOptions) (GitResult, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return GitResult{}, acquirer.err
	}
	return GitResult{Path: acquirer.path, Revision: acquirer.revision, FromCache: acquirer.fromCache, Network: acquirer.network}, nil
}

type recordingChartAcquirer struct {
	chartDir  string
	fromCache bool
	err       error
	requests  []ChartRequest
	options   []ChartOptions
}

func (acquirer *recordingChartAcquirer) Acquire(_ context.Context, request ChartRequest, opts ChartOptions) (ChartResult, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return ChartResult{}, acquirer.err
	}
	return ChartResult{
		ChartDir:   acquirer.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  acquirer.fromCache,
	}, nil
}

type recordingRemoteAcquirer struct {
	path      string
	revision  string
	fromCache bool
	err       error
	requests  []RemoteResourceRequest
	options   []RemoteResourceOptions
}

func (acquirer *recordingRemoteAcquirer) Acquire(_ context.Context, request RemoteResourceRequest, opts RemoteResourceOptions) (RemoteResourceResult, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return RemoteResourceResult{}, acquirer.err
	}
	return RemoteResourceResult{Path: acquirer.path, URL: request.URL, Revision: acquirer.revision, FromCache: acquirer.fromCache}, nil
}
func writeDiffTrees(t *testing.T, leftVersion, rightVersion string) (string, string) {
	t.Helper()
	left := t.TempDir()
	right := t.TempDir()
	writeAPIAppTree(t, left, "demo", configMapBody("demo", leftVersion))
	writeAPIAppTree(t, right, "demo", configMapBody("demo", rightVersion))
	return left, right
}
func writeImageDiffTrees(t *testing.T, leftImage, rightImage string) (string, string) {
	t.Helper()
	left := t.TempDir()
	right := t.TempDir()
	writeAPIAppTree(t, left, "demo", deploymentBody("demo", leftImage))
	writeAPIAppTree(t, right, "demo", deploymentBody("demo", rightImage))
	return left, right
}

func writeIgnoredFieldDiffTrees(t *testing.T, leftChartVersion, rightChartVersion string) (string, string) {
	t.Helper()
	left := t.TempDir()
	right := t.TempDir()
	writeAPIAppTree(t, left, "demo", helmMetadataDeploymentBody("demo", leftChartVersion))
	writeAPIAppTree(t, right, "demo", helmMetadataDeploymentBody("demo", rightChartVersion))
	return left, right
}

func initPublicGitRepo(t *testing.T, root string) (*git.Repository, *git.Worktree) {
	t.Helper()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	return repo, wt
}

func commitPublicGitRepo(t *testing.T, repo *git.Repository, wt *git.Worktree, message string) plumbing.Hash {
	t.Helper()
	if _, err := wt.Add("."); err != nil {
		t.Fatalf("Add(.) error = %v", err)
	}
	hash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Commit(%s) error = %v", message, err)
	}
	return hash
}

func checkoutPublicGitBranch(t *testing.T, wt *git.Worktree, name string) {
	t.Helper()
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	}); err != nil {
		t.Fatalf("Checkout(create %s) error = %v", name, err)
	}
}

func writeAPIPluginAppTree(t *testing.T, root string) {
	t.Helper()
	writeAPIPluginAppTreeNamed(t, root, "plugin-app")
}
func writeAPIPluginAppTreeNamed(t *testing.T, root, name string) {
	t.Helper()
	writeAPIFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: apps/plugin
    plugin:
      name: cue
      env:
        - name: FEATURE
          value: enabled
      parameters:
        - name: mode
          string: fast
        - name: labels
          map:
            tier: backend
        - name: args
          array:
            - --debug
        - name: empty-map
          map: {}
        - name: empty-array
          array: []
  destination:
    name: in-cluster
    namespace: rendered
`)
	if err := os.MkdirAll(filepath.Join(root, "apps", "plugin"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
}
func writeAPIAppTree(t *testing.T, root, name, manifest string) {
	t.Helper()
	writeAPIFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: manifests/`+name+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeAPIFile(t, filepath.Join(root, "manifests", name, "manifest.yaml"), manifest)
}
func writeAPIAppTreeWithProject(t *testing.T, root, appName, projectName, repoURL string) {
	t.Helper()
	writeAPIFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
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
    namespace: default
`)
	writeAPIFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), configMapBody(appName, "v1"))
}
func configMapBody(name, version string) string {
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: ` + name + `
data:
  version: ` + version + `
`
}
func deploymentBody(name, image string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + `
spec:
  selector:
    matchLabels:
      app: ` + name + `
  template:
    metadata:
      labels:
        app: ` + name + `
    spec:
      containers:
        - name: ` + name + `
          image: ` + image + `
`
}

func helmMetadataDeploymentBody(name, chartVersion string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + `
  labels:
    helm.sh/chart: ` + chartVersion + `
spec:
  selector:
    matchLabels:
      app: ` + name + `
  template:
    metadata:
      annotations:
        checksum/config: ` + chartVersion + `
      labels:
        helm.sh/chart: ` + chartVersion + `
    spec:
      containers:
        - name: ` + name + `
          image: repo/` + name + `:v1
`
}

func deploymentObject(name, image string) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name": name,
		},
		"spec": map[string]any{
			"selector": map[string]any{
				"matchLabels": map[string]any{"app": name},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app": name},
				},
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":  name,
							"image": image,
						},
					},
				},
			},
		},
	}
}
func writeAPIChart(t *testing.T, chartDir, name, version, template string) {
	t.Helper()
	writeAPIFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: `+name+`
version: `+version+`
`)
	writeAPIFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), template)
}
func writeAPIChartApplication(t *testing.T, root string) {
	t.Helper()
	writeAPIFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    chart: demo
    targetRevision: 1.2.3
  destination:
    name: in-cluster
    namespace: default
`)
}
func writeAPIFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
func hasDiagnostic(diagnostics []Diagnostic, category, message string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Category == category && strings.Contains(diagnostic.Message, message) {
			return true
		}
	}
	return false
}
func hasDiagnosticCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
func hasPublicCacheEvent(events []CacheEvent, source, action, target string) bool {
	for _, event := range events {
		if event.Source == source && event.Action == action && event.Target == target {
			return true
		}
	}
	return false
}
func hasStatus(statuses []ApplicationStatus, name, status string) bool {
	for _, item := range statuses {
		if item.Application.Name == name && item.Status == status {
			return true
		}
	}
	return false
}
func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
func assertAPIMessageRedacted(t *testing.T, message string, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(message, secret) {
			t.Fatalf("message = %q leaked secret %q", message, secret)
		}
	}
	if strings.Contains(message, "[redacted]") {
		return
	}
	if message != "" {
		t.Fatalf("message = %q, want redacted marker", message)
	}
}
func boolPtr(value bool) *bool {
	return &value
}
