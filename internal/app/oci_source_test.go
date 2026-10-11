package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// fakeOCIArtifactAcquirer is the unit-test seam fake: it resolves every
// revision to a fixed digest (or fails with resolveErr) and extracts fixed
// files, mirroring the OnAcquired contract of the production acquirer. It
// records the (repoURL, revision) handed to Resolve and the (repoURL, digest)
// handed to Extract so tests can pin WHAT was pulled, not only that a pull
// happened. extractRoot, when set, hosts the extraction directories (for
// hand-built providers, which never release them through a session).
type fakeOCIArtifactAcquirer struct {
	mu          sync.Mutex
	digest      string
	files       map[string]string
	resolveErr  error
	extractRoot string
	resolves    int
	extracts    int
	resolved    []fakeOCIRequest
	extracted   []fakeOCIRequest
}

// fakeOCIRequest is one recorded acquirer call: revision is the requested
// revision for Resolve and the digest for Extract.
type fakeOCIRequest struct {
	repoURL  string
	revision string
}

func (a *fakeOCIArtifactAcquirer) Resolve(_ context.Context, repoURL, revision string, _ ociartifact.Options) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolves++
	a.resolved = append(a.resolved, fakeOCIRequest{repoURL: repoURL, revision: revision})
	if a.resolveErr != nil {
		return "", a.resolveErr
	}
	return a.digest, nil
}

func (a *fakeOCIArtifactAcquirer) Extract(_ context.Context, repoURL, digest string, opts ociartifact.Options) (string, func(), error) {
	a.mu.Lock()
	a.extracts++
	a.extracted = append(a.extracted, fakeOCIRequest{repoURL: repoURL, revision: digest})
	a.mu.Unlock()
	dir, err := os.MkdirTemp(a.extractRoot, "drydock-fake-oci-*")
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

// requests returns copies of the recorded Resolve and Extract calls, in order.
func (a *fakeOCIArtifactAcquirer) requests() (resolved, extracted []fakeOCIRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]fakeOCIRequest(nil), a.resolved...), append([]fakeOCIRequest(nil), a.extracted...)
}

// fakeOCIChartFiles is the fake acquirer's content for chart-shaped
// artifacts: a chart whose ConfigMap is named after the release and echoes
// .Values.message, plus any extra files (an .argocd-source.yaml override, a
// replacement template).
func fakeOCIChartFiles(name string, extra map[string]string) map[string]string {
	files := map[string]string{
		"Chart.yaml":        "apiVersion: v2\nname: " + name + "\nversion: 1.2.3\n",
		"values.yaml":       "message: from-defaults\n",
		"templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\ndata:\n  message: {{ .Values.message | quote }}\n",
	}
	maps.Copy(files, extra)
	return files
}

func plainConfigMapYAML(name string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\ndata: {}\n"
}

func diagnosticsWithCode(diags []diagnostic.Diagnostic, code string) []diagnostic.Diagnostic {
	var out []diagnostic.Diagnostic
	for _, diag := range diags {
		if diag.Code == code {
			out = append(out, diag)
		}
	}
	return out
}

// assertOCIChartIgnoredWarningOnce pins the advisory contract: exactly one
// source.oci-chart-ignored diagnostic, always a warning in category source.
func assertOCIChartIgnoredWarningOnce(t *testing.T, diags []diagnostic.Diagnostic) diagnostic.Diagnostic {
	t.Helper()
	found := diagnosticsWithCode(diags, ociChartIgnoredCode)
	if len(found) != 1 {
		t.Fatalf("%s diagnostics = %d, want exactly 1: %#v", ociChartIgnoredCode, len(found), diags)
	}
	diag := found[0]
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("%s severity = %s, want warning: %#v", ociChartIgnoredCode, diag.Severity, diag)
	}
	if diag.Category != "source" {
		t.Fatalf("%s category = %q, want source: %#v", ociChartIgnoredCode, diag.Category, diag)
	}
	return diag
}

func assertNoOCIChartIgnoredWarning(t *testing.T, diags []diagnostic.Diagnostic) {
	t.Helper()
	if found := diagnosticsWithCode(diags, ociChartIgnoredCode); len(found) != 0 {
		t.Fatalf("%s fired for a shape it must stay quiet on: %#v", ociChartIgnoredCode, found)
	}
}

