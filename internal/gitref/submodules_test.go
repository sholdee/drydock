package gitref

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gittree/submoduletest"
	"github.com/sholdee/drydock/internal/source"
)

func requireSnapshotFile(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("snapshot lacks %s: %v", rel, err)
	}
}

// TestSnapshotChecksOutSubmodulesFromTheLocalStore pins that a snapshot of
// a Git ref holds the content of its submodules, nested ones included,
// read from the repository's own .git/modules store without an opener, so
// without the network.
func TestSnapshotChecksOutSubmodulesFromTheLocalStore(t *testing.T) {
	fixture := submoduletest.New(t)
	result, err := Snapshot(t.Context(), Request{
		Repo:       fixture.Work,
		Ref:        "HEAD",
		Submodules: &SubmoduleOptions{AllowFileTransport: true},
	})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	t.Cleanup(func() { _ = result.Cleanup() })
	requireSnapshotFile(t, result.Path, "mods/child/child/app/cm.yaml")
	requireSnapshotFile(t, result.Path, "mods/child/deep/deep/cm.yaml")
	if !slices.Equal(result.Gitlinks, []string{"mods/child"}) || !result.SubmodulesConfigured {
		t.Fatalf("Gitlinks = %q, configured = %v, want mods/child", result.Gitlinks, result.SubmodulesConfigured)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "mods", "child", ".git")); !os.IsNotExist(err) {
		t.Fatalf("stat submodule .git = %v, want no .git in the snapshot", err)
	}
}

// TestSnapshotFetchesSubmodulesMissingFromTheLocalStore pins that a
// submodule commit that .git/modules lacks comes from the opener (drydock's
// Git cache, and the network when online), and that offline with an empty
// cache the snapshot fails with an offline cache miss.
func TestSnapshotFetchesSubmodulesMissingFromTheLocalStore(t *testing.T) {
	fixture := submoduletest.New(t)
	plain := filepath.Join(fixture.Dir, "plain")
	submoduletest.Run(t, fixture.Dir, "clone", "-q", filepath.Join(fixture.Dir, "parent.git"), plain)

	cacheDir := t.TempDir()
	offline := source.GitOptions{CacheDir: cacheDir, Submodules: true, AllowFileSubmoduleTransport: true}
	_, err := Snapshot(t.Context(), Request{
		Repo:       plain,
		Ref:        "HEAD",
		Submodules: &SubmoduleOptions{Opener: source.SubmoduleCacheOpener(fixture.ParentURL, offline, nil), AllowFileTransport: true},
	})
	if err == nil || !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("offline Snapshot() error = %v, want an offline cache miss", err)
	}

	online := offline
	online.AllowNetwork = true
	result, err := Snapshot(t.Context(), Request{
		Repo:       plain,
		Ref:        "HEAD",
		Submodules: &SubmoduleOptions{Opener: source.SubmoduleCacheOpener(fixture.ParentURL, online, nil), AllowFileTransport: true},
	})
	if err != nil {
		t.Fatalf("online Snapshot() error = %v", err)
	}
	t.Cleanup(func() { _ = result.Cleanup() })
	requireSnapshotFile(t, result.Path, "mods/child/deep/deep/cm.yaml")
	if entries, _ := os.ReadDir(filepath.Join(plain, ".git", "modules")); len(entries) != 0 {
		t.Fatalf("the repository's .git/modules holds %d entries, want drydock to never write there", len(entries))
	}
}

// TestSnapshotWithoutSubmodulesKeepsEmptyGitlinks pins that a snapshot
// that is not asked for submodule content keeps each gitlink an empty
// directory and still lists the gitlinks.
func TestSnapshotWithoutSubmodulesKeepsEmptyGitlinks(t *testing.T) {
	fixture := submoduletest.New(t)
	result, err := Snapshot(t.Context(), Request{Repo: fixture.Work, Ref: "HEAD"})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	t.Cleanup(func() { _ = result.Cleanup() })
	entries, err := os.ReadDir(filepath.Join(result.Path, "mods", "child"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("mods/child = %d entries, %v; want an empty directory", len(entries), err)
	}
	if !slices.Equal(result.Gitlinks, []string{"mods/child"}) {
		t.Fatalf("Gitlinks = %q, want mods/child", result.Gitlinks)
	}
}
