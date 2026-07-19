package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/diagnostic"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Build/list-surface self-repo resolution suite (issue #206 follow-up):
// sources naming the local checkout's own repository at ""/HEAD or a
// symref-derived default-branch name resolve to the local tree on render
// surfaces, exactly like the #207 diff behavior. Fixture conventions follow
// diff_ref_sources_test.go: real git repositories with configured remotes and
// remote-HEAD symrefs, plus counting acquirer fakes.

func setSelfRepoRemoteHeadSymref(t *testing.T, repo *git.Repository, remoteName, branch string) {
	t.Helper()
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.ReferenceName("refs/remotes/"+remoteName+"/HEAD"),
		plumbing.ReferenceName("refs/remotes/"+remoteName+"/"+branch),
	)); err != nil {
		t.Fatalf("SetReference(%s/HEAD) error = %v", remoteName, err)
	}
}

func buildConfigMapValue(t *testing.T, result BuildResult, name string) string {
	t.Helper()
	for _, renderedManifest := range result.Manifests {
		if renderedManifest.Object == nil || renderedManifest.Object.GetKind() != "ConfigMap" || renderedManifest.Object.GetName() != name {
			continue
		}
		value, _, err := unstructured.NestedString(renderedManifest.Object.Object, "data", "value")
		if err != nil {
			t.Fatalf("NestedString(data.value) error = %v", err)
		}
		return value
	}
	t.Fatalf("no ConfigMap %q in result manifests: %#v", name, result.Manifests)
	return ""
}

// writeSelfRepoAppOfAppsFixture writes a parent Application whose local helm
// chart source consumes a $repo value file from the repository's own URL at
// the default-branch NAME and templates a child Application. The child's name
// exists ONLY in the local tree's value file, so listing surfaces can only
// discover it when the $repo ref resolves locally (frontier discovery
// swallows render errors, so a broken ref silently drops the child).
func writeSelfRepoAppOfAppsFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "parent.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: parent
  namespace: argocd
spec:
  sources:
    - repoURL: `+selfRepoSpecURL+`
      targetRevision: "main"
      ref: repo
    - repoURL: `+selfRepoSpecURL+`
      targetRevision: HEAD
      path: bootstrap-chart
      helm:
        valueFiles:
          - $repo/values/parent.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "Chart.yaml"), `apiVersion: v2
name: bootstrap
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "values.yaml"), `childName: default-child
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "child.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: {{ .Values.childName }}
  namespace: argocd
