package cli

import (
	"errors"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/app"
)

// initSelfRepoCLICheckout creates a checkout whose origin remote names the
// repository the specs reference and whose origin/HEAD symref names "main" —
// the state actions/checkout plus the pr-action symref write leave behind.
func initSelfRepoCLICheckout(t *testing.T) string {
	t.Helper()
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
	return root
}

// writeSelfRepoCLIFixture writes the issue #206 repro shape: a parent
// Application whose local helm chart source consumes a $repo value file from
// the repository's own URL at the default-branch NAME, templating a child
// Application whose name exists only in the local tree's value file.
func writeSelfRepoCLIFixture(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "parent.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: parent
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: "main"
      ref: repo
    - repoURL: https://github.com/example/repo
      targetRevision: HEAD
      path: bootstrap-chart
      helm:
        valueFiles:
          - $repo/values/parent.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(root, "bootstrap-chart", "Chart.yaml"), `apiVersion: v2
name: bootstrap
version: 0.1.0
`)
	writeCLITestFile(t, filepath.Join(root, "bootstrap-chart", "values.yaml"), `childName: default-child
`)
	writeCLITestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "child.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: {{ .Values.childName }}
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: HEAD
    path: manifests/child
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(root, "values", "parent.yaml"), `childName: local-only-child
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "child", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: child-config
  namespace: default
data:
  value: child
`)
}

// test apps must pass on the repro-shaped checkout WITHOUT --repo-map: the
// self-referential $repo values ref resolves to the local tree.
func TestTestAppsResolvesSelfRepoRefsWithoutRepoMap(t *testing.T) {
	root := initSelfRepoCLICheckout(t)
	writeSelfRepoCLIFixture(t, root)

	gitAcquirer := &recordingCLIGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result := runCLIWithDependencies(t, Dependencies{
		Orchestrator: app.Orchestrator{GitAcquirer: gitAcquirer},
	}, "test", "apps", "--path", root)
	assertStdoutContainsAll(t, result, "parent", "local-only-child", "PASS")
	assertStdoutExcludesAll(t, result, "FAIL")
	if len(gitAcquirer.requests) != 0 {
		t.Fatalf("git acquire requests = %#v, want none", gitAcquirer.requests)
	}
}

// get apps must list the child Application discovered through the local
// $repo values WITHOUT --repo-map.
func TestGetAppsListsSelfRepoAppOfAppsChildWithoutRepoMap(t *testing.T) {
	root := initSelfRepoCLICheckout(t)
	writeSelfRepoCLIFixture(t, root)

	gitAcquirer := &recordingCLIGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result := runCLIWithDependencies(t, Dependencies{
		Orchestrator: app.Orchestrator{GitAcquirer: gitAcquirer},
	}, "get", "apps", "--path", root)
	assertStdoutContainsAll(t, result, "parent", "local-only-child")
	if len(gitAcquirer.requests) != 0 {
		t.Fatalf("git acquire requests = %#v, want none", gitAcquirer.requests)
	}
}
