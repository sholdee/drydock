package cli

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
	"github.com/sirupsen/logrus"
)

// writeOCIAppForCLI writes an Application sourcing an OCI artifact from the
// hermetic registry.
func writeOCIAppForCLI(t *testing.T, root, appName, repoURL, sourcePath, revision string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: "`+revision+`"
    path: `+sourcePath+`
  destination:
    name: in-cluster
    namespace: default
`)
}

// writeOCIChartAppForCLI writes an Application in the issue #399 shape: an
// oci:// repoURL plus a chart: (ignored by Argo CD and drydock), no path, and
// extra carries the helm block when one is wanted. writeOCIAppForCLI
// hard-codes a path line, which this shape must not have.
func writeOCIChartAppForCLI(t *testing.T, root, appName, repoURL, chart, revision, extra string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: "`+revision+`"
    chart: `+chart+`
`+extra+`  destination:
    name: in-cluster
    namespace: default
`)
}

func pushCLIChartArtifact(t *testing.T, reg *ocitest.Registry, repoName, tag, marker string) {
	t.Helper()
	ocitest.PushPlainManifestsArtifact(t, reg, repoName, tag, map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + marker + "\ndata: {}\n",
	})
}

// CLI integration against the hermetic registry through the REAL acquirer
// wrapper (no injected fakes): the nil-EventHandlers panic class would
// surface on any of these paths.
func TestCLIGetTestBuildOCISource(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "v1", "cli-artifact-content")
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", reg.RepoURL("manifests/app"), ".", "v1")
	ociCacheDir := t.TempDir()

	getResult := runCLI(t, "get", "apps", "--path", root, "--oci-cache-dir", ociCacheDir)
	assertStdoutContainsAll(t, getResult, "demo")

	testResult := runCLI(t, "test", "apps", "--path", root, "--oci-cache-dir", ociCacheDir)
	assertStdoutContainsAll(t, testResult, "demo", "PASS")

	buildResult := runCLI(t, "build", "apps", "--path", root, "--oci-cache-dir", ociCacheDir)
	assertStdoutContainsAll(t, buildResult, "cli-artifact-content")
}

func TestCLIDiffOCISourceBothSidesAndOneSided(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "v1", "artifact-v1")
	pushCLIChartArtifact(t, reg, "manifests/app", "v2", "artifact-v2")
	repoURL := reg.RepoURL("manifests/app")
	ociCacheDir := t.TempDir()

	// Both sides at different tags: the diff reflects artifact content change.
	left := t.TempDir()
	right := t.TempDir()
	writeOCIAppForCLI(t, left, "demo", repoURL, ".", "v1")
	writeOCIAppForCLI(t, right, "demo", repoURL, ".", "v2")
	diffResult := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--oci-cache-dir", ociCacheDir, "--exit-code=false")
	assertStdoutContainsAll(t, diffResult, "artifact-v1", "artifact-v2")

	// One-sided: the app exists only on the right side (added).
	emptyLeft := t.TempDir()
	writeCLITestFile(t, filepath.Join(emptyLeft, ".keep"), "")
	oneSided := runCLI(t, "diff", "apps", "--path-orig", emptyLeft, "--path", right, "--oci-cache-dir", ociCacheDir, "--exit-code=false")
	assertStdoutContainsAll(t, oneSided, "artifact-v2")
}

// TestCLIOCIBasicAuthSuccess drives the full CLI through the REAL acquirer
// against a Basic-auth registry: the flag family reaches the wire.
func TestCLIOCIBasicAuthSuccess(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "v1", "authed-artifact-content")
	reg.EnableBasicAuth("oci-user", "sekrit-pass")
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", reg.RepoURL("manifests/app"), ".", "v1")

	result := runCLI(t, "build", "apps",
		"--path", root,
		"--oci-cache-dir", t.TempDir(),
		"--oci-username", "oci-user",
		"--oci-password", "sekrit-pass",
	)
	assertStdoutContainsAll(t, result, "authed-artifact-content")
}

