package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fakeOCIArtifactAcquirer is the unit-test seam fake: it resolves every
// revision to a fixed digest and extracts fixed files, mirroring the
// OnAcquired contract of the production acquirer.
type fakeOCIArtifactAcquirer struct {
	mu       sync.Mutex
	digest   string
	files    map[string]string
	resolves int
	extracts int
}

func (a *fakeOCIArtifactAcquirer) Resolve(_ context.Context, _, _ string, _ ociartifact.Options) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolves++
	return a.digest, nil
}

func (a *fakeOCIArtifactAcquirer) Extract(_ context.Context, _, _ string, opts ociartifact.Options) (string, func(), error) {
	a.mu.Lock()
	a.extracts++
	a.mu.Unlock()
	dir, err := os.MkdirTemp("", "drydock-fake-oci-*")
	if err != nil {
		return "", nil, err
	}
	for name, data := range a.files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			return "", nil, err
		}
	}
	if opts.OnAcquired != nil {
		opts.OnAcquired(false)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func (a *fakeOCIArtifactAcquirer) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resolves, a.extracts
}

func writeOCIApplication(t *testing.T, root, appName, repoURL, sourcePath, revision, extra string) {
	t.Helper()
	pathLine := ""
	if sourcePath != "" {
		pathLine = "    path: " + sourcePath + "\n"
	}
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: "`+revision+`"
`+pathLine+extra+`  destination:
    name: in-cluster
    namespace: default
`)
}

func ociEventCount(events []cacheevent.Event) int {
	count := 0
	for _, event := range events {
		if event.Source == cacheevent.SourceOCI {
			count++
		}
	}
	return count
}

func ociBuildRequest(t *testing.T, root string) BuildRequest {
	t.Helper()
	return BuildRequest{
		Path: root,
		AcquisitionOptions: AcquisitionOptions{
			OCICacheDir:       t.TempDir(),
			RecordCacheEvents: true,
		},
	}
}

// Issue #220 shape WITHOUT a helm block: the silent-masking mode. Path "."
// always exists locally, so without total classification the local checkout
// (including this decoy) would render instead of the artifact content.
func TestBuildRendersOCIArtifactWithoutHelmBlock(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/demo", "1.2.3", ocitest.HelmChartSpec{Name: "demo", Version: "1.2.3"})
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", reg.RepoURL("charts/demo"), ".", "1.2.3", "")
	writeTestFile(t, filepath.Join(root, "decoy.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: local-decoy
data: {}
`)

	request := ociBuildRequest(t, root)
	result, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	manifest := assertManifestNamed(t, result.Manifests, "demo-demo")
	if marker, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "marker"); marker != "oci-artifact-content" {
		t.Fatalf("marker = %q, want artifact content", marker)
	}
	if message, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "message"); message != "from-defaults" {
		t.Fatalf("message = %q, want chart default", message)
	}
	if _, ok := manifestByName(result.Manifests, "local-decoy"); ok {
		t.Fatal("local checkout content leaked into an OCI artifact render")
	}
	if got := ociEventCount(result.CacheEvents); got != 1 {
		t.Fatalf("oci cache events = %d, want 1: %#v", got, result.CacheEvents)
	}
	for _, event := range result.CacheEvents {
		if event.Source == cacheevent.SourceOCI && event.Action != cacheevent.ActionFetch {
			t.Fatalf("first online acquisition action = %q, want fetch", event.Action)
		}
	}
}

// Issue #220 shape WITH a helm block: values flow into the artifact chart.
func TestBuildRendersOCIArtifactWithHelmValues(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/demo", "1.2.3", ocitest.HelmChartSpec{Name: "demo", Version: "1.2.3"})
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", reg.RepoURL("charts/demo"), ".", "1.2.3", `    helm:
      values: |
        message: overridden
`)

	result, err := Orchestrator{}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	manifest := assertManifestNamed(t, result.Manifests, "demo-demo")
	if message, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "message"); message != "overridden" {
		t.Fatalf("message = %q, want helm values override", message)
	}
}

// Local-path masking pin: the declared path exists in the local checkout with
// different content; the artifact content must win with zero local reads.
func TestBuildOCIArtifactIgnoresExistingLocalPath(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushPlainManifestsArtifact(t, reg, "manifests/app", "v1", map[string]string{
		"manifests/demo/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: artifact-content\ndata: {}\n",
	})
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", reg.RepoURL("manifests/app"), "manifests/demo", "v1", "")
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: local-decoy
data: {}
`)

	result, err := Orchestrator{}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "artifact-content")
	if _, ok := manifestByName(result.Manifests, "local-decoy"); ok {
		t.Fatal("existing local path masked the OCI artifact source")
	}
}

