package render

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/remote"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestKustomizeRendererRecordsRemoteRefreshCacheEvent(t *testing.T) {
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
	recorder := cacheevent.NewRecorder(true)

	_, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{
		RemoteResourceAcquirer: &fakeRemoteAcquirer{path: remoteFile, fromCache: false},
		RemoteResourceCacheDir: t.TempDir(),
		RefreshRemoteResources: true,
		CacheEventRecorder:     recorder,
	})
	if err != nil {
		t.Fatalf("Render() error = %v, diagnostics = %#v", err, diags)
	}
	if !hasRenderCacheEvent(recorder.Events(), "remote", "refresh", "https://raw.githubusercontent.com/example/repo/main/resource.yaml") {
		t.Fatalf("Cache events = %#v, want remote refresh", recorder.Events())
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
