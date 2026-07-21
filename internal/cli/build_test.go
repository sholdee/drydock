package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func TestBuildAppsRendersManifests(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", filepath.Join("..", "..", "testdata", "applications", "e2e")})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"---\n", "kind: ConfigMap", "name: demo", "version: v1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("build apps output missing %q:\n%s", want, got)
		}
	}
}

func TestBuildAppsSkipSecretsOmitsSecretManifests(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "kept")
	writeCLITestFile(t, filepath.Join(root, "manifests", "demo", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: demo
stringData:
  password: secret
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root, "--skip-secrets"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "kind: ConfigMap") || !strings.Contains(stdout.String(), "value: kept") {
		t.Fatalf("stdout missing kept ConfigMap:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "kind: Secret") || strings.Contains(stdout.String(), "password") {
		t.Fatalf("stdout included filtered Secret:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestBuildAppsAVPCompatibilityFlagReplacesRenderedManifestPlaceholders(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "kept")
	writeCLITestFile(t, filepath.Join(root, "manifests", "demo", "secret-ref.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: secret-ref
data:
  domain: argocd.<path:vaults/Kubernetes/items/cluster#domain>
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root, "--enable-avp-compat"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "argocd.drydock-redacted-") {
		t.Fatalf("stdout missing redacted AVP value:\n%s", stdout.String())
	}
	for _, forbidden := range []string{"vaults", "Kubernetes", "cluster", "<path:"} {
		if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("output leaked placeholder material %q\nstdout:\n%s\nstderr:\n%s", forbidden, stdout.String(), stderr.String())
		}
	}
	for _, want := range []string{"warning plugin:", "argocd-vault-plugin placeholders were replaced with deterministic redacted values"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want AVP compatibility diagnostic fragment %q", stderr.String(), want)
		}
	}
}

func TestBuildAppsPrintsUnsupportedApplicationSetDiagnosticToStderr(t *testing.T) {
	root := t.TempDir()
	writeCLITestFile(t, filepath.Join(root, "direct.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: direct
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/direct
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "direct", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: direct
  namespace: default
data:
  key: value
`)
	writeCLITestFile(t, filepath.Join(root, "unsupported-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
  namespace: argocd
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
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root})
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"---\n", "kind: ConfigMap", "name: direct", "key: value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("build apps stdout missing %q:\n%s", want, got)
		}
	}
	wantStderr := "warning appset: unsupported ApplicationSet generator; supported generators are git directories, git files, list, matrix, and merge (path: unsupported-appset.yaml, pointer: spec.generators)\n"
	if got := stderr.String(); got != wantStderr {
		t.Fatalf("build apps stderr = %q, want %q", got, wantStderr)
	}
}

func TestBuildAppRendersOnlyNamedApplication(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "old")
	writeNamedCLIApplication(t, root, "other", "other", "skip")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "app", "demo", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{"kind: ConfigMap", "name: demo", "value: old"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "skip") || strings.Contains(stdout.String(), "other") {
		t.Fatalf("stdout included non-selected app:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestBuildAppReportsMissingApplication(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "old")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "app", "missing", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want missing app error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found`) {
		t.Fatalf("error = %v, want missing app message", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestBuildAppsSuppressesPartialStdoutWhenOutputWouldBeInvalid(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeFailingCLIApplication(t, root, "broken")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty partial output on build error", stdout.String())
	}
	if !strings.Contains(stderr.String(), "error render:") {
		t.Fatalf("stderr = %q, want render diagnostic", stderr.String())
	}
}

func TestBuildAppsFailsClosedForPluginSource(t *testing.T) {
	for _, shape := range []string{"directory", "kustomize", "helm"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			writePluginCLIApplication(t, root, "cue", shape)

			cmd := NewRootCommand(VersionInfo{})
			cmd.SetArgs([]string{"build", "apps", "--path", root})
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			err := cmd.Execute()
			if code := commandErrorCode(err); code != 2 {
				t.Fatalf("error code = %d, want 2; err = %v", code, err)
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty partial output", stdout.String())
			}
			for _, want := range []string{
				"error plugin:",
				"config management plugin cue is not supported by the default renderer",
				"no compatible native renderer",
			} {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("stderr = %q, want %q", stderr.String(), want)
				}
			}
			for _, unwanted := range []string{"should-not-render", "kind: ConfigMap", "kind: Deployment"} {
				if strings.Contains(stdout.String(), unwanted) {
					t.Fatalf("stdout rendered plugin source through %s fallback:\n%s", shape, stdout.String())
				}
			}
		})
	}
}

func TestBuildAppsPassesAuthenticatedSourceFlags(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	registryConfig := filepath.Join(t.TempDir(), "registry.json")
	if err := writeCLIFile(registryConfig, `{"auths":{}}`); err != nil {
		t.Fatalf("write registry config: %v", err)
	}
	writeExternalCLIApplication(t, root, "https://github.com/example/private", "manifests/external")
	writeCLITestFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(external, "manifests", "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: fetched
`)
	writeCLITestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeCLITestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: chart
data:
  source: chart
`)
	writeOCIAppForCLI(t, root, "oci-demo", "oci://registry.example.test/org/app", ".", "v1")
	ociCertFile, ociKeyFile := ocitest.GenerateClientCertFiles(t)
	ociCAFile, _ := ocitest.GenerateClientCertFiles(t)
	gitAcquirer := &recordingCLIGitAcquirer{path: external}
	chartAcquirer := &recordingCLIChartAcquirer{chartDir: chartDir}
	ociAcquirer := &recordingCLIOCIAcquirer{
		digests: map[string]string{"v1": "sha256:" + strings.Repeat("ab", 32)},
		files:   map[string]string{"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: oci\ndata: {}\n"},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{
			GitAcquirer:         gitAcquirer,
			ChartAcquirer:       chartAcquirer,
			OCIArtifactAcquirer: ociAcquirer,
		},
	})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", root,
		"--git-cache-dir", t.TempDir(),
		"--git-username", "git-user",
		"--git-password", "git-pass",
		"--git-bearer-token", "git-token",
		"--helm-username", "helm-user",
		"--helm-password", "helm-pass",
		"--helm-bearer-token", "helm-token",
		"--registry-config", registryConfig,
		"--oci-username", "oci-user",
		"--oci-password", "oci-pass",
		"--oci-ca-file", ociCAFile,
		"--oci-client-cert-file", ociCertFile,
		"--oci-client-key-file", ociKeyFile,
		"--oci-insecure-skip-verify",
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if len(gitAcquirer.options) != 1 {
		t.Fatalf("git options = %d, want 1", len(gitAcquirer.options))
	}
	if got := gitAcquirer.options[0].Credentials; got.Username != "git-user" || got.Password != "git-pass" || got.BearerToken != "git-token" {
		t.Fatalf("git credentials = %#v", got)
	}
	if len(chartAcquirer.options) != 1 {
		t.Fatalf("chart options = %d, want 1", len(chartAcquirer.options))
	}
	if got := chartAcquirer.options[0].Credentials; got.Username != "helm-user" || got.Password != "helm-pass" || got.BearerToken != "helm-token" || got.RegistryConfig != registryConfig {
		t.Fatalf("chart credentials = %#v, want registry config %q", got, registryConfig)
	}
	wantOCICredentials := ociartifact.Credentials{
		Username:           "oci-user",
		Password:           "oci-pass",
		CAFile:             ociCAFile,
		ClientCertFile:     ociCertFile,
		ClientKeyFile:      ociKeyFile,
		InsecureSkipVerify: true,
	}
	recordedOCIOptions := ociAcquirer.recorded()
	if len(recordedOCIOptions) == 0 {
		t.Fatal("oci acquirer never invoked")
	}
	for i, opts := range recordedOCIOptions {
		if opts.Credentials != wantOCICredentials {
			t.Fatalf("oci credentials[%d] = %#v, want %#v", i, opts.Credentials, wantOCICredentials)
		}
	}
}

// TestBuildAppsRedactsOCICredentialFlagValuesFromErrors mirrors the chart
// pin below: an acquisition error echoing the OCI credential flag values —
// raw AND in the base64(user:pass) Basic-auth form that registry error
// bodies echo back — must reach neither the command error nor stderr.
func TestBuildAppsRedactsOCICredentialFlagValuesFromErrors(t *testing.T) {
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "oci-demo", "oci://registry.example.test/org/app", ".", "v1")
	basicForm := base64.StdEncoding.EncodeToString([]byte("oci-user:oci-pass"))
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{
			OCIArtifactAcquirer: &recordingCLIOCIAcquirer{err: errors.New("boom oci-user oci-pass " + basicForm)},
		},
	})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", root,
		"--oci-username", "oci-user",
		"--oci-password", "oci-pass",
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want oci acquisition error")
	}
	for _, leaked := range []string{"oci-pass", basicForm} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(stderr.String(), leaked) || strings.Contains(stdout.String(), leaked) {
			t.Fatalf("error/output leaked %q: err=%q stderr=%q", leaked, err, stderr.String())
		}
	}
}

