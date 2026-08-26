package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/diagnostic"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

const (
	selfRepoRemoteURL = "https://github.com/example/repo.git"
	selfRepoSpecURL   = "https://github.com/example/repo"
)

// countingGitAcquirer is a mutex-guarded call-counting git fake. Diff sides
// may run concurrently when parallelism rises; unlike recordingGitAcquirer it
// is safe under -race regardless of the test's Parallelism setting.
type countingGitAcquirer struct {
	mu       sync.Mutex
	path     string
	revision string
	err      error
	requests []sourcepkg.GitRequest
}

func (a *countingGitAcquirer) Acquire(_ context.Context, request sourcepkg.GitRequest, _ sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, request)
	if a.err != nil {
		return sourcepkg.GitResult{}, a.err
	}
	return sourcepkg.GitResult{Path: a.path, Revision: a.revision, FromCache: false, Network: true}, nil
}

func (a *countingGitAcquirer) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

// initSelfRepoDiffGitRepo initializes a git repository whose origin remote
// names the repository the committed app specs reference, so diff commands
// can recognize sources pointing back at the repository under diff.
func initSelfRepoDiffGitRepo(t *testing.T, remoteURL string) (string, *gitRepoFixture) {
	t.Helper()
	root := t.TempDir()
	repo, wt := initDiffGitRepo(t, root)
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remoteURL}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	return root, &gitRepoFixture{repo: repo, wt: wt}
}

type gitRepoFixture struct {
	repo *git.Repository
	wt   *git.Worktree
}

// writeSelfRepoRefValuesApp writes a committed multi-source Application whose
// ref-only source names the repository under diff and whose chart source
// consumes a $repo value file from it, plus that value file.
func writeSelfRepoRefValuesApp(t *testing.T, root, name, refRepoURL, targetRevision, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  sources:
    - repoURL: `+refRepoURL+`
      targetRevision: "`+targetRevision+`"
      ref: repo
    - repoURL: https://charts.example.test
      targetRevision: 1.2.3
      chart: demo
      helm:
        valueFiles:
          - $repo/values/`+name+`.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeSelfRepoRefValuesFile(t, root, name, value)
}

func writeSelfRepoRefValuesFile(t *testing.T, root, name, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "values", name+".yaml"), "value: "+value+"\n")
}

// newSelfRepoValueChartAcquirer serves the external chart source of the
// committed multi-source app fixture.
func newSelfRepoValueChartAcquirer(t *testing.T) *recordingChartAcquirer {
	t.Helper()
	chartRoot := t.TempDir()
	writeAppTestValueChart(t, chartRoot)
	return &recordingChartAcquirer{chartDir: chartRoot}
}

func selfRepoGitEventActions(events []cacheevent.Event, targetFragment string) []string {
	var actions []string
	for _, event := range events {
		if event.Source == cacheevent.SourceGit && strings.Contains(event.Target, targetFragment) {
			actions = append(actions, string(event.Action))
		}
	}
	return actions
}

func assertSingleDiffContains(t *testing.T, result DiffResult, wants ...string) {
	t.Helper()
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1: %#v", len(result.Results), result.Results)
	}
	for _, want := range wants {
		if !strings.Contains(result.Results[0].Diff, want) {
			t.Fatalf("Diff = %q, want %q", result.Results[0].Diff, want)
		}
	}
}

// Test 1 — SILENT regression: a ref-only self source's $repo value file must
// resolve to each side's tree, never the shared remote worktree that erased
// the change from both sides.
func TestDiffRefSelfRepoRefValuesResolveToSideTrees(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "HEAD", "old")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:              root,
		RefOrig:           "master",
		Ref:               "feature",
		Unified:           3,
		Parallelism:       1,
		RecordCacheEvents: true,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
	if !hasCacheEvent(result.CacheEvents, "git", "local", "github.com/example/repo") {
		t.Fatalf("CacheEvents = %#v, want local git event for the self-mapped $repo ref", result.CacheEvents)
	}
}

