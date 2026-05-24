package render

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/cacheevent"
	"github.com/home-operations/argocd-local/internal/chart"
	"github.com/home-operations/argocd-local/internal/remote"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	return remote.Result{Path: acquiredPath, URL: request.URL, Revision: request.Revision, FromCache: acquirer.fromCache}, nil
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

func TestKustomizeRendererRendersHTTPFileResource(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: remote
resources:
  - https://raw.githubusercontent.com/example/repo/main/crd.yaml
  - local.yaml
`)
	writeFile(t, filepath.Join(root, "app", "local.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: local
`)
	remoteFile := filepath.Join(t.TempDir(), "resource.yaml")
	writeFile(t, remoteFile, `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  names:
    kind: Widget
    plural: widgets
  scope: Namespaced
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`)
	acquirer := &fakeRemoteAcquirer{path: remoteFile}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
		OfflineRemoteResources: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("remote acquire calls = %d, want 1", len(acquirer.requests))
	}
	if acquirer.requests[0].URL != "https://raw.githubusercontent.com/example/repo/main/crd.yaml" {
		t.Fatalf("remote URL = %q", acquirer.requests[0].URL)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("remote acquire options = %d, want 1", len(acquirer.options))
	}
	if !acquirer.options[0].Offline {
		t.Fatalf("remote acquire Offline = false, want true")
	}
	if len(acquirer.options[0].ForbiddenRoots) != 1 || acquirer.options[0].ForbiddenRoots[0] != root {
		t.Fatalf("remote acquire ForbiddenRoots = %#v, want repo root", acquirer.options[0].ForbiddenRoots)
	}
	if !containsManifest(manifests, "CustomResourceDefinition", "widgets.example.com") {
		t.Fatalf("rendered manifests = %#v, want remote CRD", manifests)
	}
	if !containsManifest(manifests, "ConfigMap", "local") {
		t.Fatalf("rendered manifests = %#v, want local ConfigMap", manifests)
	}
}

func TestKustomizeRendererRecordsRemoteCacheEvents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/resource.yaml
`)
	remoteFile := filepath.Join(t.TempDir(), "resource.yaml")
	writeFile(t, remoteFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	acquirer := &fakeRemoteAcquirer{path: remoteFile, fromCache: true}
	recorder := cacheevent.NewRecorder(true)

	_, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
		CacheEventRecorder:     recorder,
	})
	if err != nil {
		t.Fatalf("Render() error = %v, diagnostics = %#v", err, diags)
	}
	if !hasRenderCacheEvent(recorder.Events(), "remote", "hit", "https://raw.githubusercontent.com/example/repo/main/resource.yaml") {
		t.Fatalf("Cache events = %#v, want remote hit", recorder.Events())
	}
}

func TestKustomizeRendererPassesRemoteCredentialsForHTTPResources(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/resource.yaml
`)
	remoteFile := filepath.Join(t.TempDir(), "resource.yaml")
	writeFile(t, remoteFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	credentials := remote.Credentials{
		Username:    "remote-user",
		Password:    "remote-pass",
		BearerToken: "remote-token",
	}
	acquirer := &fakeRemoteAcquirer{path: remoteFile}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer:    acquirer,
		RemoteResourceCredentials: credentials,
		RemoteResourceCacheDir:    t.TempDir(),
		OfflineRemoteResources:    true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("remote acquire options = %d, want 1", len(acquirer.options))
	}
	if got := acquirer.options[0].Credentials; got != credentials {
		t.Fatalf("remote credentials = %#v, want %#v", got, credentials)
	}
}

func TestKustomizeRendererPassesRemoteGitCredentialsAndCacheOptions(t *testing.T) {
	root := t.TempDir()
	cacheDir := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//manifests/resource.yaml?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	remoteFile := filepath.Join(remoteRepo, "manifests", "resource.yaml")
	writeFile(t, remoteFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	gitCredentials := remote.GitCredentials{
		Username:          "git-user",
		Password:          "git-pass",
		BearerToken:       "git-token",
		SSHPrivateKeyPath: filepath.Join(root, "id_ed25519"),
		SSHPassphrase:     "git-phrase",
		SSHKnownHostsPath: filepath.Join(root, "known_hosts"),
	}
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer:       acquirer,
		RemoteResourceGitCredentials: gitCredentials,
		RemoteResourceCacheDir:       cacheDir,
		OfflineRemoteResources:       true,
		RefreshRemoteResources:       true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("remote acquire options = %d, want 1", len(acquirer.options))
	}
	if got := acquirer.options[0]; got.CacheDir != cacheDir || !got.Offline || !got.Refresh {
		t.Fatalf("remote cache options = %#v, want cache/offline/refresh", got)
	}
	if got := acquirer.requests[0]; got.Kind != remote.RequestGitRepo || got.RepoURL != "https://github.com/example/repo.git" || got.Revision != "v1.2.3" {
		t.Fatalf("remote request = %#v, want Git repo metadata", got)
	}
	if got := acquirer.options[0].GitCredentials; got != gitCredentials {
		t.Fatalf("remote git credentials = %#v, want %#v", got, gitCredentials)
	}
}

