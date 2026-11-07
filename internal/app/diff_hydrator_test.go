package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func writeDiffHydratorApp(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "hyd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: hyd
  namespace: argocd
spec:
  project: default
  sourceHydrator:
    drySource:
      repoURL: `+selfRepoSpecURL+`
      targetRevision: HEAD
      path: dry
    syncSource:
      targetBranch: master
      path: hydrated
  destination:
    server: https://kubernetes.default.svc
    namespace: demo
`)
}

func writeDiffHydratedConfigMap(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "hydrated", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: hydrated\ndata:\n  value: "+value+"\n")
}

// TestDiffRefHydratorSyncBranchReadsEachSideTree pins a --ref diff of an
// Application whose source hydrator syncs from the default branch of the
// repository under diff. The sync source follows the self-repo rule of
// every other source of that repository (selfRepoMatcher.matches), so each
// side reads its own tree and a change to the hydrated path shows. Both
// diff forms are covered: two branch names, and the CI form with HEAD and
// the remote-tracking default branch.
func TestDiffRefHydratorSyncBranchReadsEachSideTree(t *testing.T) {
	for _, tt := range []struct {
		name    string
		refOrig string
		ref     string
	}{
		{name: "branch names", refOrig: "master", ref: "feature"},
		{name: "HEAD against origin/master", refOrig: "origin/master", ref: "HEAD"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
			writeDiffHydratorApp(t, root)
			writeDiffHydratedConfigMap(t, root, "old")
			writeTestFile(t, filepath.Join(root, "dry", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dry\n")
			baseline := commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
			if err := fixture.repo.Storer.SetReference(plumbing.NewHashReference("refs/remotes/origin/master", baseline)); err != nil {
				t.Fatal(err)
			}
			if err := fixture.repo.Storer.SetReference(plumbing.NewSymbolicReference("refs/remotes/origin/HEAD", "refs/remotes/origin/master")); err != nil {
				t.Fatal(err)
			}
			checkoutDiffGitBranch(t, fixture.wt, "feature")
			writeDiffHydratedConfigMap(t, root, "new")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature hydrated")

			gitAcquirer := &countingGitAcquirer{err: errors.New("the tracked sync branch must not be acquired")}
			result, err := (Orchestrator{GitAcquirer: gitAcquirer}).DiffApps(context.Background(), DiffRequest{
				Repo:        root,
				RefOrig:     tt.refOrig,
				Ref:         tt.ref,
				Unified:     3,
				Parallelism: 1,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v", err)
			}
			if got := gitAcquirer.calls(); got != 0 {
				t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
			}
			assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
		})
	}
}
