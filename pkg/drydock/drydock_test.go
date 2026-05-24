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

	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func TestRenderApplications(t *testing.T) {
	result, err := Render(context.Background(), Config{Path: filepath.Join("..", "..", "testdata", "applications", "e2e")})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want 1", len(result.Manifests))
	}
}

func TestPublicRenderParallelismPreservesManifestOrder(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTreeNamed(t, root, "app-a")
	writeAPIPluginAppTreeNamed(t, root, "app-b")
	writeAPIPluginAppTreeNamed(t, root, "app-c")
	renderer := newControlledPublicPluginRenderer([]string{"app-a", "app-b", "app-c"})

	resultCh := make(chan struct {
		result RenderResult
		err    error
	}, 1)
	go func() {
		result, err := Render(context.Background(), Config{
			Path:           root,
			Parallelism:    3,
			PluginRenderer: renderer,
		})
		resultCh <- struct {
			result RenderResult
			err    error
		}{result: result, err: err}
	}()

	renderer.waitStarted(t, "app-a", "app-b", "app-c")
	renderer.release("app-c")
	renderer.release("app-b")
	renderer.release("app-a")

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Render() error = %v", out.err)
	}
	var names []string
	for _, manifest := range out.result.Manifests {
		metadata, ok := manifest.Object["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("manifest metadata = %#v, want object", manifest.Object["metadata"])
		}
		names = append(names, metadata["name"].(string))
	}
	if !slices.Equal(names, []string{"app-a", "app-b", "app-c"}) {
		t.Fatalf("manifest names = %#v, want selected Application order", names)
	}
}

func TestPublicConfigParallelismWiresRequests(t *testing.T) {
	client := NewClient(Config{Parallelism: 5})

	if got := client.buildRequest().Parallelism; got != 5 {
		t.Fatalf("build request Parallelism = %d, want 5", got)
	}
	if got := client.diffRequest().Parallelism; got != 5 {
		t.Fatalf("diff request Parallelism = %d, want 5", got)
	}
}

func TestRenderAppliesResourceFilters(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "v1"))

	result, err := Render(context.Background(), Config{
		Path:      root,
		SkipKinds: []string{"ConfigMap"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("Manifests = %d, want 0 after ConfigMap filter", len(result.Manifests))
	}
	if !hasStatus(result.Statuses, "demo", "PASS") {
		t.Fatalf("Statuses = %#v, want PASS status for filtered render", result.Statuses)
	}
}

func TestRenderReportsAdvancedSettingsDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "v1"))
	writeAPIFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.ignoreResourceUpdates.apps_Deployment: |
    jsonPointers:
      - /status
  resource.customizations.health.apps_Deployment: |
    return { status = "Healthy" }
  resource.customizations.useOpenLibs.apps_Deployment: "true"
  resource.customizations.actions.apps_Deployment: |
    definitions:
      - name: restart
        action.lua: |
          return obj
`)

	result, err := Render(context.Background(), Config{Path: root})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "ignoreResourceUpdates are parsed but not applied") {
		t.Fatalf("Diagnostics = %#v, want ignoreResourceUpdates warning", result.Diagnostics)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "health Lua is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want health warning", result.Diagnostics)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "useOpenLibs is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want useOpenLibs warning", result.Diagnostics)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "actions are parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want actions warning", result.Diagnostics)
	}
}

func TestRenderReportsProjectValidationDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTreeWithProject(t, root, "demo", "platform", "https://github.com/example/denied")
	writeAPIFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/allowed
  destinations:
    - server: https://kubernetes.default.svc
      namespace: default
`)

	result, err := Render(context.Background(), Config{Path: root})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnostic(result.Diagnostics, "project", "source repository") {
		t.Fatalf("Diagnostics = %#v, want project source warning", result.Diagnostics)
	}
}