func TestKustomizeRendererRendersRemoteGitResourceDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !containsManifest(manifests, "ConfigMap", "remote-base") {
		t.Fatalf("rendered manifests = %#v, want remote base ConfigMap", manifests)
	}
}

func TestKustomizeRendererRendersRemoteGitResourceBase(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - https://github.com/example/remote-base.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("remote acquire calls = %d, want 1", len(acquirer.requests))
	}
	if !containsManifest(manifests, "ConfigMap", "remote-base") {
		t.Fatalf("rendered manifests = %#v, want remote base ConfigMap", manifests)
	}
}

func TestKustomizeRendererAllowsRemoteRelativeRefsInsideRemoteRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//overlays/dev?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "overlays", "dev", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !containsManifest(manifests, "ConfigMap", "remote-base") {
		t.Fatalf("rendered manifests = %#v, want remote base ConfigMap", manifests)
	}
}

func TestKustomizeRendererIgnoresUnrelatedCallerSymlinkInWorkspaceCopy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(root, "unrelated-link.yaml"))
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !containsManifest(manifests, "ConfigMap", "remote-base") {
		t.Fatalf("rendered manifests = %#v, want remote base ConfigMap", manifests)
	}
}

func TestKustomizeRendererRejectsCallerGeneratedDirSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	outside := t.TempDir()
	symlink(t, outside, filepath.Join(root, "app", ".argocd-local"))
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want generated symlink rejection")
	}
	if !strings.Contains(err.Error(), "generated kustomize path") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want generated symlink rejection", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("outside symlink target entries = %d, want 0", len(entries))
	}
}

func TestKustomizeRendererIgnoresUnrelatedRemoteSymlinkOutsideGraph(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(remoteRepo, "unrelated-link.yaml"))
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !containsManifest(manifests, "ConfigMap", "remote-base") {
		t.Fatalf("rendered manifests = %#v, want remote base ConfigMap", manifests)
	}
}

func TestKustomizeRendererRejectsRemoteGeneratedDirSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/outer.git//base?ref=v1.2.3
`)
	outerRepo := t.TempDir()
	writeFile(t, filepath.Join(outerRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/inner.git//base?ref=v2.0.0
`)
	outside := t.TempDir()
	symlink(t, outside, filepath.Join(outerRepo, "base", ".argocd-local"))
	innerRepo := t.TempDir()
	writeFile(t, filepath.Join(innerRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(innerRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: inner-base
`)
	acquirer := &fakeRemoteAcquirer{paths: map[string]string{
		"https://github.com/example/outer.git": outerRepo,
		"https://github.com/example/inner.git": innerRepo,
	}}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want generated symlink rejection")
	}
	if !strings.Contains(err.Error(), "generated kustomize path") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want generated symlink rejection", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("outside symlink target entries = %d, want 0", len(entries))
	}
}

func TestKustomizeRendererRendersRemoteGitComponent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
components:
  - https://github.com/example/remote-component.git//component?ref=v1.2.3
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: local
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "component", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - serviceaccount.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "component", "serviceaccount.yaml"), `apiVersion: v1
kind: ServiceAccount
metadata:
  name: remote-component
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !containsManifest(manifests, "ConfigMap", "local") {
		t.Fatalf("rendered manifests = %#v, want local ConfigMap", manifests)
	}
	if !containsManifest(manifests, "ServiceAccount", "remote-component") {
		t.Fatalf("rendered manifests = %#v, want remote component ServiceAccount", manifests)
	}
}

func TestKustomizeRendererRendersNestedRemoteKustomizeRefs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/outer.git//base?ref=v1.0.0
`)
	outerRepo := t.TempDir()
	writeFile(t, filepath.Join(outerRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
  - https://github.com/example/inner.git//base?ref=v2.0.0
`)
	writeFile(t, filepath.Join(outerRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: outer
`)
	innerRepo := t.TempDir()
	writeFile(t, filepath.Join(innerRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(innerRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: inner
`)
	acquirer := &fakeRemoteAcquirer{paths: map[string]string{
		"https://github.com/example/outer.git": outerRepo,
		"https://github.com/example/inner.git": innerRepo,
	}}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 2 {
		t.Fatalf("remote acquire calls = %d, want 2", len(acquirer.requests))
	}
	if !containsManifest(manifests, "ConfigMap", "outer") {
		t.Fatalf("rendered manifests = %#v, want outer ConfigMap", manifests)
	}
	if !containsManifest(manifests, "ConfigMap", "inner") {
		t.Fatalf("rendered manifests = %#v, want nested inner ConfigMap", manifests)
	}
}

func TestKustomizeRendererRejectsRemoteBoundaryEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../outside.yaml
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want remote boundary escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want escape rejection", err)
	}
}