func TestBuildRendersPlainManifestsOCIArtifact(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushPlainManifestsArtifact(t, reg, "manifests/plain", "v1", map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain-artifact\ndata: {}\n",
	})
	root := t.TempDir()
	writeOCIApplication(t, root, "plain", reg.RepoURL("manifests/plain"), ".", "v1", "")

	result, err := Orchestrator{}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "plain-artifact")
}

// Classic-shape regression pin: oci:// + chart keeps drydock's existing
// helm-chart flow (recorded divergence) — the chart acquirer is hit, the
// artifact acquirer never runs.
func TestBuildClassicOCIChartShapeUsesChartAcquirer(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "demo")
	writeTestFile(t, filepath.Join(root, "apps", "classic.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: classic
  namespace: argocd
spec:
  source:
    repoURL: oci://charts.example.test/org
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: default
`)
	chartAcquirer := &recordingChartAcquirer{chartDir: filepath.Join(chartRoot, "demo")}
	artifactAcquirer := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32)}

	result, err := Orchestrator{ChartAcquirer: chartAcquirer, OCIArtifactAcquirer: artifactAcquirer}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "classic")
	if len(chartAcquirer.requests) == 0 {
		t.Fatal("chart acquirer not hit for oci:// + chart shape")
	}
	if resolves, extracts := artifactAcquirer.counts(); resolves != 0 || extracts != 0 {
		t.Fatalf("artifact acquirer hit for oci:// + chart shape: %d resolves %d extracts", resolves, extracts)
	}
}

// The hybrid shape (oci:// + chart + path) errors clearly instead of falling
// through to the masked branches.
func TestBuildHybridOCIChartPathShapeFails(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "hybrid.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: hybrid
  namespace: argocd
spec:
  source:
    repoURL: oci://charts.example.test/org
    targetRevision: 1.2.3
    chart: demo
    path: .
  destination:
    name: in-cluster
    namespace: default
`)
	artifactAcquirer := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32)}
	_, err := Orchestrator{OCIArtifactAcquirer: artifactAcquirer}.Build(context.Background(), ociBuildRequest(t, root))
	assertBuildErrorContains(t, err, "unsupported source shape")
	if resolves, extracts := artifactAcquirer.counts(); resolves != 0 || extracts != 0 {
		t.Fatalf("artifact acquirer hit for hybrid shape: %d resolves %d extracts", resolves, extracts)
	}
}