// sortedManifestYAML serializes manifests in a stable order for whole-render
// equality checks between two builds of one Application.
func sortedManifestYAML(t *testing.T, manifests []render.Manifest) []byte {
	t.Helper()
	docs := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		data, err := yaml.Marshal(manifest.Object.Object)
		if err != nil {
			t.Fatalf("yaml.Marshal() error = %v", err)
		}
		docs = append(docs, string(data))
	}
	sort.Strings(docs)
	return []byte(strings.Join(docs, "---\n"))
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
		Path:              root,
		OCICacheDir:       t.TempDir(),
		RecordCacheEvents: true,
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

// TestBuildAndDiffFailFastOnInvalidOCICredentials pins the
// request-construction validation wiring on both surfaces: unusable OCI TLS
// material fails before any rendering starts, with the error naming the flag
// (the vendored client would otherwise degrade silently mid-render).
func TestBuildAndDiffFailFastOnInvalidOCICredentials(t *testing.T) {
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", "oci://registry.example.test/org/app", ".", "v1", "")
	nonPEM := filepath.Join(t.TempDir(), "not-a-cert.pem")
	if err := os.WriteFile(nonPEM, []byte("not pem data"), 0o600); err != nil {
		t.Fatal(err)
	}

	request := ociBuildRequest(t, root)
	request.OCICredentials = ociartifact.Credentials{CAFile: nonPEM}
	if _, err := (Orchestrator{}).Build(context.Background(), request); err == nil || !strings.Contains(err.Error(), "--oci-ca-file") {
		t.Fatalf("Build() error = %v, want fail-fast validation naming --oci-ca-file", err)
	}

	diffRequest := DiffRequest{
		LeftPath:       root,
		RightPath:      root,
		OCICacheDir:    t.TempDir(),
		OCICredentials: ociartifact.Credentials{CAFile: nonPEM},
	}
	if _, err := (Orchestrator{}).DiffApps(context.Background(), diffRequest); err == nil || !strings.Contains(err.Error(), "--oci-ca-file") {
		t.Fatalf("DiffApps() error = %v, want fail-fast validation naming --oci-ca-file", err)
	}
}

// TestBuildWithoutOCISourcesIgnoresMissingRegistryConfig pins the lazy
// contract on the Build surface: --registry-config is validated at the first
// oci:// acquisition, so a run with no OCI artifact source never opens it. CI
// that passes the flag unconditionally (pr-action extra-*-args) keeps building
// git-only repositories.
func TestBuildWithoutOCISourcesIgnoresMissingRegistryConfig(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "local-cm")
	request := BuildRequest{
		Path:           root,
		OCICredentials: ociartifact.Credentials{RegistryConfig: filepath.Join(t.TempDir(), "missing.json")},
	}

	result, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() error = %v, want success: a missing --registry-config must not fail a run without OCI sources", err)
	}
	assertManifestNamed(t, result.Manifests, "local-cm")
}