func TestKustomizeRendererRejectsRemotePathBearingBoundaryEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	writeFile(t, filepath.Join(root, "app", "caller-patch.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
  labels:
    leaked: "true"
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
patches:
  - path: ../../../../caller-patch.yaml
    target:
      version: v1
      kind: ConfigMap
      name: remote
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want remote path-bearing boundary escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want escape rejection", err)
	}
}

func TestKustomizeRendererRejectsRemoteHelmBoundaryEscapes(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		want          string
	}{
		{
			name: "chartHome",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmGlobals:
  chartHome: ../../../../caller-charts
helmCharts:
  - name: demo
    releaseName: demo
`,
			want: "escapes",
		},
		{
			name: "chart name",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmGlobals:
  chartHome: .
helmCharts:
  - name: ../../../../caller-chart
    releaseName: demo
`,
			want: "escapes",
		},
		{
			name: "nameTemplate",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
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
			name: "valuesFile",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    valuesFile: ../../../../caller-values.yaml
`,
			want: "escapes",
		},
		{
			name: "additionalValuesFiles",
			kustomization: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    additionalValuesFiles:
      - ../../../../caller-values.yaml
`,
			want: "escapes",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
			remoteRepo := t.TempDir()
			writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), tt.kustomization)
			acquirer := &fakeRemoteAcquirer{path: remoteRepo}

			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, RenderOptions{
				RemoteResourceAcquirer: acquirer,
				RemoteResourceCacheDir: t.TempDir(),
			})
			if err == nil {
				t.Fatalf("Render() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Render() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestKustomizeRendererRejectsRemoteHelmValueRefsWithoutLeakingSecrets(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		opts RenderOptions
	}{
		{
			name: "local",
			body: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    valuesFile: https://user:secret@example.test/values.yaml?token=secret
`,
		},
		{
			name: "remote",
			body: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`,
			opts: RenderOptions{RemoteResourceAcquirer: &fakeRemoteAcquirer{}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), tt.body)
			if tt.name == "remote" {
				remoteRepo := t.TempDir()
				writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    additionalValuesFiles:
      - https://user:secret@example.test/values.yaml?token=secret
`)
				tt.opts.RemoteResourceAcquirer = &fakeRemoteAcquirer{path: remoteRepo}
			}

			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, tt.opts)
			if err == nil {
				t.Fatal("Render() error = nil, want remote Helm values rejection")
			}
			if !strings.Contains(err.Error(), "remote") {
				t.Fatalf("Render() error = %v, want remote ref rejection", err)
			}
			for _, secret := range []string{"user", "secret", "token=secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Render() error = %q leaked %q", err.Error(), secret)
				}
			}
		})
	}
}

func TestKustomizeRendererRejectsRemoteHTTPComponentWithoutAcquire(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
components:
  - https://raw.githubusercontent.com/example/repo/main/component.yaml
resources:
  - local.yaml
`)
	writeFile(t, filepath.Join(root, "app", "local.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: local
`)
	acquirer := &fakeRemoteAcquirer{path: filepath.Join(t.TempDir(), "component.yaml")}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want remote HTTP component rejection")
	}
	if !strings.Contains(err.Error(), "must resolve to a Kustomization directory") {
		t.Fatalf("Render() error = %v, want Kustomization directory rejection", err)
	}
	if len(acquirer.requests) != 0 {
		t.Fatalf("remote acquire calls = %d, want 0", len(acquirer.requests))
	}
}

func TestKustomizeRendererRejectsRemoteSymlinkedGraphEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/remote.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - linked.yaml
`)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(remoteRepo, "base", "linked.yaml"))
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink rejection", err)
	}
}

