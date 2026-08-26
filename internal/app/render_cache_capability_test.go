package app

// Guard tests: render cache keys must include CapabilityOptions so that a
// second render with different --api-versions or --kube-version does not
// return a stale hit from an earlier render.
//
// Step 2 (RED): these tests are written before the production fix.  They must
// FAIL on the pre-fix codebase and PASS after Steps 3-4 add KubeVersion and
// APIVersions to both cache key inputs.

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// writeCapabilityGatedHelmApp writes a Helm chart whose template conditionally
// includes a ServiceMonitor when the monitoring.coreos.com/v1 API is available.
// This lets us detect whether the render used the requested capabilities or a
// stale cached result.
func writeCapabilityGatedHelmApp(t *testing.T, root, name string) {
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
	writeTestFile(t, filepath.Join(root, "charts", name, "values.yaml"), `value: base
`)
	// A ConfigMap is always rendered; a ServiceMonitor is only rendered when
	// the monitoring API is present.  The two resources let us distinguish a
	// re-render from a stale cache hit.
	writeTestFile(t, filepath.Join(root, "charts", name, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: {{ .Values.value | quote }}
`)
	writeTestFile(t, filepath.Join(root, "charts", name, "templates", "monitor.yaml"), `{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: `+name+`
spec: {}
{{- end }}
`)
}

// hasServiceMonitor reports whether any manifest in result is a ServiceMonitor.
func hasServiceMonitor(result BuildResult, appName string) bool {
	for _, m := range result.ApplicationManifests {
		if m.Application.Name != appName {
			continue
		}
		if m.Manifest.Object == nil {
			continue
		}
		if m.Manifest.Object.GetKind() == "ServiceMonitor" {
			return true
		}
	}
	return false
}

// --- In-memory cache key guard test ---

// TestInMemoryCacheKeyIncludesAPIVersions asserts that two renders of the same
// application with different APIVersions produce distinct in-memory cache keys,
// and therefore a second render with fewer API versions is NOT served from the
// cache populated by the first render (which had extra API versions).
func TestInMemoryCacheKeyIncludesAPIVersions(t *testing.T) {
	repoRoot := t.TempDir()
	writeCapabilityGatedHelmApp(t, repoRoot, "demo")
	gitCommitAll(t, repoRoot, "initial")

	application := argoappv1.Application{
		Name: "demo", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://github.com/example/repo",
				Path:           "charts/demo",
				TargetRevision: "main",
			},
			Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "default"},
		},
	}

	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot},
		rootInputMode:  rootInputModeDirty,
		cacheEvents:    cacheevent.NewRecorder(false),
		acquisitions:   cacheevent.NewAcquisitionCollector(),
	}

	cache := newApplicationRenderCache()

	// First render: with monitoring API → ServiceMonitor must appear.
	withMonitoring := BuildRequest{
		APIVersions: []string{"monitoring.coreos.com/v1"},
	}
	ctx1 := renderContext{
		context:  context.Background(),
		provider: provider,
		cache:    cache,
		request:  withMonitoring,
	}
	result1, err := renderApplicationCached(ctx1, application)
	if err != nil {
		t.Fatalf("first renderApplicationCached() error = %v", err)
	}
	hasMonitor1 := false
	for _, m := range result1.Manifests {
		if m.Object != nil && m.Object.GetKind() == "ServiceMonitor" {
			hasMonitor1 = true
		}
	}
	if !hasMonitor1 {
		t.Fatal("first render (with monitoring API) must produce a ServiceMonitor; chart template may not be working")
	}

	// Second render: without monitoring API → must NOT return the cached
	// ServiceMonitor from the first render.
	withoutMonitoring := BuildRequest{
		CapabilityOptions: CapabilityOptions{},
	}
	ctx2 := renderContext{
		context:  context.Background(),
		provider: provider,
		cache:    cache,
		request:  withoutMonitoring,
	}
	result2, err := renderApplicationCached(ctx2, application)
	if err != nil {
		t.Fatalf("second renderApplicationCached() error = %v", err)
	}
	for _, m := range result2.Manifests {
		if m.Object != nil && m.Object.GetKind() == "ServiceMonitor" {
			t.Fatal("second render (no monitoring API) returned a ServiceMonitor — stale in-memory cache hit: CapabilityOptions is not part of the cache key")
		}
	}
}

// TestInMemoryCacheKeyIncludesKubeVersion asserts that different KubeVersion
// values produce distinct cache keys so a second render with a different
// kube-version is not served from a stale entry.
func TestInMemoryCacheKeyIncludesKubeVersion(t *testing.T) {
	repoRoot := t.TempDir()
	// A chart that renders the kube version into a ConfigMap.
	writeTestFile(t, filepath.Join(repoRoot, "apps", "kv.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: kv
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: charts/kv
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(repoRoot, "charts", "kv", "Chart.yaml"), `apiVersion: v2
name: kv
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(repoRoot, "charts", "kv", "values.yaml"), ``)
	writeTestFile(t, filepath.Join(repoRoot, "charts", "kv", "templates", "cm.yaml"),
		`apiVersion: v1
kind: ConfigMap
metadata:
  name: kv
data:
  kubeVersion: {{ .Capabilities.KubeVersion.Version | quote }}
`)
	gitCommitAll(t, repoRoot, "initial")

	application := argoappv1.Application{
		Name: "kv", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://github.com/example/repo",
				Path:           "charts/kv",
				TargetRevision: "main",
			},
			Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "default"},
		},
	}

	provider := localProvider{
		repoRoot:       repoRoot,
		sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}),
		rootIdentity:   SourceIdentity{Kind: sourceIdentityKindRoot},
		rootInputMode:  rootInputModeDirty,
		cacheEvents:    cacheevent.NewRecorder(false),
		acquisitions:   cacheevent.NewAcquisitionCollector(),
	}

	cache := newApplicationRenderCache()

	// First render: kubeVersion 1.28.0
	ctx1 := renderContext{
		context:  context.Background(),
		provider: provider,
		cache:    cache,
		request:  BuildRequest{CapabilityOptions: CapabilityOptions{KubeVersion: "1.28.0"}},
	}
	result1, err := renderApplicationCached(ctx1, application)
	if err != nil {
		t.Fatalf("first render error = %v", err)
	}
	kubeVersion1 := kubeVersionFromCMResult(result1)
	if !strings.Contains(kubeVersion1, "1.28") {
		t.Fatalf("first render kubeVersion = %q, want 1.28.x; chart template may not be working", kubeVersion1)
	}

	// Second render: kubeVersion 1.30.0 — must NOT return the cached 1.28 value.
	ctx2 := renderContext{
		context:  context.Background(),
		provider: provider,
		cache:    cache,
		request:  BuildRequest{CapabilityOptions: CapabilityOptions{KubeVersion: "1.30.0"}},
	}
	result2, err := renderApplicationCached(ctx2, application)
	if err != nil {
		t.Fatalf("second render error = %v", err)
	}
	kubeVersion2 := kubeVersionFromCMResult(result2)
	if strings.Contains(kubeVersion2, "1.28") {
		t.Fatalf("second render (kube 1.30.0) returned kubeVersion %q — stale in-memory cache hit: KubeVersion is not part of the cache key", kubeVersion2)
	}
}