// Test 2 — LOUD regression: an Application added on the diffed ref must render
// with its own side's $repo values instead of failing acquisition.
func TestDiffRefSelfRepoRefAddedApplicationRenders(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "HEAD", "same")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesApp(t, root, "added", selfRepoSpecURL, "HEAD", "brand-new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature adds app")

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:        root,
		RefOrig:     "master",
		Ref:         "feature",
		Unified:     3,
		Parallelism: 1,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	var added string
	for _, item := range result.Results {
		if item.Change == "added" {
			added = item.Diff
		}
	}
	if !strings.Contains(added, "+  value: brand-new") {
		t.Fatalf("Results = %#v, want added manifest with brand-new value", result.Results)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// Test 3 — SHARING PRESERVED, pinned SHA: full-commit-SHA self references keep
// today's shared acquisition across sides.
func TestDiffRefSelfRepoPinnedSHAStillSharesAcquisition(t *testing.T) {
	pinned := "1111111111111111111111111111111111111111"
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, pinned, "unused")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "unused-too")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

	fetchedRoot := t.TempDir()
	writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "pinned")
	gitAcquirer := &countingGitAcquirer{path: fetchedRoot, revision: pinned}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:              root,
		RefOrig:           "master",
		Ref:               "feature",
		Unified:           3,
		Parallelism:       1,
		RecordCacheEvents: true,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results = %#v, want none for pinned tree on both sides", result.Results)
	}
	if got := gitAcquirer.calls(); got != 1 {
		t.Fatalf("git acquire calls = %d, want 1: %#v", got, gitAcquirer.requests)
	}
	actions := selfRepoGitEventActions(result.CacheEvents, "github.com/example/repo")
	if !reflect.DeepEqual(actions, []string{"fetch", "hit"}) {
		t.Fatalf("git event actions = %#v, want [fetch hit]", actions)
	}
}

// Test 4 — SHARING PRESERVED, non-self remote URL @HEAD: unrelated remote
// repositories keep today's single shared acquisition and fetch/hit events.
func TestDiffRefNonSelfRemoteURLStillSharesAcquisition(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", "https://github.com/other/unrelated", "HEAD", "unused")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "unused-too")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

	fetchedRoot := t.TempDir()
	writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "remote")
	gitAcquirer := &countingGitAcquirer{path: fetchedRoot, revision: "2222222222222222222222222222222222222222"}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:              root,
		RefOrig:           "master",
		Ref:               "feature",
		Unified:           3,
		Parallelism:       1,
		RecordCacheEvents: true,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results = %#v, want none for shared remote tree on both sides", result.Results)
	}
	if got := gitAcquirer.calls(); got != 1 {
		t.Fatalf("git acquire calls = %d, want 1: %#v", got, gitAcquirer.requests)
	}
	actions := selfRepoGitEventActions(result.CacheEvents, "github.com/other/unrelated")
	if !reflect.DeepEqual(actions, []string{"fetch", "hit"}) {
		t.Fatalf("git event actions = %#v, want [fetch hit]", actions)
	}
}

// Test 5 — TAG/BRANCH GATE: only ""/HEAD and the literal diffed ref names
// self-map; tags and unrelated branches keep today's remote acquisition.
func TestDiffRefSelfRepoSymbolicRevisionGate(t *testing.T) {
	for _, tt := range []struct {
		name           string
		targetRevision string
		wantAcquired   bool
	}{
		{name: "tag revision acquires remotely", targetRevision: "v1.2.3", wantAcquired: true},
		{name: "unrelated branch acquires remotely", targetRevision: "release-1.x", wantAcquired: true},
		{name: "diffed ref name maps per side", targetRevision: "master", wantAcquired: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
			writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, tt.targetRevision, "old")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
			checkoutDiffGitBranch(t, fixture.wt, "feature")
			writeSelfRepoRefValuesFile(t, root, "demo", "new")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

			gitAcquirer := &countingGitAcquirer{err: errors.New("diffed ref name must not be acquired")}
			if tt.wantAcquired {
				fetchedRoot := t.TempDir()
				writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "remote")
				gitAcquirer = &countingGitAcquirer{path: fetchedRoot, revision: "3333333333333333333333333333333333333333"}
			}
			result, err := (Orchestrator{
				GitAcquirer:   gitAcquirer,
				ChartAcquirer: newSelfRepoValueChartAcquirer(t),
			}).DiffApps(context.Background(), DiffRequest{
				Repo:        root,
				RefOrig:     "master",
				Ref:         "feature",
				Unified:     3,
				Parallelism: 1,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v", err)
			}
			if tt.wantAcquired {
				if got := gitAcquirer.calls(); got != 1 {
					t.Fatalf("git acquire calls = %d, want 1: %#v", got, gitAcquirer.requests)
				}
				if len(result.Results) != 0 {
					t.Fatalf("Results = %#v, want no spurious side-tree diff", result.Results)
				}
				return
			}
			if got := gitAcquirer.calls(); got != 0 {
				t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
			}
			assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
		})
	}
}

