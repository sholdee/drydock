package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const persistentCacheTestSHA = "0123456789abcdef0123456789abcdef01234567"

func gitCommitAll(t *testing.T, root, message string) string {
	t.Helper()
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{})
	if err != nil {
		repo, err = git.PlainInit(root, false)
		if err != nil {
			t.Fatalf("PlainInit() error = %v", err)
		}
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("AddWithOptions() error = %v", err)
	}
	signature := &object.Signature{
		Name:  "Test",
		Email: "test@example.invalid",
		When:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	hash, err := worktree.Commit(message, &git.CommitOptions{Author: signature, Committer: signature})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return hash.String()
}

func writePersistentCacheApp(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: charts/`+name+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "Chart.yaml"), `apiVersion: v2
name: `+name+`
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "values.yaml"), `value: persistent-cache
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: {{ .Values.value | quote }}
`)
}

func writePersistentDirectoryCacheApp(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+name+`
    targetRevision: main
    directory: {}
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: initial
`)
}

func writePersistentDirectorySource(t *testing.T, root, name, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: `+value+`
`)
}

func writePersistentCacheAppSetFileGenerator(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: generated
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        files:
          - path: clusters/*.yaml
  template:
    metadata:
      name: "{{.name}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: "manifests/{{.name}}"
        directory: {}
      destination:
        name: in-cluster
        namespace: default
`)
}

func writePersistentRenderedFleetCacheFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: root
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: bootstrap-chart
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "Chart.yaml"), `apiVersion: v2
name: bootstrap
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "child.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/child
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writePersistentDirectorySource(t, root, "child", "initial-child")
}

func writePersistentJsonnetCacheApp(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+name+`
    targetRevision: main
    directory:
      jsonnet:
        libs:
          - lib
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm.jsonnet"), `local helper = import 'helper.libsonnet';
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: '`+name+`' },
  data: { value: helper.value + '-source-a' },
}
`)
	writeTestFile(t, filepath.Join(root, "lib", "helper.libsonnet"), `{
  value: 'lib-a',
}
`)
}

func writePersistentCacheSameRepoValuesHelmApp(t *testing.T, root, name, valueFile string, ignoreMissing bool) {
	t.Helper()
	ignoreMissingBlock := ""
	if ignoreMissing {
		ignoreMissingBlock = "        ignoreMissingValueFiles: true\n"
	}
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      path: charts/`+name+`
      targetRevision: main
      helm:
`+ignoreMissingBlock+`        valueFiles:
          - `+valueFile+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "Chart.yaml"), `apiVersion: v2
name: `+name+`
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "values.yaml"), `value: default
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: {{ .Values.value | quote }}
`)
}

func persistentBuildRequest(root, cacheDir string) BuildRequest {
	return BuildRequest{
		Path:               root,
		RenderCacheEnabled: true,
		RenderCacheDir:     cacheDir,
		EngineFingerprint:  testEngineFingerprint(),
	}
}

func countingOrchestrator(counter *atomic.Int64) Orchestrator {
	orchestrator := Orchestrator{}
	orchestrator.renderObserver = func(render.ResolvedSource) { counter.Add(1) }
	return orchestrator
}

func countRenderCacheEntries(t *testing.T, cacheDir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(cacheDir, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json.gz") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", cacheDir, err)
	}
	return count
}

func readRenderCachePayloads(t *testing.T, cacheDir string) map[string][]byte {
	t.Helper()
	payloads := map[string][]byte{}
	err := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json.gz") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		data, err := io.ReadAll(gz)
		if err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		var envelope struct {
			Key    string          `json:"key"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return err
		}
		payloads[envelope.Key] = []byte(envelope.Result)
		return nil
	})
	if err != nil {
		t.Fatalf("read payloads: %v", err)
	}
	return payloads
}

func TestPersistentCacheColdThenWarmBuildSkipsRenderEngines(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	writePersistentCacheApp(t, root, "beta")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()

	var coldRenders atomic.Int64
	coldResult, err := countingOrchestrator(&coldRenders).Build(context.Background(), persistentBuildRequest(root, cacheDir))
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if coldRenders.Load() == 0 {
		t.Fatalf("cold run performed zero render-engine invocations")
	}
	if got := countRenderCacheEntries(t, cacheDir); got < 2 {
		t.Fatalf("entries after cold run = %d, want at least 2", got)
	}

	var warmRenders atomic.Int64
	warmResult, err := countingOrchestrator(&warmRenders).Build(context.Background(), persistentBuildRequest(root, cacheDir))
	if err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := warmRenders.Load(); got != 0 {
		t.Fatalf("warm run render-engine invocations = %d, want 0", got)
	}
	if len(warmResult.ApplicationManifests) != len(coldResult.ApplicationManifests) {
		t.Fatalf("warm manifests = %d, cold = %d", len(warmResult.ApplicationManifests), len(coldResult.ApplicationManifests))
	}
	for i := range coldResult.ApplicationManifests {
		coldManifest := coldResult.ApplicationManifests[i]
		warmManifest := warmResult.ApplicationManifests[i]
		if !reflect.DeepEqual(coldManifest.Manifest.Object, warmManifest.Manifest.Object) {
			t.Fatalf("manifest %d differs between cold and warm runs", i)
		}
	}
}