// writeCLIRegistryConfig writes a Docker-format registry config file with one
// plain auths entry for host and returns its path.
func writeCLIRegistryConfig(t *testing.T, root, host, username, password string) string {
	t.Helper()
	path := filepath.Join(root, "registry-config.json")
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	writeCLITestFile(t, path, `{"auths":{"`+host+`":{"auth":"`+auth+`"}}}`)
	return path
}

// TestCLIOCIRegistryConfigAuthSuccess drives the full CLI through the REAL
// acquirer against a Basic-auth registry with the credential coming from
// --registry-config alone: the per-host file lookup reaches the wire.
func TestCLIOCIRegistryConfigAuthSuccess(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "v1", "registry-config-artifact-content")
	reg.EnableBasicAuth("oci-user", "sekrit-pass")
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", reg.RepoURL("manifests/app"), ".", "v1")
	registryConfig := writeCLIRegistryConfig(t, t.TempDir(), reg.Host, "oci-user", "sekrit-pass")

	result := runCLI(t, "build", "apps",
		"--path", root,
		"--oci-cache-dir", t.TempDir(),
		"--registry-config", registryConfig,
	)
	assertStdoutContainsAll(t, result, "registry-config-artifact-content")
}

// TestCLIOCIRegistryConfigWrongPasswordRedacted is the file-sourced twin of
// TestCLIOCIAuthFailureRedactsCredentials: the wrong password exists only in
// --registry-config, so only the per-host pre-resolution feeding
// ociSensitiveValues can keep it and its base64(user:pass) form out of the
// command error, stdout, stderr (which carries --cache-events for build) and
// the cache directory. The constraint revision forces the /tags/list GET whose
// errcode body echoes the Authorization header back.
func TestCLIOCIRegistryConfigWrongPasswordRedacted(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "1.0.0", "authed-artifact-content")
	reg.EnableBasicAuth("oci-user", "correct-pass")
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", reg.RepoURL("manifests/app"), ".", "~1.0")
	registryConfig := writeCLIRegistryConfig(t, t.TempDir(), reg.Host, "oci-user", "sekrit-wrong")

	cacheDir := t.TempDir()
	stdout, stderr, err := executeCLIExpectingError(t, "build", "apps",
		"--path", root,
		"--oci-cache-dir", cacheDir,
		"--registry-config", registryConfig,
		"--cache-events",
	)
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %q, want a clear 401 auth error", err)
	}
	basicForm := base64.StdEncoding.EncodeToString([]byte("oci-user:sekrit-wrong"))
	for _, leaked := range []string{"sekrit-wrong", basicForm} {
		for surface, text := range map[string]string{"error": err.Error(), "stdout": stdout, "stderr": stderr} {
			if strings.Contains(text, leaked) {
				t.Fatalf("%s leaked %q:\n%s", surface, leaked, text)
			}
		}
	}
	if walkErr := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, leaked := range []string{"sekrit-wrong", basicForm} {
			if strings.Contains(string(data), leaked) {
				t.Fatalf("cache file %s leaked %q", path, leaked)
			}
		}
		return nil
	}); walkErr != nil {
		t.Fatalf("walk cache dir: %v", walkErr)
	}
}