// Offline phase split: online build populates the cache, the registry
// closes, and a fresh orchestrator must render from cache — while an unseen
// tag fails with the offline cache miss contract text.
func TestBuildOCIArtifactOffline(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/demo", "1.2.3", ocitest.HelmChartSpec{Name: "demo", Version: "1.2.3"})
	root := t.TempDir()
	repoURL := reg.RepoURL("charts/demo")
	writeOCIApplication(t, root, "demo", repoURL, ".", "1.2.3", "")
	ociCacheDir := t.TempDir()

	onlineRequest := BuildRequest{Path: root, AcquisitionOptions: AcquisitionOptions{OCICacheDir: ociCacheDir, RecordCacheEvents: true}}
	onlineResult, err := Orchestrator{}.Build(context.Background(), onlineRequest)
	if err != nil {
		t.Fatalf("online Build() error = %v", err)
	}
	onlineManifest := assertManifestNamed(t, onlineResult.Manifests, "demo-demo")

	reg.Server.Close()

	offlineRequest := BuildRequest{Path: root, AcquisitionOptions: AcquisitionOptions{OCICacheDir: ociCacheDir, Offline: true, RecordCacheEvents: true}}
	offlineResult, err := Orchestrator{}.Build(context.Background(), offlineRequest)
	if err != nil {
		t.Fatalf("offline Build() error = %v", err)
	}
	offlineManifest := assertManifestNamed(t, offlineResult.Manifests, "demo-demo")
	if marker, _, _ := unstructured.NestedString(offlineManifest.Object.Object, "data", "marker"); marker != "oci-artifact-content" {
		t.Fatalf("offline marker = %q, want artifact content", marker)
	}
	if onlineName, offlineName := onlineManifest.Object.GetName(), offlineManifest.Object.GetName(); onlineName != offlineName {
		t.Fatalf("offline render diverged: %q vs %q", onlineName, offlineName)
	}
	sawHit := false
	for _, event := range offlineResult.CacheEvents {
		if event.Source == cacheevent.SourceOCI && event.Action == cacheevent.ActionHit && event.Offline {
			sawHit = true
		}
	}
	if !sawHit {
		t.Fatalf("offline build events missing oci hit: %#v", offlineResult.CacheEvents)
	}

	// Unseen tag offline: clear offline cache miss error.
	unseenRoot := t.TempDir()
	writeOCIApplication(t, unseenRoot, "demo", repoURL, ".", "9.9.9", "")
	_, err = Orchestrator{}.Build(context.Background(), BuildRequest{Path: unseenRoot, AcquisitionOptions: AcquisitionOptions{OCICacheDir: ociCacheDir, Offline: true}})
	assertBuildErrorContains(t, err, "offline cache miss")
}

// Two-sided diff at one digest: the session memo makes the whole diff a
// single real acquisition (one oci cache event).
func TestDiffTwoSidedOCISingleAcquisitionEvent(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/demo", "1.2.3", ocitest.HelmChartSpec{Name: "demo", Version: "1.2.3"})
	repoURL := reg.RepoURL("charts/demo")
	left := t.TempDir()
	right := t.TempDir()
	writeOCIApplication(t, left, "demo", repoURL, ".", "1.2.3", "")
	writeOCIApplication(t, right, "demo", repoURL, ".", "1.2.3", "")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Unified:   3,
		AcquisitionOptions: AcquisitionOptions{
			OCICacheDir:       t.TempDir(),
			RecordCacheEvents: true,
		},
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results = %#v, want none for identical sides", result.Results)
	}
	if got := ociEventCount(result.CacheEvents); got != 1 {
		t.Fatalf("oci cache events = %d, want 1 (session memo): %#v", got, result.CacheEvents)
	}
}

// Identity pin 9a: re-pushing the SAME tag with different content is
// reflected on the next online render (tags re-resolve via network HEAD).
func TestBuildOCISameTagRepushReflectsNewContent(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushPlainManifestsArtifact(t, reg, "manifests/app", "stable", map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: content-v1\ndata: {}\n",
	})
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", reg.RepoURL("manifests/app"), ".", "stable", "")
	ociCacheDir := t.TempDir()
	request := BuildRequest{Path: root, AcquisitionOptions: AcquisitionOptions{OCICacheDir: ociCacheDir}}

	first, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	assertManifestNamed(t, first.Manifests, "content-v1")

	ocitest.PushPlainManifestsArtifact(t, reg, "manifests/app", "stable", map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: content-v2\ndata: {}\n",
	})
	second, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	assertManifestNamed(t, second.Manifests, "content-v2")
	if _, ok := manifestByName(second.Manifests, "content-v1"); ok {
		t.Fatal("stale tag content served after same-tag re-push")
	}
}