func TestKustomizeRendererRejectsRemoteGitSubpathEscape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//../outside.yaml?ref=v1.2.3
`)
	acquirer := &fakeRemoteAcquirer{path: t.TempDir()}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want subpath escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes acquired repository") {
		t.Fatalf("Render() error = %v, want acquired repository escape error", err)
	}
}

func TestKustomizeRendererRejectsRemoteGitSymlinkedSubpath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//link.yaml?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: outside\n")
	symlink(t, outside, filepath.Join(remoteRepo, "link.yaml"))
	acquirer := &fakeRemoteAcquirer{path: remoteRepo}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink error", err)
	}
}

func TestKustomizeRendererRejectsRemoteGitSymlinkedRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//base?ref=v1.2.3
`)
	remoteRepo := t.TempDir()
	writeFile(t, filepath.Join(remoteRepo, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(remoteRepo, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
	remoteLink := filepath.Join(t.TempDir(), "remote-link")
	symlink(t, remoteRepo, remoteLink)
	acquirer := &fakeRemoteAcquirer{path: remoteLink}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink root rejection")
	}
	if !strings.Contains(err.Error(), "symlinked repository root") {
		t.Fatalf("Render() error = %v, want symlinked repository root error", err)
	}
}

func TestKustomizeRendererRedactsRemoteAcquireCredentialErrors(t *testing.T) {
	root := t.TempDir()
	remoteRef := "https://github.com/example/repo.git//manifests/resource.yaml?ref=secret%2Frevision"
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
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
	acquirer := &fakeRemoteAcquirer{err: errors.New("failed " + strings.Join(secrets, " "))}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCredentials: remote.Credentials{
			Username:    "remote-user",
			Password:    "remote-pass",
			BearerToken: "remote-token",
		},
		RemoteResourceGitCredentials: remote.GitCredentials{
			Username:      "git-user",
			Password:      "git-pass",
			BearerToken:   "git-token",
			SSHPrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-line\n-----END OPENSSH PRIVATE KEY-----",
			SSHPassphrase: "git-phrase",
		},
		RemoteResourceCacheDir: t.TempDir(),
		OfflineRemoteResources: true,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want acquire failure")
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Render() error = %q, leaked secret %q", err.Error(), secret)
		}
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("Render() error = %q, want redacted marker", err.Error())
	}
}

func TestKustomizeRendererRejectsRemoteGitRefWhenOfflineCacheMisses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo.git//manifests/resource.yaml?ref=v1.2.3
`)

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceCacheDir: t.TempDir(),
		OfflineRemoteResources: true,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want offline cache miss")
	}
	if !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("Render() error = %v, want offline cache miss", err)
	}
}

func TestKustomizeRendererRejectsRemoteResourceSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/resource.yaml
`)
	outside := filepath.Join(t.TempDir(), "secret.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: leaked\n")
	cacheFile := filepath.Join(t.TempDir(), "resource.yaml")
	symlink(t, outside, cacheFile)
	acquirer := &fakeRemoteAcquirer{path: cacheFile}

	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
		OfflineRemoteResources: true,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink rejection", err)
	}
}

func TestKustomizeRendererRendersRemotePatchPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
patches:
  - path: https://raw.githubusercontent.com/example/patches/main/patch-label.yaml
    target:
      version: v1
      kind: ConfigMap
      name: demo
patchesJson6902:
  - target:
      version: v1
      kind: ConfigMap
      name: demo
    path: https://github.com/example/json-patches.git//patches/json6902.yaml?ref=v1.2.3
patchesStrategicMerge:
  - https://raw.githubusercontent.com/example/patches/main/strategic.yaml