// Default-branch-name gate: a spec pinned to the repository's default branch
// NAME (targetRevision: main-style — more common in real repos than HEAD)
// tracks the diffed tree exactly like a --ref name. The name is discovered
// from the remote HEAD symref (refs/remotes/<remote>/HEAD), which clones set
// and pr-action's fetch-base restores via git remote set-head. Without the
// symref (or when it names a different branch) the source keeps acquiring
// remotely — the documented limitation and its remedy.
func TestDiffRefSelfRepoDefaultBranchNameMapsViaSymref(t *testing.T) {
	for _, tt := range []struct {
		name         string
		symrefTarget string // "" = no symref
		wantAcquired bool
	}{
		{name: "symref names the spec branch: maps per side", symrefTarget: "trunk", wantAcquired: false},
		{name: "no symref: acquires remotely", symrefTarget: "", wantAcquired: true},
		{name: "symref names a different branch: acquires remotely", symrefTarget: "prod", wantAcquired: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
			if tt.symrefTarget != "" {
				if err := fixture.repo.Storer.SetReference(plumbing.NewSymbolicReference(
					plumbing.ReferenceName("refs/remotes/origin/HEAD"),
					plumbing.ReferenceName("refs/remotes/origin/"+tt.symrefTarget),
				)); err != nil {
					t.Fatalf("SetReference(origin/HEAD) error = %v", err)
				}
			}
			writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "trunk", "old")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
			checkoutDiffGitBranch(t, fixture.wt, "feature")
			writeSelfRepoRefValuesFile(t, root, "demo", "new")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

			gitAcquirer := &countingGitAcquirer{err: errors.New("default-branch self ref must not be acquired")}
			if tt.wantAcquired {
				fetchedRoot := t.TempDir()
				writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "remote")
				gitAcquirer = &countingGitAcquirer{path: fetchedRoot, revision: "4444444444444444444444444444444444444444"}
			}
			result, err := (Orchestrator{
				GitAcquirer:   gitAcquirer,
				ChartAcquirer: newSelfRepoValueChartAcquirer(t),
			}).DiffApps(context.Background(), DiffRequest{
				Repo:        root,
				RefOrig:     "master",
				Ref:         "feature",
				Unified:     3,
				Parallelism: 1,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v", err)
			}
			if tt.wantAcquired {
				if got := gitAcquirer.calls(); got != 1 {
					t.Fatalf("git acquire calls = %d, want 1: %#v", got, gitAcquirer.requests)
				}
				return
			}
			if got := gitAcquirer.calls(); got != 0 {
				t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
			}
			assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
		})
	}
}

// Test 6 — PATH-BASED variant: real checkouts on both sides self-map through
// the union of every side's remotes, including the judge's mirror-remote
// wrinkle (matching remote only on the left, non-matching mirror on the right).
func TestDiffPathSelfRepoRemoteUnionResolvesPerSide(t *testing.T) {
	right := t.TempDir()
	repo, wt := initDiffGitRepo(t, right)
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{"https://mirror.example.test/example/other.git"}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	writeSelfRepoRefValuesApp(t, right, "demo", selfRepoSpecURL, "sidebranch", "old")
	commitDiffGitRepo(t, repo, wt, "baseline")

	left := t.TempDir()
	leftRepo, leftWt := initDiffGitRepo(t, left)
	if _, err := leftRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "upstream", URLs: []string{selfRepoRemoteURL}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	// The spec below pins targetRevision to a branch name that only THIS side's
	// HEAD symref names. For path diffs repoPath defaults to RightPath, whose
	// checkout has no such symref — so the mapping can only come from the
	// LeftPath contribution of the DefaultBranchNames union in
	// detectSelfRepoRefs, pinning that union.
	if err := leftRepo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.ReferenceName("refs/remotes/upstream/HEAD"),
		plumbing.ReferenceName("refs/remotes/upstream/sidebranch"),
	)); err != nil {
		t.Fatalf("SetReference(upstream/HEAD) error = %v", err)
	}
	writeSelfRepoRefValuesApp(t, left, "demo", selfRepoSpecURL, "sidebranch", "old")
	commitDiffGitRepo(t, leftRepo, leftWt, "baseline")

	writeSelfRepoRefValuesFile(t, right, "demo", "new")
	commitDiffGitRepo(t, repo, wt, "new values")

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		Unified:     3,
		Parallelism: 1,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// Test 7 — REPO-MAP REWRITE: an explicit --repo-map entry pointing at the