func TestRenderReturnsDiagnosticCodes(t *testing.T) {
	root := t.TempDir()
	writeAPIFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
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
      destination:
        name: in-cluster
        namespace: default
`)

	result, err := Render(context.Background(), Config{Path: root})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnosticCode(result.Diagnostics, "appset.unsupported-generator") {
		t.Fatalf("Diagnostics = %#v, want stable appset diagnostic code", result.Diagnostics)
	}
}

func TestPublicConfigRoutesProviderFixtureDataWithoutInternalTypes(t *testing.T) {
	client := NewClient(Config{
		Path:                           "/tmp/repo",
		ApplicationSetProviderFixtures: []string{"clusters.yaml"},
		ApplicationSetProviderData: ApplicationSetProviderData{
			Clusters: []ApplicationSetProviderCluster{{
				Name:        "prod",
				Server:      "https://prod.example.invalid",
				Project:     "platform",
				Labels:      map[string]string{"environment": "prod"},
				Annotations: map[string]string{"owner": "platform"},
				Values:      map[string]string{"region": "home"},
			}},
		},
	})

	buildRequest := client.buildRequest()
	if !slices.Equal(buildRequest.ApplicationSetProviderFixtures, []string{"clusters.yaml"}) {
		t.Fatalf("ApplicationSetProviderFixtures = %#v, want clusters.yaml", buildRequest.ApplicationSetProviderFixtures)
	}
	if got := buildRequest.ApplicationSetProviderData.Clusters; len(got) != 1 || got[0].Name != "prod" || got[0].Labels["environment"] != "prod" {
		t.Fatalf("ApplicationSetProviderData.Clusters = %#v, want public data converted to internal request data", got)
	}

	diffRequest := client.diffRequest()
	if !slices.Equal(diffRequest.ApplicationSetProviderFixtures, []string{"clusters.yaml"}) {
		t.Fatalf("diff ApplicationSetProviderFixtures = %#v, want clusters.yaml", diffRequest.ApplicationSetProviderFixtures)
	}
	if got := diffRequest.ApplicationSetProviderData.Clusters; len(got) != 1 || got[0].Name != "prod" {
		t.Fatalf("diff ApplicationSetProviderData.Clusters = %#v, want public data converted to internal request data", got)
	}
}

func TestClientUsesInjectedAcquirersForRemoteSources(t *testing.T) {
	root := t.TempDir()
	externalRepo := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	remoteFile := filepath.Join(t.TempDir(), "remote.yaml")
	writeAPIFile(t, filepath.Join(externalRepo, "external", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeAPIFile(t, filepath.Join(externalRepo, "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
`)
	writeAPIChart(t, chartDir, "demo", "1.2.3", `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	writeAPIFile(t, remoteFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	writeAPIFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: https://git.example.test/org/repo.git
    targetRevision: main
    path: external
  destination:
    name: in-cluster
    namespace: default
`)
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
	writeAPIFile(t, filepath.Join(root, "apps", "remote.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: remote
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: remote
  destination:
    name: in-cluster
    namespace: default
`)
	writeAPIFile(t, filepath.Join(root, "remote", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/remote.yaml
`)

	gitAcquirer := &recordingGitAcquirer{path: externalRepo, revision: "abc123"}
	chartAcquirer := &recordingChartAcquirer{chartDir: chartDir}
	remoteAcquirer := &recordingRemoteAcquirer{path: remoteFile}
	result, err := NewClient(Config{
		Path:                   root,
		AllowNetwork:           true,
		GitCredentials:         GitCredentials{Username: "git-user", Password: "git-pass"},
		ChartCredentials:       ChartCredentials{BearerToken: "helm-token"},
		GitAcquirer:            gitAcquirer,
		ChartAcquirer:          chartAcquirer,
		RemoteResourceAcquirer: remoteAcquirer,
	}).Render(context.Background())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Manifests) != 3 {
		t.Fatalf("Manifests = %d, want 3", len(result.Manifests))
	}
	if len(gitAcquirer.requests) != 1 {
		t.Fatalf("Git acquire calls = %d, want 1", len(gitAcquirer.requests))
	}
	if !gitAcquirer.options[0].AllowNetwork {
		t.Fatalf("Git AllowNetwork = false, want true")
	}
	if gitAcquirer.options[0].Credentials.Username != "git-user" || gitAcquirer.options[0].Credentials.Password != "git-pass" {
		t.Fatalf("Git credentials were not passed through")
	}
	if len(chartAcquirer.requests) != 1 {
		t.Fatalf("Chart acquire calls = %d, want 1", len(chartAcquirer.requests))
	}
	if chartAcquirer.requests[0].Repository != "https://charts.example.test" {
		t.Fatalf("Chart repository = %q", chartAcquirer.requests[0].Repository)
	}
	if chartAcquirer.options[0].Credentials.BearerToken != "helm-token" {
		t.Fatalf("Chart bearer token was not passed through")
	}
	if len(remoteAcquirer.requests) != 1 {
		t.Fatalf("Remote acquire calls = %d, want 1", len(remoteAcquirer.requests))
	}
}

