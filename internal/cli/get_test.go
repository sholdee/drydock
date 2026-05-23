package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetAppsPrintsApplicationNames(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", filepath.Join("..", "..", "testdata", "applications", "e2e")})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got, want := out.String(), "demo\n"; got != want {
		t.Fatalf("get apps output = %q, want %q", got, want)
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

	if got, want := out.String(), "missing-render-path\n"; got != want {
		t.Fatalf("get apps output = %q, want %q", got, want)
	}
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
    - list:
        elements:
          - name: generated
  template:
    metadata:
      name: '{{name}}'
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
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := out.String(), "direct\n"; got != want {
		t.Fatalf("get apps output = %q, want %q", got, want)
	}

	cmd = NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", root, "--strict"})
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("Execute() error = nil, want unsupported ApplicationSet error")
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("Execute() error = %q, want unsupported ApplicationSet generator", err.Error())
	}
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