// TestCLIOCIAuthFailureRedactsCredentials pins end-to-end redaction on the
// leak-shaped path: the fixture 401 body echoes the received Authorization
// header raw and base64-decoded, so the wrong password and its
// base64(user:pass) form flow back inside the vendored client's error text
// and must be scrubbed from the command error, stdout, stderr, and the
// --cache-events stream. The revision is a CONSTRAINT deliberately: the
// exact-tag lookup is a HEAD (401 body discarded), while the constraint
// fallback GETs /tags/list, whose errcode body — echo included — lands in
// the oras error text. Only that path makes these assertions bite.
func TestCLIOCIAuthFailureRedactsCredentials(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "1.0.0", "authed-artifact-content")
	reg.EnableBasicAuth("oci-user", "correct-pass")
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", reg.RepoURL("manifests/app"), ".", "~1.0")

	cacheDir := t.TempDir()
	stdout, stderr, err := executeCLIExpectingError(t, "build", "apps",
		"--path", root,
		"--oci-cache-dir", cacheDir,
		"--oci-username", "oci-user",
		"--oci-password", "sekrit-wrong",
		"--cache-events",
	)
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %q, want a clear 401 auth error", err)
	}
	basicForm := base64.StdEncoding.EncodeToString([]byte("oci-user:sekrit-wrong"))
	for _, leaked := range []string{"sekrit-wrong", basicForm} {
		for surface, text := range map[string]string{"error": err.Error(), "stdout": stdout, "stderr": stderr} {
			if strings.Contains(text, leaked) {
				t.Fatalf("%s leaked %q:\n%s", surface, leaked, text)
			}
		}
	}
	// Nothing on disk either: walk every file the run left in the OCI cache.
	if walkErr := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, leaked := range []string{"sekrit-wrong", basicForm} {
			if strings.Contains(string(data), leaked) {
				t.Fatalf("cache file %s leaked %q", path, leaked)
			}
		}
		return nil
	}); walkErr != nil {
		t.Fatalf("walk cache dir: %v", walkErr)
	}
}

// TestCLIOCIURLUserinfoRejected pins the CLI surface of the early userinfo
// rejection: a clear error naming the credential flags, with the secret in
// no output stream.
func TestCLIOCIURLUserinfoRejected(t *testing.T) {
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", "oci://cli-user:cli-sekrit@127.0.0.1:1/org/app", ".", "v1")

	stdout, stderr, err := executeCLIExpectingError(t, "build", "apps",
		"--path", root,
		"--oci-cache-dir", t.TempDir(),
		"--cache-events",
	)
	for _, flag := range []string{"--oci-username", "--oci-password"} {
		if !strings.Contains(err.Error(), flag) {
			t.Fatalf("error = %q, want rejection naming %s", err, flag)
		}
	}
	for surface, text := range map[string]string{"error": err.Error(), "stdout": stdout, "stderr": stderr} {
		if strings.Contains(text, "cli-sekrit") {
			t.Fatalf("%s leaked the URL-carried password:\n%s", surface, text)
		}
	}
}

// TestCLIOCIConstraintResolveQuietStderr pins the logrus quieting: a
// constraint resolve fetches the tag list, which the vendored client logs at
// Info level ("took to get tags", straight to the logrus writer) — the CLI
// must have dropped logrus to Warn so a normal run stays quiet.
func TestCLIOCIConstraintResolveQuietStderr(t *testing.T) {
	var logrusOut strings.Builder
	logrus.SetOutput(&logrusOut)
	defer logrus.SetOutput(os.Stderr)
	defer logrus.SetLevel(logrus.GetLevel())

	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "1.0.0", "constraint-artifact-content")
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", reg.RepoURL("manifests/app"), ".", "~1.0")

	result := runCLI(t, "build", "apps", "--path", root, "--oci-cache-dir", t.TempDir())
	assertStdoutContainsAll(t, result, "constraint-artifact-content")
	if strings.Contains(logrusOut.String(), "took to get tags") {
		t.Fatalf("vendored client Info logging reached the logrus writer:\n%s", logrusOut.String())
	}
	if strings.Contains(result.Stderr, "took to get tags") {
		t.Fatalf("stderr polluted by vendored client logging:\n%s", result.Stderr)
	}
}

