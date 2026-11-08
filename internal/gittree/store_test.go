package gittree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// lostTreeStore writes, at path, a bare store whose branch main points to
// commit and that holds the commit object but not its tree, with origin
// set to url.
func lostTreeStore(t *testing.T, path, url, source string, commit plumbing.Hash) {
	t.Helper()
	from, err := git.PlainOpen(source)
	if err != nil {
		t.Fatal(err)
	}
	object, err := from.Storer.EncodedObject(plumbing.CommitObject, commit)
	if err != nil {
		t.Fatal(err)
	}
	store, err := git.PlainInit(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Storer.SetEncodedObject(object); err != nil {
		t.Fatal(err)
	}
	if err := store.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", commit)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}, Fetch: []gitconfig.RefSpec{"+refs/heads/*:refs/heads/*"}}); err != nil {
		t.Fatal(err)
	}
	if hasCommit(store, commit) {
		t.Fatal("hasCommit() = true for a store without the tree, want false")
	}
}

// TestOpenSubmoduleStoreHealsALostTree pins that a store that holds the
// commit but lost its tree, which every fetch answers with "already up to
// date", is cloned again instead of failing forever.
func TestOpenSubmoduleStoreHealsALostTree(t *testing.T) {
	fixture := submoduletest.New(t)
	commit := plumbing.NewHash(fixture.GrandchildCommit)
	path := filepath.Join(t.TempDir(), "store")
	lostTreeStore(t, path, fixture.GrandchildURL, filepath.Join(fixture.Dir, "grandchild.git"), commit)
	repo, fetched, err := OpenSubmoduleStore(context.Background(), StoreRequest{Path: path, URL: fixture.GrandchildURL, Commit: commit, Transport: "file", AllowNetwork: true})
	if err != nil || !fetched || !hasCommit(repo, commit) {
		t.Fatalf("OpenSubmoduleStore() = fetched %t, error %v; want the store healed", fetched, err)
	}
}

// TestOpenSubmoduleStoreRemovesStaleCloneSiblings pins that a clone
// removes the "<entry>.clone-*" directories that a killed process left and
// that nothing changed for StaleCloneAge, and keeps a fresh one.
func TestOpenSubmoduleStoreRemovesStaleCloneSiblings(t *testing.T) {
	fixture := submoduletest.New(t)
	path := filepath.Join(t.TempDir(), "store")
	stale, fresh := path+".clone-1", path+".clone-2"
	for _, dir := range []string{stale, fresh} {
		if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * StaleCloneAge)
	for _, p := range []string{filepath.Join(stale, "objects"), stale} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := OpenSubmoduleStore(context.Background(), StoreRequest{Path: path, URL: fixture.GrandchildURL, Commit: plumbing.NewHash(fixture.GrandchildCommit), Transport: "file", AllowNetwork: true}); err != nil {
		t.Fatalf("OpenSubmoduleStore() error = %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stat stale clone = %v, want it removed", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("stat fresh clone = %v, want it kept", err)
	}
}

// TestScrubRemoteUserinfoCleansOldEntries pins that an entry whose remote
// URL holds userinfo, as an older drydock wrote it, loses it.
func TestScrubRemoteUserinfoCleansOldEntries(t *testing.T) {
	repo, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{"https://ci:" + secretMarker + "@host/org/repo.git"}}); err != nil {
		t.Fatal(err)
	}
	if err := ScrubRemoteUserinfo(repo); err != nil {
		t.Fatal(err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Remotes["origin"].URLs[0]; got != "https://host/org/repo.git" {
		t.Fatalf("origin URL = %q, want the clean URL", got)
	}
}