func TestPublicRenderReturnsCacheEventsWhenEnabled(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeAPIChart(t, chartDir, "demo", "1.2.3", `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	writeAPIChartApplication(t, root)

	result, err := Render(context.Background(), Config{
		Path:              root,
		RecordCacheEvents: true,
		ChartAcquirer:     &recordingChartAcquirer{chartDir: chartDir, fromCache: true},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasPublicCacheEvent(result.CacheEvents, "chart", "hit", "https://charts.example.test") {
		t.Fatalf("CacheEvents = %#v, want chart hit", result.CacheEvents)
	}
}

func TestPublicDiffApplicationsReturnsCacheEventsWhenEnabled(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeAPIChart(t, chartDir, "demo", "1.2.3", `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	writeAPIChartApplication(t, left)
	writeAPIChartApplication(t, right)

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:          left,
		Path:              right,
		ChangedOnly:       boolPtr(false),
		RecordCacheEvents: true,
		ChartAcquirer:     &recordingChartAcquirer{chartDir: chartDir, fromCache: true},
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if len(result.CacheEvents) != 2 {
		t.Fatalf("CacheEvents = %#v, want left then right events", result.CacheEvents)
	}
	for _, event := range result.CacheEvents {
		if event.Source != "chart" || event.Action != "hit" || event.Target != "https://charts.example.test" {
			t.Fatalf("CacheEvents = %#v, want chart hits", result.CacheEvents)
		}
	}
}

func TestPublicDiffImagesReturnsCacheEventsWhenEnabled(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeAPIChart(t, chartDir, "demo", "1.2.3", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: charted
spec:
  selector:
    matchLabels:
      app: charted
  template:
    metadata:
      labels:
        app: charted
    spec:
      containers:
        - name: app
          image: repo/demo:v1
`)
	writeAPIChartApplication(t, left)
	writeAPIChartApplication(t, right)

	result, err := DiffImages(context.Background(), Config{
		PathOrig:          left,
		Path:              right,
		ChangedOnly:       boolPtr(false),
		RecordCacheEvents: true,
		ChartAcquirer:     &recordingChartAcquirer{chartDir: chartDir, fromCache: true},
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if len(result.CacheEvents) != 2 {
		t.Fatalf("CacheEvents = %#v, want left then right events", result.CacheEvents)
	}
	for _, event := range result.CacheEvents {
		if event.Source != "chart" || event.Action != "hit" || event.Target != "https://charts.example.test" {
			t.Fatalf("CacheEvents = %#v, want chart hits", result.CacheEvents)
		}
	}
}

func TestClientPassesRemoteResourceCredentials(t *testing.T) {
	root := t.TempDir()
	remoteFile := filepath.Join(t.TempDir(), "remote.yaml")
	writeAPIFile(t, filepath.Join(root, "apps", "remote.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: remote
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: remote
  destination:
    name: in-cluster
    namespace: default
`)
	writeAPIFile(t, filepath.Join(root, "remote", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/remote.yaml
`)
	writeAPIFile(t, remoteFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	credentials := RemoteResourceCredentials{
		Username:    "remote-user",
		Password:    "remote-pass",
		BearerToken: "remote-token",
	}
	gitCredentials := GitCredentials{
		Username:          "git-user",
		Password:          "git-pass",
		BearerToken:       "git-token",
		SSHPrivateKeyPath: filepath.Join(root, "id_ed25519"),
		SSHPassphrase:     "git-phrase",
		SSHKnownHostsPath: filepath.Join(root, "known_hosts"),
	}
	remoteAcquirer := &recordingRemoteAcquirer{path: remoteFile}

	_, err := NewClient(Config{
		Path:                      root,
		Offline:                   true,
		RefreshRemoteResources:    true,
		RemoteResourceCacheDir:    t.TempDir(),
		RemoteResourceCredentials: credentials,
		GitCredentials:            gitCredentials,
		RemoteResourceAcquirer:    remoteAcquirer,
	}).Render(context.Background())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(remoteAcquirer.options) != 1 {
		t.Fatalf("remote options = %d, want 1", len(remoteAcquirer.options))
	}
	if got := remoteAcquirer.options[0].Credentials; got != credentials {
		t.Fatalf("remote credentials = %#v, want %#v", got, credentials)
	}
	if got := remoteAcquirer.options[0].GitCredentials; got != gitCredentials {
		t.Fatalf("remote git credentials = %#v, want %#v", got, gitCredentials)
	}
}

func TestClientPassesRemoteGitMetadataToInjectedRemoteAcquirer(t *testing.T) {
	remoteAcquirer := &recordingRemoteAcquirer{
		path:      t.TempDir(),
		revision:  "resolved-sha",
		fromCache: true,
	}
	adapter := remoteResourceAcquirerAdapter{acquirer: remoteAcquirer}

	result, err := adapter.Acquire(context.Background(), remote.Request{
		URL:      "git::https://github.com/example/repo.git//manifests?ref=v1",
		Kind:     remote.RequestGitRepo,
		RepoURL:  "https://github.com/example/repo.git",
		Revision: "v1",
	}, remote.Options{
		CacheDir: t.TempDir(),
		GitCredentials: remote.GitCredentials{
			Username:    "git-user",
			Password:    "git-pass",
			BearerToken: "git-token",
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if len(remoteAcquirer.requests) != 1 {
		t.Fatalf("remote requests = %d, want 1", len(remoteAcquirer.requests))
	}
	if got := remoteAcquirer.requests[0]; got.Kind != RemoteResourceGitRepo || got.RepoURL != "https://github.com/example/repo.git" || got.Revision != "v1" {
		t.Fatalf("remote request = %#v, want Git metadata", got)
	}
	if got := remoteAcquirer.options[0].GitCredentials; got.Username != "git-user" || got.Password != "git-pass" || got.BearerToken != "git-token" {
		t.Fatalf("remote git credentials = %#v, want public Git credentials", got)
	}
	if result.Revision != "resolved-sha" || !result.FromCache {
		t.Fatalf("remote result = %#v, want revision/from-cache metadata", result)
	}
}

func TestClientPassesGitCacheMetadataFromInjectedGitAcquirer(t *testing.T) {
	gitAcquirer := &recordingGitAcquirer{path: t.TempDir(), revision: "resolved-sha", fromCache: true, network: false}
	adapter := gitAcquirerAdapter{acquirer: gitAcquirer}

	result, err := adapter.Acquire(context.Background(), sourcepkg.GitRequest{
		URL:      "https://git.example.test/repo.git",
		Revision: "main",
	}, sourcepkg.GitOptions{})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if !result.FromCache {
		t.Fatalf("FromCache = false, want true")
	}
	if result.Network {
		t.Fatalf("Network = true, want false")
	}
}

func TestClientRedactsRemoteResourceCredentialErrors(t *testing.T) {
	root := t.TempDir()
	remoteRef := "https://github.com/example/repo.git//manifests/remote.yaml?ref=secret%2Frevision"
	writeAPIFile(t, filepath.Join(root, "apps", "remote.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: remote
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: remote
  destination:
    name: in-cluster
    namespace: default
`)
	writeAPIFile(t, filepath.Join(root, "remote", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - `+remoteRef+`
`)
	secrets := []string{
		remoteRef,
		"secret%2Frevision",
		"secret/revision",
		"remote-user",
		"remote-pass",
		"remote-token",
		"git-user",
		"git-pass",
		"git-token",
		"git-phrase",
		"private-key-line",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
	}
	remoteAcquirer := &recordingRemoteAcquirer{err: errors.New("failed " + strings.Join(secrets, " "))}

	result, err := NewClient(Config{
		Path:    root,
		Offline: true,
		RemoteResourceCredentials: RemoteResourceCredentials{
			Username:    "remote-user",
			Password:    "remote-pass",
			BearerToken: "remote-token",
		},
		GitCredentials: GitCredentials{
			Username:      "git-user",
			Password:      "git-pass",
			BearerToken:   "git-token",
			SSHPrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-line\n-----END OPENSSH PRIVATE KEY-----",
			SSHPassphrase: "git-phrase",
		},
		RemoteResourceCacheDir: t.TempDir(),
		RemoteResourceAcquirer: remoteAcquirer,
	}).Render(context.Background())
	if err == nil {
		t.Fatal("Render() error = nil, want injected remote failure")
	}
	assertAPIMessageRedacted(t, err.Error(), secrets)
	for _, diagnostic := range result.Diagnostics {
		assertAPIMessageRedacted(t, diagnostic.Message, secrets)
	}
	for _, status := range result.Statuses {
		assertAPIMessageRedacted(t, status.Message, secrets)
	}
}

func TestRenderReturnsPartialResultDiagnosticsAndStatusesOnInjectedError(t *testing.T) {
	root := t.TempDir()
	writeAPIFile(t, filepath.Join(root, "apps", "local.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: local
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: local
  destination:
    name: in-cluster
    namespace: default
`)
	writeAPIFile(t, filepath.Join(root, "local", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: local
`)
	writeAPIFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: https://git.example.test/org/repo.git
    targetRevision: main
    path: external
  destination:
    name: in-cluster
    namespace: default
`)

	result, err := NewClient(Config{
		Path:         root,
		AllowNetwork: true,
		GitAcquirer:  &recordingGitAcquirer{err: errors.New("fixture auth failure")},
	}).Render(context.Background())
	if err == nil {
		t.Fatal("Render() error = nil, want injected failure")
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want partial local manifest", len(result.Manifests))
	}
	if !hasDiagnostic(result.Diagnostics, "render", "fixture auth failure") {
		t.Fatalf("Diagnostics = %#v, want stable render diagnostic with injected error", result.Diagnostics)
	}
	if !hasStatus(result.Statuses, "local", "PASS") {
		t.Fatalf("Statuses = %#v, want PASS status for local app", result.Statuses)
	}
	if !hasStatus(result.Statuses, "external", "FAIL") {
		t.Fatalf("Statuses = %#v, want FAIL status for external app", result.Statuses)
	}
}

func TestRenderUsesInjectedPluginRenderer(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		assertPublicPluginRequest(t, request)
		return publicPluginConfigMapResult(), nil
	})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: renderer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertRenderedPluginConfigMap(t, result)
}

func TestRenderUsesNamedPluginRegistry(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		assertPublicPluginRequest(t, request)
		return publicPluginConfigMapResult(), nil
	})
	registry := NewPluginRegistry(map[string]PluginRenderer{" cue ": renderer})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: registry})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertRenderedPluginConfigMap(t, result)
}

