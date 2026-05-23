package cli

import (
	"bytes"
	"os"
	"path/filepath"
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