// TestBuildOCISourceFailsOnMissingRegistryConfig is the other half of the lazy
// contract: the same missing file fails the first oci:// acquisition, before
// the acquirer is asked anything, with the flag named.
func TestBuildOCISourceFailsOnMissingRegistryConfig(t *testing.T) {
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", "oci://registry.example.test/org/app", ".", "v1", "")
	fake := &fakeOCIArtifactAcquirer{
		digest: "sha256:" + strings.Repeat("ab", 32),
		files:  map[string]string{"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: artifact\ndata: {}\n"},
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	request := ociBuildRequest(t, root)
	request.OCICredentials = ociartifact.Credentials{RegistryConfig: missing}

	_, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), request)
	assertBuildErrorContains(t, err, "--registry-config")
	assertBuildErrorContains(t, err, "does not exist")
	if resolves, extracts := fake.counts(); resolves != 0 || extracts != 0 {
		t.Fatalf("acquirer calls = (%d resolves, %d extracts), want none before the registry config is validated", resolves, extracts)
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

// Argo CD dispatch pin (issue #399): oci:// + chart is an OCI artifact
// source — repoURL:targetRevision is pulled as spelled (never
// <repoURL>/<chart>), the chart acquirer stays idle, the artifact renders,
// and the source carries exactly one advisory that names the Argo-equivalent
// remedy (drop chart, path: .) before the schemeless alternative.
func TestBuildOCIChartShapeUsesArtifactAcquirer(t *testing.T) {
	root := t.TempDir()
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
	chartAcquirer := &recordingChartAcquirer{chartDir: t.TempDir()}
	artifactAcquirer := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: fakeOCIChartFiles("demo", nil)}

	result, err := Orchestrator{ChartAcquirer: chartAcquirer, OCIArtifactAcquirer: artifactAcquirer}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "classic")
	if resolves, extracts := artifactAcquirer.counts(); resolves < 1 || extracts < 1 {
		t.Fatalf("artifact acquirer calls = (%d resolves, %d extracts), want the artifact pulled", resolves, extracts)
	}
	resolved, extracted := artifactAcquirer.requests()
	for _, request := range append(resolved, extracted...) {
		if request.repoURL != "oci://charts.example.test/org" {
			t.Fatalf("acquirer pulled %q, want the repoURL as spelled (chart must never join the reference)", request.repoURL)
		}
	}
	for _, request := range resolved {
		if request.revision != "1.2.3" {
			t.Fatalf("Resolve revision = %q, want targetRevision 1.2.3", request.revision)
		}
	}
	if len(chartAcquirer.requests) != 0 {
		t.Fatalf("chart acquirer requests = %#v, want none for an oci:// source", chartAcquirer.requests)
	}
	diag := assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
	for _, fragment := range []string{"is ignored", "oci://charts.example.test/org", "path: .", "schemeless Helm OCI shape"} {
		if !strings.Contains(diag.Message, fragment) {
			t.Fatalf("diagnostic message = %q, want fragment %q", diag.Message, fragment)
		}
	}
	if strings.Index(diag.Message, "path: .") >= strings.Index(diag.Message, "schemeless") {
		t.Fatalf("diagnostic message = %q, want the path: . remedy before the schemeless alternative", diag.Message)
	}
}

// chart + path on one oci:// source is valid: path selects within the
// artifact and chart is ignored (Argo CD has no exclusivity check outside the
// API-create ValidateRepo path). The advisory fires once in both shapes.
func TestBuildOCIChartPathShapeRendersArtifactAtPath(t *testing.T) {
	for _, tt := range []struct {
		name  string
		path  string
		files map[string]string
		want  string
	}{
		{name: "path dot", path: ".", files: map[string]string{"cm.yaml": plainConfigMapYAML("hybrid-root")}, want: "hybrid-root"},
		{name: "path manifests", path: "manifests", files: map[string]string{"manifests/cm.yaml": plainConfigMapYAML("hybrid-nested")}, want: "hybrid-nested"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeOCIApplication(t, root, "hybrid", "oci://charts.example.test/org", tt.path, "1.2.3", "    chart: demo\n")
			artifactAcquirer := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: tt.files}

			result, err := Orchestrator{OCIArtifactAcquirer: artifactAcquirer}.Build(context.Background(), ociBuildRequest(t, root))
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			assertManifestNamed(t, result.Manifests, tt.want)
			if resolves, extracts := artifactAcquirer.counts(); resolves < 1 || extracts < 1 {
				t.Fatalf("artifact acquirer calls = (%d resolves, %d extracts), want the artifact pulled", resolves, extracts)
			}
			assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
		})
	}
}

