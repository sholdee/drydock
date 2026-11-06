package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
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
// the commit drydock renders. listRemote peels annotated tags, so only a
// full or abbreviated SHA keeps its written form.
func TestLsRemoteRevision(t *testing.T) {
	fixture := createGitFixture(t)
	first := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: one\n")
	commit := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: two\n")
	createAnnotatedFixtureTag(t, fixture.repo, "v1.0.0", commit)
	if _, err := fixture.repo.CreateTag("light", commit, nil); err != nil {
		t.Fatalf("CreateTag(light) error = %v", err)
	}
	sha := commit.String()
	short := sha[:9]
	// A branch whose name looks like an abbreviated SHA: ls-remote matches
	// the ref name first.
	hexBranch := sha[:8]
	if err := fixture.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(hexBranch), commit)); err != nil {
		t.Fatalf("SetReference(branch %s) error = %v", hexBranch, err)
	}
	for _, tc := range []struct {
		name     string
		revision string
		want     string
	}{
		{name: "full SHA as written", revision: strings.ToUpper(sha), want: strings.ToUpper(sha)},
		{name: "HEAD", revision: "HEAD", want: sha},
		{name: "empty", revision: "", want: sha},
		{name: "branch", revision: "master", want: sha},
		{name: "full branch ref", revision: "refs/heads/master", want: sha},
		{name: "annotated tag peeled", revision: "v1.0.0", want: sha},
		{name: "full annotated tag ref peeled", revision: "refs/tags/v1.0.0", want: sha},
		{name: "lightweight tag", revision: "light", want: sha},
		{name: "abbreviated SHA as written", revision: short, want: short},
		{name: "abbreviated SHA upper case as written", revision: strings.ToUpper(short), want: strings.ToUpper(short)},
		{name: "hex branch name", revision: hexBranch, want: sha},
		{name: "abbreviated SHA of another commit", revision: first.String()[:9], want: sha},
		{name: "unknown name", revision: "missing", want: sha},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LsRemoteRevision(fixture.repo, tc.revision, sha); got != tc.want {
				t.Fatalf("LsRemoteRevision(%q) = %q, want %q", tc.revision, got, tc.want)
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