func TestRenderNamedPluginRegistryReportsMissingRenderer(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	result, err := Render(context.Background(), Config{
		Path:           root,
		PluginRenderer: NewPluginRegistry(map[string]PluginRenderer{"jsonnet": nil}),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want missing plugin renderer error")
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("Manifests = %d, want no fallback manifests", len(result.Manifests))
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.unsupported") {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, did not want plugin.failed for registry miss", result.Diagnostics)
	}
	if !hasStatus(result.Statuses, "plugin-app", "FAIL") {
		t.Fatalf("Statuses = %#v, want plugin-app FAIL", result.Statuses)
	}
}

func TestRenderPluginRendererHonorsConfiguredTimeout(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "ok", configMapBody("ok", "v1"))
	writeAPIPluginAppTree(t, root)

	result, err := Render(context.Background(), Config{
		Path:           root,
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Nanosecond,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want plugin timeout")
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want successful non-plugin manifest", len(result.Manifests))
	}
	if !hasStatus(result.Statuses, "ok", "PASS") || !hasStatus(result.Statuses, "plugin-app", "FAIL") {
		t.Fatalf("Statuses = %#v, want ok PASS and plugin-app FAIL", result.Statuses)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffApplicationsPluginRendererHonorsConfiguredTimeout(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Nanosecond,
	})
	if err == nil {
		t.Fatal("DiffApplications() error = nil, want plugin timeout")
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffImagesPluginRendererHonorsConfiguredTimeout(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffImages(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Nanosecond,
	})
	if err == nil {
		t.Fatal("DiffImages() error = nil, want plugin timeout")
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffApplicationsReturnsResultsFromSuccessfulAppsWithPluginFailure(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIAppTree(t, left, "demo", configMapBody("demo", "old"))
	writeAPIAppTree(t, right, "demo", configMapBody("demo", "new"))
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: failingPublicPluginRenderer{},
	})
	if err == nil {
		t.Fatal("DiffApplications() error = nil, want partial plugin render error")
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want successful app diff despite plugin error: %#v", len(result.Results), result.Results)
	}
	if result.Results[0].Parent.Name != "demo" || result.Results[0].Change != "modified" {
		t.Fatalf("Results[0] = %#v, want modified demo diff", result.Results[0])
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffImagesReturnsResultsFromSuccessfulAppsWithPluginFailure(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIAppTree(t, left, "demo", deploymentBody("demo", "repo/demo:v1"))
	writeAPIAppTree(t, right, "demo", deploymentBody("demo", "repo/demo:v2"))
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffImages(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: failingPublicPluginRenderer{},
	})
	if err == nil {
		t.Fatal("DiffImages() error = nil, want partial plugin render error")
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1 despite plugin error", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2 despite plugin error", result.Added)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestRenderPluginRendererPreservesCallerCancellation(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := Render(ctx, Config{
		Path:           root,
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Hour,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Render() error = %v, want context.Canceled", err)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, did not want plugin.failed for caller cancellation", result.Diagnostics)
	}
}

func TestInjectedPluginRendererDiagnosticsKeepStableCodes(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, _ PluginRequest) (PluginResult, error) {
		return PluginResult{
			Manifests: publicPluginConfigMapResult().Manifests,
			Diagnostics: []Diagnostic{{
				Severity: "warning",
				Category: "plugin",
				Message:  "plugin emitted a warning",
			}},
		}, nil
	})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: renderer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.unspecified") {
		t.Fatalf("Diagnostics = %#v, want stable neutral plugin diagnostic code", result.Diagnostics)
	}
}

func TestInjectedPluginRendererErrorPreservesDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "ok", configMapBody("ok", "v1"))
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, _ PluginRequest) (PluginResult, error) {
		return PluginResult{Diagnostics: []Diagnostic{{
			Code:     "plugin.custom",
			Severity: "error",
			Category: "plugin",
			Message:  "renderer supplied diagnostic",
		}}}, errors.New("renderer failed")
	})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: renderer})
	if err == nil {
		t.Fatal("Render() error = nil, want plugin renderer error")
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want successful non-plugin manifest", len(result.Manifests))
	}
	if !hasStatus(result.Statuses, "ok", "PASS") || !hasStatus(result.Statuses, "plugin-app", "FAIL") {
		t.Fatalf("Statuses = %#v, want ok PASS and plugin-app FAIL", result.Statuses)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.custom") {
		t.Fatalf("Diagnostics = %#v, want renderer diagnostic", result.Diagnostics)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "FEATURE=enabled") {
			t.Fatalf("Diagnostics = %#v, leaked plugin env value", result.Diagnostics)
		}
	}
}

func TestListApplications(t *testing.T) {
	result, err := ListApplications(context.Background(), Config{Path: filepath.Join("..", "..", "testdata", "applications", "e2e")})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "demo" {
		t.Fatalf("Applications = %#v, want demo", result.Applications)
	}
}

