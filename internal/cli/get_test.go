package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetAppsDefaultOutputIsTable(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", filepath.Join("..", "..", "testdata", "applications", "e2e")})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	assertTableContainsApp(t, out.String(), "NAMESPACE", "NAME", "demo")
}

func TestGetAppsExplicitTableOutput(t *testing.T) {
	root := t.TempDir()
	writeLabeledAppForCLI(t, root, "demo", map[string]string{"app.kubernetes.io/name": "demo"})

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "table"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	assertTableContainsApp(t, out.String(), "NAMESPACE", "NAME", "demo")
}

func TestGetAppsNameOutput(t *testing.T) {
	root := t.TempDir()
	writeLabeledAppForCLI(t, root, "demo", map[string]string{"app.kubernetes.io/name": "demo"})

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got, want := out.String(), "argocd/demo\n"; got != want {
		t.Fatalf("get apps -o name output = %q, want %q", got, want)
	}
}

func TestGetAppsJSONOutput(t *testing.T) {
	root := t.TempDir()
	writeLabeledAppForCLI(t, root, "demo", map[string]string{"app.kubernetes.io/name": "demo"})

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var apps []map[string]any
	if err := json.Unmarshal(out.Bytes(), &apps); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput:\n%s", err, out.String())
	}
	if len(apps) != 1 || apps[0]["name"] != "demo" || apps[0]["namespace"] != "argocd" {
		t.Fatalf("apps = %#v, want argocd/demo", apps)
	}
}

func TestGetAppsYAMLOutput(t *testing.T) {
	root := t.TempDir()
	writeLabeledAppForCLI(t, root, "demo", map[string]string{"app.kubernetes.io/name": "demo"})

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "yaml"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, want := range []string{"---\n", "namespace: argocd\n", "name: demo\n", "project: default\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("get apps -o yaml output = %q, want %q", out.String(), want)
		}
	}
}

func TestGetAppsPrintsApplicationNamesWithoutRendering(t *testing.T) {
	root := t.TempDir()
	application := []byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: missing-render-path
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: does-not-exist
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	if err := os.WriteFile(filepath.Join(root, "app.yaml"), application, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	assertTableContainsApp(t, out.String(), "NAMESPACE", "NAME", "missing-render-path")
}

func TestGetAppsSkipsUnsupportedApplicationSetUnlessStrict(t *testing.T) {
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
	writeCLITestFile(t, filepath.Join(root, "unsupported-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
  namespace: argocd
spec:
  generators:
    - matrix:
        generators: []
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
	cmd.SetArgs([]string{"get", "apps", "--path", root})
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertTableContainsApp(t, out.String(), "NAMESPACE", "NAME", "direct")
	wantStderr := "warning appset: unsupported ApplicationSet generator; supported generators are git directories, git files, and list (path: unsupported-appset.yaml, pointer: spec.generators)\n"
	if got := stderr.String(); got != wantStderr {
		t.Fatalf("get apps stderr = %q, want %q", got, wantStderr)
	}

	cmd = NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "--strict"})
	out.Reset()
	stderr.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("Execute() error = nil, want unsupported ApplicationSet error")
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("Execute() error = %q, want unsupported ApplicationSet generator", err.Error())
	}
}

func TestGetAppsSelector(t *testing.T) {
	root := t.TempDir()
	writeLabeledAppForCLI(t, root, "demo", map[string]string{
		"app.kubernetes.io/name": "demo",
		"env":                    "prod",
		"tier":                   "api",
	})
	writeLabeledAppForCLI(t, root, "other", map[string]string{
		"app.kubernetes.io/name": "other",
		"env":                    "dev",
		"tier":                   "test",
	})

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name", "-l", "app.kubernetes.io/name=demo"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := out.String(), "argocd/demo\n"; got != want {
		t.Fatalf("selector output = %q, want %q", got, want)
	}
}

