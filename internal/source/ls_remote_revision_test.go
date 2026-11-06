package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func createAnnotatedFixtureTag(t *testing.T, repo *git.Repository, name string, hash plumbing.Hash) plumbing.Hash {
	t.Helper()
	ref, err := repo.CreateTag(name, hash, &git.CreateTagOptions{
		Tagger:  &object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(2, 0)},
		Message: "annotated " + name,
	})
	if err != nil {
		t.Fatalf("CreateTag(%s) error = %v", name, err)
	}
	if ref.Hash() == hash {
		t.Fatalf("annotated tag %s holds the commit hash, want a tag object", name)
	}
	return ref.Hash()
}

// LsRemoteRevision mirrors nativeGitClient.lsRemote and listRemote in Argo
// CD v3.6.1 (util/git/client.go:961-1027, util/git/workaround.go:63-98) for
// the commit that the fetch path checks out (resolveCheckoutRevision).
// listRemote peels annotated tags, so only a full or abbreviated SHA keeps
// its written form.
func TestLsRemoteRevision(t *testing.T) {
	fixture := createGitFixture(t)
	first := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: one\n")
	commit := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: two\n")
	createAnnotatedFixtureTag(t, fixture.repo, "v1.0.0", commit)
	if _, err := fixture.repo.CreateTag("light", commit, nil); err != nil {
		t.Fatalf("CreateTag(light) error = %v", err)
	}
	sha := commit.String()
	// A branch whose name is an abbreviation of the first commit but which
	// points to the second: ls-remote matches the ref name first, so the
	// checkout and the reported value follow the branch.
	hexBranch := first.String()[:8]
	if err := fixture.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(hexBranch), commit)); err != nil {
		t.Fatalf("SetReference(branch %s) error = %v", hexBranch, err)
	}
	// The same through a remote that is not origin.
	hexRemoteBranch := first.String()[:11]
	if _, err := fixture.repo.CreateRemote(&config.RemoteConfig{Name: "upstream", URLs: []string{"https://example.invalid/upstream.git"}}); err != nil {
		t.Fatalf("CreateRemote(upstream) error = %v", err)
	}
	if err := fixture.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewRemoteReferenceName("upstream", hexRemoteBranch), commit)); err != nil {
		t.Fatalf("SetReference(upstream/%s) error = %v", hexRemoteBranch, err)
	}
	for _, tc := range []struct {
		name     string
		revision string
		checkout string
		want     string
	}{
		{name: "full SHA as written", revision: strings.ToUpper(sha), checkout: sha, want: strings.ToUpper(sha)},
		{name: "branch", revision: "master", checkout: sha, want: sha},
		{name: "full branch ref", revision: "refs/heads/master", checkout: sha, want: sha},
		{name: "annotated tag peeled", revision: "v1.0.0", checkout: sha, want: sha},
		{name: "full annotated tag ref peeled", revision: "refs/tags/v1.0.0", checkout: sha, want: sha},
		{name: "lightweight tag", revision: "light", checkout: sha, want: sha},
		{name: "abbreviated SHA as written", revision: sha[:9], checkout: sha, want: sha[:9]},
		{name: "abbreviated SHA upper case as written", revision: strings.ToUpper(sha[:9]), checkout: sha, want: strings.ToUpper(sha[:9])},
		{name: "abbreviated SHA of an older commit", revision: first.String()[:9], checkout: first.String(), want: first.String()[:9]},
		{name: "hex branch name wins over the commit", revision: hexBranch, checkout: sha, want: sha},
		{name: "hex branch of another remote wins over the commit", revision: hexRemoteBranch, checkout: sha, want: sha},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := resolveCheckoutRevision(fixture.repo, tc.revision)
			if err != nil {
				t.Fatalf("resolveCheckoutRevision(%q) error = %v", tc.revision, err)
			}
			if hash.String() != tc.checkout {
				t.Fatalf("resolveCheckoutRevision(%q) = %s, want %s", tc.revision, hash, tc.checkout)
			}
			if got := LsRemoteRevision(fixture.repo, tc.revision, hash.String()); got != tc.want {
				t.Fatalf("LsRemoteRevision(%q) = %q, want %q", tc.revision, got, tc.want)
			}
		})
	}
}