func kubeVersionFromCMResult(result RenderResult) string {
	for _, m := range result.Manifests {
		if m.Object == nil || m.Object.GetKind() != "ConfigMap" {
			continue
		}
		data, ok := m.Object.Object["data"].(map[string]any)
		if !ok {
			continue
		}
		v, _ := data["kubeVersion"].(string)
		return v
	}
	return ""
}

// --- Persistent cache guard test ---

// TestPersistentCacheKeyIncludesAPIVersions asserts that the persistent render
// cache key includes APIVersions, so a second Build with different APIVersions
// triggers a re-render rather than returning a stale cache hit.
func TestPersistentCacheKeyIncludesAPIVersions(t *testing.T) {
	root := t.TempDir()
	writeCapabilityGatedHelmApp(t, root, "gated")
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()

	// Run 1: with monitoring API → ServiceMonitor is rendered and stored.
	req1 := persistentBuildRequest(root, cacheDir)
	req1.CapabilityOptions = CapabilityOptions{
		APIVersions: []string{"monitoring.coreos.com/v1"},
	}
	result1, err := (Orchestrator{}).Build(context.Background(), req1)
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	if !hasServiceMonitor(result1, "gated") {
		t.Fatal("first Build (with monitoring API) must produce a ServiceMonitor; chart template may not be working")
	}

	// Run 2: WITHOUT monitoring API — must re-render (cache key is different)
	// and must NOT return the ServiceMonitor from run 1.
	req2 := persistentBuildRequest(root, cacheDir)
	req2.CapabilityOptions = CapabilityOptions{}
	result2, err := (Orchestrator{}).Build(context.Background(), req2)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if hasServiceMonitor(result2, "gated") {
		t.Fatal("second Build (no monitoring API) returned a ServiceMonitor — stale persistent cache hit: APIVersions is not part of the persistent cache key")
	}
}