// Empty-path normalization pin: an OCI Application with NO path line renders
// the artifact manifests exactly like `path: .` — argo renders the extraction
// root when an OCI source omits path (apppathutil.Path(ociPath, ""),
// repository.go:425 at v3.5.4) — whether or not chart is set.
func TestBuildRendersOCIArtifactWithoutPath(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/demo", "1.2.3", ocitest.HelmChartSpec{Name: "demo", Version: "1.2.3"})
	for _, tt := range []struct {
		name        string
		extra       string
		wantWarning bool
	}{
		{name: "no chart"},
		{name: "chart set", extra: "    chart: demo\n", wantWarning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeOCIApplication(t, root, "demo", reg.RepoURL("charts/demo"), "", "1.2.3", tt.extra)

			result, err := Orchestrator{}.Build(context.Background(), ociBuildRequest(t, root))
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			manifest := assertManifestNamed(t, result.Manifests, "demo-demo")
			if marker, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "marker"); marker != "oci-artifact-content" {
				t.Fatalf("marker = %q, want artifact content", marker)
			}
			if tt.wantWarning {
				assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
			} else {
				assertNoOCIChartIgnoredWarning(t, result.Diagnostics)
			}
		})
	}
}

// The advisory goes through RedactURL: credentials embedded in the repoURL
// spelling never reach the diagnostic text or any error.
func TestBuildOCIChartIgnoredWarningRedactsCredentials(t *testing.T) {
	root := t.TempDir()
	writeOCIApplication(t, root, "hybrid", "oci://user:sekret@charts.example.test/org", "", "1.2.3", "    chart: demo\n")
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: fakeOCIChartFiles("demo", nil)}

	result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
	diag := assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
	text := diag.Message
	if err != nil {
		text += "\n" + err.Error()
	}
	for _, leaked := range []string{"sekret", "user:sekret"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("diagnostic or error leaks URL credentials %q:\n%s", leaked, text)
		}
	}
	if !strings.Contains(diag.Message, "oci://charts.example.test/org") {
		t.Fatalf("diagnostic message = %q, want the redacted repository URL", diag.Message)
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

	onlineRequest := BuildRequest{Path: root, OCICacheDir: ociCacheDir, RecordCacheEvents: true}
	onlineResult, err := Orchestrator{}.Build(context.Background(), onlineRequest)
	if err != nil {
		t.Fatalf("online Build() error = %v", err)
	}
	onlineManifest := assertManifestNamed(t, onlineResult.Manifests, "demo-demo")

	reg.Server.Close()

	offlineRequest := BuildRequest{Path: root, OCICacheDir: ociCacheDir, Offline: true, RecordCacheEvents: true}
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
	_, err = Orchestrator{}.Build(context.Background(), BuildRequest{Path: unseenRoot, OCICacheDir: ociCacheDir, Offline: true})
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
		LeftPath:          left,
		RightPath:         right,
		Unified:           3,
		OCICacheDir:       t.TempDir(),
		RecordCacheEvents: true,
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
	request := BuildRequest{Path: root, OCICacheDir: ociCacheDir}

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
		if _, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, OCICacheDir: ociCacheDir}); err != nil {
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

	// The moved shape (chart set, with or without path) is an artifact source
	// too: digest identity, never the chart flow's spec-derived name+version
	// identity — the persistent render-cache key follows the content.
	for _, source := range []render.ResolvedSource{
		{RepoURL: "oci://registry.example/org/app", TargetRevision: "1.2.3", Chart: "app"},
		{RepoURL: "oci://registry.example/org/app", TargetRevision: "1.2.3", Chart: "app", Path: "manifests"},
	} {
		_, identity, err := provider.resolveSourceRootIdentity(context.Background(), source)
		if err != nil {
			t.Fatalf("resolveSourceRootIdentity(chart=%q path=%q) error = %v", source.Chart, source.Path, err)
		}
		if identity.Kind != sourceIdentityKindOCI || identity.Revision != digest || identity.Locator != ociartifact.RedactURL(source.RepoURL) {
			t.Fatalf("identity(chart=%q path=%q) = %#v, want kind oci, digest revision, redacted locator", source.Chart, source.Path, identity)
		}
		if identity == chartOnlySourceIdentity(source) {
			t.Fatalf("identity(chart=%q path=%q) = %#v, must not be the chart flow's spec-derived identity", source.Chart, source.Path, identity)
		}
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
// over OCI classification (today's only #220 workaround stays intact) — for
// the chart-bearing spellings too, which render the mapped tree (the mapped
// root when path is empty) with the advisory still emitted.
func TestBuildOCIRepoMapPrecedence(t *testing.T) {
	mapped := t.TempDir()
	writeTestFile(t, filepath.Join(mapped, "manifests", "cm.yaml"), plainConfigMapYAML("mapped-content"))
	writeTestFile(t, filepath.Join(mapped, "cm.yaml"), plainConfigMapYAML("mapped-root-content"))
	for _, tt := range []struct {
		name        string
		path        string
		extra       string
		want        string
		wantWarning bool
	}{
		{name: "path", path: "manifests", want: "mapped-content"},
		{name: "chart and path", path: "manifests", extra: "    chart: demo\n", want: "mapped-content", wantWarning: true},
		{name: "chart only renders the mapped root", extra: "    chart: demo\n", want: "mapped-root-content", wantWarning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeOCIApplication(t, root, "demo", "oci://registry.example/org/app", tt.path, "1.2.3", tt.extra)
			fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("aa", 32)}
			request := ociBuildRequest(t, root)
			request.RepoMaps = []sourcepkg.RepoMap{{URL: "oci://registry.example/org/app", Path: mapped}}

			result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), request)
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			assertManifestNamed(t, result.Manifests, tt.want)
			if resolves, extracts := fake.counts(); resolves != 0 || extracts != 0 {
				t.Fatalf("artifact acquirer hit despite repo-map: %d resolves %d extracts", resolves, extracts)
			}
			if tt.wantWarning {
				assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
			} else {
				assertNoOCIChartIgnoredWarning(t, result.Diagnostics)
			}
		})
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