// Identity pin 9b: two tags aliasing one digest share a single image-cache
// entry.
func TestBuildOCITagAliasesShareImageCacheEntry(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushPlainManifestsArtifact(t, reg, "manifests/app", "v1", map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: aliased\ndata: {}\n",
	})
	ocitest.TagAlias(t, reg, "manifests/app", "v1", "v1-alias")
	repoURL := reg.RepoURL("manifests/app")
	ociCacheDir := t.TempDir()

	for _, revision := range []string{"v1", "v1-alias"} {
		root := t.TempDir()
		writeOCIApplication(t, root, "demo", repoURL, ".", revision, "")
		if _, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, AcquisitionOptions: AcquisitionOptions{OCICacheDir: ociCacheDir}}); err != nil {
			t.Fatalf("Build(%s) error = %v", revision, err)
		}
	}

	entries, err := os.ReadDir(ociCacheDir)
	if err != nil {
		t.Fatal(err)
	}
	entryCount := 0
	for _, entry := range entries {
		if entry.IsDir() && len(entry.Name()) == 64 {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Fatalf("image cache entries = %d, want 1 (tags alias one digest)", entryCount)
	}
}

// Identity pin 9c (fake-level): the identity derives from the resolved
// digest, not the revision string.
func TestOCISourceIdentityDerivesFromDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	fake := &fakeOCIArtifactAcquirer{digest: digest, files: map[string]string{"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata: {}\n"}}
	root := t.TempDir()
	request := ociBuildRequest(t, root)
	recorder := cacheevent.NewRecorder(false)
	provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{OCIArtifactAcquirer: fake}, root, config.ArgoSettings{}, request, recorder, "drydock-oci-identity-test-*")
	if err != nil {
		t.Fatalf("newLocalProvider() error = %v", err)
	}
	defer cleanup()

	identities := make([]SourceIdentity, 0, 2)
	for _, revision := range []string{"1.2.3", "some-tag"} {
		_, identity, err := provider.resolveSourceRootIdentity(context.Background(), render.ResolvedSource{
			RepoURL:        "oci://registry.example/org/app",
			TargetRevision: revision,
			Path:           ".",
		})
		if err != nil {
			t.Fatalf("resolveSourceRootIdentity(%s) error = %v", revision, err)
		}
		identities = append(identities, identity)
	}
	if identities[0] != identities[1] {
		t.Fatalf("identities differ across revision spellings of one digest: %#v vs %#v", identities[0], identities[1])
	}
	if identities[0].Kind != sourceIdentityKindOCI || identities[0].Revision != digest {
		t.Fatalf("identity = %#v, want kind oci with digest revision", identities[0])
	}
}

// Near-miss/self-repo exclusion: an oci:// URL whose scheme-stripped form
// FULL-matches a git remote of the local checkout must not resolve to the
// local tree ("demo"), and a fork-shaped oci:// URL (same host and repo name,
// different owner, "fork") must not emit the near-miss warning.
func TestBuildOCISourceSkipsSelfRepoAndNearMiss(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{"https://registry.example/org/app"}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	writeOCIApplication(t, root, "demo", "oci://registry.example/org/app", ".", "1.2.3", "")
	// Empty targetRevision passes the near-miss revision gate, so only the
	// oci:// URL guard keeps the fork warning quiet.
	writeOCIApplication(t, root, "fork", "oci://registry.example/other/app", ".", "", "")
	fake := &fakeOCIArtifactAcquirer{
		digest: "sha256:" + strings.Repeat("cd", 32),
		files:  map[string]string{"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: artifact-content\ndata: {}\n"},
	}

	result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "artifact-content")
	if _, extracts := fake.counts(); extracts == 0 {
		t.Fatal("OCI source resolved without the artifact acquirer (self-repo mask)")
	}
	for _, diag := range result.Diagnostics {
		if diag.Code == selfRepoNearMissCode {
			t.Fatalf("near-miss diagnostic fired for an oci:// URL: %#v", diag)
		}
	}
}

