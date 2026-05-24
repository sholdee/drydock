package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppSetProviderFixtureFlagExpandsGetApps(t *testing.T) {
	root, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "--appset-provider-fixture", fixture, "-o", "name"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "argocd/prod-a\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestAppSetProviderFixtureFlagExpandsGetImages(t *testing.T) {
	root, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "images", "--path", root, "--appset-provider-fixture", fixture, "-o", "name"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "ghcr.io/example/demo:1.0.0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAppSetProviderFixtureFlagExpandsBuildApps(t *testing.T) {
	root, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root, "--appset-provider-fixture", fixture})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{"kind: ConfigMap", "name: prod-a-config", "value: old"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestAppSetProviderFixtureFlagExpandsBuildApp(t *testing.T) {
	root, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "app", "prod-a", "--path", root, "--appset-provider-fixture", fixture})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "value: old") {
		t.Fatalf("stdout = %q, want rendered named provider app", stdout.String())
	}
}

func TestAppSetProviderFixtureFlagExpandsTestApps(t *testing.T) {
	root, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--appset-provider-fixture", fixture})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "PASS argocd/prod-a\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAppSetProviderFixtureFlagExpandsTestApp(t *testing.T) {
	root, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "app", "prod-a", "--path", root, "--appset-provider-fixture", fixture})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "PASS argocd/prod-a\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAppSetProviderFixtureFlagExpandsDiag(t *testing.T) {
	root, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--appset-provider-fixture", fixture, "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var report struct {
		Diagnostics []map[string]any `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", report.Diagnostics)
	}
}

func TestAppSetProviderFixtureFlagExpandsDiffApps(t *testing.T) {
	left, fixture := writeProviderBackedCLIRepo(t, "old", "ghcr.io/example/demo:1.0.0")
	right, _ := writeProviderBackedCLIRepo(t, "new", "ghcr.io/example/demo:1.0.0")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "--changed-only=false", "--exit-code=false", "--appset-provider-fixture", fixture})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{"-  value: old", "+  value: new"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestAppSetProviderFixtureInvalidPathDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeProviderBackedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--appset-provider-fixture", "https://example.invalid/providers.yaml", "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want invalid fixture path error")
	}
	var report struct {
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	if unmarshalErr := json.Unmarshal(stdout.Bytes(), &report); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", unmarshalErr, stdout.String())
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "appset.provider-fixture-invalid" {
		t.Fatalf("diagnostics = %#v, want provider fixture invalid", report.Diagnostics)
	}
}

func TestAppSetProviderFixtureNoMatchStrictFails(t *testing.T) {
	root := t.TempDir()
	writeProviderBackedApplicationSetForCLI(t, root)
	fixture := filepath.Join(t.TempDir(), "providers.yaml")
	writeCLITestFile(t, fixture, "clusters: []\n")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--appset-provider-fixture", fixture, "--strict", "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want strict provider no-match error")
	}
	var report struct {
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	if unmarshalErr := json.Unmarshal(stdout.Bytes(), &report); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", unmarshalErr, stdout.String())
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "appset.provider-no-match" {
		t.Fatalf("diagnostics = %#v, want provider no-match", report.Diagnostics)
	}
}

func writeProviderBackedCLIRepo(t *testing.T, value, image string) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeProviderBackedApplicationSetForCLI(t, root)
	writeCLITestFile(t, filepath.Join(root, "manifests", "prod-a", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: prod-a-config
data:
  value: `+value+`
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "prod-a", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: prod-a
spec:
  selector:
    matchLabels:
      app: prod-a
  template:
    metadata:
      labels:
        app: prod-a
    spec:
      containers:
        - name: app
          image: `+image+`
`)
	fixture := filepath.Join(t.TempDir(), "providers.yaml")
	writeCLITestFile(t, fixture, `clusters:
  - name: prod-a
    server: https://prod-a.example.invalid
`)
	return root, fixture
}

func writeProviderBackedApplicationSetForCLI(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: provider-backed
  namespace: argocd
spec:
  generators:
    - clusters: {}
  template:
    metadata:
      name: '{{name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: manifests/{{name}}
        targetRevision: main
      destination:
        server: '{{server}}'
        namespace: default
`)
}