// TestCLIDiffRefBothSidesShareOCICredentials pins that a ref diff hands the
// SAME credential set to the acquirer on both diff sides (the shared
// acquisitionOptions path is what keeps the sides identical).
func TestCLIDiffRefBothSidesShareOCICredentials(t *testing.T) {
	root := t.TempDir()
	repo, wt := initCLIGitRepo(t, root)
	writeOCIAppForCLI(t, root, "demo", "oci://registry.example.test/org/app", ".", "v1")
	commitCLIGitRepo(t, repo, wt, "baseline")
	checkoutCLIGitBranch(t, wt, "feature")
	writeOCIAppForCLI(t, root, "demo", "oci://registry.example.test/org/app", ".", "v2")
	commitCLIGitRepo(t, repo, wt, "feature")

	ociAcquirer := &recordingCLIOCIAcquirer{
		digests: map[string]string{
			"v1": "sha256:" + strings.Repeat("ab", 32),
			"v2": "sha256:" + strings.Repeat("cd", 32),
		},
		files: map[string]string{"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: oci\ndata: {}\n"},
	}
	runCLIWithDependencies(t, Dependencies{
		Orchestrator: app.Orchestrator{OCIArtifactAcquirer: ociAcquirer},
	},
		"diff", "apps",
		"--repo", root,
		"--ref-orig", "master",
		"--ref", "feature",
		"--exit-code=false",
		"--oci-username", "oci-user",
		"--oci-password", "oci-pass",
	)

	recorded := ociAcquirer.recorded()
	// Two sides at two revisions/digests: resolve+extract per side.
	if len(recorded) < 4 {
		t.Fatalf("recorded %d acquirer calls, want at least 4 (both sides resolve+extract)", len(recorded))
	}
	want := ociartifact.Credentials{Username: "oci-user", Password: "oci-pass"}
	for i, opts := range recorded {
		if opts.Credentials != want {
			t.Fatalf("acquirer call %d credentials = %#v, want %#v (both diff sides must share the flag credentials)", i, opts.Credentials, want)
		}
	}
}

// Same invariant on the single-app surface: `diff app <name>` builds its
// sides through the same shared AcquisitionOptions vehicle.
func TestCLIDiffAppBothSidesShareOCICredentials(t *testing.T) {
	root := t.TempDir()
	repo, wt := initCLIGitRepo(t, root)
	writeOCIAppForCLI(t, root, "demo", "oci://registry.example.test/org/app", ".", "v1")
	commitCLIGitRepo(t, repo, wt, "baseline")
	checkoutCLIGitBranch(t, wt, "feature")
	writeOCIAppForCLI(t, root, "demo", "oci://registry.example.test/org/app", ".", "v2")
	commitCLIGitRepo(t, repo, wt, "feature")

	ociAcquirer := &recordingCLIOCIAcquirer{
		digests: map[string]string{
			"v1": "sha256:" + strings.Repeat("ab", 32),
			"v2": "sha256:" + strings.Repeat("cd", 32),
		},
		files: map[string]string{"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: oci\ndata: {}\n"},
	}
	runCLIWithDependencies(t, Dependencies{
		Orchestrator: app.Orchestrator{OCIArtifactAcquirer: ociAcquirer},
	},
		"diff", "app", "demo",
		"--repo", root,
		"--ref-orig", "master",
		"--ref", "feature",
		"--exit-code=false",
		"--oci-username", "oci-user",
		"--oci-password", "oci-pass",
	)

	recorded := ociAcquirer.recorded()
	if len(recorded) < 4 {
		t.Fatalf("recorded %d acquirer calls, want at least 4 (both sides resolve+extract)", len(recorded))
	}
	want := ociartifact.Credentials{Username: "oci-user", Password: "oci-pass"}
	for i, opts := range recorded {
		if opts.Credentials != want {
			t.Fatalf("acquirer call %d credentials = %#v, want %#v (both diff-app sides must share the flag credentials)", i, opts.Credentials, want)
		}
	}
}

