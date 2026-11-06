package source

import (
	"context"
	"fmt"
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
			hash, err := resolveCheckoutRevision(fixture.repo, tc.revision, "")
			if err != nil {
				t.Fatalf("resolveCheckoutRevision(%q) error = %v", tc.revision, err)
			}
			if hash.String() != tc.checkout {
				t.Fatalf("resolveCheckoutRevision(%q) = %s, want %s", tc.revision, hash, tc.checkout)
			}
			if got := LsRemoteRevision(fixture.repo, tc.revision, hash.String(), ""); got != tc.want {
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
	// git checkout of an abbreviated annotated tag object checks out the
	// commit the tag points to.
	tagObject := createAnnotatedFixtureTag(t, fixture.repo, "v1.0.0", first)
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
		{name: "abbreviated annotated tag object", repo: fixture.repo, revision: tagObject.String()[:10], single: first.String(), multi: tagObject.String()[:10], ok: true},
		{name: "abbreviated SHA that names no commit", repo: fixture.repo, revision: "abcdef0123", single: "", multi: "abcdef0123", ok: true},
		{name: "hex branch name", repo: fixture.repo, revision: hexBranch, single: commit.String(), multi: commit.String(), ok: true},
		{name: "branch", repo: fixture.repo, revision: "master"},
		{name: "HEAD", repo: fixture.repo, revision: "HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			single, multi, ok, err := PinnedRevision(tc.repo, tc.revision, "")
			if err != nil || single != tc.single || multi != tc.multi || ok != tc.ok {
				t.Fatalf("PinnedRevision(%q) = %q, %q, %t, %v; want %q, %q, %t", tc.revision, single, multi, ok, err, tc.single, tc.multi, tc.ok)
			}
		})
	}
}

// An abbreviation that matches more than one commit or tag fails, as git
// checkout fails with "short object ID ... is ambiguous". Seven hex digits
// cannot be made to collide in a test, so this uses a one-digit prefix that
// two of many commits share.
func TestResolveAbbreviatedCommitFailsWhenAmbiguous(t *testing.T) {
	fixture := createGitFixture(t)
	byFirstDigit := map[byte][]plumbing.Hash{}
	var shared byte
	for i := 0; i < 64 && shared == 0; i++ {
		hash := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", fmt.Sprintf("version: %d\n", i))
		digit := hash.String()[0]
		byFirstDigit[digit] = append(byFirstDigit[digit], hash)
		if len(byFirstDigit[digit]) == 2 {
			shared = digit
		}
	}
	if shared == 0 {
		t.Fatal("no two commits share a first hex digit")
	}
	if _, _, err := resolveAbbreviatedCommit(fixture.repo, string(shared)); err == nil || !strings.Contains(err.Error(), "is ambiguous") {
		t.Fatalf("resolveAbbreviatedCommit(%q) error = %v, want ambiguous", string(shared), err)
	}
	unique := byFirstDigit[shared][0]
	hash, found, err := resolveAbbreviatedCommit(fixture.repo, unique.String()[:12])
	if err != nil || !found || hash != unique {
		t.Fatalf("resolveAbbreviatedCommit(unique) = %s, %t, %v; want %s", hash, found, err, unique)
	}
}

// ls-remote matches ref names first, also for a short name of hex digits
// such as the tags `c` and `2024`. go-git would prefer a commit whose SHA
// starts with the name.
func TestResolveCheckoutRevisionPrefersShortHexRefNames(t *testing.T) {
	fixture := createGitFixture(t)
	tagged := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: tagged\n")
	var prefixed plumbing.Hash
	for i := 0; i < 256 && prefixed.IsZero(); i++ {
		hash := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", fmt.Sprintf("version: %d\n", i))
		if strings.HasPrefix(hash.String(), "c") && hash != tagged {
			prefixed = hash
		}
	}
	if prefixed.IsZero() {
		t.Fatal("no commit SHA starts with c")
	}
	for _, name := range []string{"c", "2024"} {
		createAnnotatedFixtureTag(t, fixture.repo, name, tagged)
		hash, err := resolveCheckoutRevision(fixture.repo, name, "")
		if err != nil || *hash != tagged {
			t.Fatalf("resolveCheckoutRevision(%q) = %v, %v; want the tag commit %s", name, hash, err, tagged)
		}
	}
	if _, err := resolveCheckoutRevision(fixture.repo, "cafe", ""); err == nil {
		t.Fatal("resolveCheckoutRevision(cafe) error = nil, want unable to resolve a short hex name that names no ref")
	}
}

// A branch of the remote whose URL is the repoURL of the source wins. When
// no remote has that URL, the remotes are tried in name order.
func TestLsRemoteRefCommitPicksTheRemoteOfTheRepoURL(t *testing.T) {
	fixture := createGitFixture(t)
	older := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: older\n")
	newer := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: newer\n")
	for _, remote := range []struct {
		name, url string
		hash      plumbing.Hash
	}{
		{name: "aaa", url: "https://example.invalid/other.git", hash: older},
		{name: "zzz", url: "git@example.invalid:org/app.git", hash: newer},
	} {
		if _, err := fixture.repo.CreateRemote(&config.RemoteConfig{Name: remote.name, URLs: []string{remote.url}}); err != nil {
			t.Fatalf("CreateRemote(%s) error = %v", remote.name, err)
		}
		if err := fixture.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewRemoteReferenceName(remote.name, "deadbeef1"), remote.hash)); err != nil {
			t.Fatalf("SetReference(%s/deadbeef1) error = %v", remote.name, err)
		}
	}
	if hash, ok := lsRemoteRefCommit(fixture.repo, "deadbeef1", "https://example.invalid/org/app"); !ok || hash != newer {
		t.Fatalf("lsRemoteRefCommit(matching URL) = %s, %t; want %s", hash, ok, newer)
	}
	if hash, ok := lsRemoteRefCommit(fixture.repo, "deadbeef1", "https://example.invalid/unknown.git"); !ok || hash != older {
		t.Fatalf("lsRemoteRefCommit(no matching URL) = %s, %t; want the first remote by name %s", hash, ok, older)
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