// PinnedRevision gives the value the repo-server reports for a SHA pin,
// for a local tree that drydock renders without checking out the pin.
func TestPinnedRevision(t *testing.T) {
	fixture := createGitFixture(t)
	first := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: one\n")
	commit := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: two\n")
	hexBranch := first.String()[:8]
	if err := fixture.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(hexBranch), commit)); err != nil {
		t.Fatalf("SetReference(branch %s) error = %v", hexBranch, err)
	}
	upper := strings.ToUpper(first.String())
	for _, tc := range []struct {
		name          string
		repo          *git.Repository
		revision      string
		single, multi string
		ok            bool
	}{
		{name: "full SHA", repo: fixture.repo, revision: upper, single: first.String(), multi: upper, ok: true},
		{name: "full SHA without a repository", revision: upper, single: first.String(), multi: upper, ok: true},
		{name: "abbreviated SHA of an older commit", repo: fixture.repo, revision: first.String()[:10], single: first.String(), multi: first.String()[:10], ok: true},
		{name: "abbreviated SHA that names no commit", repo: fixture.repo, revision: "abcdef0123", single: "", multi: "abcdef0123", ok: true},
		{name: "hex branch name", repo: fixture.repo, revision: hexBranch, single: commit.String(), multi: commit.String(), ok: true},
		{name: "branch", repo: fixture.repo, revision: "master"},
		{name: "HEAD", repo: fixture.repo, revision: "HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			single, multi, ok := PinnedRevision(tc.repo, tc.revision)
			if single != tc.single || multi != tc.multi || ok != tc.ok {
				t.Fatalf("PinnedRevision(%q) = %q, %q, %t; want %q, %q, %t", tc.revision, single, multi, ok, tc.single, tc.multi, tc.ok)
			}
		})
	}
}

// The Git acquirer reports the ls-remote form online and from the offline
// cache: an abbreviated SHA as written, and the commit for an annotated tag.
func TestDefaultGitAcquirerReportsLsRemoteRevision(t *testing.T) {
	remote := createGitFixture(t)
	commit := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: tag\n")
	createAnnotatedFixtureTag(t, remote.repo, "v1.0.0", commit)
	commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: later\n")
	repoURL := "file://" + filepath.ToSlash(remote.path)
	short := commit.String()[:10]
	cacheDir := t.TempDir()

	for _, tc := range []struct{ revision, want string }{
		{revision: "v1.0.0", want: commit.String()},
		{revision: short, want: short},
	} {
		for _, allowNetwork := range []bool{true, false} {
			result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repoURL, Revision: tc.revision}, GitOptions{AllowNetwork: allowNetwork, CacheDir: cacheDir})
			if err != nil {
				t.Fatalf("Acquire(%s, network %t) error = %v", tc.revision, allowNetwork, err)
			}
			if result.Revision != commit.String() || result.LsRemoteRevision != tc.want {
				t.Fatalf("Acquire(%s, network %t) Revision, LsRemoteRevision = %s, %s; want %s, %s", tc.revision, allowNetwork, result.Revision, result.LsRemoteRevision, commit, tc.want)
			}
		}
	}
}

// A fetched source whose revision is both a branch name and an abbreviation
// of another commit checks out the branch, as the repo-server does: its
// ls-remote matches ref names before it takes a hex revision as a SHA.
func TestDefaultGitAcquirerPrefersHexBranchOverCommitPrefix(t *testing.T) {
	remote := createGitFixture(t)
	older := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: older\n")
	newer := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: newer\n")
	hexBranch := older.String()[:8]
	if err := remote.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(hexBranch), newer)); err != nil {
		t.Fatalf("SetReference(branch %s) error = %v", hexBranch, err)
	}
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: "file://" + filepath.ToSlash(remote.path), Revision: hexBranch}, GitOptions{AllowNetwork: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Revision != newer.String() || result.LsRemoteRevision != newer.String() {
		t.Fatalf("Revision, LsRemoteRevision = %s, %s; want the branch commit %s", result.Revision, result.LsRemoteRevision, newer)
	}
}
