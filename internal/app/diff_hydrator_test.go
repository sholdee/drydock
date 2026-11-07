package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/config"
	sourcepkg "github.com/sholdee/drydock/internal/source"
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

// TestSelectionOwnsHydratorSyncPathOnlyWhenRenderedLocally pins changed-only
// selection for a source hydrator: it owns the sync path only when the
// render reads it from the local tree (hydratorSyncRendersFromRoot), not
// whenever the path exists locally.
func TestSelectionOwnsHydratorSyncPathOnlyWhenRenderedLocally(t *testing.T) {
	root := t.TempDir()
	writeDiffHydratedConfigMap(t, root, "local")
	hydratorApp := func(name, repoURL, branch string) ApplicationSelectionInput {
		app := argoappv1.Application{Name: name, Spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: selfRepoSpecURL, Path: "dry"},
			SyncSource: argoappv1.SyncSource{RepoURL: repoURL, TargetBranch: branch, Path: "hydrated"},
		}}}
		return ApplicationSelectionInput{Application: app, Paths: []string{"apps/" + name + ".yaml"}}
	}
	inputs := []ApplicationSelectionInput{
		hydratorApp("tracked", selfRepoSpecURL, "main"),
		hydratorApp("other-branch", selfRepoSpecURL, "env/prod"),
		hydratorApp("other-repo", "https://github.com/example/deployments", "main"),
	}
	selfRepo := selfRepoRefs{urlKeys: []string{sourcepkg.CanonicalGitURLKey(selfRepoRemoteURL)}, revisions: []string{"main"}}
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepo, inputs).inputs
	selected, _ := SelectChangedApplicationInputs(got, []string{"hydrated/cm.yaml"})
	if names := applicationNames(selected); !slices.Equal(names, []string{"tracked"}) {
		t.Fatalf("selected = %v, want only the Application whose sync branch renders from this tree", names)
	}
}

// TestPersistentRenderCacheKeyFollowsHydratorSyncBranchCommit pins that the
// persistent render cache key of a fetched source hydrator sync source
// changes with the commit of its branch: the source identity carries the
// fetched revision. A sync branch read from a --repo-map snapshot has no
// identity and is never cached.
func TestPersistentRenderCacheKeyFollowsHydratorSyncBranchCommit(t *testing.T) {
	root := t.TempDir()
	plan, err := Plan(argoappv1.Application{Name: "hyd", Namespace: "argocd", Spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
		DrySource:  argoappv1.DrySource{RepoURL: "https://github.com/example/config", Path: "dry"},
		SyncSource: argoappv1.SyncSource{RepoURL: "https://github.com/example/deployments", TargetBranch: "env/prod", Path: "hydrated"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	keyAt := func(revision string) string {
		t.Helper()
		acquirer := &countingGitAcquirer{path: t.TempDir(), revision: revision}
		provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{GitAcquirer: acquirer}, root, config.ArgoSettings{}, BuildRequest{Path: root}, nil, "drydock-hydrator-key-*")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		_, identity, err := provider.resolveSourceRootIdentity(context.Background(), resolvedSourceForPlan(plan.Sources[0]))
		if err != nil {
			t.Fatalf("resolveSourceRootIdentity() error = %v", err)
		}
		if identity.Kind != sourceIdentityKindGit || identity.Revision != revision {
			t.Fatalf("identity = %#v, want the fetched sync branch commit %s", identity, revision)
		}
		if got := acquirer.calls(); got != 1 || acquirer.requests[0].Revision != "env/prod" {
			t.Fatalf("git requests = %#v, want one for branch env/prod", acquirer.requests)
		}
		input := persistentKeyFixtureInput()
		input.Sources = []SourceIdentity{identity}
		key, err := persistentRenderCacheKey(input)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	first := keyAt("1111111111111111111111111111111111111111")
	second := keyAt("2222222222222222222222222222222222222222")
	if first == second {
		t.Fatal("a new sync branch commit did not change the persistent render cache key")
	}
}