// Repo-map escape hatch at the frontier: a repo-mapped oci:// parent renders
// from the mapped local tree, so it keeps frontier eligibility and discovers
// child Applications — whatever chart says, with or without path (the OCI
// rule is evaluated before the chart-only skip); an unmapped oci:// parent
// stays skipped in every spelling.
func TestOCISourceRepoMappedEntersDiscoveryFrontier(t *testing.T) {
	root := t.TempDir()
	// A discovery-relevant file at the local `path: .` keeps the unmapped
	// assertion honest: without the OCI skip it would enter the frontier.
	writeBuildApplication(t, root, "decoy", "decoy-cm")
	mapped := t.TempDir()
	writeBuildApplication(t, mapped, "child", "child-cm")
	request := BuildRequest{
		RepoMaps: []sourcepkg.RepoMap{{URL: "oci://registry.example/org/app", Path: mapped}}}
	for _, tt := range []struct {
		name   string
		source argoappv1.ApplicationSource
	}{
		{name: "path", source: argoappv1.ApplicationSource{RepoURL: "oci://registry.example/org/app", TargetRevision: "1.2.3", Path: "."}},
		{name: "chart and path", source: argoappv1.ApplicationSource{RepoURL: "oci://registry.example/org/app", TargetRevision: "1.2.3", Chart: "demo", Path: "."}},
		{name: "chart only", source: argoappv1.ApplicationSource{RepoURL: "oci://registry.example/org/app", TargetRevision: "1.2.3", Chart: "demo"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := tt.source
			app := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Source: &source}}
			if !applicationMayRenderDiscoveryObjects(root, request, discovery.Result{}, app) {
				t.Fatal("repo-mapped OCI parent lost frontier eligibility (its child Applications would be silently dropped)")
			}
			if applicationMayRenderDiscoveryObjects(root, BuildRequest{}, discovery.Result{}, app) {
				t.Fatal("unmapped OCI source entered the discovery frontier")
			}
		})
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

// Value-file guard pin (local shape): an OCI source's helm value files
// resolve inside the extracted artifact, so a changed path at the value-file
// location must stay unowned.
func TestOCISourceLocalValueFilesDoNotClaimChangedPaths(t *testing.T) {
	app := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "oci://registry.example/org/app",
				TargetRevision: "1.2.3",
				Path:           "manifests",
				Helm:           &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"values.yaml"}},
			},
		},
	}
	selected, unowned := SelectChangedApplications([]argoappv1.Application{app}, []string{"manifests/values.yaml"})
	if len(selected) != 0 {
		t.Fatalf("selected = %#v, want none (OCI value files are artifact content)", selected)
	}
	if len(unowned) != 1 || unowned[0] != "manifests/values.yaml" {
		t.Fatalf("unowned = %#v, want the changed path", unowned)
	}
}

