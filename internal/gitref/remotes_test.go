package gitref

import (
	"reflect"
	"sort"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestRemoteURLsListsEveryRemoteURL(t *testing.T) {
	repoPath := t.TempDir()
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/me/repo.git", "git@github.com:me/repo.git"},
	}); err != nil {
		t.Fatalf("CreateRemote(origin) error = %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "upstream",
		URLs: []string{"https://github.com/upstream/repo.git"},
	}); err != nil {
		t.Fatalf("CreateRemote(upstream) error = %v", err)
	}

	urls := RemoteURLs(repoPath)
	sort.Strings(urls)
	want := []string{
		"git@github.com:me/repo.git",
		"https://github.com/me/repo.git",
		"https://github.com/upstream/repo.git",
	}
	if !reflect.DeepEqual(urls, want) {
		t.Fatalf("RemoteURLs() = %#v, want %#v", urls, want)
	}
}

func TestRemoteURLsNonGitDirReturnsNil(t *testing.T) {
	if urls := RemoteURLs(t.TempDir()); urls != nil {
		t.Fatalf("RemoteURLs(non-git dir) = %#v, want nil", urls)
	}
	if urls := RemoteURLs(""); urls != nil {
		t.Fatalf("RemoteURLs(empty) = %#v, want nil", urls)
	}
	if urls := RemoteURLs("   "); urls != nil {
		t.Fatalf("RemoteURLs(blank) = %#v, want nil", urls)
	}
}

func TestRemoteURLsRepositoryWithoutRemotesReturnsEmpty(t *testing.T) {
	repoPath := t.TempDir()
	if _, err := git.PlainInit(repoPath, false); err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if urls := RemoteURLs(repoPath); len(urls) != 0 {
		t.Fatalf("RemoteURLs(no remotes) = %#v, want empty", urls)
	}
}

func TestDefaultBranchNamesReadsRemoteHEADSymrefs(t *testing.T) {
	repoPath := t.TempDir()
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/me/repo.git"},
	}); err != nil {
		t.Fatalf("CreateRemote(origin) error = %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "upstream",
		URLs: []string{"https://github.com/upstream/repo.git"},
	}); err != nil {
		t.Fatalf("CreateRemote(upstream) error = %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.ReferenceName("refs/remotes/origin/HEAD"),
		plumbing.ReferenceName("refs/remotes/origin/main"),
	)); err != nil {
		t.Fatalf("SetReference(origin/HEAD) error = %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.ReferenceName("refs/remotes/upstream/HEAD"),
		plumbing.ReferenceName("refs/remotes/upstream/release-1.x"),
	)); err != nil {
		t.Fatalf("SetReference(upstream/HEAD) error = %v", err)
	}

	names := DefaultBranchNames(repoPath)
	sort.Strings(names)
	want := []string{"main", "release-1.x"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("DefaultBranchNames() = %#v, want %#v", names, want)
	}
}

func TestDefaultBranchNamesWithoutSymrefReturnsNil(t *testing.T) {
	repoPath := t.TempDir()
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/me/repo.git"},
	}); err != nil {
		t.Fatalf("CreateRemote(origin) error = %v", err)
	}
	if names := DefaultBranchNames(repoPath); names != nil {
		t.Fatalf("DefaultBranchNames() = %#v, want nil", names)
	}
}

func TestDefaultBranchNamesNonGitDirReturnsNil(t *testing.T) {
	if names := DefaultBranchNames(t.TempDir()); names != nil {
		t.Fatalf("DefaultBranchNames() = %#v, want nil", names)
	}
}