// checkout under diff is rewritten to each side's snapshot, so the previously
// silent one-worktree-for-both-sides case now diffs.
func TestDiffRefRepoMapPointingAtDiffedRepoRewritesPerSide(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "HEAD", "old")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

	gitAcquirer := &countingGitAcquirer{err: errors.New("mapped repository must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:              root,
		RefOrig:           "master",
		Ref:               "feature",
		Unified:           3,
		Parallelism:       1,
		RecordCacheEvents: true,
		RepoMaps:          []sourcepkg.RepoMap{{URL: selfRepoRemoteURL, Path: root}},
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
	if !hasCacheEvent(result.CacheEvents, "git", "mapped", "github.com/example/repo") {
		t.Fatalf("CacheEvents = %#v, want mapped git event for the rewritten repo map", result.CacheEvents)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// Test 8 — NEAR-MISS DIAGNOSTIC: a fork-shaped URL (same host and repo name,
// different owner) warns once per URL per side and still acquires remotely.
// The strict variant pins the diagnostic's strict exemption: --strict must
// neither escalate the warning nor fail the diff (the affected population —
// fork layouts running strict CI diffs — is exactly who the hint serves).
func TestDiffRefSelfRepoForkNearMissWarnsOncePerSide(t *testing.T) {
	for _, tt := range []struct {
		name   string
		strict bool
	}{
		{name: "default"},
		{name: "strict still succeeds", strict: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, "https://github.com/me/repo.git")
			writeSelfRepoRefValuesApp(t, root, "demo", "https://github.com/upstream/repo", "HEAD", "unused")
			writeSelfRepoRefValuesApp(t, root, "second", "https://github.com/upstream/repo", "HEAD", "unused")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
			checkoutDiffGitBranch(t, fixture.wt, "feature")
			writeSelfRepoRefValuesFile(t, root, "demo", "unused-too")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

			fetchedRoot := t.TempDir()
			writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "remote")
			writeSelfRepoRefValuesFile(t, fetchedRoot, "second", "remote")
			gitAcquirer := &countingGitAcquirer{path: fetchedRoot, revision: "4444444444444444444444444444444444444444"}
			result, err := (Orchestrator{
				GitAcquirer:   gitAcquirer,
				ChartAcquirer: newSelfRepoValueChartAcquirer(t),
			}).DiffApps(context.Background(), DiffRequest{
				Repo:        root,
				RefOrig:     "master",
				Ref:         "feature",
				Strict:      tt.strict,
				Unified:     3,
				Parallelism: 1,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v", err)
			}
			var nearMisses []diagnostic.Diagnostic
			for _, diag := range result.Diagnostics {
				if diag.Code == selfRepoNearMissCode {
					nearMisses = append(nearMisses, diag)
				}
			}
			if len(nearMisses) != 2 {
				t.Fatalf("near-miss diagnostics = %d, want exactly one per side across two apps: %#v", len(nearMisses), result.Diagnostics)
			}
			for _, diag := range nearMisses {
				if diag.Severity != diagnostic.SeverityWarning {
					t.Fatalf("near-miss severity = %s, want warning: %#v", diag.Severity, diag)
				}
				for _, fragment := range []string{"https://github.com/upstream/repo", "github.com/me/repo", "--repo-map"} {
					if !strings.Contains(diag.Message, fragment) {
						t.Fatalf("near-miss message = %q, want fragment %q", diag.Message, fragment)
					}
				}
			}
			if got := gitAcquirer.calls(); got != 1 {
				t.Fatalf("git acquire calls = %d, want 1 (remote acquisition still attempted): %#v", got, gitAcquirer.requests)
			}
		})
	}
}

// Test 8b — the remediation hint accompanies the acquisition failure: when
// the fork-shaped URL's own acquisition fails (offline, unreachable fork),
// the near-miss warning is emitted alongside the failure instead of being
// dropped — nearMissDiags is computed before any acquisition.
func TestDiffRefSelfRepoForkNearMissAccompaniesAcquisitionFailure(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, "https://github.com/me/repo.git")
	writeSelfRepoRefValuesApp(t, root, "demo", "https://github.com/upstream/repo", "HEAD", "unused")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "unused-too")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

	gitAcquirer := &countingGitAcquirer{err: errors.New("upstream unreachable")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:        root,
		RefOrig:     "master",
		Ref:         "feature",
		Unified:     3,
		Parallelism: 1,
	})
	if err == nil {
		t.Fatal("DiffApps() error = nil, want acquisition failure")
	}
	found := false
	for _, diag := range result.Diagnostics {
		if diag.Code == selfRepoNearMissCode {
			found = true
			if diag.Severity != diagnostic.SeverityWarning {
				t.Fatalf("near-miss severity = %s, want warning: %#v", diag.Severity, diag)
			}
		}
	}
	if !found {
		t.Fatalf("Diagnostics = %#v, want the --repo-map near-miss hint alongside the acquisition failure", result.Diagnostics)
	}
}

