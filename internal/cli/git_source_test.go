package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestBuildAppsRendersRepoMappedGitSource(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeExternalCLIApplication(t, root, "https://github.com/example/external", "manifests/external")
	writeCLITestFile(t, filepath.Join(external, "manifests", "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: repo-map
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root, "--repo-map", "https://github.com/example/external.git=" + external})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "source: repo-map") {
		t.Fatalf("stdout missing mapped manifest:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiagUsesRepoMappedGitSource(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeExternalCLIApplication(t, root, "https://github.com/example/external", "manifests/external")
	writeCLITestFile(t, filepath.Join(external, "manifests", "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: repo-map
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--repo-map", "https://github.com/example/external.git=" + external})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := stdout.String(), "No diagnostics found.\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestBuildAppsFetchesGitSourceByDefault(t *testing.T) {
	root := t.TempDir()
	cacheDir := t.TempDir()
	remote := createCLIGitRepository(t)
	writeCLIGitFile(t, remote, "manifests/external/cm.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: fetched
`)
	writeExternalCLIApplication(t, root, "file://"+filepath.ToSlash(remote), "manifests/external")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root, "--git-cache-dir", cacheDir})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "source: fetched") {
		t.Fatalf("stdout missing fetched manifest:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiagFetchesGitSourceByDefault(t *testing.T) {
	root := t.TempDir()
	cacheDir := t.TempDir()
	remote := createCLIGitRepository(t)
	writeCLIGitFile(t, remote, "manifests/external/cm.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: fetched
`)
	writeExternalCLIApplication(t, root, "file://"+filepath.ToSlash(remote), "manifests/external")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--git-cache-dir", cacheDir})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "No diagnostics found.\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func writeExternalCLIApplication(t *testing.T, root, repoURL, sourcePath string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: HEAD
    path: `+sourcePath+`
  destination:
    name: in-cluster
    namespace: default
`)
}

func createCLIGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	return root
}

func writeCLIGitFile(t *testing.T, root, name, content string) {
	t.Helper()
	repo, err := git.PlainOpen(root)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatalf("Worktree.Add() error = %v", err)
	}
	if _, err := worktree.Commit("add "+name, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Unix(1, 0),
		},
	}); err != nil {
		t.Fatalf("Worktree.Commit() error = %v", err)
	}
}