func TestBuildAppsRedactsChartCredentialFlagValuesFromErrors(t *testing.T) {
	root := t.TempDir()
	writeCLITestFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: default
`)
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{
			ChartAcquirer: &recordingCLIChartAcquirer{err: errors.New("boom helm-pass helm-token")},
		},
	})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", root,
		"--helm-password", "helm-pass",
		"--helm-bearer-token", "helm-token",
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want chart error")
	}
	for _, leaked := range []string{"helm-pass", "helm-token"} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(stderr.String(), leaked) {
			t.Fatalf("error/stderr leaked %q: err=%q stderr=%q", leaked, err, stderr.String())
		}
	}
}

type recordingCLIGitAcquirer struct {
	path     string
	err      error
	requests []sourcepkg.GitRequest
	options  []sourcepkg.GitOptions
}

func (acquirer *recordingCLIGitAcquirer) Acquire(_ context.Context, request sourcepkg.GitRequest, opts sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return sourcepkg.GitResult{}, acquirer.err
	}
	return sourcepkg.GitResult{Path: acquirer.path, Revision: "abc123"}, nil
}

// recordingCLIOCIAcquirer records the ociartifact.Options handed to every
// Resolve and Extract call. Guarded by a mutex: diff sides can invoke it
// concurrently.
type recordingCLIOCIAcquirer struct {
	mu      sync.Mutex
	digests map[string]string
	files   map[string]string
	err     error
	options []ociartifact.Options
}

func (acquirer *recordingCLIOCIAcquirer) record(opts ociartifact.Options) {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	acquirer.options = append(acquirer.options, opts)
}

func (acquirer *recordingCLIOCIAcquirer) recorded() []ociartifact.Options {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return append([]ociartifact.Options(nil), acquirer.options...)
}

func (acquirer *recordingCLIOCIAcquirer) Resolve(_ context.Context, _, revision string, opts ociartifact.Options) (string, error) {
	acquirer.record(opts)
	if acquirer.err != nil {
		return "", acquirer.err
	}
	if digest, ok := acquirer.digests[revision]; ok {
		return digest, nil
	}
	return "sha256:" + strings.Repeat("cd", 32), nil
}

func (acquirer *recordingCLIOCIAcquirer) Extract(_ context.Context, _, _ string, opts ociartifact.Options) (string, func(), error) {
	acquirer.record(opts)
	if acquirer.err != nil {
		return "", nil, acquirer.err
	}
	dir, err := os.MkdirTemp("", "drydock-cli-oci-*")
	if err != nil {
		return "", nil, err
	}
	for name, data := range acquirer.files {
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

type recordingCLIChartAcquirer struct {
	chartDir string
	err      error
	requests []chart.Request
	options  []chart.Options
}

func (acquirer *recordingCLIChartAcquirer) Acquire(_ context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return chart.Result{}, acquirer.err
	}
	return chart.Result{ChartDir: acquirer.chartDir, Repository: request.Repository, Name: request.Name, Version: request.Version, Kind: request.Kind}, nil
}

func TestBuildAppsAPIVersionsFlagGatesCapabilityResources(t *testing.T) {
	root := t.TempDir()

	// Application pointing at a local Helm chart at manifests/cap-demo in the same repo root.
	writeCLITestFile(t, filepath.Join(root, "apps", "cap-demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cap-demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/cap-demo
    targetRevision: main
    helm:
      releaseName: cap-demo
  destination:
    name: in-cluster
    namespace: default
`)

	// Minimal Helm chart whose template emits a ServiceMonitor only when
	// the monitoring.coreos.com/v1 API version is available.
	chartDir := filepath.Join(root, "manifests", "cap-demo")
	writeCLITestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: cap-demo