// Value-file guard pin ($ref shape): a $ref value file whose ref source is
// OCI must not claim the changed path either, even when the consuming source
// shares the repoURL spelling.
func TestOCIRefValueFilesDoNotClaimChangedPaths(t *testing.T) {
	app := argoappv1.Application{
		Spec: argoappv1.ApplicationSpec{
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "oci://registry.example/org/app", TargetRevision: "1.2.3", Ref: "values"},
				{
					RepoURL:        "oci://registry.example/org/app",
					TargetRevision: "1.2.3",
					Path:           "manifests",
					Helm:           &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"$values/values.yaml"}},
				},
			},
		},
	}
	selected, unowned := SelectChangedApplications([]argoappv1.Application{app}, []string{"values.yaml"})
	if len(selected) != 0 {
		t.Fatalf("selected = %#v, want none (OCI $ref value files are artifact content)", selected)
	}
	if len(unowned) != 1 || unowned[0] != "values.yaml" {
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
		LeftPath:    left,
		RightPath:   right,
		Unified:     3,
		OCICacheDir: t.TempDir(),
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

// The advisory is strict-exempt: --strict neither escalates it nor fails the
// build, because the render is Argo-identical whether or not it fires.
func TestBuildOCIChartIgnoredWarningIsStrictExempt(t *testing.T) {
	for _, tt := range []struct {
		name   string
		strict bool
	}{
		{name: "default"},
		{name: "strict still succeeds", strict: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeOCIApplication(t, root, "demo", "oci://charts.example.test/org", "", "1.2.3", "    chart: demo\n")
			fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: fakeOCIChartFiles("demo", nil)}
			request := ociBuildRequest(t, root)
			request.Strict = tt.strict

			result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), request)
			if err != nil {
				t.Fatalf("Build() error = %v (oci-chart-ignored must stay strict-exempt)", err)
			}
			assertManifestNamed(t, result.Manifests, "demo")
			assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
		})
	}
}

// Reporter's shape (issue #399) end to end through the hermetic registry: the
// chart is pushed under the full repoURL, chart: names it redundantly, no
// path, helm.valuesObject and releaseName set. It renders the same bytes the
// no-chart `path: .` spelling renders, through the OCI artifact flow: one OCI
// acquisition event per build, zero chart-acquirer requests. The twins are
// built separately under one Application name so tracking metadata cannot
// differ (one build would also memoize the second pull away).
func TestBuildRendersOCIChartShapeWithHelmValuesObject(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/app-template", "1.2.3", ocitest.HelmChartSpec{Name: "app-template", Version: "1.2.3"})
	helmBlock := "    helm:\n      releaseName: twin\n      valuesObject:\n        message: from-values-object\n"
	rendered := map[string][]byte{}
	for _, tt := range []struct {
		name        string
		path        string
		extra       string
		wantWarning bool
	}{
		{name: "chart shape", extra: "    chart: app-template\n" + helmBlock, wantWarning: true},
		{name: "path shape", path: ".", extra: helmBlock},
	} {
		root := t.TempDir()
		writeOCIApplication(t, root, "demo", reg.RepoURL("charts/app-template"), tt.path, "1.2.3", tt.extra)
		chartAcquirer := &recordingChartAcquirer{chartDir: t.TempDir()}

		result, err := Orchestrator{ChartAcquirer: chartAcquirer}.Build(context.Background(), ociBuildRequest(t, root))
		if err != nil {
			t.Fatalf("%s: Build() error = %v", tt.name, err)
		}
		manifest := assertManifestNamed(t, result.Manifests, "twin-demo")
		if message, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "message"); message != "from-values-object" {
			t.Fatalf("%s: message = %q, want the helm.valuesObject override", tt.name, message)
		}
		if len(chartAcquirer.requests) != 0 {
			t.Fatalf("%s: chart acquirer requests = %#v, want none (oci:// is an artifact source)", tt.name, chartAcquirer.requests)
		}
		if got := ociEventCount(result.CacheEvents); got != 1 {
			t.Fatalf("%s: oci cache events = %d, want 1: %#v", tt.name, got, result.CacheEvents)
		}
		if tt.wantWarning {
			assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
		} else {
			assertNoOCIChartIgnoredWarning(t, result.Diagnostics)
		}
		rendered[tt.name] = sortedManifestYAML(t, result.Manifests)
	}
	if !bytes.Equal(rendered["chart shape"], rendered["path shape"]) {
		t.Fatalf("chart shape and path shape rendered differently:\n--- chart shape ---\n%s\n--- path shape ---\n%s", rendered["chart shape"], rendered["path shape"])
	}
}