spec:
  source:
    repoURL: `+selfRepoSpecURL+`
    targetRevision: HEAD
    path: manifests/child
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "values", "parent.yaml"), `childName: local-only-child
`)
	writeTestFile(t, filepath.Join(root, "manifests", "child", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: child-config
  namespace: default
data:
  value: child
`)
}

// Test 1 — LIST-surface pin (app-of-apps): the child Application exists only
// in the local tree's $repo value file, so its presence in ListApplications
// output proves the discovery providers received the self-repo refs.
func TestListApplicationsSelfRepoRefAppOfAppsDiscoversLocalOnlyChild(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", "main")
	writeSelfRepoAppOfAppsFixture(t, root)
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer: gitAcquirer,
	}).ListApplications(context.Background(), BuildRequest{
		Path:               root,
		AcquisitionOptions: AcquisitionOptions{RecordCacheEvents: true},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	names := make([]string, 0, len(result.Applications))
	for _, application := range result.Applications {
		names = append(names, application.Name)
	}
	if !slices.Contains(names, "local-only-child") {
		t.Fatalf("Applications = %#v, want the $repo-values-driven child discovered from the local tree", names)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
	if !hasCacheEvent(result.CacheEvents, "git", "local", "github.com/example/repo") {
		t.Fatalf("CacheEvents = %#v, want local git event for the self-mapped $repo ref", result.CacheEvents)
	}
	for _, event := range result.CacheEvents {
		if event.Source == cacheevent.SourceGit && strings.Contains(event.Target, "github.com/example/repo") && event.Action != cacheevent.ActionLocal {
			t.Fatalf("unexpected non-local git event for the self URL during discovery: %#v", event)
		}
	}
}

// Test 2 — BUILD-surface repro shape (orchestrator-level direct Build, per
// the review verdict: a CLI-level shape could be masked by list-phase
// render-cache reuse): a $repo values ref at the symref-derived
// default-branch name renders from the local tree with an ActionLocal event.
func TestBuildSelfRepoRefDefaultBranchValuesRenderLocally(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", "main")
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "main", "from-local-tree")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).Build(context.Background(), BuildRequest{
		Path:               root,
		AcquisitionOptions: AcquisitionOptions{RecordCacheEvents: true},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := buildConfigMapValue(t, result, "demo"); got != "from-local-tree" {
		t.Fatalf("rendered value = %q, want from-local-tree", got)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
	if !hasCacheEvent(result.CacheEvents, "git", "local", "github.com/example/repo") {
		t.Fatalf("CacheEvents = %#v, want local git event for the self-mapped $repo ref", result.CacheEvents)
	}
}

// Test 3 — symref gate on a render surface: only the remote-HEAD symref may
// contribute default-branch names. A wrapper hallucinating from
// init.defaultBranch or the checked-out HEAD fails cases (b) and (d).
func TestBuildSelfRepoDefaultBranchSymrefGate(t *testing.T) {
	for _, tt := range []struct {
		name           string
		symrefTarget   string // "" = no symref
		targetRevision string
		checkoutBranch string // branch to check out before building; "" = stay put
		wantAcquired   bool
	}{
		{name: "symref names the spec branch: resolves locally", symrefTarget: "main", targetRevision: "main"},
		{name: "no symref: acquires remotely (symref-or-nothing)", targetRevision: "main", wantAcquired: true},
		{name: "symref names a different branch: acquires remotely", symrefTarget: "prod", targetRevision: "main", wantAcquired: true},
		{name: "checked-out non-default branch name: acquires remotely", symrefTarget: "main", targetRevision: "feature", checkoutBranch: "feature", wantAcquired: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
			if tt.symrefTarget != "" {
				setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", tt.symrefTarget)
			}
			writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, tt.targetRevision, "from-local-tree")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
			if tt.checkoutBranch != "" {
				checkoutDiffGitBranch(t, fixture.wt, tt.checkoutBranch)
			}

			gitAcquirer := &countingGitAcquirer{err: errors.New("self ref must not be acquired")}
			if tt.wantAcquired {
				fetchedRoot := t.TempDir()
				writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "from-remote")
				gitAcquirer = &countingGitAcquirer{path: fetchedRoot, revision: "7777777777777777777777777777777777777777"}
			}
			result, err := (Orchestrator{
				GitAcquirer:   gitAcquirer,
				ChartAcquirer: newSelfRepoValueChartAcquirer(t),
			}).Build(context.Background(), BuildRequest{Path: root})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if tt.wantAcquired {
				if got := gitAcquirer.calls(); got != 1 {
					t.Fatalf("git acquire calls = %d, want 1: %#v", got, gitAcquirer.requests)
				}
				if got := buildConfigMapValue(t, result, "demo"); got != "from-remote" {
					t.Fatalf("rendered value = %q, want from-remote", got)
				}
				return
			}
			if got := gitAcquirer.calls(); got != 0 {
				t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
			}
			if got := buildConfigMapValue(t, result, "demo"); got != "from-local-tree" {
				t.Fatalf("rendered value = %q, want from-local-tree", got)
			}
		})
	}
}

// Test 4 — revision gate on a render surface: ""/HEAD resolve locally; pinned
// commit SHAs and unrelated branches keep acquiring, and the acquired content
// (not the local tree) is what renders.
func TestBuildSelfRepoRevisionGate(t *testing.T) {
	for _, tt := range []struct {
		name           string
		targetRevision string
		wantAcquired   bool
	}{
		{name: "empty revision resolves locally", targetRevision: ""},
		{name: "HEAD resolves locally", targetRevision: "HEAD"},
		{name: "pinned SHA acquires remotely", targetRevision: "1111111111111111111111111111111111111111", wantAcquired: true},
		{name: "unrelated branch acquires remotely", targetRevision: "release-1.x", wantAcquired: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
			writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, tt.targetRevision, "from-local-tree")
			commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

			gitAcquirer := &countingGitAcquirer{err: errors.New("self ref must not be acquired")}
			if tt.wantAcquired {
				fetchedRoot := t.TempDir()
				writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "from-remote")
				gitAcquirer = &countingGitAcquirer{path: fetchedRoot, revision: "8888888888888888888888888888888888888888"}
			}
			result, err := (Orchestrator{
				GitAcquirer:   gitAcquirer,
				ChartAcquirer: newSelfRepoValueChartAcquirer(t),
			}).Build(context.Background(), BuildRequest{Path: root})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			wantCalls, wantValue := 0, "from-local-tree"
			if tt.wantAcquired {
				wantCalls, wantValue = 1, "from-remote"
			}
			if got := gitAcquirer.calls(); got != wantCalls {
				t.Fatalf("git acquire calls = %d, want %d: %#v", got, wantCalls, gitAcquirer.requests)
			}
			if got := buildConfigMapValue(t, result, "demo"); got != wantValue {
				t.Fatalf("rendered value = %q, want %q", got, wantValue)
			}
		})
	}
}

// Test 5 — OFFLINE render surface: self refs resolve before any acquirer, so
// --offline renders instead of failing with an offline cache miss.
func TestBuildSelfRepoRefRendersOffline(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", "main")
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "main", "from-local-tree")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

	result, err := (Orchestrator{
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).Build(context.Background(), BuildRequest{
		Path: root,
		AcquisitionOptions: AcquisitionOptions{
			Offline:     true,
			GitCacheDir: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := buildConfigMapValue(t, result, "demo"); got != "from-local-tree" {
		t.Fatalf("rendered value = %q, want from-local-tree", got)
	}
}

// Test 6a — NO-CLOBBER, ref-diff side shape: a .git-less root (as diff-side
// snapshots are) with pre-populated selfRepo refs must keep them. Unconditional
// re-detection would find no remotes, wipe the refs, and acquire.
func TestBuildPrepopulatedSelfRepoRefsPreservedOnGitlessRoot(t *testing.T) {
	root := t.TempDir() // .git-less: models a materialized ref-diff side snapshot
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "sidebranch", "from-snapshot")

	gitAcquirer := &countingGitAcquirer{err: errors.New("prepopulated self ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).Build(context.Background(), BuildRequest{
		Path: root,
		selfRepo: selfRepoRefs{
			urlKeys:   []string{sourcepkg.CanonicalGitURLKey(selfRepoRemoteURL)},
			revisions: []string{"sidebranch"},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := buildConfigMapValue(t, result, "demo"); got != "from-snapshot" {
		t.Fatalf("rendered value = %q, want from-snapshot", got)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// Test 6b — NO-CLOBBER, path-diff shape: both side checkouts carry a matching
// remote, but the revision gate name ("sidebranch") is known only from the
// LEFT checkout's HEAD symref. The per-side builds must preserve the
// diff-detected refs: wrongful re-detection on the right side would drop
// "sidebranch" (no symref there) and acquire instead of using the side tree.
func TestDiffPathSelfRepoBothSidesWithRemotesPreserveDiffDetectedRefs(t *testing.T) {
	right := t.TempDir()
	rightRepo, rightWt := initDiffGitRepo(t, right)
	if _, err := rightRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{selfRepoRemoteURL}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	writeSelfRepoRefValuesApp(t, right, "demo", selfRepoSpecURL, "sidebranch", "old")
	commitDiffGitRepo(t, rightRepo, rightWt, "baseline")

	left := t.TempDir()
	leftRepo, leftWt := initDiffGitRepo(t, left)
	if _, err := leftRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{selfRepoRemoteURL}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	setSelfRepoRemoteHeadSymref(t, leftRepo, "origin", "sidebranch")
	writeSelfRepoRefValuesApp(t, left, "demo", selfRepoSpecURL, "sidebranch", "old")
	commitDiffGitRepo(t, leftRepo, leftWt, "baseline")

	writeSelfRepoRefValuesFile(t, right, "demo", "new")
	commitDiffGitRepo(t, rightRepo, rightWt, "new values")

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).DiffApps(context.Background(), DiffRequest{
		LeftPath:         left,
		RightPath:        right,
		Unified:          3,
		ExecutionOptions: ExecutionOptions{Parallelism: 1},
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	assertSingleDiffContains(t, result, "-  value: old", "+  value: new")
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// Test 7 — REPO-MAP precedence: MappedPath resolution precedes the self-repo
// gate, so an explicit --repo-map wins even when the URL is a self reference.
// The mapped directory carries DIFFERENT content than the local tree because
// <url>=<local tree> could not discriminate the two resolutions.
func TestBuildSelfRepoRepoMapPrecedesSelfResolution(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", "main")
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "main", "from-local-tree")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

	mapped := t.TempDir()
	writeSelfRepoRefValuesFile(t, mapped, "demo", "from-repo-map")

	gitAcquirer := &countingGitAcquirer{err: errors.New("mapped repository must not be acquired")}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).Build(context.Background(), BuildRequest{
		Path: root,
		AcquisitionOptions: AcquisitionOptions{
			RecordCacheEvents: true,
			RepoMaps:          []sourcepkg.RepoMap{{URL: selfRepoRemoteURL, Path: mapped}},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := buildConfigMapValue(t, result, "demo"); got != "from-repo-map" {
		t.Fatalf("rendered value = %q, want from-repo-map", got)
	}
	if !hasCacheEvent(result.CacheEvents, "git", "mapped", "github.com/example/repo") {
		t.Fatalf("CacheEvents = %#v, want mapped git event for the repo-mapped self URL", result.CacheEvents)
	}
	if hasCacheEvent(result.CacheEvents, "git", "local", "github.com/example/repo") {
		t.Fatalf("CacheEvents = %#v, want no local git event when a repo-map covers the URL", result.CacheEvents)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// Test 8 — NEAR-MISS on a render surface: fork-shaped URLs warn with the
// surface-neutral wording, still acquire remotely, and stay strict-exempt.
// The post-dedupe user-visible count is asserted deliberately: on a
// single-tree build one provider chain emits once per URL across both apps.
func TestBuildSelfRepoForkNearMissSurfaceNeutralWarning(t *testing.T) {
	for _, tt := range []struct {
		name   string
		strict bool
	}{
		{name: "default"},
		{name: "strict still succeeds", strict: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := initSelfRepoDiffGitRepo(t, "https://github.com/me/repo.git")
			writeSelfRepoRefValuesApp(t, root, "demo", "https://github.com/upstream/repo", "HEAD", "unused")
			writeSelfRepoRefValuesApp(t, root, "second", "https://github.com/upstream/repo", "HEAD", "unused")

			fetchedRoot := t.TempDir()
			writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "from-remote")
			writeSelfRepoRefValuesFile(t, fetchedRoot, "second", "from-remote")
			gitAcquirer := &countingGitAcquirer{path: fetchedRoot, revision: "9999999999999999999999999999999999999999"}
			result, err := (Orchestrator{
				GitAcquirer:   gitAcquirer,
				ChartAcquirer: newSelfRepoValueChartAcquirer(t),
			}).Build(context.Background(), BuildRequest{
				Path:   root,
				Strict: tt.strict,
			})
			if err != nil {
				t.Fatalf("Build() error = %v (near-miss must stay strict-exempt)", err)
			}
			var nearMisses []diagnostic.Diagnostic
			for _, diag := range result.Diagnostics {
				if diag.Code == selfRepoNearMissCode {
					nearMisses = append(nearMisses, diag)
				}
			}
			if len(nearMisses) != 1 {
				t.Fatalf("near-miss diagnostics = %d, want exactly 1 post-dedupe across two apps: %#v", len(nearMisses), result.Diagnostics)
			}
			diag := nearMisses[0]
			if diag.Severity != diagnostic.SeverityWarning {
				t.Fatalf("near-miss severity = %s, want warning: %#v", diag.Severity, diag)
			}
			for _, fragment := range []string{
				"https://github.com/upstream/repo",
				"github.com/me/repo",
				"--repo-map",
				"resembles a remote of the local checkout",
				"may not reflect the local tree",
			} {
				if !strings.Contains(diag.Message, fragment) {
					t.Fatalf("near-miss message = %q, want fragment %q", diag.Message, fragment)
				}
			}
			for _, forbidden := range []string{"under diff", "diff side"} {
				if strings.Contains(diag.Message, forbidden) {
					t.Fatalf("near-miss message = %q, must not carry diff-only wording %q", diag.Message, forbidden)
				}
			}
			if got := gitAcquirer.calls(); got != 1 {
				t.Fatalf("git acquire calls = %d, want 1 (remote acquisition still attempted): %#v", got, gitAcquirer.requests)
			}
		})
	}
}

// Test 9 — DIRTY-WORKTREE persistent-cache pin: an uncommitted edit to a
// self-$repo value file between two cache-enabled builds must reflect in the
// second build (never serve the stale cached render).
func TestBuildSelfRepoDirtyWorktreeEditReflectedWithPersistentCache(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", "main")
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "main", "one")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

	request := BuildRequest{
		Path: root,
		RenderCacheOptions: RenderCacheOptions{
			RenderCacheEnabled: true,
			RenderCacheDir:     t.TempDir(),
			EngineFingerprint:  testEngineFingerprint(),
		},
	}
	orchestrator := Orchestrator{
		GitAcquirer:   &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")},
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}
	first, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}
	if got := buildConfigMapValue(t, first, "demo"); got != "one" {
		t.Fatalf("first rendered value = %q, want one", got)
	}

	writeSelfRepoRefValuesFile(t, root, "demo", "two") // uncommitted: dirty worktree

	second, err := orchestrator.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	if got := buildConfigMapValue(t, second, "demo"); got != "two" {
		t.Fatalf("second rendered value = %q, want the dirty-worktree edit (two), not a stale cached render", got)
	}
}

// Test 10 — GREEN-TO-RED pin: a self-URL source whose Path was deleted
// locally but still exists on the remote tip now fails path-not-found instead
// of silently rendering acquired content — correct desired-state semantics,
// documented as a behavior change.
func TestBuildSelfRepoLocallyMissingPathFailsInsteadOfAcquiring(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", "main")
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: `+selfRepoSpecURL+`
    targetRevision: main
    path: manifests/removed
  destination:
    name: in-cluster
    namespace: default
`)
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

	// The remote tip still has the path; previously it rendered via acquisition.
	fetchedRoot := t.TempDir()
	writeTestFile(t, filepath.Join(fetchedRoot, "manifests", "removed", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: removed
  namespace: default
data:
  value: remote-only
`)
	gitAcquirer := &countingGitAcquirer{path: fetchedRoot, revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	result, err := (Orchestrator{GitAcquirer: gitAcquirer}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatalf("Build() error = nil, want path-not-found failure for the locally deleted path: %#v", result.Manifests)
	}
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0 (self URL must resolve locally): %#v", got, gitAcquirer.requests)
	}
	failed := false
	for _, status := range result.Statuses {
		if status.Status == ApplicationStatusFail && strings.Contains(status.Message, "manifests/removed") {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("Statuses = %#v, want a FAIL naming manifests/removed", result.Statuses)
	}
}

// Test 11 — SUBDIR limitation pin: gitref helpers open the repository at the
// given --path without DetectDotGit walk-up, so a subdirectory path gets no
// self-resolution and the source acquires — the documented behavior (run from
// the checkout root or use --repo-map).
func TestBuildSelfRepoSubdirPathGetsNoSelfResolution(t *testing.T) {
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	setSelfRepoRemoteHeadSymref(t, fixture.repo, "origin", "main")
	deploy := filepath.Join(root, "deploy")
	writeSelfRepoRefValuesApp(t, deploy, "demo", selfRepoSpecURL, "main", "from-local-tree")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")

	fetchedRoot := t.TempDir()
	writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "from-remote")
	gitAcquirer := &countingGitAcquirer{path: fetchedRoot, revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	result, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).Build(context.Background(), BuildRequest{Path: deploy})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := gitAcquirer.calls(); got != 1 {
		t.Fatalf("git acquire calls = %d, want 1 (subdir paths do not self-resolve): %#v", got, gitAcquirer.requests)
	}
	if got := buildConfigMapValue(t, result, "demo"); got != "from-remote" {
		t.Fatalf("rendered value = %q, want from-remote", got)
	}
}