`)
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	patchFile := filepath.Join(t.TempDir(), "patch-label.yaml")
	writeFile(t, patchFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
  labels:
    patch-path: remote
`)
	jsonPatchRepo := t.TempDir()
	writeFile(t, filepath.Join(jsonPatchRepo, "patches", "json6902.yaml"), `- op: add
  path: /metadata/labels/json6902
  value: remote
`)
	strategicPatch := filepath.Join(t.TempDir(), "strategic.yaml")
	writeFile(t, strategicPatch, `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
  labels:
    strategic: remote
`)
	acquirer := &fakeRemoteAcquirer{paths: map[string]string{
		"https://raw.githubusercontent.com/example/patches/main/patch-label.yaml": patchFile,
		"https://github.com/example/json-patches.git":                             jsonPatchRepo,
		"https://raw.githubusercontent.com/example/patches/main/strategic.yaml":   strategicPatch,
	}}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 3 {
		t.Fatalf("remote acquire calls = %d, want 3", len(acquirer.requests))
	}
	configMaps := filterObjects(manifests, "ConfigMap")
	if len(configMaps) != 1 {
		t.Fatalf("len(configMaps) = %d, want 1", len(configMaps))
	}
	for key, want := range map[string]string{
		"patch-path": "remote",
		"json6902":   "remote",
		"strategic":  "remote",
	} {
		got, _, _ := unstructured.NestedString(configMaps[0].Object, "metadata", "labels", key)
		if got != want {
			t.Fatalf("ConfigMap label %q = %q, want %q", key, got, want)
		}
	}
}

func TestKustomizeRendererRendersRemoteGeneratorsTransformersValidators(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
generators:
  - https://raw.githubusercontent.com/example/kustomize/main/generator.yaml
transformers:
  - https://github.com/example/kustomize-tools.git//transformers/labels.yaml?ref=v1.2.3
validators:
  - https://raw.githubusercontent.com/example/kustomize/main/validator.yaml
`)
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)
	generator := filepath.Join(t.TempDir(), "generator.yaml")
	writeFile(t, generator, `apiVersion: builtin
kind: ConfigMapGenerator
metadata:
  name: remote-generated
literals:
  - generated=true
`)
	transformerRepo := t.TempDir()
	writeFile(t, filepath.Join(transformerRepo, "transformers", "labels.yaml"), `apiVersion: builtin
kind: LabelTransformer
metadata:
  name: remote-labels
labels:
  transformed: remote
fieldSpecs:
  - path: metadata/labels
    create: true
`)
	validator := filepath.Join(t.TempDir(), "validator.yaml")
	writeFile(t, validator, `apiVersion: builtin
kind: LabelTransformer
metadata:
  name: no-op-validator
labels: {}
fieldSpecs: []
`)
	acquirer := &fakeRemoteAcquirer{paths: map[string]string{
		"https://raw.githubusercontent.com/example/kustomize/main/generator.yaml": generator,
		"https://github.com/example/kustomize-tools.git":                          transformerRepo,
		"https://raw.githubusercontent.com/example/kustomize/main/validator.yaml": validator,
	}}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 3 {
		t.Fatalf("remote acquire calls = %d, want 3", len(acquirer.requests))
	}
	demo := findManifest(manifests, "ConfigMap", "demo")
	if demo == nil {
		t.Fatalf("rendered manifests = %#v, want local ConfigMap", manifests)
	}
	if got, _, _ := unstructured.NestedString(demo.Object, "metadata", "labels", "transformed"); got != "remote" {
		t.Fatalf("demo transformed label = %q, want remote", got)
	}
	if !containsConfigMapData(manifests, "generated", "true") {
		t.Fatalf("rendered manifests = %#v, want generated ConfigMap from remote generator", manifests)
	}
}

func TestKustomizeRendererRendersRemoteGeneratorDataFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generatorOptions:
  disableNameSuffixHash: true
configMapGenerator:
  - name: remote-data
    files:
      - config.txt=https://github.com/example/data.git//files/config.txt?ref=v1.2.3
      - https://github.com/example/data.git//files/plain.txt?ref=v1.2.3
    envs:
      - https://github.com/example/data.git//env/config.env?ref=v1.2.3
    env: https://github.com/example/data.git//env/single.env?ref=v1.2.3
secretGenerator:
  - name: remote-secret
    files:
      - password=https://github.com/example/data.git//files/password.txt?ref=v1.2.3
      - https://github.com/example/data.git//files/token.txt?ref=v1.2.3
    envs:
      - https://github.com/example/data.git//env/secret.env?ref=v1.2.3
    env: https://github.com/example/data.git//env/single-secret.env?ref=v1.2.3
`)
	dataRepo := t.TempDir()
	writeFile(t, filepath.Join(dataRepo, "files", "config.txt"), "from-file\n")
	writeFile(t, filepath.Join(dataRepo, "files", "plain.txt"), "from-unkeyed-file\n")
	writeFile(t, filepath.Join(dataRepo, "files", "password.txt"), "s3cr3t\n")
	writeFile(t, filepath.Join(dataRepo, "files", "token.txt"), "token-value\n")
	writeFile(t, filepath.Join(dataRepo, "env", "config.env"), "ENV_VALUE=from-envs\n")
	writeFile(t, filepath.Join(dataRepo, "env", "single.env"), "SINGLE_VALUE=from-env\n")
	writeFile(t, filepath.Join(dataRepo, "env", "secret.env"), "SECRET_VALUE=from-secret-envs\n")
	writeFile(t, filepath.Join(dataRepo, "env", "single-secret.env"), "SINGLE_SECRET=hidden\n")
	acquirer := &fakeRemoteAcquirer{paths: map[string]string{
		"https://github.com/example/data.git": dataRepo,
	}}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 8 {
		t.Fatalf("remote acquire calls = %d, want 8", len(acquirer.requests))
	}
	assertConfigMapData(t, manifests, "config.txt", "from-file\n")
	assertConfigMapData(t, manifests, "plain.txt", "from-unkeyed-file\n")
	assertConfigMapData(t, manifests, "ENV_VALUE", "from-envs")
	assertConfigMapData(t, manifests, "SINGLE_VALUE", "from-env")
	secret := findManifest(manifests, "Secret", "remote-secret")
	if secret == nil {
		t.Fatalf("rendered manifests = %#v, want generated Secret", manifests)
	}
	for key, want := range map[string]string{
		"password":      "s3cr3t\n",
		"token.txt":     "token-value\n",
		"SECRET_VALUE":  "from-secret-envs",
		"SINGLE_SECRET": "hidden",
	} {
		encoded, _, _ := unstructured.NestedString(secret.Object, "data", key)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("Secret data[%q] decode error = %v", key, err)
		}
		if string(decoded) != want {
			t.Fatalf("Secret data[%q] = %q, want %q", key, string(decoded), want)
		}
	}
}