// $ref→OCI is excluded (upstream parity): a helm value file referencing an
// OCI ref source fails with a clear error.
func TestBuildOCIRefSourceExcluded(t *testing.T) {
	root := t.TempDir()
	writeTestChart(t, root, "chart")
	writeTestFile(t, filepath.Join(root, "apps", "ref.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ref
  namespace: argocd
spec:
  sources:
    - repoURL: oci://registry.example/org/values
      targetRevision: "1.0.0"
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: chart
      helm:
        valueFiles:
          - $values/values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ef", 32), files: map[string]string{"values.yaml": "message: nope\n"}}
	_, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
	assertBuildErrorContains(t, err, "$values", "cannot be referenced as $ref value sources")
}

// repo-map precedence: mapping the oci:// URL to a local path keeps winning
// over OCI classification (today's only #220 workaround stays intact).
func TestBuildOCIRepoMapPrecedence(t *testing.T) {
	root := t.TempDir()
	mapped := t.TempDir()
	writeTestFile(t, filepath.Join(mapped, "manifests", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: mapped-content
data: {}
`)
	writeOCIApplication(t, root, "demo", "oci://registry.example/org/app", "manifests", "1.2.3", "")
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("aa", 32)}
	request := ociBuildRequest(t, root)
	request.RepoMaps = []sourcepkg.RepoMap{{URL: "oci://registry.example/org/app", Path: mapped}}

	result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "mapped-content")
	if resolves, extracts := fake.counts(); resolves != 0 || extracts != 0 {
		t.Fatalf("artifact acquirer hit despite repo-map: %d resolves %d extracts", resolves, extracts)
	}
}

// Discovery frontier: OCI sources are skipped exactly like chart-only ones.
func TestOCISourceSkippedInDiscoveryFrontier(t *testing.T) {
	root := t.TempDir()
	// Application manifests at the local path an unclassified `path: .` would
	// resolve to — the skip must keep them out of the frontier.
	writeBuildApplication(t, root, "inner", "inner-cm")
	app := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "oci://registry.example/org/app",
				TargetRevision: "1.2.3",
				Path:           ".",
			},
		},
	}
	if applicationMayRenderDiscoveryObjects(root, BuildRequest{}, discovery.Result{}, app) {
		t.Fatal("OCI source entered the discovery frontier")
	}
}

// Changed-path selection: an OCI source with `path: .` must not claim every
// changed path; its changes stay unowned.
func TestOCISourceDoesNotClaimChangedPaths(t *testing.T) {
	app := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "oci://registry.example/org/app",
				TargetRevision: "1.2.3",
				Path:           ".",
				Helm:           &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"values.yaml"}},
			},
		},
	}
	selected, unowned := SelectChangedApplications([]argoappv1.Application{app}, []string{"manifests/other/cm.yaml"})
	if len(selected) != 0 {
		t.Fatalf("selected = %#v, want none", selected)
	}
	if len(unowned) != 1 || unowned[0] != "manifests/other/cm.yaml" {
		t.Fatalf("unowned = %#v, want the changed path", unowned)
	}
}

// Extraction lifecycle: the extracted dir is present during render and
// released with the session; two sides at one digest with different paths do
// not contaminate each other.
func TestOCIExtractionLifecycleAndTwoSidedPaths(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushPlainManifestsArtifact(t, reg, "manifests/app", "v1", map[string]string{
		"a/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: side-a\ndata: {}\n",
		"b/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: side-b\ndata: {}\n",
	})
	repoURL := reg.RepoURL("manifests/app")
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", repoURL, "a", "v1", "")

	var renderRoots []string
	orchestrator := Orchestrator{}
	orchestrator.renderObserver = func(source render.ResolvedSource) {
		renderRoots = append(renderRoots, source.RepoRoot)
	}
	result, err := orchestrator.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "side-a")
	if len(renderRoots) == 0 {
		t.Fatal("render observer captured no source roots")
	}
	for _, renderRoot := range renderRoots {
		if _, err := os.Stat(renderRoot); !os.IsNotExist(err) {
			t.Fatalf("extraction root %q survived session release: %v", renderRoot, err)
		}
	}

	// Two sides, same digest, different paths.
	left := t.TempDir()
	right := t.TempDir()
	writeOCIApplication(t, left, "demo", repoURL, "a", "v1", "")
	writeOCIApplication(t, right, "demo", repoURL, "b", "v1", "")
	diffResult, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:           left,
		RightPath:          right,
		Unified:            3,
		AcquisitionOptions: AcquisitionOptions{OCICacheDir: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	names := map[string]bool{}
	for _, diffEntry := range diffResult.Results {
		names[diffEntry.Resource.Name] = true
	}
	if !names["side-a"] || !names["side-b"] {
		t.Fatalf("diff results = %#v, want side-a and side-b (no cross-side contamination)", diffResult.Results)
	}
}
