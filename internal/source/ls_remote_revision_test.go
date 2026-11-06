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

// LsRemoteRevision mirrors nativeGitClient.lsRemote in Argo CD v3.6.1
// (util/git/client.go:961-1027) for the commit drydock renders.
func TestLsRemoteRevision(t *testing.T) {
	fixture := createGitFixture(t)
	first := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: one\n")
	commit := commitFixtureFile(t, fixture.repo, fixture.worktree, "config.yaml", "version: two\n")
	annotated := createAnnotatedFixtureTag(t, fixture.repo, "v1.0.0", commit)
	if _, err := fixture.repo.CreateTag("light", commit, nil); err != nil {
		t.Fatalf("CreateTag(light) error = %v", err)
	}
	createAnnotatedFixtureTag(t, fixture.repo, "old", first)
	// A branch and an annotated tag with the same name: the branch wins.
	if err := fixture.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("same"), commit)); err != nil {
		t.Fatalf("SetReference(branch same) error = %v", err)
	}
	createAnnotatedFixtureTag(t, fixture.repo, "same", commit)

	sha := commit.String()
	short := sha[:9]
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
		{name: "annotated tag", revision: "v1.0.0", want: annotated.String()},
		{name: "full annotated tag ref", revision: "refs/tags/v1.0.0", want: annotated.String()},
		{name: "lightweight tag", revision: "light", want: sha},
		{name: "branch wins over tag", revision: "same", want: sha},
		{name: "tag of another commit", revision: "old", want: sha},
		{name: "abbreviated SHA as written", revision: short, want: short},
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

// The Git acquirer reports the ls-remote form of a fetched annotated tag
// online and from the offline cache, from the tag objects the clone fetched.
func TestDefaultGitAcquirerReportsLsRemoteRevisionOfAnnotatedTag(t *testing.T) {
	remote := createGitFixture(t)
	commit := commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: tag\n")
	annotated := createAnnotatedFixtureTag(t, remote.repo, "v1.0.0", commit)
	commitFixtureFile(t, remote.repo, remote.worktree, "config.yaml", "version: later\n")
	request := GitRequest{URL: "file://" + filepath.ToSlash(remote.path), Revision: "v1.0.0"}
	cacheDir := t.TempDir()

	for _, allowNetwork := range []bool{true, false} {
		result, err := DefaultGitAcquirer{}.Acquire(context.Background(), request, GitOptions{AllowNetwork: allowNetwork, CacheDir: cacheDir})
		if err != nil {
			t.Fatalf("Acquire(network %t) error = %v", allowNetwork, err)
		}
		if result.Revision != commit.String() || result.LsRemoteRevision != annotated.String() {
			t.Fatalf("Acquire(network %t) Revision, LsRemoteRevision = %s, %s; want %s, %s", allowNetwork, result.Revision, result.LsRemoteRevision, commit, annotated)
		}
	}
}
