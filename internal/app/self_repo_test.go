package app

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func initSelfRepoRemoteDir(t *testing.T, remotes map[string][]string) string {
	t.Helper()
	repoPath := t.TempDir()
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: name, URLs: remotes[name]}); err != nil {
			t.Fatalf("CreateRemote(%s) error = %v", name, err)
		}
	}
	return repoPath
}

func TestDetectSelfRepoRefsUnionsRemotesAcrossRepoAndSides(t *testing.T) {
	repoPath := initSelfRepoRemoteDir(t, map[string][]string{
		"origin": {"https://github.com/me/repo.git"},
	})
	left := initSelfRepoRemoteDir(t, map[string][]string{
		"origin":   {"https://github.com/me/repo.git"}, // duplicate of repoPath's — must dedupe
		"upstream": {"https://github.com/upstream/repo.git"},
	})
	right := t.TempDir() // non-git: contributes nothing

	refs := detectSelfRepoRefs(DiffRequest{
		LeftPath:  left,
		RightPath: right,
		Ref:       "feature",
		RefOrig:   "master",
	}, repoPath)

	gotKeys := append([]string(nil), refs.urlKeys...)
	sort.Strings(gotKeys)
	wantKeys := []string{"github.com/me/repo", "github.com/upstream/repo"}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("urlKeys = %#v, want %#v", gotKeys, wantKeys)
	}
	if !reflect.DeepEqual(refs.revisions, []string{"feature", "master"}) {
		t.Fatalf("revisions = %#v, want [feature master]", refs.revisions)
	}
}

func TestDetectSelfRepoRefsFiltersRevisions(t *testing.T) {
	repoPath := initSelfRepoRemoteDir(t, map[string][]string{
		"origin": {"https://github.com/me/repo.git"},
	})

	shaRefs := detectSelfRepoRefs(DiffRequest{
		Ref:     "0123456789abcdef0123456789abcdef01234567",
		RefOrig: "HEAD",
	}, repoPath)
	if len(shaRefs.revisions) != 0 {
		t.Fatalf("revisions = %#v, want none for SHA and HEAD refs", shaRefs.revisions)
	}

	dupRefs := detectSelfRepoRefs(DiffRequest{
		Ref:     "  feature  ",
		RefOrig: "feature",
	}, repoPath)
	if !reflect.DeepEqual(dupRefs.revisions, []string{"feature"}) {
		t.Fatalf("revisions = %#v, want deduped [feature]", dupRefs.revisions)
	}

	if refs := detectSelfRepoRefs(DiffRequest{Ref: "feature"}, t.TempDir()); len(refs.urlKeys) != 0 {
		t.Fatalf("urlKeys = %#v, want none for non-git paths", refs.urlKeys)
	}
}

func TestSelfRepoRefsCloneIsolatesSlices(t *testing.T) {
	original := selfRepoRefs{
		urlKeys:   []string{"github.com/me/repo"},
		revisions: []string{"feature"},
	}
	cloned := original.clone()
	cloned.urlKeys[0] = "mutated"
	cloned.revisions[0] = "mutated"
	if original.urlKeys[0] != "github.com/me/repo" || original.revisions[0] != "feature" {
		t.Fatalf("clone shares backing arrays: %#v", original)
	}
}

// countingRemoteAcquirer is a mutex-guarded call-counting remote fake, safe
// under -race regardless of parallelism.
type countingRemoteAcquirer struct {
	mu       sync.Mutex
	path     string
	err      error
	requests []remote.Request
}

func (a *countingRemoteAcquirer) Acquire(_ context.Context, request remote.Request, _ remote.Options) (remote.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, request)
	if a.err != nil {
		return remote.Result{}, a.err
	}
	return remote.Result{Path: a.path, URL: request.URL}, nil
}

func (a *countingRemoteAcquirer) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func newSelfMapRemoteTestProvider(t *testing.T) localProvider {
	t.Helper()
	return localProvider{
		repoRoot:     t.TempDir(),
		rootIdentity: SourceIdentity{Kind: sourceIdentityKindRoot, Revision: "5555555555555555555555555555555555555555"},
		selfRepoURLKeys: map[string]struct{}{
			sourcepkg.CanonicalGitURLKey(selfRepoRemoteURL): {},
		},
	}
}