version: 0.1.0
`)
	writeCLITestFile(t, filepath.Join(chartDir, "templates", "always.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: always-present
data:
  value: present
`)
	writeCLITestFile(t, filepath.Join(chartDir, "templates", "gated.yaml"), `{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: gated-monitor
spec:
  selector: {}
  endpoints: []
{{- end }}
`)

	t.Run("with api-versions flag resource appears", func(t *testing.T) {
		cmd := NewRootCommand(VersionInfo{})
		cmd.SetArgs([]string{
			"build", "apps",
			"--path", root,
			"--api-versions", "monitoring.coreos.com/v1",
		})
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)

		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "kind: ServiceMonitor") {
			t.Fatalf("stdout missing gated ServiceMonitor with --api-versions set:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "kind: ConfigMap") {
			t.Fatalf("stdout missing always-present ConfigMap:\n%s", stdout.String())
		}
	})

	t.Run("without api-versions flag resource is absent", func(t *testing.T) {
		cmd := NewRootCommand(VersionInfo{})
		cmd.SetArgs([]string{
			"build", "apps",
			"--path", root,
		})
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)

		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "kind: ServiceMonitor") {
			t.Fatalf("stdout included gated ServiceMonitor without --api-versions:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "kind: ConfigMap") {
			t.Fatalf("stdout missing always-present ConfigMap:\n%s", stdout.String())
		}
	})
}

func writeCLIFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