// Test 9 — OFFLINE self-ref: resolves locally with zero network instead of
// failing with "offline cache miss".
func TestDiffRefSelfRepoRefRendersOffline(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "HEAD", "old")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoRefValuesFile(t, root, "demo", "new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature values")

	result, err := (Orchestrator{
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		Repo:        root,
		RefOrig:     "master",
		Ref:         "feature",
		Unified:     3,
		Parallelism: 1,
		Offline:     true,
		GitCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
}

// Test 10 — DiffImages variant: a $repo-values-driven image tag change reports
// the image change through the same buildDiffSides path.
func TestDiffImagesSelfRepoRefValuesReportImageChange(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  sources:
    - repoURL: `+selfRepoSpecURL+`
      targetRevision: HEAD
      ref: repo
    - repoURL: https://charts.example.test
      targetRevision: 1.2.3
      chart: demo
      helm:
        valueFiles:
          - $repo/values/demo.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "values", "demo.yaml"), "image: example/app:v1\n")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeTestFile(t, filepath.Join(root, "values", "demo.yaml"), "image: example/app:v2\n")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature image")

	chartRoot := t.TempDir()
	writeTestFile(t, filepath.Join(chartRoot, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(chartRoot, "values.yaml"), "image: example/app:default\n")
	writeTestFile(t, filepath.Join(chartRoot, "templates", "deploy.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: {{ .Values.image }}
`)

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: &recordingChartAcquirer{chartDir: chartRoot},
	}).DiffImages(context.Background(), DiffRequest{
		Repo:        root,
		RefOrig:     "master",
		Ref:         "feature",
		Parallelism: 1,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if !reflect.DeepEqual(result.Added, []string{"example/app:v2"}) {
		t.Fatalf("Added = %#v, want example/app:v2", result.Added)
	}
	if !reflect.DeepEqual(result.Removed, []string{"example/app:v1"}) {
		t.Fatalf("Removed = %#v, want example/app:v1", result.Removed)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// Test 11 — KUSTOMIZE GIT BASE (row 5 wiring pin): a kustomization declaring a
// self-referential remote git base (<ownURL>//dir?ref=HEAD) must resolve to
// the active side tree through selfMapRemote with zero remote acquisitions,
// reporting a hit event (no phantom fetch) that carries the side snapshot SHA.
func TestDiffRefSelfRepoKustomizeGitBaseResolvesPerSide(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: `+selfRepoSpecURL+`
    targetRevision: HEAD
    path: overlay
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "overlay", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - `+selfRepoRemoteURL+`//base?ref=HEAD
`)
	writeTestFile(t, filepath.Join(root, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - config.yaml
`)
	writeSelfRepoKustomizeBaseConfig(t, root, "old")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeSelfRepoKustomizeBaseConfig(t, root, "new")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "feature base values")

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	remoteAcquirer := &countingRemoteAcquirer{err: errors.New("self-repo kustomize base must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:            gitAcquirer,
		RemoteResourceAcquirer: remoteAcquirer,
	}).DiffApps(context.Background(), DiffRequest{
		Repo:              root,
		RefOrig:           "master",
		Ref:               "feature",
		Unified:           3,
		Parallelism:       1,
		RecordCacheEvents: true,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
	if got := remoteAcquirer.calls(); got != 0 {
		t.Fatalf("remote acquire calls = %d, want 0: %#v", got, remoteAcquirer.requests)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
	if !hasCacheEvent(result.CacheEvents, "remote", "hit", "github.com/example/repo") {
		t.Fatalf("CacheEvents = %#v, want remote hit event for the self-mapped git base", result.CacheEvents)
	}
	for _, event := range result.CacheEvents {
		if event.Source == cacheevent.SourceRemote && event.Action == cacheevent.ActionHit && strings.Contains(event.Target, "github.com/example/repo") && event.Revision == "" {
			t.Fatalf("remote hit event revision empty, want side snapshot SHA: %#v", event)
		}
	}
}

func writeSelfRepoKustomizeBaseConfig(t *testing.T, root, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "base", "config.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
  namespace: default
data:
  value: `+value+`
`)
}
