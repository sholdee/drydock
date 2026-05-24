package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/home-operations/argocd-local/internal/chart"
	sourcepkg "github.com/home-operations/argocd-local/internal/source"
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
	wantStderr := "warning appset: unsupported ApplicationSet generator; supported generators are git directories, git files, list, and matrix (path: unsupported-appset.yaml, pointer: spec.generators)\n"
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
	gitAcquirer := &recordingCLIGitAcquirer{path: external}
	chartAcquirer := &recordingCLIChartAcquirer{chartDir: chartDir}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{
			GitAcquirer:   gitAcquirer,
			ChartAcquirer: chartAcquirer,
		},
	})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", root,
		"--allow-network",
		"--git-cache-dir", t.TempDir(),
		"--git-username", "git-user",
		"--git-password", "git-pass",
		"--git-bearer-token", "git-token",
		"--helm-username", "helm-user",
		"--helm-password", "helm-pass",
		"--helm-bearer-token", "helm-token",
		"--registry-config", registryConfig,
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

func writeCLIFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