func TestDiffApplications(t *testing.T) {
	left, right := writeDiffTrees(t, "v1", "v2")

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %q, want modified", result.Results[0].Change)
	}
}

func TestPublicDiffApplicationsParallelismPreservesResults(t *testing.T) {
	left, right := writeDiffTrees(t, "v1", "v2")

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
		Parallelism: 2,
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %q, want modified", result.Results[0].Change)
	}
}

func TestDiffApplicationsUsesInjectedPluginRenderer(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	renderCount := 0
	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		renderCount++
		value := "left"
		if renderCount == 2 {
			value = "right"
		}
		return PluginResult{Manifests: []PluginManifest{{
			Path: "plugin/cm.yaml",
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "from-plugin",
					"namespace": "rendered",
				},
				"data": map[string]any{"value": value},
			},
		}}}, nil
	})

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: renderer,
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if renderCount != 2 {
		t.Fatalf("plugin render calls = %d, want 2", renderCount)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %q, want modified", result.Results[0].Change)
	}
}

func TestDiffImages(t *testing.T) {
	left, right := writeImageDiffTrees(t, "repo/demo:v1", "repo/demo:v2")

	result, err := DiffImages(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2", result.Added)
	}
}

func TestPublicDiffImagesParallelismPreservesResults(t *testing.T) {
	left, right := writeImageDiffTrees(t, "repo/demo:v1", "repo/demo:v2")

	result, err := DiffImages(context.Background(), Config{
		PathOrig:    left,
		Path:        right,
		ChangedOnly: boolPtr(false),
		Parallelism: 2,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2", result.Added)
	}
}

func TestDiffImagesUsesInjectedPluginRenderer(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	renderCount := 0
	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		renderCount++
		image := "repo/demo:v1"
		if renderCount == 2 {
			image = "repo/demo:v2"
		}
		return PluginResult{Manifests: []PluginManifest{{
			Path:   "plugin/deployment.yaml",
			Object: deploymentObject("from-plugin", image),
		}}}, nil
	})

	result, err := DiffImages(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: renderer,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if renderCount != 2 {
		t.Fatalf("plugin render calls = %d, want 2", renderCount)
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2", result.Added)
	}
}

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