// Upstream merges .argocd-source*.yaml at the OCI app path
// (repository.go:1920-1923 at v3.5.4) — the artifact root for the pathless
// shape. PrepareSource therefore acquires oci:// + chart exactly as it does
// `path: .`, so an override inside the artifact reaches the helm block in
// both spellings and they render the same bytes.
func TestBuildOCIChartShapeMergesArgocdSourceOverrides(t *testing.T) {
	files := fakeOCIChartFiles("demo", map[string]string{
		".argocd-source.yaml": "helm:\n  valuesObject:\n    message: from-override\n",
	})
	rendered := map[string][]byte{}
	for _, tt := range []struct {
		name        string
		path        string
		extra       string
		wantWarning bool
	}{
		{name: "chart shape", extra: "    chart: demo\n", wantWarning: true},
		{name: "path shape", path: "."},
	} {
		root := t.TempDir()
		writeOCIApplication(t, root, "demo", "oci://charts.example.test/org", tt.path, "1.2.3", tt.extra)
		fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: files}

		result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
		if err != nil {
			t.Fatalf("%s: Build() error = %v", tt.name, err)
		}
		manifest := assertManifestNamed(t, result.Manifests, "demo")
		if message, _, _ := unstructured.NestedString(manifest.Object.Object, "data", "message"); message != "from-override" {
			t.Fatalf("%s: message = %q, want the .argocd-source.yaml override merged at the artifact root", tt.name, message)
		}
		if tt.wantWarning {
			assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
		} else {
			assertNoOCIChartIgnoredWarning(t, result.Diagnostics)
		}
		rendered[tt.name] = sortedManifestYAML(t, result.Manifests)
	}
	if !bytes.Equal(rendered["chart shape"], rendered["path shape"]) {
		t.Fatalf("chart shape and path shape rendered differently:\n--- chart shape ---\n%s\n--- path shape ---\n%s", rendered["chart shape"], rendered["path shape"])
	}
}

