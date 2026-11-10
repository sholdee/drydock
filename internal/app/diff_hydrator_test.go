package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
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
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepo, sourceTypeGates{}, inputs).inputs
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

// TestDiffRefHydratorSyncBranchWithSelfRepoMap pins a --ref diff with a
// --repo-map of the repository under diff, which the diff rewrites to each
// side's snapshot. The map names the repository under analysis, so the sync
// source follows the self-repo revision rule as without the map: each side
// reads its own tree, with and without changed-only selection.
func TestDiffRefHydratorSyncBranchWithSelfRepoMap(t *testing.T) {
	for _, changedOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed-only %t", changedOnly), func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
			writeDiffHydratorApp(t, root)
			writeDiffHydratedConfigMap(t, root, "old")
			writeTestFile(t, filepath.Join(root, "dry", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dry\n")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
			checkoutDiffGitBranch(t, fixture.wt, "feature")
			writeDiffHydratedConfigMap(t, root, "new")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature hydrated")

			gitAcquirer := &countingGitAcquirer{err: errors.New("the self-mapped sync branch must not be acquired")}
			result, err := (Orchestrator{GitAcquirer: gitAcquirer}).DiffApps(context.Background(), DiffRequest{
				Repo:        root,
				RefOrig:     "master",
				Ref:         "feature",
				Unified:     3,
				Parallelism: 1,
				ChangedOnly: changedOnly,
				RepoMaps:    []sourcepkg.RepoMap{{URL: selfRepoRemoteURL, Path: root}},
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

// TestHydratorSyncMapToAnotherTreeWins pins that a --repo-map to another
// directory wins over the self-repo rule, as for every source: the sync
// source then never renders from the tree under analysis.
func TestHydratorSyncMapToAnotherTreeWins(t *testing.T) {
	root := t.TempDir()
	provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{}, root, config.ArgoSettings{}, BuildRequest{
		Path:     root,
		RepoMaps: []sourcepkg.RepoMap{{URL: selfRepoRemoteURL, Path: t.TempDir()}},
		selfRepo: selfRepoRefs{urlKeys: []string{sourcepkg.CanonicalGitURLKey(selfRepoRemoteURL)}, revisions: []string{"main"}},
	}, nil, "drydock-hydrator-map-*")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	source := resolvedSourceForPlan(SourcePlan{HydratorSync: true, Source: argoappv1.ApplicationSource{RepoURL: selfRepoSpecURL, TargetRevision: "main", Path: "hydrated"}})
	if provider.hydratorSyncIsLocalTree(source) {
		t.Fatal("hydratorSyncIsLocalTree() = true, want false for a map to another directory")
	}
	self := provider
	self.sourceResolver = sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: []sourcepkg.RepoMap{{URL: selfRepoRemoteURL, Path: root}}})
	if !self.hydratorSyncIsLocalTree(source) {
		t.Fatal("hydratorSyncIsLocalTree() = false, want true for a map to the tree under analysis at a tracked branch")
	}
}

// writeDiffHydratorAppWithDry writes the Application of writeDiffHydratorApp
// with another dry source repository and path. The sync source stays in the
// repository under diff.
func writeDiffHydratorAppWithDry(t *testing.T, root, dryRepoURL, dryPath string) {
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
      repoURL: `+dryRepoURL+`
      targetRevision: HEAD
      path: '`+dryPath+`'
    syncSource:
      repoURL: `+selfRepoSpecURL+`
      targetBranch: master
      path: hydrated
  destination:
    server: https://kubernetes.default.svc
    namespace: demo
`)
}

// TestDiffRefHydratorWarnsOnlyWhenTheDiffChangesTheDrySource pins when a
// diff gives source.hydrator-sync-source: only when the dry source is in the
// repository under diff and the diff changes a file under its path. The
// diff renders the sync source, so the dry change cannot show there.
func TestDiffRefHydratorWarnsOnlyWhenTheDiffChangesTheDrySource(t *testing.T) {
	for _, tt := range []struct {
		name        string
		dryRepoURL  string
		dryPath     string
		changedFile string
		changedOnly bool
		diffApp     bool
		want        string
	}{
		{name: "hydrated path only", dryRepoURL: selfRepoSpecURL, dryPath: "dry", changedFile: "hydrated/extra.yaml"},
		{name: "dry path", dryRepoURL: selfRepoSpecURL, dryPath: "dry", changedFile: "dry/cm.yaml", want: `dry source path "dry"`},
		{name: "dry path with changed-only", dryRepoURL: selfRepoSpecURL, dryPath: "dry", changedFile: "dry/cm.yaml", changedOnly: true, want: `dry source path "dry"`},
		{name: "dry path with diff app", dryRepoURL: selfRepoSpecURL, dryPath: "dry", changedFile: "dry/cm.yaml", diffApp: true, want: `dry source path "dry"`},
		{name: "dry path with dot and trailing slash", dryRepoURL: selfRepoSpecURL, dryPath: "./dry/", changedFile: "dry/cm.yaml", want: `dry source path "dry"`},
		{name: "sibling with the same prefix", dryRepoURL: selfRepoSpecURL, dryPath: "dry", changedFile: "dryer/cm.yaml"},
		{name: "repository root", dryRepoURL: selfRepoSpecURL, dryPath: ".", changedFile: "notes/readme.txt", want: `dry source path "."`},
		{name: "remote dry source", dryRepoURL: "https://github.com/example/config", dryPath: "dry", changedFile: "dry/cm.yaml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
			writeDiffHydratorAppWithDry(t, root, tt.dryRepoURL, tt.dryPath)
			writeDiffHydratedConfigMap(t, root, "same")
			writeTestFile(t, filepath.Join(root, "dry", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dry\n")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
			checkoutDiffGitBranch(t, fixture.wt, "feature")
			writeTestFile(t, filepath.Join(root, filepath.FromSlash(tt.changedFile)), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: changed\n")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature")

			request := DiffRequest{
				Repo:        root,
				RefOrig:     "master",
				Ref:         "feature",
				Unified:     3,
				Parallelism: 1,
				ChangedOnly: tt.changedOnly,
				Offline:     true,
			}
			var result DiffResult
			var err error
			if tt.diffApp {
				result, err = Orchestrator{}.DiffApp(context.Background(), DiffAppRequest{DiffRequest: request, Name: "hyd"})
			} else {
				result, err = Orchestrator{}.DiffApps(context.Background(), request)
			}
			if err != nil {
				t.Fatalf("diff error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			var warnings []string
			for _, diag := range result.Diagnostics {
				if diag.Code == sourceHydratorCode {
					warnings = append(warnings, diag.Message)
				}
			}
			if tt.want == "" {
				if len(warnings) != 0 {
					t.Fatalf("hydrator warnings = %q, want none", warnings)
				}
				return
			}
			if len(warnings) != 1 {
				t.Fatalf("hydrator warnings = %q, want one", warnings)
			}
			for _, want := range []string{"Application argocd/hyd", tt.want, `sync source branch "master", path "hydrated"`} {
				if !strings.Contains(warnings[0], want) {
					t.Fatalf("hydrator warning = %q, want %q", warnings[0], want)
				}
			}
		})
	}
}

// TestDiffRefHydratorDryChangeWarningIsStrictExempt pins that the dry
// source warning does not fail a --strict diff.
func TestDiffRefHydratorDryChangeWarningIsStrictExempt(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeDiffHydratorApp(t, root)
	writeDiffHydratedConfigMap(t, root, "same")
	writeTestFile(t, filepath.Join(root, "dry", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dry\n")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeTestFile(t, filepath.Join(root, "dry", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dry-changed\n")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature")

	request := DiffRequest{Repo: root, RefOrig: "master", Ref: "feature", Unified: 3, Parallelism: 1, Strict: true, Offline: true}
	result, err := Orchestrator{}.DiffApps(context.Background(), request)
	if err != nil {
		t.Fatalf("DiffApps(--strict) error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if !hasDiagnosticCode(result.Diagnostics, sourceHydratorCode) {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, sourceHydratorCode)
	}
}

// TestHydratorDryChangeDiagnosticsKeepsTheWarningWhenChangesAreUnknown pins
// that drydock never hides a dry source change: when it cannot list the
// files that the diff changes, a local dry source still warns. A dry source
// in another repository never asks for the list.
func TestHydratorDryChangeDiagnosticsKeepsTheWarningWhenChangesAreUnknown(t *testing.T) {
	root := t.TempDir()
	application := func(dryRepoURL string) argoappv1.Application {
		return argoappv1.Application{Name: "hyd", Namespace: "argocd", Spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: dryRepoURL, TargetRevision: "HEAD", Path: "dry"},
			SyncSource: argoappv1.SyncSource{TargetBranch: "master", Path: "hydrated"},
		}}}
	}
	request := BuildRequest{Path: root, selfRepo: selfRepoRefs{urlKeys: []string{sourcepkg.CanonicalGitURLKey(selfRepoRemoteURL)}}}
	calls := 0
	unknown := func() diffChangedFiles {
		calls++
		return diffChangedFiles{unknown: "listing failed"}
	}

	diags := hydratorDryChangeDiagnostics([]hydratorDiffSide{{request: request, applications: []argoappv1.Application{application(selfRepoSpecURL)}}}, unknown)
	if len(diags) != 1 || diags[0].Code != sourceHydratorCode || !strings.Contains(diags[0].Message, "could not list the files") || !strings.Contains(diags[0].Message, "listing failed") {
		t.Fatalf("diagnostics = %#v, want one warning that the changed files are unknown", diags)
	}

	calls = 0
	diags = hydratorDryChangeDiagnostics([]hydratorDiffSide{{request: request, applications: []argoappv1.Application{application("https://github.com/example/config")}}}, unknown)
	if len(diags) != 0 || calls != 0 {
		t.Fatalf("diagnostics = %#v, calls = %d, want none for a remote dry source", diags, calls)
	}
}

// TestDiffChangedFilesComparesTreesForAPathDiff pins that a path diff
// without --changed-only lists its changed files by a compare of the two
// trees, as changed-only does.
func TestDiffChangedFilesComparesTreesForAPathDiff(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeTestFile(t, filepath.Join(left, "dry", "cm.yaml"), "a: 1\n")
	writeTestFile(t, filepath.Join(right, "dry", "cm.yaml"), "a: 2\n")
	writeTestFile(t, filepath.Join(left, "same.yaml"), "a: 1\n")
	writeTestFile(t, filepath.Join(right, "same.yaml"), "a: 1\n")
	files := DiffRequest{LeftPath: left, RightPath: right}.diffChangedFiles(context.Background())
	if files.unknown != "" || !slices.Equal(files.paths, []string{"dry/cm.yaml"}) {
		t.Fatalf("diffChangedFiles() = %#v, want only dry/cm.yaml", files)
	}
}
