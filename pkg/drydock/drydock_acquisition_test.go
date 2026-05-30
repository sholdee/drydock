package drydock

import (
	"context"
	"errors"
	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"path/filepath"
	"strings"
	"testing"
)

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
		ChangedOnly:       new(false),
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
		ChangedOnly:       new(false),
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

func TestConfigOfflineOverridesDeprecatedAllowNetwork(t *testing.T) {
	root := t.TempDir()
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
	gitAcquirer := &recordingGitAcquirer{err: errors.New("offline cache miss")}

	_, err := NewClient(Config{
		Path:         root,
		Offline:      true,
		AllowNetwork: true,
		GitAcquirer:  gitAcquirer,
	}).Render(context.Background())
	if err == nil {
		t.Fatal("Render() error = nil, want offline cache miss")
	}
	if len(gitAcquirer.options) != 1 {
		t.Fatalf("Git options = %#v, want one call", gitAcquirer.options)
	}
	if gitAcquirer.options[0].AllowNetwork {
		t.Fatalf("Git AllowNetwork = true, want false when Offline is authoritative")
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
		Path:        root,
		GitAcquirer: &recordingGitAcquirer{err: errors.New("fixture auth failure")},
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