// TestCLIBuildOCIChartShapeWarnsAndRenders drives the reporter's shape through
// the full CLI and the REAL acquirer: the artifact renders with the
// helm.valuesObject override on stdout, stderr carries the advisory exactly
// once, and --strict changes nothing (exit 0, same output, same advisory).
func TestCLIBuildOCIChartShapeWarnsAndRenders(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/app-template", "1.2.3", ocitest.HelmChartSpec{Name: "app-template", Version: "1.2.3"})
	root := t.TempDir()
	writeOCIChartAppForCLI(t, root, "demo", reg.RepoURL("charts/app-template"), "app-template", "1.2.3", "    helm:\n      valuesObject:\n        message: cli-values-object\n")
	ociCacheDir := t.TempDir()

	for _, extra := range [][]string{nil, {"--strict"}} {
		result := runCLI(t, append([]string{"build", "apps", "--path", root, "--oci-cache-dir", ociCacheDir}, extra...)...)
		assertStdoutContainsAll(t, result, "demo-demo", "cli-values-object")
		if got := strings.Count(result.Stderr, "source.oci-chart-ignored"); got != 1 {
			t.Fatalf("args %v: stderr mentions source.oci-chart-ignored %d times, want exactly 1:\n%s", extra, got, result.Stderr)
		}
	}
}

// TestCLIDiffOCIChartShapeWarnsOncePerSide pins the per-side advisory count on
// the diff surface: two sides, one Application each, exactly two mentions. The
// sides differ in a helm.valuesObject entry so the default changed-only
// selection renders the Application on both sides (identical trees select
// nothing and render nothing).
func TestCLIDiffOCIChartShapeWarnsOncePerSide(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/app-template", "1.2.3", ocitest.HelmChartSpec{Name: "app-template", Version: "1.2.3"})
	left := t.TempDir()
	right := t.TempDir()
	for side, message := range map[string]string{left: "left-values", right: "right-values"} {
		writeOCIChartAppForCLI(t, side, "demo", reg.RepoURL("charts/app-template"), "app-template", "1.2.3", "    helm:\n      valuesObject:\n        message: "+message+"\n")
	}

	result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--oci-cache-dir", t.TempDir(), "--exit-code=false")
	assertStdoutContainsAll(t, result, "left-values", "right-values")
	if got := strings.Count(result.Stderr, "source.oci-chart-ignored"); got != 2 {
		t.Fatalf("stderr mentions source.oci-chart-ignored %d times, want exactly one per side:\nstderr:\n%s\nstdout:\n%s", got, result.Stderr, result.Stdout)
	}
}

// TestCLIDiagRenderJSONSurfacesOCIChartIgnored pins the structured surface:
// diag --render -o json reports the advisory with its stable code, warning
// severity and source category, and nothing reaches stderr.
func TestCLIDiagRenderJSONSurfacesOCIChartIgnored(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/app-template", "1.2.3", ocitest.HelmChartSpec{Name: "app-template", Version: "1.2.3"})
	root := t.TempDir()
	writeOCIChartAppForCLI(t, root, "demo", reg.RepoURL("charts/app-template"), "app-template", "1.2.3", "")

	result := runCLI(t, "diag", "--path", root, "--render", "-o", "json", "--oci-cache-dir", t.TempDir())
	if result.Stderr != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", result.Stderr)
	}
	var report struct {
		Diagnostics []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Category string `json:"category"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, result.Stdout)
	}
	var found []string
	for _, diag := range report.Diagnostics {
		if diag.Code != "source.oci-chart-ignored" {
			continue
		}
		if diag.Severity != "warning" || diag.Category != "source" {
			t.Fatalf("diagnostic = %#v, want severity warning in category source", diag)
		}
		if !strings.Contains(diag.Message, "path: .") {
			t.Fatalf("diagnostic message = %q, want the path: . remedy", diag.Message)
		}
		found = append(found, diag.Message)
	}
	if len(found) != 1 {
		t.Fatalf("source.oci-chart-ignored diagnostics = %d, want exactly 1:\n%s", len(found), result.Stdout)
	}
}
