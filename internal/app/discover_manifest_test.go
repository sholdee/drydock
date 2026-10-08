package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// writeDiscoverManifestAppSet writes an ApplicationSet that lives outside the
// repository under diff and generates one Application whose directory source
// names that repository at its default-branch name.
func writeDiscoverManifestAppSet(t *testing.T, path string) {
	t.Helper()
	writeTestFile(t, path, `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: external
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - name: demo
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      source:
        repoURL: `+selfRepoSpecURL+`
        targetRevision: trunk
        path: manifests/{{name}}
      destination:
        name: in-cluster
        namespace: default
`)
}

func writeDiscoverManifestConfigMap(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
  namespace: default
data:
  value: `+value+`
`)
}

// initDiscoverManifestRepo commits a manifest-only repository (no Application
// or ApplicationSet) on master and a one-line change on feature. The remote
// HEAD symref names trunk, the ApplicationSet's targetRevision.
func initDiscoverManifestRepo(t *testing.T) string {
	t.Helper()
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	if err := fixture.repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.ReferenceName("refs/remotes/origin/HEAD"),
		plumbing.ReferenceName("refs/remotes/origin/trunk"),
	)); err != nil {
		t.Fatalf("SetReference(origin/HEAD) error = %v", err)
	}
	writeDiscoverManifestConfigMap(t, root, "old")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeDiscoverManifestConfigMap(t, root, "new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature value")
	return root
}

func TestDiffRefDiscoverManifestFindsExternalApplicationSet(t *testing.T) {
	root := initDiscoverManifestRepo(t)
	external := filepath.Join(t.TempDir(), "appset.yaml")
	writeDiscoverManifestAppSet(t, external)

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo source must not be acquired")}
	result, err := (Orchestrator{GitAcquirer: gitAcquirer}).DiffApps(context.Background(), DiffRequest{
		Repo:                  root,
		RefOrig:               "master",
		Ref:                   "feature",
		DiscoverManifestPaths: []string{external},
		ChangedOnly:           true,
		StrictChangedOnly:     true,
		Unified:               3,
		Parallelism:           1,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

func TestDiffRefIgnoresWorkingTreeAppSetWithoutDiscoverManifest(t *testing.T) {
	root := initDiscoverManifestRepo(t)
	// Untracked in the operator checkout: a --ref diff reads commit trees only.
	writeDiscoverManifestAppSet(t, filepath.Join(root, "appset.yaml"))

	result, err := (Orchestrator{GitAcquirer: &countingGitAcquirer{err: errors.New("unexpected acquire")}}).DiffApps(context.Background(), DiffRequest{
		Repo:        root,
		RefOrig:     "master",
		Ref:         "feature",
		Unified:     3,
		Parallelism: 1,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results = %#v, want none without --discover-manifest", result.Results)
	}
}

func TestDiffRefOrigWithPathOrigStillRejectedWithDiscoverManifest(t *testing.T) {
	external := filepath.Join(t.TempDir(), "appset.yaml")
	writeDiscoverManifestAppSet(t, external)
	_, err := (Orchestrator{}).DiffApps(context.Background(), DiffRequest{
		LeftPath:              t.TempDir(),
		RefOrig:               "master",
		DiscoverManifestPaths: []string{external},
	})
	if err == nil || err.Error() != "--ref-orig cannot be combined with --path-orig" {
		t.Fatalf("DiffApps() error = %v, want --ref-orig cannot be combined with --path-orig", err)
	}
}

// The external file's absolute path is not a repository path, so it must not
// mark the repository root as already discovered for an app-of-apps source.
func TestListApplicationsDiscoverManifestAppOfAppsRendersChildren(t *testing.T) {
	for _, tt := range []struct {
		name   string
		source string
	}{
		{name: "repository root", source: "path: .\n    directory:\n      recurse: true"},
		{name: "subdirectory", source: "path: apps"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "apps", "child.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/child
  destination:
    name: in-cluster
    namespace: child
`)
			external := filepath.Join(t.TempDir(), "parent.yaml")
			writeTestFile(t, external, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: parent
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    `+tt.source+`
  destination:
    name: in-cluster
    namespace: argocd
`)

			result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
				Path:                  root,
				DiscoverIgnoreGlobs:   []string{"apps/**"},
				DiscoverManifestPaths: []string{external},
			})
			if err != nil {
				t.Fatalf("ListApplications() error = %v", err)
			}
			if names := applicationNames(result.Applications); strings.Join(names, ",") != "child,parent" {
				t.Fatalf("Applications = %#v, want child and parent", names)
			}
		})
	}
}

func TestLoadDiscoverManifestsRejectsUnsafeOrInvalidInput(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "appset.yaml")
	writeDiscoverManifestAppSet(t, valid)
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	configMap := filepath.Join(dir, "configmap.yaml")
	writeTestFile(t, configMap, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")
	empty := filepath.Join(dir, "empty.yaml")
	writeTestFile(t, empty, "")
	mixed := filepath.Join(dir, "mixed.yaml")
	writeTestFile(t, mixed, "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: a\n---\napiVersion: argoproj.io/v1alpha1\nkind: AppProject\nmetadata:\n  name: p\n")

	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: filepath.Join(dir, "missing.yaml"), want: "does not exist"},
		{name: "dotdot", path: filepath.Join(dir, "sub") + "/../appset.yaml", want: "must not contain .. components"},
		{name: "symlink", path: link, want: "is a symlink"},
		{name: "directory", path: dir, want: "must be a regular file"},
		{name: "wrong kind", path: configMap, want: "only argoproj.io/v1alpha1 Application and ApplicationSet"},
		{name: "empty", path: empty, want: "contains no Application or ApplicationSet"},
		{name: "mixed kinds", path: mixed, want: "AppProject"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDiscoverManifests([]string{tt.path})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadDiscoverManifests() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadDiscoverManifestsMultiDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argo.yaml")
	writeTestFile(t, path, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: direct
  namespace: argocd
spec:
  source:
    repoURL: `+selfRepoSpecURL+`
    path: manifests/demo
  destination:
    name: in-cluster
---
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: generated
  namespace: argocd
spec:
  generators:
    - list:
        elements: []
`)
	result, err := loadDiscoverManifests([]string{path, path})
	if err != nil {
		t.Fatalf("loadDiscoverManifests() error = %v", err)
	}
	if len(result.Applications) != 1 || len(result.ApplicationSets) != 1 {
		t.Fatalf("result = %d Applications, %d ApplicationSets; want 1 and 1", len(result.Applications), len(result.ApplicationSets))
	}
	if paths := discoveredApplicationInputPaths(result.Applications[0]); len(paths) != 0 {
		t.Fatalf("input paths = %#v, want none: the external file owns no repository path", paths)
	}
}
