package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagCleanRepositoryPrintsNoManifests(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiagPrintsUnsupportedApplicationSetWarning(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "warning appset: unsupported ApplicationSet generator; supported generators are git directories, git files, list, and matrix (path: unsupported-appset.yaml, pointer: spec.generators)\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestDiagStrictUnsupportedApplicationSetErrors(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--strict"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want strict diagnostic error")
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("error = %v, want unsupported ApplicationSet generator", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "error appset:") {
		t.Fatalf("stderr = %q, want error appset diagnostic", stderr.String())
	}
}

func TestDiagRejectsInvalidRepoMap(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--repo-map", "https://github.com/example/repo"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want repo-map error")
	}
	if !strings.Contains(err.Error(), "must use URL=PATH") {
		t.Fatalf("error = %v, want repo-map parse error", err)
	}
}

func writeUnsupportedApplicationSetForCLI(t *testing.T, root string) {
	t.Helper()
	writeSimpleAppForCLI(t, root, "ok")
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
}