func TestSplitGeneratorFileSourceHandlesRemoteRefs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		source  string
		wantKey string
		wantRef string
		wantSet bool
	}{
		{
			name:    "explicit key remote",
			source:  "config.txt=https://github.com/example/data.git//files/config.txt?ref=v1.2.3",
			wantKey: "config.txt",
			wantRef: "https://github.com/example/data.git//files/config.txt?ref=v1.2.3",
			wantSet: true,
		},
		{
			name:    "unkeyed remote with query",
			source:  "https://github.com/example/data.git//files/plain.txt?ref=v1.2.3",
			wantRef: "https://github.com/example/data.git//files/plain.txt?ref=v1.2.3",
		},
		{
			name:    "local key",
			source:  "config.txt=files/config.txt",
			wantKey: "config.txt",
			wantRef: "files/config.txt",
			wantSet: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotRef, gotSet := splitGeneratorFileSource(tt.source)
			if gotKey != tt.wantKey || gotRef != tt.wantRef || gotSet != tt.wantSet {
				t.Fatalf("splitGeneratorFileSource() = (%q, %q, %t), want (%q, %q, %t)", gotKey, gotRef, gotSet, tt.wantKey, tt.wantRef, tt.wantSet)
			}
		})
	}
}

func TestKustomizeRendererRendersRemoteCrdsOpenAPIConfigurationsReplacements(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namePrefix: prod-
resources:
  - source.yaml
  - target.yaml
  - settings.yaml
  - deployment.yaml
crds:
  - https://raw.githubusercontent.com/example/schema/main/widgets.json
openapi:
  path: https://github.com/example/schema.git//openapi/schema.json?ref=v1.2.3
configurations:
  - https://raw.githubusercontent.com/example/schema/main/name-reference.yaml
replacements:
  - path: https://github.com/example/replacements.git//configs/replacement.yaml?ref=v1.2.3
`)
	writeFile(t, filepath.Join(root, "app", "source.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: source
data:
  value: from-source
`)
	writeFile(t, filepath.Join(root, "app", "target.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: target
data:
  value: placeholder
`)
	writeFile(t, filepath.Join(root, "app", "settings.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
`)
	writeFile(t, filepath.Join(root, "app", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      volumes:
        - name: config
          configMap:
            name: settings
      containers:
        - name: app
          image: example/app:v1
`)
	crd := filepath.Join(t.TempDir(), "widgets.json")
	writeFile(t, crd, `{}`)
	schemaRepo := t.TempDir()
	writeFile(t, filepath.Join(schemaRepo, "openapi", "schema.json"), `{"definitions":{}}`)
	config := filepath.Join(t.TempDir(), "name-reference.yaml")
	writeFile(t, config, `nameReference:
  - kind: ConfigMap
    fieldSpecs:
      - kind: Deployment
        path: spec/template/spec/volumes/configMap/name
`)
	replacementRepo := t.TempDir()
	writeFile(t, filepath.Join(replacementRepo, "configs", "replacement.yaml"), `source:
  kind: ConfigMap
  name: source
  fieldPath: data.value
targets:
  - select:
      kind: ConfigMap
      name: target
    fieldPaths:
      - data.value
`)
	acquirer := &fakeRemoteAcquirer{paths: map[string]string{
		"https://raw.githubusercontent.com/example/schema/main/widgets.json":        crd,
		"https://github.com/example/schema.git":                                     schemaRepo,
		"https://raw.githubusercontent.com/example/schema/main/name-reference.yaml": config,
		"https://github.com/example/replacements.git":                               replacementRepo,
	}}

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(acquirer.requests) != 4 {
		t.Fatalf("remote acquire calls = %d, want 4", len(acquirer.requests))
	}
	target := findManifest(manifests, "ConfigMap", "prod-target")
	if target == nil {
		t.Fatalf("rendered manifests = %#v, want target ConfigMap", manifests)
	}
	if got, _, _ := unstructured.NestedString(target.Object, "data", "value"); got != "from-source" {
		t.Fatalf("target data.value = %q, want from-source", got)
	}
	deployment := findManifest(manifests, "Deployment", "prod-demo")
	if deployment == nil {
		t.Fatalf("rendered manifests = %#v, want Deployment", manifests)
	}
	volumes, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "volumes")
	if len(volumes) != 1 {
		t.Fatalf("Deployment volumes = %#v, want one volume", volumes)
	}
	volume, ok := volumes[0].(map[string]any)
	if !ok {
		t.Fatalf("Deployment volume = %#v, want object", volumes[0])
	}
	if got, _, _ := unstructured.NestedString(volume, "configMap", "name"); got != "prod-settings" {
		t.Fatalf("Deployment configMap name = %q, want prod-settings", got)
	}
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
			name: "remote file component",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
components:
  - https://raw.githubusercontent.com/example/repo/main/component.yaml
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

func TestKustomizeRendererRejectsUnsupportedRemoteRefsWithoutLeakingSecrets(t *testing.T) {
	for name, body := range map[string]string{
		"malformed-resource-url": `resources:
  - https://user:secret@example.test/%zz.yaml?token=secret#fragment
`,
		"component": `components:
  - https://user:secret@example.test/component.yaml?token=secret
resources:
  - local.yaml
`,
		"malformed-component-url": `components:
  - https://user:secret@example.test/%zz.yaml?token=secret#fragment
resources:
  - local.yaml
`,
		"git-scp": `components:
  - git@github.com:org/repo.git//base?ref=main&token=secret
resources:
  - local.yaml
`,
		"schemeless-github": `components:
  - github.com/org/repo//base?ref=main&token=secret
resources:
  - local.yaml
`,
		"patch": `patches:
  - path: https://user:secret@example.test/patch.yaml?token=secret
resources:
  - local.yaml
`,
		"malformed-patch-url": `patches:
  - path: https://user:secret@example.test/%zz.yaml?token=secret#fragment
resources:
  - local.yaml
`,
		"schemeless-fragment": `components:
  - github.com/org/repo//base#token=secret
resources:
  - local.yaml
`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "local.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: local\n")
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"+body)

			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want unsupported remote ref error")
			}
			message := err.Error()
			for _, leaked := range []string{"secret", "token=", "user:secret", "user:", "@example.test", "?token", "#fragment", "#token"} {
				if strings.Contains(message, leaked) {
					t.Fatalf("Render() error leaked %q: %s", leaked, message)
				}
			}
			if !strings.Contains(message, "remote Kustomize refs are unsupported") {
				t.Fatalf("Render() error = %v, want unsupported remote message", err)
			}
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