func TestSelfMapRemoteAcquirerServesSideRootForSelfGitBase(t *testing.T) {
	provider := newSelfMapRemoteTestProvider(t)
	delegate := &countingRemoteAcquirer{path: t.TempDir()}
	acquirer := provider.selfMapRemote(delegate)

	result, err := acquirer.Acquire(context.Background(), remote.Request{
		URL:      "git::" + selfRepoRemoteURL + "//manifests/demo?ref=HEAD",
		Kind:     remote.RequestGitRepo,
		RepoURL:  selfRepoRemoteURL,
		Revision: "HEAD",
	}, remote.Options{})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Path != provider.repoRoot {
		t.Fatalf("Path = %q, want side root %q", result.Path, provider.repoRoot)
	}
	if !result.FromCache {
		t.Fatal("FromCache = false, want true (no phantom network event)")
	}
	if result.Revision != provider.rootIdentity.Revision {
		t.Fatalf("Revision = %q, want side revision %q", result.Revision, provider.rootIdentity.Revision)
	}
	if result.Release == nil {
		t.Fatal("Release = nil, want idempotent no-op")
	}
	result.Release()
	result.Release()
	if got := delegate.calls(); got != 0 {
		t.Fatalf("delegate calls = %d, want 0", got)
	}
}

func TestSelfMapRemoteAcquirerDelegatesNonSelfRequests(t *testing.T) {
	provider := newSelfMapRemoteTestProvider(t)

	for _, tt := range []struct {
		name    string
		request remote.Request
	}{
		{
			name: "pinned SHA delegates",
			request: remote.Request{
				URL:      "git::" + selfRepoRemoteURL + "//manifests/demo?ref=0123456789abcdef0123456789abcdef01234567",
				Kind:     remote.RequestGitRepo,
				RepoURL:  selfRepoRemoteURL,
				Revision: "0123456789abcdef0123456789abcdef01234567",
			},
		},
		{
			name: "http file delegates",
			request: remote.Request{
				URL:  "https://raw.example.test/resource.yaml",
				Kind: remote.RequestHTTPFile,
			},
		},
		{
			name: "non-self git repo delegates",
			request: remote.Request{
				URL:      "git::https://github.com/other/unrelated.git//dir?ref=HEAD",
				Kind:     remote.RequestGitRepo,
				RepoURL:  "https://github.com/other/unrelated.git",
				Revision: "HEAD",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			delegate := &countingRemoteAcquirer{path: t.TempDir()}
			acquirer := provider.selfMapRemote(delegate)
			result, err := acquirer.Acquire(context.Background(), tt.request, remote.Options{})
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			if result.Path != delegate.path {
				t.Fatalf("Path = %q, want delegate path %q", result.Path, delegate.path)
			}
			if got := delegate.calls(); got != 1 {
				t.Fatalf("delegate calls = %d, want 1", got)
			}
		})
	}
}

func TestSelfMapRemoteEmptyKeysReturnsDelegateUnchanged(t *testing.T) {
	provider := localProvider{repoRoot: t.TempDir()}
	delegate := &countingRemoteAcquirer{path: t.TempDir()}
	if got := provider.selfMapRemote(delegate); got != remote.Acquirer(delegate) {
		t.Fatalf("selfMapRemote() = %T, want the delegate unchanged", got)
	}
}

// Single-side control: Build never populates selfRepo, so a self-URL ref
// source still acquires remotely even when the build root's own remotes match
// the spec URL — the inertness pin for build/test/pkg/drydock.
func TestBuildSelfRepoURLRefSourceStillAcquiresOutsideDiffs(t *testing.T) {
	root, _ := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeSelfRepoRefValuesApp(t, root, "demo", selfRepoSpecURL, "HEAD", "from-current-root")

	fetchedRoot := t.TempDir()
	writeSelfRepoRefValuesFile(t, fetchedRoot, "demo", "from-remote")
	gitAcquirer := &countingGitAcquirer{path: fetchedRoot, revision: "6666666666666666666666666666666666666666"}
	_, err := (Orchestrator{
		GitAcquirer:   gitAcquirer,
		ChartAcquirer: newSelfRepoValueChartAcquirer(t),
	}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := gitAcquirer.calls(); got != 1 {
		t.Fatalf("git acquire calls = %d, want 1 (self mapping must stay dead outside diffs): %#v", got, gitAcquirer.requests)
	}
}