// A config management plugin on oci:// + chart runs on the extracted artifact
// at the (defaulted) path, like a chart-only source's plugin runs on the
// fetched chart: the default AVP compatibility renders the chart natively and
// substitutes its placeholders, and the advisory still fires.
func TestBuildOCIChartShapeWithAVPCompatPluginRendersArtifact(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: oci-chart-plugin
  namespace: argocd
spec:
  project: default
  source:
    repoURL: oci://charts.example.test/org
    targetRevision: 1.2.3
    chart: demo
    plugin:
      name: argocd-vault-plugin
  destination:
    name: in-cluster
    namespace: default
`)
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: fakeOCIChartFiles("demo", map[string]string{
		"templates/cm.yaml": `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-oci-chart
  annotations:
    avp.kubernetes.io/path: vaults/K8s/items/chart-secret
data:
  value: <CHART_VALUE>
`,
	})}

	result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-oci-chart", "value", "")
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, default AVP plugin compatibility should be quiet", result.Diagnostics)
	}
	if resolves, extracts := fake.counts(); resolves < 1 || extracts < 1 {
		t.Fatalf("artifact acquirer calls = (%d resolves, %d extracts), want the plugin source pulled as an artifact", resolves, extracts)
	}
	assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
}

// The advisory accompanies an acquisition failure: when the pull of
// repoURL:targetRevision fails (a stale chart that meant a different
// artifact, an unreachable registry), the user still gets the hint that
// explains which reference drydock — and Argo CD — tried.
func TestBuildOCIChartIgnoredWarningAccompaniesAcquisitionFailure(t *testing.T) {
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", "oci://charts.example.test/org", "", "1.2.3", "    chart: demo\n")
	fake := &fakeOCIArtifactAcquirer{resolveErr: errors.New("registry unreachable")}

	result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
	assertBuildErrorContains(t, err, "registry unreachable")
	assertOCIChartIgnoredWarningOnce(t, result.Diagnostics)
}

// Two-sided diff: the advisory is kept per side (one per Application per
// side), strict or not.
func TestDiffOCIChartIgnoredWarnsOncePerSide(t *testing.T) {
	for _, tt := range []struct {
		name   string
		strict bool
	}{
		{name: "default"},
		{name: "strict still succeeds", strict: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			left := t.TempDir()
			right := t.TempDir()
			for _, side := range []string{left, right} {
				writeOCIApplication(t, side, "demo", "oci://charts.example.test/org", "", "1.2.3", "    chart: demo\n")
			}
			fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: fakeOCIChartFiles("demo", nil)}

			result, err := Orchestrator{OCIArtifactAcquirer: fake}.DiffApps(context.Background(), DiffRequest{
				LeftPath:    left,
				RightPath:   right,
				Unified:     3,
				Strict:      tt.strict,
				OCICacheDir: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v (oci-chart-ignored must stay strict-exempt)", err)
			}
			found := diagnosticsWithCode(result.Diagnostics, ociChartIgnoredCode)
			if len(found) != 2 {
				t.Fatalf("%s diagnostics = %d, want exactly one per side: %#v", ociChartIgnoredCode, len(found), result.Diagnostics)
			}
			for _, diag := range found {
				if diag.Severity != diagnostic.SeverityWarning {
					t.Fatalf("%s severity = %s, want warning: %#v", ociChartIgnoredCode, diag.Severity, diag)
				}
			}
		})
	}
}

// Negative pin: an oci:// source without chart never carries the advisory.
func TestBuildOCIArtifactWithoutChartEmitsNoChartIgnoredWarning(t *testing.T) {
	root := t.TempDir()
	writeOCIApplication(t, root, "demo", "oci://charts.example.test/org", ".", "1.2.3", "")
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ab", 32), files: map[string]string{"cm.yaml": plainConfigMapYAML("artifact-content")}}

	result, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertManifestNamed(t, result.Manifests, "artifact-content")
	assertNoOCIChartIgnoredWarning(t, result.Diagnostics)
}

// Negative pin: the schemeless Helm-OCI shape is unchanged — the chart
// acquirer is hit, the artifact acquirer never runs, no advisory.
func TestBuildSchemelessOCIChartShapeUsesChartAcquirerWithoutWarning(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "demo")
	writeTestFile(t, filepath.Join(root, "apps", "schemeless.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: schemeless
  namespace: argocd
spec:
  source:
    repoURL: charts.example.test/org
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
	assertManifestNamed(t, result.Manifests, "schemeless")
	if len(chartAcquirer.requests) == 0 {
		t.Fatal("chart acquirer not hit for the schemeless Helm-OCI shape")
	}
	if resolves, extracts := artifactAcquirer.counts(); resolves != 0 || extracts != 0 {
		t.Fatalf("artifact acquirer hit for the schemeless Helm-OCI shape: %d resolves %d extracts", resolves, extracts)
	}
	assertNoOCIChartIgnoredWarning(t, result.Diagnostics)
}

// An oci:// entry may set both ref and chart (Plan no longer rejects it:
// upstream dispatches it as an OCI artifact source and renders it), but
// consuming its $ref stays rejected by the OCI $ref gate, as for any oci://
// ref source.
func TestBuildOCIRefChartSourceCannotBeConsumedAsRef(t *testing.T) {
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
      chart: values
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
	fake := &fakeOCIArtifactAcquirer{digest: "sha256:" + strings.Repeat("ef", 32), files: map[string]string{"cm.yaml": plainConfigMapYAML("values-artifact")}}
	_, err := Orchestrator{OCIArtifactAcquirer: fake}.Build(context.Background(), ociBuildRequest(t, root))
	assertBuildErrorContains(t, err, "$values", "cannot be referenced as $ref value sources")
}
