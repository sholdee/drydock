package drydock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// Public-API mapping pin: self-repo resolution needs no new Config surface —
// the internal build/list leaves populate it from the checkout at Path, so a
// $repo values ref naming the checkout's own repository at the symref-derived
// default-branch name renders from the local tree through Render as well.
func TestRenderSelfRepoRefResolvesToLocalTree(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{"https://github.com/example/repo.git"}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.ReferenceName("refs/remotes/origin/HEAD"),
		plumbing.ReferenceName("refs/remotes/origin/main"),
	)); err != nil {
		t.Fatalf("SetReference(origin/HEAD) error = %v", err)
	}
	writeAPIFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: "main"
      ref: repo
    - repoURL: https://github.com/example/repo
      targetRevision: HEAD
      path: chart
      helm:
        valueFiles:
          - $repo/values/demo.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAPIFile(t, filepath.Join(root, "chart", "Chart.yaml"), `apiVersion: v2
name: demo
version: 0.1.0
`)
	writeAPIFile(t, filepath.Join(root, "chart", "values.yaml"), `value: default
`)
	writeAPIFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
`)
	writeAPIFile(t, filepath.Join(root, "values", "demo.yaml"), `value: from-local-tree
`)

	gitAcquirer := &recordingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := Render(context.Background(), Config{Path: root, GitAcquirer: gitAcquirer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want 1", len(result.Manifests))
	}
	data, ok := result.Manifests[0].Object["data"].(map[string]any)
	if !ok {
		t.Fatalf("Object[data] = %#v, want map", result.Manifests[0].Object["data"])
	}
	if got := data["value"]; got != "from-local-tree" {
		t.Fatalf("rendered value = %#v, want from-local-tree", got)
	}
	if len(gitAcquirer.requests) != 0 {
		t.Fatalf("git acquire requests = %#v, want none", gitAcquirer.requests)
	}
}