// TestPersistentCacheKeyIncludesKubeVersion asserts that the persistent render
// cache key includes KubeVersion, so a second Build with a different kube
// version triggers a re-render.
func TestPersistentCacheKeyIncludesKubeVersion(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "kv2.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: kv2
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: charts/kv2
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "charts", "kv2", "Chart.yaml"), `apiVersion: v2
name: kv2
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "kv2", "values.yaml"), ``)
	writeTestFile(t, filepath.Join(root, "charts", "kv2", "templates", "cm.yaml"),
		`apiVersion: v1
kind: ConfigMap
metadata:
  name: kv2
data:
  kubeVersion: {{ .Capabilities.KubeVersion.Version | quote }}
`)
	gitCommitAll(t, root, "initial")
	cacheDir := t.TempDir()

	// Run 1: kube 1.28.0
	req1 := persistentBuildRequest(root, cacheDir)
	req1.CapabilityOptions = CapabilityOptions{KubeVersion: "1.28.0"}
	result1, err := (Orchestrator{}).Build(context.Background(), req1)
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	kv1 := kubeVersionFromBuildResult(result1, "kv2")
	if !strings.Contains(kv1, "1.28") {
		t.Fatalf("first Build kubeVersion = %q, want 1.28.x", kv1)
	}

	// Run 2: kube 1.30.0 — must re-render, must NOT return 1.28 value.
	req2 := persistentBuildRequest(root, cacheDir)
	req2.CapabilityOptions = CapabilityOptions{KubeVersion: "1.30.0"}
	result2, err := (Orchestrator{}).Build(context.Background(), req2)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	kv2 := kubeVersionFromBuildResult(result2, "kv2")
	if strings.Contains(kv2, "1.28") {
		t.Fatalf("second Build (kube 1.30.0) returned kubeVersion %q — stale persistent cache hit: KubeVersion is not part of the persistent cache key", kv2)
	}
}

// TestPersistentCacheKeyDiffPathIncludesAPIVersions confirms the diff path
// also keys on CapabilityOptions: a second DiffApps with different APIVersions
// must trigger fresh renders, not return stale cache hits.
func TestPersistentCacheKeyDiffPathIncludesAPIVersions(t *testing.T) {
	root := t.TempDir()
	writeCapabilityGatedHelmApp(t, root, "diffgated")
	baseline := gitCommitAll(t, root, "initial")
	// A no-op second commit so HEAD != baseline and a real diff is attempted.
	writeTestFile(t, filepath.Join(root, "charts", "diffgated", "values.yaml"), "value: v2\n")
	gitCommitAll(t, root, "bump diffgated")
	diffCacheDir := t.TempDir()

	// Diff run 1: with monitoring API — renders and stores.
	diffReq1 := DiffRequest{
		Repo:               root,
		Ref:                "HEAD",
		RefOrig:            baseline,
		RenderCacheEnabled: true,
		RenderCacheDir:     diffCacheDir,
		EngineFingerprint:  testEngineFingerprint(),
		APIVersions:        []string{"monitoring.coreos.com/v1"},
	}
	var run1Renders atomic.Int64
	if _, err := countingOrchestrator(&run1Renders).DiffApps(context.Background(), diffReq1); err != nil {
		t.Fatalf("diff run 1 error = %v", err)
	}
	if run1Renders.Load() == 0 {
		t.Fatal("diff run 1 performed zero renders; fixture may be broken")
	}
	entriesAfterRun1 := countRenderCacheEntries(t, diffCacheDir)

	// Diff run 2: WITHOUT monitoring API, same cache dir.
	// Because the key must include APIVersions, the run-1 entries must not be
	// reused — run 2 must re-render (render count > 0) AND store new entries
	// (entry count must increase).
	diffReq2 := diffReq1
	diffReq2.CapabilityOptions = CapabilityOptions{}
	var run2Renders atomic.Int64
	if _, err := countingOrchestrator(&run2Renders).DiffApps(context.Background(), diffReq2); err != nil {
		t.Fatalf("diff run 2 error = %v", err)
	}
	if got := run2Renders.Load(); got == 0 {
		t.Fatal("diff run 2 (no monitoring API) performed zero renders — stale persistent cache hit on diff path: APIVersions not in key")
	}
	if got := countRenderCacheEntries(t, diffCacheDir); got <= entriesAfterRun1 {
		t.Fatalf("diff run 2 entry count = %d, want > %d (must store new entries for different capabilities)", got, entriesAfterRun1)
	}
}

func kubeVersionFromBuildResult(result BuildResult, appName string) string {
	for _, item := range result.ApplicationManifests {
		if item.Application.Name != appName {
			continue
		}
		if item.Manifest.Object == nil || item.Manifest.Object.GetKind() != "ConfigMap" {
			continue
		}
		data, ok := item.Manifest.Object.Object["data"].(map[string]any)
		if !ok {
			continue
		}
		v, _ := data["kubeVersion"].(string)
		return v
	}
	return ""
}