func TestGetAppsSelectorExpression(t *testing.T) {
	root := t.TempDir()
	writeLabeledAppForCLI(t, root, "prod", map[string]string{"env": "prod", "tier": "api"})
	writeLabeledAppForCLI(t, root, "stage", map[string]string{"env": "stage", "tier": "worker"})
	writeLabeledAppForCLI(t, root, "test", map[string]string{"env": "prod", "tier": "test"})
	writeLabeledAppForCLI(t, root, "dev", map[string]string{"env": "dev", "tier": "api"})

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "-o", "name", "-l", "env in (prod,stage),tier!=test"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := out.String(), "argocd/prod\nargocd/stage\n"; got != want {
		t.Fatalf("selector expression output = %q, want %q", got, want)
	}
}

func TestGetAppsInvalidSelectorReturnsError(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "-l", "env in ("})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if !strings.Contains(err.Error(), "invalid selector") {
		t.Fatalf("error = %v, want invalid selector", err)
	}
	if strings.Contains(out.String(), "Usage:") {
		t.Fatalf("output contains usage spam:\n%s", out.String())
	}
}

func TestGetAppsInvalidOutputReturnsError(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "-o", "wide"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if !strings.Contains(err.Error(), "unsupported output") {
		t.Fatalf("error = %v, want unsupported output", err)
	}
	if strings.Contains(out.String(), "Usage:") {
		t.Fatalf("output contains usage spam:\n%s", out.String())
	}
}

func TestGetAppsRejectsDiffOutput(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "-o", "diff"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if !strings.Contains(err.Error(), "diff output is only supported for diff commands") {
		t.Fatalf("error = %v, want diff-only message", err)
	}
}

func TestGetImagesNameOutput(t *testing.T) {
	root := t.TempDir()
	writeImageAppForCLI(t, root, "ghcr.io/example/demo:v1")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "images", "--path", root, "-o", "name"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := out.String(), "ghcr.io/example/demo:v1\n"; got != want {
		t.Fatalf("get images -o name output = %q, want %q", got, want)
	}
}

func TestGetImagesDefaultOutputIsTable(t *testing.T) {
	root := t.TempDir()
	writeImageAppForCLI(t, root, "ghcr.io/example/demo:v1")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "images", "--path", root})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertTableContainsApp(t, out.String(), "IMAGE", "ghcr.io/example/demo:v1")
}

func TestGetImagesJSONOutput(t *testing.T) {
	root := t.TempDir()
	writeImageAppForCLI(t, root, "ghcr.io/example/demo:v1")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "images", "--path", root, "-o", "json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var images []map[string]any
	if err := json.Unmarshal(out.Bytes(), &images); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput:\n%s", err, out.String())
	}
	if len(images) != 1 || images[0]["image"] != "ghcr.io/example/demo:v1" {
		t.Fatalf("images = %#v, want ghcr.io/example/demo:v1", images)
	}
}

func TestGetImagesYAMLOutput(t *testing.T) {
	root := t.TempDir()
	writeImageAppForCLI(t, root, "ghcr.io/example/demo:v1")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "images", "--path", root, "-o", "yaml"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"---\n", "image: ghcr.io/example/demo:v1\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("get images -o yaml output = %q, want %q", out.String(), want)
		}
	}
}

func TestGetImagesRejectsDiffOutput(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "images", "-o", "diff"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if !strings.Contains(err.Error(), "diff output is only supported for diff commands") {
		t.Fatalf("error = %v, want diff-only message", err)
	}
}

func assertTableContainsApp(t *testing.T, output string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("table output = %q, want %q", output, want)
		}
	}
}

func commandErrorCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return 2
}

func writeCLITestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeLabeledAppForCLI(t *testing.T, root, appName string, labels map[string]string) {
	t.Helper()
	var labelLines strings.Builder
	if len(labels) > 0 {
		labelLines.WriteString("  labels:\n")
		for key, value := range labels {
			labelLines.WriteString("    " + key + ": " + value + "\n")
		}
	}
	writeCLITestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
`+labelLines.String()+`spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+appName+`
data:
  value: `+appName+`
`)
}
