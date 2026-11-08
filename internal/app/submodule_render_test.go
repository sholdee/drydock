package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

const submoduleTestRepoURL = "https://git.example.test/org/parent.git"

// submoduleApplicationRepo returns a clone of the submodule fixture whose
// origin is submoduleTestRepoURL, with an Application that renders a path
// in the submodule. initialized clones with the submodules checked out.
// It returns the clone and the commit before the Application.
func submoduleApplicationRepo(t *testing.T, initialized bool) (string, string) {
	return submoduleApplicationRepoAt(t, initialized, "mods/child/child/app")
}

// submoduleApplicationRepoAt is submoduleApplicationRepo with the source
// path sourcePath.
func submoduleApplicationRepoAt(t *testing.T, initialized bool, sourcePath string) (string, string) {
	t.Helper()
	fixture := submoduletest.New(t)
	root := fixture.Work
	if !initialized {
		root = filepath.Join(fixture.Dir, "plain")
		submoduletest.Run(t, fixture.Dir, "clone", "-q", filepath.Join(fixture.Dir, "parent.git"), root)
	}
	submoduletest.Run(t, root, "remote", "set-url", "origin", submoduleTestRepoURL)
	base := submoduletest.Run(t, root, "rev-parse", "HEAD")
	submoduletest.WriteFile(t, root, "apps/sub.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: sub
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+submoduleTestRepoURL+`
    targetRevision: HEAD
    path: `+sourcePath+`
  destination:
    name: in-cluster
    namespace: default
`)
	submoduletest.Run(t, root, "add", "apps/sub.yaml")
	submoduletest.Run(t, root, "commit", "-q", "-m", "add sub app")
	return root, base
}

// TestDiffRefSnapshotRendersSubmoduleContent pins that a --ref snapshot
// holds submodule content, as the Argo CD repo-server's checkout does with
// reposerver.enable.git.submodule on (the default). The content comes from
// the repository's own .git/modules store, so it works offline.
func TestDiffRefSnapshotRendersSubmoduleContent(t *testing.T) {
	root, base := submoduleApplicationRepo(t, true)
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		RightPath:   root,
		Repo:        root,
		Ref:         "HEAD",
		RefOrig:     base,
		Offline:     true,
		GitCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	var found bool
	for _, item := range result.Results {
		if item.Resource.Kind == "ConfigMap" && item.Resource.Name == "child" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Results = %#v, want the ConfigMap of the submodule", result.Results)
	}
}

// TestDiffRefSnapshotFailsOfflineWithoutSubmoduleContent pins that a --ref
// snapshot whose submodule commit is neither in .git/modules nor in
// drydock's Git cache fails offline as an offline cache miss, as a fetched
// Git source does.
func TestDiffRefSnapshotFailsOfflineWithoutSubmoduleContent(t *testing.T) {
	root, base := submoduleApplicationRepo(t, false)
	_, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		RightPath:   root,
		Repo:        root,
		Ref:         "HEAD",
		RefOrig:     base,
		Offline:     true,
		GitCacheDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "offline cache miss") || !strings.Contains(err.Error(), `submodule "mods/child"`) {
		t.Fatalf("DiffApps() error = %v, want an offline cache miss for the submodule", err)
	}
}

// TestBuildWarnsForUninitializedSubmoduleSource pins that a working tree
// whose submodule is not initialized renders the empty directory, which
// drydock never fills in the user's tree, and warns that Argo CD renders
// the submodule content. A path below the submodule then does not exist
// and fails; the submodule directory itself renders empty.
func TestBuildWarnsForUninitializedSubmoduleSource(t *testing.T) {
	root, _ := submoduleApplicationRepoAt(t, false, "mods/child")
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Offline: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !hasDiagnosticCode(result.Diagnostics, submoduleWorktreeCode) {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, submoduleWorktreeCode)
	}
	initialized, _ := submoduleApplicationRepoAt(t, true, "mods/child/child/app")
	result, err = Orchestrator{}.Build(context.Background(), BuildRequest{Path: initialized, Offline: true})
	if err != nil {
		t.Fatalf("Build(initialized) error = %v", err)
	}
	if hasDiagnosticCode(result.Diagnostics, submoduleWorktreeCode) {
		t.Fatalf("Diagnostics = %#v, want no warning for an initialized submodule", result.Diagnostics)
	}
}