func TestPersistentCacheLocalInputsReuseUnchangedAppAcrossCleanCommits(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	writePersistentCacheApp(t, root, "beta")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 2 {
		t.Fatalf("entries after cold run = %d, want 2", got)
	}

	writeTestFile(t, filepath.Join(root, "charts", "alpha", "values.yaml"), `value: changed
`)
	gitCommitAll(t, root, "change alpha")

	var renderedMu sync.Mutex
	renderedPaths := map[string]int{}
	orchestrator := Orchestrator{}
	orchestrator.renderObserver = func(source render.ResolvedSource) {
		renderedMu.Lock()
		defer renderedMu.Unlock()
		renderedPaths[filepath.ToSlash(source.Path)]++
	}
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["charts/alpha"]; got != 1 {
		t.Fatalf("second build alpha renders = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["charts/beta"]; got != 0 {
		t.Fatalf("second build beta renders = %d, want 0 persistent hit; all renders = %#v", got, renderedPaths)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 3 {
		t.Fatalf("entries after second run = %d, want old alpha + beta + new alpha", got)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionHit, "argocd/beta", "") {
		t.Fatalf("CacheEvents = %#v, want beta persistent hit", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent miss", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionStore, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent store", result.CacheEvents)
	}
}

func TestPersistentCacheUnchangedLocalAppHitsAcrossHeadChange(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	writePersistentCacheApp(t, root, "beta")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()

	var coldRenders atomic.Int64
	if _, err := countingOrchestrator(&coldRenders).Build(context.Background(), persistentBuildRequest(root, cacheDir)); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := coldRenders.Load(); got != 2 {
		t.Fatalf("cold render-engine invocations = %d, want 2", got)
	}

	writeTestFile(t, filepath.Join(root, "charts", "alpha", "values.yaml"), `value: changed-alpha
`)
	gitCommitAll(t, root, "change alpha")

	var renderedMu sync.Mutex
	renderedPaths := map[string]int{}
	warmOrchestrator := Orchestrator{}
	warmOrchestrator.renderObserver = func(source render.ResolvedSource) {
		renderedMu.Lock()
		defer renderedMu.Unlock()
		renderedPaths[filepath.ToSlash(source.Path)]++
	}
	warmResult, err := warmOrchestrator.Build(context.Background(), persistentBuildRequest(root, cacheDir))
	if err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := renderedPaths["charts/alpha"]; got != 1 {
		t.Fatalf("alpha renders after alpha commit = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["charts/beta"]; got != 0 {
		t.Fatalf("beta renders after alpha commit = %d, want 0; all renders = %#v", got, renderedPaths)
	}
	if len(warmResult.ApplicationManifests) != 2 {
		t.Fatalf("warm manifests = %d, want 2", len(warmResult.ApplicationManifests))
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 3 {
		t.Fatalf("entries after app-scoped miss = %d, want 3", got)
	}
}

func TestPersistentCacheStaticApplicationYAMLChangeInvalidatesOnlyThatApp(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	writePersistentCacheApp(t, root, "beta")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	alphaAppPath := filepath.Join(root, "apps", "alpha.yaml")
	raw, err := os.ReadFile(alphaAppPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", alphaAppPath, err)
	}
	writeTestFile(t, alphaAppPath, string(raw)+"# discovery input changed without changing the Application spec\n")
	gitCommitAll(t, root, "change alpha application yaml comment")

	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["charts/alpha"]; got != 1 {
		t.Fatalf("alpha renders after app yaml change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["charts/beta"]; got != 0 {
		t.Fatalf("beta renders after alpha app yaml change = %d, want 0; all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionHit, "argocd/beta", "") {
		t.Fatalf("CacheEvents = %#v, want beta persistent hit", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent miss", result.CacheEvents)
	}
}

func TestPersistentCacheApplicationSetGeneratorFileChangeInvalidatesOnlyGeneratedApp(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheAppSetFileGenerator(t, root)
	writePersistentDirectorySource(t, root, "alpha", "initial-alpha")
	writePersistentDirectorySource(t, root, "beta", "initial-beta")
	writeTestFile(t, filepath.Join(root, "clusters", "alpha.yaml"), "name: alpha\nunused: one\n")
	writeTestFile(t, filepath.Join(root, "clusters", "beta.yaml"), "name: beta\nunused: one\n")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, "clusters", "alpha.yaml"), "name: alpha\nunused: two\n")
	gitCommitAll(t, root, "change alpha generator file")

	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["manifests/alpha"]; got != 1 {
		t.Fatalf("alpha renders after generator file change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["manifests/beta"]; got != 0 {
		t.Fatalf("beta renders after alpha generator file change = %d, want 0; all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionHit, "argocd/beta", "") {
		t.Fatalf("CacheEvents = %#v, want beta persistent hit", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent miss", result.CacheEvents)
	}
}

func TestPersistentCacheRenderedFleetParentInputChangeInvalidatesChild(t *testing.T) {
	root := t.TempDir()
	writePersistentRenderedFleetCacheFixture(t, root)
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	childTemplatePath := filepath.Join(root, "bootstrap-chart", "templates", "child.yaml")
	raw, err := os.ReadFile(childTemplatePath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", childTemplatePath, err)
	}
	writeTestFile(t, childTemplatePath, string(raw)+"# rendered fleet parent input changed\n")
	gitCommitAll(t, root, "change rendered child template comment")

	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["bootstrap-chart"]; got != 1 {
		t.Fatalf("root renders after bootstrap template change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["manifests/child"]; got != 1 {
		t.Fatalf("child renders after rendered parent input change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/child", "") {
		t.Fatalf("CacheEvents = %#v, want child persistent miss", result.CacheEvents)
	}
}

func TestPersistentCacheRenderedFleetApplicationYAMLChangeInvalidatesDiscoveryRender(t *testing.T) {
	root := t.TempDir()
	writePersistentRenderedFleetCacheFixture(t, root)
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	rootAppPath := filepath.Join(root, "apps", "root.yaml")
	raw, err := os.ReadFile(rootAppPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", rootAppPath, err)
	}
	writeTestFile(t, rootAppPath, string(raw)+"# discovery input changed without changing rendered output\n")
	gitCommitAll(t, root, "change rendered parent application yaml comment")

	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["bootstrap-chart"]; got != 1 {
		t.Fatalf("root discovery render after app yaml change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["manifests/child"]; got != 1 {
		t.Fatalf("child render after parent app yaml change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/root", "") {
		t.Fatalf("CacheEvents = %#v, want root persistent miss", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/child", "") {
		t.Fatalf("CacheEvents = %#v, want child persistent miss", result.CacheEvents)
	}
}

func TestPersistentCacheSameRepoHelmValuesChangeInvalidatesOnlyThatApp(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheSameRepoValuesHelmApp(t, root, "alpha", "$values/shared/alpha.yaml", false)
	writePersistentCacheSameRepoValuesHelmApp(t, root, "beta", "$values/shared/beta.yaml", false)
	writeTestFile(t, filepath.Join(root, "shared", "alpha.yaml"), "value: alpha-one\n")
	writeTestFile(t, filepath.Join(root, "shared", "beta.yaml"), "value: beta-one\n")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 2 {
		t.Fatalf("entries after cold run = %d, want 2", got)
	}

	writeTestFile(t, filepath.Join(root, "shared", "alpha.yaml"), "value: alpha-two\n")
	gitCommitAll(t, root, "change alpha shared values")

	var renderedMu sync.Mutex
	renderedPaths := map[string]int{}
	orchestrator := Orchestrator{}
	orchestrator.renderObserver = func(source render.ResolvedSource) {
		renderedMu.Lock()
		defer renderedMu.Unlock()
		renderedPaths[filepath.ToSlash(source.Path)]++
	}
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["charts/alpha"]; got != 1 {
		t.Fatalf("alpha renders after same-repo values change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["charts/beta"]; got != 0 {
		t.Fatalf("beta renders after alpha values change = %d, want 0; all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionHit, "argocd/beta", "") {
		t.Fatalf("CacheEvents = %#v, want beta persistent hit", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent miss", result.CacheEvents)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 3 {
		t.Fatalf("entries after second run = %d, want old alpha + beta + new alpha", got)
	}
}

func TestPersistentCacheOptionalMissingSameRepoHelmValueAddInvalidates(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheSameRepoValuesHelmApp(t, root, "alpha", "$values/optional/alpha.yaml", true)
	writePersistentCacheSameRepoValuesHelmApp(t, root, "beta", "$values/optional/beta.yaml", true)
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 2 {
		t.Fatalf("entries after cold run = %d, want 2", got)
	}

	writeTestFile(t, filepath.Join(root, "optional", "alpha.yaml"), "value: alpha-added\n")
	gitCommitAll(t, root, "add optional alpha values")

	var renderedMu sync.Mutex
	renderedPaths := map[string]int{}
	orchestrator := Orchestrator{}
	orchestrator.renderObserver = func(source render.ResolvedSource) {
		renderedMu.Lock()
		defer renderedMu.Unlock()
		renderedPaths[filepath.ToSlash(source.Path)]++
	}
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["charts/alpha"]; got != 1 {
		t.Fatalf("alpha renders after optional values add = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["charts/beta"]; got != 0 {
		t.Fatalf("beta renders after optional values add = %d, want 0; all renders = %#v", got, renderedPaths)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionHit, "argocd/beta", "") {
		t.Fatalf("CacheEvents = %#v, want beta persistent hit", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent miss", result.CacheEvents)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 3 {
		t.Fatalf("entries after optional add = %d, want old alpha + beta + new alpha", got)
	}
}

func writePersistentCacheKustomizeApp(t *testing.T, root, name string, kustomization string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+name+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "kustomization.yaml"), kustomization)
}

func writePersistentCachePlainKustomizeApp(t *testing.T, root, name string) {
	t.Helper()
	writePersistentCacheKustomizeApp(t, root, name, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: initial
`)
}

func renderedSourceCounts(orchestrator *Orchestrator) map[string]int {
	renderedMu := sync.Mutex{}
	renderedPaths := map[string]int{}
	orchestrator.renderObserver = func(source render.ResolvedSource) {
		renderedMu.Lock()
		defer renderedMu.Unlock()
		renderedPaths[filepath.ToSlash(source.Path)]++
	}
	return renderedPaths
}

func TestPersistentCacheKustomizeSharedBaseInvalidatesAllUsers(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheKustomizeApp(t, root, "alpha", `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../bases/shared
`)
	writePersistentCacheKustomizeApp(t, root, "beta", `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../bases/shared
`)
	writeTestFile(t, filepath.Join(root, "bases", "shared", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "bases", "shared", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
data:
  value: initial
`)
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, "bases", "shared", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
data:
  value: changed
`)
	gitCommitAll(t, root, "change shared base")

	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	if _, err := orchestrator.Build(context.Background(), request); err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := renderedPaths["manifests/alpha"]; got != 1 {
		t.Fatalf("alpha renders after shared base change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["manifests/beta"]; got != 1 {
		t.Fatalf("beta renders after shared base change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
}

func TestPersistentCacheKustomizeOverlayChangeInvalidatesOnlyThatApp(t *testing.T) {
	root := t.TempDir()
	writePersistentCachePlainKustomizeApp(t, root, "alpha")
	writePersistentCachePlainKustomizeApp(t, root, "beta")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, "manifests", "alpha", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: alpha
data:
  value: changed
`)
	gitCommitAll(t, root, "change alpha overlay")

	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	if _, err := orchestrator.Build(context.Background(), request); err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := renderedPaths["manifests/alpha"]; got != 1 {
		t.Fatalf("alpha renders after alpha overlay change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["manifests/beta"]; got != 0 {
		t.Fatalf("beta renders after alpha overlay change = %d, want 0; all renders = %#v", got, renderedPaths)
	}
}

func TestPersistentCacheKustomizeHelmValuesAndLocalChartInvalidate(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheKustomizeApp(t, root, "alpha", `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: demo
    releaseName: demo
    valuesFile: values.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", "alpha", "values.yaml"), "value: initial\n")
	writeTestFile(t, filepath.Join(root, "manifests", "alpha", "charts", "demo", "Chart.yaml"), `apiVersion: v2
name: demo
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "manifests", "alpha", "charts", "demo", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
`)
	writePersistentCachePlainKustomizeApp(t, root, "beta")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}

	writeTestFile(t, filepath.Join(root, "manifests", "alpha", "values.yaml"), "value: changed-values\n")
	gitCommitAll(t, root, "change kustomize helm values")
	valuesOrchestrator := Orchestrator{}
	valuesRendered := renderedSourceCounts(&valuesOrchestrator)
	if _, err := valuesOrchestrator.Build(context.Background(), request); err != nil {
		t.Fatalf("values warm Build() error = %v", err)
	}
	if got := valuesRendered["manifests/alpha"]; got != 1 {
		t.Fatalf("alpha renders after values change = %d, want 1; all renders = %#v", got, valuesRendered)
	}
	if got := valuesRendered["manifests/beta"]; got != 0 {
		t.Fatalf("beta renders after values change = %d, want 0; all renders = %#v", got, valuesRendered)
	}

	writeTestFile(t, filepath.Join(root, "manifests", "alpha", "charts", "demo", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
  template: changed
`)
	gitCommitAll(t, root, "change kustomize local chart template")
	templateOrchestrator := Orchestrator{}
	templateRendered := renderedSourceCounts(&templateOrchestrator)
	if _, err := templateOrchestrator.Build(context.Background(), request); err != nil {
		t.Fatalf("template warm Build() error = %v", err)
	}
	if got := templateRendered["manifests/alpha"]; got != 1 {
		t.Fatalf("alpha renders after template change = %d, want 1; all renders = %#v", got, templateRendered)
	}
	if got := templateRendered["manifests/beta"]; got != 0 {
		t.Fatalf("beta renders after template change = %d, want 0; all renders = %#v", got, templateRendered)
	}
}

func TestPersistentCacheEntriesByteIdenticalAcrossCheckoutLocations(t *testing.T) {
	cacheDirA := t.TempDir()
	cacheDirB := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	for _, root := range []string{rootA, rootB} {
		writePersistentCacheApp(t, root, "alpha")
		gitCommitAll(t, root, "initial")
	}

	if _, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(rootA, cacheDirA)); err != nil {
		t.Fatalf("Build(rootA) error = %v", err)
	}
	if _, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(rootB, cacheDirB)); err != nil {
		t.Fatalf("Build(rootB) error = %v", err)
	}

	payloadsA := readRenderCachePayloads(t, cacheDirA)
	payloadsB := readRenderCachePayloads(t, cacheDirB)
	if len(payloadsA) != 1 || len(payloadsB) != 1 {
		t.Fatalf("payload counts = %d/%d, want 1/1", len(payloadsA), len(payloadsB))
	}
	for key, payloadA := range payloadsA {
		payloadB, ok := payloadsB[key]
		if !ok {
			t.Fatalf("key %s missing from second checkout's cache", key[:12])
		}
		if !bytes.Equal(payloadA, payloadB) {
			t.Fatalf("payloads differ between checkout locations")
		}
	}
}

func hasRenderCacheEvent(events []cacheevent.Event, action cacheevent.Action, target, reason string) bool {
	for _, event := range events {
		if event.Source != cacheevent.SourceRender || event.Action != action || event.Target != target {
			continue
		}
		if reason != "" && event.Reason != reason {
			continue
		}
		return true
	}
	return false
}

func persistentConfigMapDataValue(t *testing.T, result BuildResult, appName, cmName, key string) string {
	t.Helper()
	for _, item := range result.ApplicationManifests {
		if item.Application.Name != appName || item.Manifest.Object == nil || item.Manifest.Object.GetName() != cmName {
			continue
		}
		data, ok := item.Manifest.Object.Object["data"].(map[string]any)
		if !ok {
			t.Fatalf("manifest %s/%s data = %#v, want object", appName, cmName, item.Manifest.Object.Object["data"])
		}
		value, ok := data[key].(string)
		if !ok {
			t.Fatalf("manifest %s/%s data[%q] = %#v, want string", appName, cmName, key, data[key])
		}
		return value
	}
	t.Fatalf("ApplicationManifests = %#v, missing %s/%s", result.ApplicationManifests, appName, cmName)
	return ""
}

func TestPersistentCacheDirectoryInputsReuseUnchangedSiblingAcrossCleanCommits(t *testing.T) {
	root := t.TempDir()
	writePersistentDirectoryCacheApp(t, root, "alpha")
	writePersistentDirectoryCacheApp(t, root, "beta")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 2 {
		t.Fatalf("entries after cold run = %d, want 2", got)
	}

	writeTestFile(t, filepath.Join(root, "manifests", "alpha", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: alpha
data:
  value: changed
`)
	gitCommitAll(t, root, "change alpha directory")

	orchestrator := Orchestrator{}
	renderedPaths := renderedSourceCounts(&orchestrator)
	result, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := renderedPaths["manifests/alpha"]; got != 1 {
		t.Fatalf("alpha renders after source change = %d, want 1; all renders = %#v", got, renderedPaths)
	}
	if got := renderedPaths["manifests/beta"]; got != 0 {
		t.Fatalf("beta renders after sibling change = %d, want 0 persistent hit; all renders = %#v", got, renderedPaths)
	}
	if got := persistentConfigMapDataValue(t, result, "alpha", "alpha", "value"); got != "changed" {
		t.Fatalf("alpha value = %q, want changed", got)
	}
	if got := persistentConfigMapDataValue(t, result, "beta", "beta", "value"); got != "initial" {
		t.Fatalf("beta value = %q, want initial", got)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionHit, "argocd/beta", "") {
		t.Fatalf("CacheEvents = %#v, want beta persistent hit", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionMiss, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent miss", result.CacheEvents)
	}
	if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionStore, "argocd/alpha", "") {
		t.Fatalf("CacheEvents = %#v, want alpha persistent store", result.CacheEvents)
	}
}

func TestPersistentCacheJsonnetInputsRotateOnSourceAndDeclaredLibChanges(t *testing.T) {
	root := t.TempDir()
	writePersistentJsonnetCacheApp(t, root, "jsonnet")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	coldResult, err := (Orchestrator{}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := persistentConfigMapDataValue(t, coldResult, "jsonnet", "jsonnet", "value"); got != "lib-a-source-a" {
		t.Fatalf("cold jsonnet value = %q, want lib-a-source-a", got)
	}

	writeTestFile(t, filepath.Join(root, "manifests", "jsonnet", "cm.jsonnet"), `local helper = import 'helper.libsonnet';
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'jsonnet' },
  data: { value: helper.value + '-source-b' },
}
`)
	gitCommitAll(t, root, "change jsonnet source")
	sourceOrchestrator := Orchestrator{}
	sourceRenders := renderedSourceCounts(&sourceOrchestrator)
	sourceResult, err := sourceOrchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("source-change Build() error = %v", err)
	}
	if got := sourceRenders["manifests/jsonnet"]; got != 1 {
		t.Fatalf("jsonnet renders after source change = %d, want 1; all renders = %#v", got, sourceRenders)
	}
	if got := persistentConfigMapDataValue(t, sourceResult, "jsonnet", "jsonnet", "value"); got != "lib-a-source-b" {
		t.Fatalf("source-change jsonnet value = %q, want lib-a-source-b", got)
	}
	if !hasRenderCacheEvent(sourceResult.CacheEvents, cacheevent.ActionMiss, "argocd/jsonnet", "") {
		t.Fatalf("CacheEvents = %#v, want jsonnet miss after source change", sourceResult.CacheEvents)
	}

	writeTestFile(t, filepath.Join(root, "lib", "helper.libsonnet"), `{
  value: 'lib-b',
}
`)
	gitCommitAll(t, root, "change jsonnet lib")
	libOrchestrator := Orchestrator{}
	libRenders := renderedSourceCounts(&libOrchestrator)
	libResult, err := libOrchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("lib-change Build() error = %v", err)
	}
	if got := libRenders["manifests/jsonnet"]; got != 1 {
		t.Fatalf("jsonnet renders after declared lib change = %d, want 1; all renders = %#v", got, libRenders)
	}
	if got := persistentConfigMapDataValue(t, libResult, "jsonnet", "jsonnet", "value"); got != "lib-b-source-b" {
		t.Fatalf("lib-change jsonnet value = %q, want lib-b-source-b", got)
	}
	if !hasRenderCacheEvent(libResult.CacheEvents, cacheevent.ActionMiss, "argocd/jsonnet", "") {
		t.Fatalf("CacheEvents = %#v, want jsonnet miss after declared lib change", libResult.CacheEvents)
	}
}

func TestPersistentCacheJsonnetUnprovableLibSkipsPersistence(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "jsonnet-skip.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: jsonnet-skip
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/jsonnet-skip
    targetRevision: main
    directory:
      jsonnet:
        libs:
          - ../outside
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "jsonnet-skip", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: jsonnet-skip
data:
  value: rendered
`)
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true

	for run := range 2 {
		var renders atomic.Int64
		result, err := countingOrchestrator(&renders).Build(context.Background(), request)
		if err != nil {
			t.Fatalf("Build() run %d error = %v", run, err)
		}
		if got := renders.Load(); got != 1 {
			t.Fatalf("run %d renders = %d, want 1 because persistence is skipped", run, got)
		}
		if got := persistentConfigMapDataValue(t, result, "jsonnet-skip", "jsonnet-skip", "value"); got != "rendered" {
			t.Fatalf("run %d value = %q, want rendered", run, got)
		}
		if !hasRenderCacheEvent(result.CacheEvents, cacheevent.ActionSkipped, "argocd/jsonnet-skip", renderCacheReasonInputGraph) {
			t.Fatalf("run %d CacheEvents = %#v, want input graph skip", run, result.CacheEvents)
		}
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 0 {
		t.Fatalf("entries for unprovable jsonnet graph = %d, want 0", got)
	}
}

func TestPersistentCacheWarmDiffRefBothSidesHit(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
  labels:
    app.kubernetes.io/part-of: argocd
data:
  kustomize.buildOptions: --enable-helm
`)
	baseline := gitCommitAll(t, root, "initial")
	writeTestFile(t, filepath.Join(root, "charts", "alpha", "values.yaml"), `value: changed
`)
	gitCommitAll(t, root, "change alpha")
	cacheDir := t.TempDir()
	diffRequest := DiffRequest{
		RightPath:          root,
		Repo:               root,
		Ref:                "HEAD",
		RefOrig:            baseline,
		RenderCacheEnabled: true,
		RenderCacheDir:     cacheDir,
		EngineFingerprint:  testEngineFingerprint(),
	}

	var coldRenders atomic.Int64
	if _, err := countingOrchestrator(&coldRenders).DiffApps(context.Background(), diffRequest); err != nil {
		t.Fatalf("cold DiffApps() error = %v", err)
	}
	if coldRenders.Load() == 0 {
		t.Fatalf("cold diff performed zero renders")
	}
	entriesAfterCold := countRenderCacheEntries(t, cacheDir)
	if entriesAfterCold == 0 {
		t.Fatalf("cold diff stored no render cache entries")
	}

	var warmRenders atomic.Int64
	warmResult, err := countingOrchestrator(&warmRenders).DiffApps(context.Background(), diffRequest)
	if err != nil {
		t.Fatalf("warm DiffApps() error = %v", err)
	}
	if got := warmRenders.Load(); got != 0 {
		t.Fatalf("warm diff render-engine invocations = %d, want 0", got)
	}
	if len(warmResult.Results) == 0 {
		t.Fatalf("warm diff returned no results; expected the cm change diff")
	}
	if got := countRenderCacheEntries(t, cacheDir); got != entriesAfterCold {
		t.Fatalf("warm diff changed entry count: %d -> %d", entriesAfterCold, got)
	}
}

func TestPersistentCacheDirtyRepositoryDiffRefsUseSnapshotIdentities(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	baseline := gitCommitAll(t, root, "initial")
	writeTestFile(t, filepath.Join(root, "charts", "alpha", "values.yaml"), `value: changed
`)
	gitCommitAll(t, root, "change alpha")
	cacheDir := t.TempDir()
	diffRequest := DiffRequest{
		Repo:               root,
		Ref:                "HEAD",
		RefOrig:            baseline,
		RenderCacheEnabled: true,
		RenderCacheDir:     cacheDir,
		EngineFingerprint:  testEngineFingerprint(),
	}

	var coldRenders atomic.Int64
	coldResult, err := countingOrchestrator(&coldRenders).DiffApps(context.Background(), diffRequest)
	if err != nil {
		t.Fatalf("cold DiffApps() error = %v", err)
	}
	if coldRenders.Load() == 0 {
		t.Fatalf("cold diff performed zero renders")
	}
	if len(coldResult.Results) == 0 {
		t.Fatalf("cold diff returned no results; expected the cm change diff")
	}
	entriesAfterCold := countRenderCacheEntries(t, cacheDir)
	if entriesAfterCold == 0 {
		t.Fatalf("cold diff stored no render cache entries")
	}

	writeTestFile(t, filepath.Join(root, "scratch.txt"), "dirty real worktree\n")
	var dirtyRepoRenders atomic.Int64
	dirtyRepoResult, err := countingOrchestrator(&dirtyRepoRenders).DiffApps(context.Background(), diffRequest)
	if err != nil {
		t.Fatalf("dirty repo DiffApps() error = %v", err)
	}
	if got := dirtyRepoRenders.Load(); got != 0 {
		t.Fatalf("dirty repo ref diff render-engine invocations = %d, want 0 snapshot cache hits", got)
	}
	if len(dirtyRepoResult.Results) == 0 {
		t.Fatalf("dirty repo ref diff returned no results; expected the cached cm change diff")
	}
	if got := countRenderCacheEntries(t, cacheDir); got != entriesAfterCold {
		t.Fatalf("dirty repo ref diff changed entry count: %d -> %d", entriesAfterCold, got)
	}
}

func TestPersistentCacheDirtyWorktreeDiffPathSidesHit(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	for _, side := range []struct {
		root  string
		value string
	}{
		{root: left, value: "left-dirty"},
		{root: right, value: "right-dirty"},
	} {
		writePersistentCacheApp(t, side.root, "alpha")
		gitCommitAll(t, side.root, "initial")
		writeTestFile(t, filepath.Join(side.root, "charts", "alpha", "values.yaml"), "value: "+side.value+"\n")
	}
	cacheDir := t.TempDir()
	diffRequest := DiffRequest{
		LeftPath:           left,
		RightPath:          right,
		RenderCacheEnabled: true,
		RenderCacheDir:     cacheDir,
		EngineFingerprint:  testEngineFingerprint(),
	}

	var coldRenders atomic.Int64
	coldResult, err := countingOrchestrator(&coldRenders).DiffApps(context.Background(), diffRequest)
	if err != nil {
		t.Fatalf("cold DiffApps() error = %v", err)
	}
	if coldRenders.Load() == 0 {
		t.Fatalf("cold dirty worktree diff performed zero renders")
	}
	if len(coldResult.Results) == 0 {
		t.Fatalf("cold dirty worktree diff returned no results; expected the cm change diff")
	}
	entriesAfterCold := countRenderCacheEntries(t, cacheDir)
	if entriesAfterCold == 0 {
		t.Fatalf("cold dirty worktree diff stored no render cache entries")
	}

	var warmRenders atomic.Int64
	warmResult, err := countingOrchestrator(&warmRenders).DiffApps(context.Background(), diffRequest)
	if err != nil {
		t.Fatalf("warm DiffApps() error = %v", err)
	}
	if got := warmRenders.Load(); got != 0 {
		t.Fatalf("warm dirty worktree diff render-engine invocations = %d, want 0", got)
	}
	if len(warmResult.Results) == 0 {
		t.Fatalf("warm dirty worktree diff returned no results; expected the cached cm change diff")
	}
	if got := countRenderCacheEntries(t, cacheDir); got != entriesAfterCold {
		t.Fatalf("warm dirty worktree diff changed entry count: %d -> %d", entriesAfterCold, got)
	}
}

func TestPersistentCacheDirtyWorktreeStoresInputScopedEntries(t *testing.T) {
	// Each dirty file lives inside the app's digested input subtree
	// (charts/alpha) so the per-path-set intersection forces the
	// worktree-inputs flow. Dirt outside the app's inputs no longer re-keys
	// it — that committed-shortcut behavior is pinned by
	// TestDirtyModeUntouchedSourceKeepsCommittedIdentity and
	// TestDirtyWorktreeServesCommittedHitsForUntouchedApps.
	cases := []struct {
		name  string
		dirty func(t *testing.T, root string)
	}{
		{name: "modified tracked file", dirty: func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, "charts", "alpha", "values.yaml"), "value: edited-locally\n")
		}},
		{name: "untracked file", dirty: func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, "charts", "alpha", "scratch.txt"), "untracked\n")
		}},
		{name: "ignored file", dirty: func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, "charts", "alpha", "ignored", "values.yaml"), "a: 1\n")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writePersistentCacheApp(t, root, "alpha")
			writeTestFile(t, filepath.Join(root, "README.md"), "readme\n")
			writeTestFile(t, filepath.Join(root, ".gitignore"), "ignored/\n")
			gitCommitAll(t, root, "initial")
			cacheDir := t.TempDir()

			if _, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, cacheDir)); err != nil {
				t.Fatalf("clean Build() error = %v", err)
			}
			entriesAfterClean := countRenderCacheEntries(t, cacheDir)
			if entriesAfterClean == 0 {
				t.Fatalf("clean run stored nothing")
			}

			testCase.dirty(t, root)
			var dirtyRenders atomic.Int64
			if _, err := countingOrchestrator(&dirtyRenders).Build(context.Background(), persistentBuildRequest(root, cacheDir)); err != nil {
				t.Fatalf("dirty Build() error = %v", err)
			}
			if dirtyRenders.Load() == 0 {
				t.Fatalf("dirty worktree run rendered nothing; want initial worktree-inputs cache population")
			}
			entriesAfterDirty := countRenderCacheEntries(t, cacheDir)
			if entriesAfterDirty <= entriesAfterClean {
				t.Fatalf("dirty run entry count = %d, want more than clean count %d", entriesAfterDirty, entriesAfterClean)
			}

			var warmDirtyRenders atomic.Int64
			if _, err := countingOrchestrator(&warmDirtyRenders).Build(context.Background(), persistentBuildRequest(root, cacheDir)); err != nil {
				t.Fatalf("warm dirty Build() error = %v", err)
			}
			if got := warmDirtyRenders.Load(); got != 0 {
				t.Fatalf("warm dirty renders = %d, want persistent cache hit", got)
			}
			if got := countRenderCacheEntries(t, cacheDir); got != entriesAfterDirty {
				t.Fatalf("warm dirty run changed entry count: %d -> %d", entriesAfterDirty, got)
			}
		})
	}
}

func TestPersistentCacheKeyMutationsMiss(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, root string, request *BuildRequest)
	}{
		{name: "app spec change", mutate: func(t *testing.T, root string, _ *BuildRequest) {
			writeTestFile(t, filepath.Join(root, "apps", "alpha.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: alpha
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: charts/alpha
    targetRevision: main
  destination:
    name: in-cluster
    namespace: other
`)
			gitCommitAll(t, root, "spec change")
		}},
		{name: "revision change", mutate: func(t *testing.T, root string, _ *BuildRequest) {
			writeTestFile(t, filepath.Join(root, "charts", "alpha", "values.yaml"), `value: rotated
`)
			gitCommitAll(t, root, "content change")
		}},
		{name: "settings change", mutate: func(t *testing.T, root string, _ *BuildRequest) {
			writeTestFile(t, filepath.Join(root, "argocd", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
  labels:
    app.kubernetes.io/part-of: argocd
data:
  application.instanceLabelKey: app.kubernetes.io/cache-test
`)
			gitCommitAll(t, root, "settings change")
		}},
		{name: "engine fingerprint change", mutate: func(_ *testing.T, _ string, request *BuildRequest) {
			request.EngineFingerprint.Commit = "fedcba9876543210fedcba9876543210fedcba98"
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writePersistentCacheApp(t, root, "alpha")
			gitCommitAll(t, root, "initial")
			cacheDir := t.TempDir()

			if _, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, cacheDir)); err != nil {
				t.Fatalf("cold Build() error = %v", err)
			}

			request := persistentBuildRequest(root, cacheDir)
			testCase.mutate(t, root, &request)
			var renders atomic.Int64
			if _, err := countingOrchestrator(&renders).Build(context.Background(), request); err != nil {
				t.Fatalf("mutated Build() error = %v", err)
			}
			if renders.Load() == 0 {
				t.Fatalf("%s did not rotate the key (warm hit observed)", testCase.name)
			}
		})
	}
}

func TestPersistentCacheFailedRenderLeavesNoEntry(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "broken.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: broken
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/broken
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "broken", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - missing.yaml
`)
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()

	if _, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, cacheDir)); err == nil {
		t.Fatalf("Build() error = nil, want render failure")
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 0 {
		t.Fatalf("entries after failed render = %d, want 0", got)
	}
}

func TestPersistentCachePluginSourceNeverPersists(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "plugged.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plugged
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/plugged
    targetRevision: main
    plugin:
      name: example
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugged", "placeholder.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: placeholder\n")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()

	request := persistentBuildRequest(root, cacheDir)
	request.PluginRenderer = internalPluginRendererFunc(func(_ context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		return []render.Manifest{{Object: &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": "from-plugin"},
		}}}}, nil, nil
	})

	for run := range 2 {
		if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
			t.Fatalf("Build() run %d error = %v", run, err)
		}
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 0 {
		t.Fatalf("entries for plugin-source app = %d, want 0", got)
	}
}

type fixedRemoteAcquirer struct {
	path     string
	revision string
}

func (acquirer fixedRemoteAcquirer) Acquire(_ context.Context, _ remote.Request, _ remote.Options) (remote.Result, error) {
	return remote.Result{Path: acquirer.path, Revision: acquirer.revision, FromCache: false}, nil
}

func writeRemoteBaseApp(t *testing.T, root, name, ref string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+name+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", name, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/base//demo?ref=`+ref+`
`)
}

func TestPersistentCachePinStabilityGate(t *testing.T) {
	for _, recordEvents := range []bool{false, true} {
		name := "cache-events off"
		if recordEvents {
			name = "cache-events on"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRemoteBaseApp(t, root, "floating", "main")
			writeRemoteBaseApp(t, root, "pinned", persistentCacheTestSHA)
			writePersistentCacheApp(t, root, "plain")
			gitCommitAll(t, root, "initial")

			remoteRoot := t.TempDir()
			writeTestFile(t, filepath.Join(remoteRoot, "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
			writeTestFile(t, filepath.Join(remoteRoot, "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote-base
`)
			cacheDir := t.TempDir()
			request := persistentBuildRequest(root, cacheDir)
			request.RecordCacheEvents = recordEvents
			request.RemoteResourceCacheDir = t.TempDir()
			orchestrator := Orchestrator{RemoteResourceAcquirer: fixedRemoteAcquirer{path: remoteRoot, revision: persistentCacheTestSHA}}

			coldResult, err := orchestrator.Build(context.Background(), request)
			if err != nil {
				t.Fatalf("cold Build() error = %v", err)
			}
			if got := countRenderCacheEntries(t, cacheDir); got != 2 {
				t.Fatalf("entries after cold run = %d, want 2", got)
			}
			if recordEvents && !hasRenderCacheEvent(coldResult.CacheEvents, cacheevent.ActionSkipped, "argocd/floating", renderCacheReasonInputGraph) {
				t.Fatalf("CacheEvents = %#v, want floating Kustomize input graph skip", coldResult.CacheEvents)
			}

			var renderedMu sync.Mutex
			renderedPaths := map[string]int{}
			warmOrchestrator := Orchestrator{RemoteResourceAcquirer: fixedRemoteAcquirer{path: remoteRoot, revision: persistentCacheTestSHA}}
			warmOrchestrator.renderObserver = func(source render.ResolvedSource) {
				renderedMu.Lock()
				defer renderedMu.Unlock()
				renderedPaths[filepath.ToSlash(source.Path)]++
			}
			if _, err := warmOrchestrator.Build(context.Background(), request); err != nil {
				t.Fatalf("warm Build() error = %v", err)
			}
			if got := renderedPaths["manifests/floating"]; got != 1 {
				t.Fatalf("warm renders for floating app = %d, want 1; all renders = %#v", got, renderedPaths)
			}
			if got := renderedPaths["manifests/pinned"]; got != 0 {
				t.Fatalf("warm renders for pinned app = %d, want 0; all renders = %#v", got, renderedPaths)
			}
			if got := renderedPaths["charts/plain"]; got != 0 {
				t.Fatalf("warm renders for plain app = %d, want 0; all renders = %#v", got, renderedPaths)
			}
		})
	}
}

func TestPersistentCacheRepoMappedSourceNeverPersists(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "mapped.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: mapped
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://git.example.test/mapped/repo.git
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	gitCommitAll(t, root, "initial")
	mappedDir := t.TempDir()
	writeTestFile(t, filepath.Join(mappedDir, "manifests", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeTestFile(t, filepath.Join(mappedDir, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: mapped
`)
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RepoMaps = []sourcepkg.RepoMap{{URL: "https://git.example.test/mapped/repo.git", Path: mappedDir}}

	for run := range 2 {
		var renders atomic.Int64
		if _, err := countingOrchestrator(&renders).Build(context.Background(), request); err != nil {
			t.Fatalf("Build() run %d error = %v", run, err)
		}
		if renders.Load() == 0 {
			t.Fatalf("run %d rendered nothing; repo-mapped app must render every run", run)
		}
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 0 {
		t.Fatalf("entries for repo-mapped app = %d, want 0", got)
	}
}

func TestPersistentCacheDisabledTouchesNothing(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	gitCommitAll(t, root, "initial")
	cacheDir := filepath.Join(t.TempDir(), "render-cache")
	request := persistentBuildRequest(root, cacheDir)
	request.RenderCacheEnabled = false

	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("disabled run touched the cache dir: stat err = %v", err)
	}
}

func TestPersistentCacheRefreshRendersForcesMissAndOverwrites(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()

	if _, err := (Orchestrator{}).Build(context.Background(), persistentBuildRequest(root, cacheDir)); err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	entriesBefore := countRenderCacheEntries(t, cacheDir)

	request := persistentBuildRequest(root, cacheDir)
	request.RefreshRenders = true
	var renders atomic.Int64
	if _, err := countingOrchestrator(&renders).Build(context.Background(), request); err != nil {
		t.Fatalf("refresh Build() error = %v", err)
	}
	if renders.Load() == 0 {
		t.Fatalf("--refresh-renders run hit the cache; want forced miss")
	}
	if got := countRenderCacheEntries(t, cacheDir); got != entriesBefore {
		t.Fatalf("entries after refresh = %d, want %d", got, entriesBefore)
	}
}

func TestPersistentCachePermissionFailureFailsFastAtValidation(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	gitCommitAll(t, root, "initial")
	occupied := filepath.Join(t.TempDir(), "occupied")
	writeTestFile(t, occupied, "not a directory")

	request := persistentBuildRequest(root, occupied)
	request.RecordCacheEvents = true
	_, err := (Orchestrator{}).Build(context.Background(), request)
	if err == nil {
		t.Fatalf("Build() error = nil, want validation failure for unopenable cache dir")
	}
}

func writeRemoteValuesHelmApp(t *testing.T, root, name, valuesURL string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: charts/`+name+`
    targetRevision: main
    helm:
      valueFiles:
        - `+valuesURL+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "Chart.yaml"), `apiVersion: v2
name: `+name+`
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "values.yaml"), "value: default\n")
	writeTestFile(t, filepath.Join(root, "charts", name, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: {{ .Values.value | quote }}
`)
}

func TestPersistentCacheRemoteHTTPValueFileNeverPersists(t *testing.T) {
	root := t.TempDir()
	writeRemoteValuesHelmApp(t, root, "urlvalues", "https://values.example.invalid/prod/values.yaml")
	writePersistentCacheApp(t, root, "plain")
	gitCommitAll(t, root, "initial")

	remoteValues := filepath.Join(t.TempDir(), "remote-values.yaml")
	writeTestFile(t, remoteValues, "value: from-remote-url\n")
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.RecordCacheEvents = true
	request.RemoteResourceCacheDir = t.TempDir()
	acquirer := fixedRemoteAcquirer{path: remoteValues, revision: persistentCacheTestSHA}

	coldResult, err := (Orchestrator{RemoteResourceAcquirer: acquirer}).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("cold Build() error = %v", err)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 1 {
		t.Fatalf("entries after cold run = %d, want 1", got)
	}
	skipped := false
	for _, event := range coldResult.CacheEvents {
		if event.Source == cacheevent.SourceRender && event.Action == cacheevent.ActionSkipped &&
			event.Target == "argocd/urlvalues" && event.Reason == renderCacheReasonInputGraph {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("no render/skipped event with reason %q for argocd/urlvalues; events = %#v",
			renderCacheReasonInputGraph, coldResult.CacheEvents)
	}

	var warmRenders atomic.Int64
	warmOrchestrator := Orchestrator{RemoteResourceAcquirer: acquirer}
	warmOrchestrator.renderObserver = func(render.ResolvedSource) { warmRenders.Add(1) }
	if _, err := warmOrchestrator.Build(context.Background(), request); err != nil {
		t.Fatalf("warm Build() error = %v", err)
	}
	if got := warmRenders.Load(); got == 0 {
		t.Fatalf("warm renders = %d, want the remote-URL value file app to re-render", got)
	}
	if got := countRenderCacheEntries(t, cacheDir); got != 1 {
		t.Fatalf("entries after warm run = %d, want 1", got)
	}
}

func TestPersistentCacheDirtyWorktreeRefSideStillHits(t *testing.T) {
	root := t.TempDir()
	writePersistentCacheApp(t, root, "alpha")
	baseline := gitCommitAll(t, root, "initial")
	writeTestFile(t, filepath.Join(root, "charts", "alpha", "values.yaml"), `value: changed
`)
	gitCommitAll(t, root, "change alpha")
	cacheDir := t.TempDir()
	diffRequest := DiffRequest{
		RightPath:          root,
		Repo:               root,
		RefOrig:            baseline,
		RenderCacheEnabled: true,
		RenderCacheDir:     cacheDir,
		EngineFingerprint:  testEngineFingerprint(),
	}

	var coldRenders atomic.Int64
	if _, err := countingOrchestrator(&coldRenders).DiffApps(context.Background(), diffRequest); err != nil {
		t.Fatalf("cold DiffApps() error = %v", err)
	}
	if coldRenders.Load() == 0 {
		t.Fatalf("cold diff performed zero renders")
	}
	entriesAfterCold := countRenderCacheEntries(t, cacheDir)
	if entriesAfterCold == 0 {
		t.Fatalf("cold diff stored nothing")
	}

	// The untracked file sits inside the app's digested input subtree so the
	// worktree side takes the worktree-inputs flow; dirt outside the app's
	// inputs would now hit the committed-key entry via the per-path-set
	// shortcut instead.
	writeTestFile(t, filepath.Join(root, "charts", "alpha", "scratch.txt"), "untracked\n")
	dirtyOrchestrator, worktreeRenders, refRenders := dirtyDiffCountingOrchestrator(t, root)
	dirtyResult, err := dirtyOrchestrator.DiffApps(context.Background(), diffRequest)
	if err != nil {
		t.Fatalf("dirty DiffApps() error = %v", err)
	}
	assertDirtyDiffInitialRender(t, dirtyResult, worktreeRenders, refRenders)
	entriesAfterDirty := countRenderCacheEntries(t, cacheDir)
	if entriesAfterDirty <= entriesAfterCold {
		t.Fatalf("dirty diff entry count = %d, want more than cold count %d", entriesAfterDirty, entriesAfterCold)
	}

	worktreeRenders.Store(0)
	refRenders.Store(0)
	warmDirtyResult, err := dirtyOrchestrator.DiffApps(context.Background(), diffRequest)
	if err != nil {
		t.Fatalf("warm dirty DiffApps() error = %v", err)
	}
	assertDirtyDiffWarmHit(t, warmDirtyResult, worktreeRenders, refRenders)
	if got := countRenderCacheEntries(t, cacheDir); got != entriesAfterDirty {
		t.Fatalf("warm dirty diff changed entry count: %d -> %d", entriesAfterDirty, got)
	}
}

func dirtyDiffCountingOrchestrator(t *testing.T, root string) (Orchestrator, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s) error = %v", root, err)
	}
	worktreeRenders := &atomic.Int64{}
	refRenders := &atomic.Int64{}
	orchestrator := Orchestrator{}
	orchestrator.renderObserver = func(source render.ResolvedSource) {
		rendered := source.RepoRoot
		if resolved, err := filepath.EvalSymlinks(rendered); err == nil {
			rendered = resolved
		}
		if rendered == realRoot || strings.HasPrefix(rendered, realRoot+string(filepath.Separator)) {
			worktreeRenders.Add(1)
			return
		}
		refRenders.Add(1)
	}
	return orchestrator, worktreeRenders, refRenders
}

func assertDirtyDiffInitialRender(t *testing.T, result DiffResult, worktreeRenders, refRenders *atomic.Int64) {
	t.Helper()
	if got := refRenders.Load(); got != 0 {
		t.Fatalf("ref-side renders = %d, want 0", got)
	}
	if worktreeRenders.Load() == 0 {
		t.Fatalf("worktree side rendered nothing; want initial worktree-inputs cache population")
	}
	if len(result.Results) == 0 {
		t.Fatalf("dirty diff returned no results; expected the cm change diff")
	}
}

func assertDirtyDiffWarmHit(t *testing.T, result DiffResult, worktreeRenders, refRenders *atomic.Int64) {
	t.Helper()
	if got := refRenders.Load(); got != 0 {
		t.Fatalf("warm dirty ref-side renders = %d, want 0", got)
	}
	if got := worktreeRenders.Load(); got != 0 {
		t.Fatalf("warm dirty worktree renders = %d, want 0", got)
	}
	if len(result.Results) == 0 {
		t.Fatalf("warm dirty diff returned no results; expected cached cm change diff")
	}
}
