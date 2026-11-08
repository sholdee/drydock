package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

func submoduleOptions(cacheDir string, network, submodules bool) GitOptions {
	return GitOptions{AllowNetwork: network, CacheDir: cacheDir, Submodules: submodules, AllowFileSubmoduleTransport: true}
}

func requireFile(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("checkout lacks %s: %v", rel, err)
	}
}

func requireEmptyDir(t *testing.T, root, rel string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("checkout lacks the directory %s: %v", rel, err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %s holds %d entries, want it empty", rel, len(entries))
	}
}

// TestDefaultGitAcquirerChecksOutSubmodulesRecursively pins that a fetched
// Git source holds the content of its submodules, nested ones included, at
// the commits its tree pins, as `git submodule update --init --recursive`
// leaves the Argo CD repo-server's checkout, with relative submodule URLs
// resolved against the source URL.
func TestDefaultGitAcquirerChecksOutSubmodulesRecursively(t *testing.T) {
	fixture := submoduletest.New(t)
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: fixture.ParentURL, Revision: "main"}, submoduleOptions(t.TempDir(), true, true))
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	requireFile(t, result.Path, "apps/top.yaml")
	requireFile(t, result.Path, "mods/child/child/app/cm.yaml")
	requireFile(t, result.Path, "mods/child/deep/deep/cm.yaml")
	if !result.Network {
		t.Fatal("Network = false, want the clone and submodule fetches reported")
	}
}

// TestDefaultGitAcquirerLeavesGitlinksEmptyWithoutSubmodules pins that
// with reposerver.enable.git.submodule off, each gitlink is an empty
// directory, also after an earlier run checked its content out.
func TestDefaultGitAcquirerLeavesGitlinksEmptyWithoutSubmodules(t *testing.T) {
	fixture := submoduletest.New(t)
	cacheDir := t.TempDir()
	request := GitRequest{URL: fixture.ParentURL, Revision: "main"}
	if _, err := (DefaultGitAcquirer{}).Acquire(context.Background(), request, submoduleOptions(cacheDir, true, true)); err != nil {
		t.Fatalf("Acquire(submodules) error = %v", err)
	}
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), request, submoduleOptions(cacheDir, true, false))
	if err != nil {
		t.Fatalf("Acquire(no submodules) error = %v", err)
	}
	requireFile(t, result.Path, "apps/top.yaml")
	requireEmptyDir(t, result.Path, "mods/child")
}

// TestDefaultGitAcquirerReadsSubmodulesFromCacheOffline pins that an
// offline run reads submodule content from drydock's Git cache, and that a
// submodule commit the cache lacks fails as an offline cache miss.
func TestDefaultGitAcquirerReadsSubmodulesFromCacheOffline(t *testing.T) {
	fixture := submoduletest.New(t)
	request := GitRequest{URL: fixture.ParentURL, Revision: "main"}

	warm := t.TempDir()
	if _, err := (DefaultGitAcquirer{}).Acquire(context.Background(), request, submoduleOptions(warm, true, true)); err != nil {
		t.Fatalf("online Acquire() error = %v", err)
	}
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), request, submoduleOptions(warm, false, true))
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	requireFile(t, result.Path, "mods/child/deep/deep/cm.yaml")

	cold := t.TempDir()
	if _, err := (DefaultGitAcquirer{}).Acquire(context.Background(), request, submoduleOptions(cold, true, false)); err != nil {
		t.Fatalf("online Acquire(no submodules) error = %v", err)
	}
	_, err = DefaultGitAcquirer{}.Acquire(context.Background(), request, submoduleOptions(cold, false, true))
	if err == nil || !strings.Contains(err.Error(), "offline cache miss") || !strings.Contains(err.Error(), `submodule "mods/child"`) {
		t.Fatalf("offline Acquire() error = %v, want an offline cache miss for the submodule", err)
	}
}

// TestDefaultGitAcquirerRefusesFileSubmodules pins that a submodule URL
// that resolves to the file transport fails, as git refuses it for
// submodules in the repo-server (protocol.file.allow=user).
func TestDefaultGitAcquirerRefusesFileSubmodules(t *testing.T) {
	fixture := submoduletest.New(t)
	opts := submoduleOptions(t.TempDir(), true, true)
	opts.AllowFileSubmoduleTransport = false
	_, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: fixture.ParentURL, Revision: "main"}, opts)
	if err == nil || !strings.Contains(err.Error(), `transport "file" not allowed`) {
		t.Fatalf("Acquire() error = %v, want the file transport refused", err)
	}
}

// TestDefaultGitAcquirerFailsForGitlinkWithoutModule pins that a gitlink
// that .gitmodules does not name fails the checkout, as `git submodule
// update` fails with "No url found for submodule path".
func TestDefaultGitAcquirerFailsForGitlinkWithoutModule(t *testing.T) {
	fixture := submoduletest.New(t)
	work := filepath.Join(fixture.Dir, "parent-work")
	submoduletest.Run(t, work, "update-index", "--add", "--cacheinfo", "160000,"+fixture.ChildCommit+",orphan")
	submoduletest.Run(t, work, "commit", "-q", "-m", "orphan gitlink")
	submoduletest.Run(t, work, "push", "-q", filepath.Join(fixture.Dir, "parent.git"), "HEAD:main")
	_, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: fixture.ParentURL, Revision: "main"}, submoduleOptions(t.TempDir(), true, true))
	if err == nil || !strings.Contains(err.Error(), `no url found for submodule path "orphan"`) {
		t.Fatalf("Acquire() error = %v, want the gitlink without a module refused", err)
	}
}

// TestDefaultGitAcquirerClearsSubmodulesOnCheckout pins that checking a
// cached clone out at a commit without the submodule removes the content
// drydock wrote for the earlier commit.
func TestDefaultGitAcquirerClearsSubmodulesOnCheckout(t *testing.T) {
	fixture := submoduletest.New(t)
	work := filepath.Join(fixture.Dir, "parent-work")
	submoduletest.Run(t, work, "rm", "-q", "mods/child")
	submoduletest.Run(t, work, "commit", "-q", "-m", "drop submodule")
	dropped := submoduletest.Run(t, work, "rev-parse", "HEAD")
	submoduletest.Run(t, work, "push", "-q", filepath.Join(fixture.Dir, "parent.git"), "HEAD:main")

	cacheDir := t.TempDir()
	// One cache entry per URL and revision: the entry of an abbreviated
	// SHA would be another one, so both use the full SHA of the branch
	// commits through checkoutGitRevision on the same clone.
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: fixture.ParentURL, Revision: fixture.ParentCommit}, submoduleOptions(cacheDir, true, true))
	if err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}
	requireFile(t, result.Path, "mods/child/child/app/cm.yaml")
	repo, err := openExistingGitRepository(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkoutGitRevision(repo, worktree, dropped, fixture.ParentURL); err != nil {
		t.Fatalf("checkoutGitRevision() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "mods", "child", "child")); !os.IsNotExist(err) {
		t.Fatalf("stat submodule content after checkout = %v, want it removed", err)
	}
}

// TestDefaultGitAcquirerListsLFSPointers pins that a fetched Git source
// whose repository sets enableLfs reports the Git LFS pointer files of its
// commit outside submodules, which drydock cannot fill, and reports none
// unless asked.
func TestDefaultGitAcquirerListsLFSPointers(t *testing.T) {
	fixture := submoduletest.New(t)
	work := filepath.Join(fixture.Dir, "parent-work")
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("ab", 32) + "\nsize 12\n"
	submoduletest.WriteFile(t, work, "assets/blob.bin", pointer)
	submoduletest.Run(t, work, "add", "-A")
	submoduletest.Run(t, work, "commit", "-q", "-m", "lfs pointer")
	submoduletest.Run(t, work, "push", "-q", filepath.Join(fixture.Dir, "parent.git"), "HEAD:main")

	opts := submoduleOptions(t.TempDir(), true, true)
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: fixture.ParentURL, Revision: "main"}, opts)
	if err != nil || len(result.LFSPointers) != 0 {
		t.Fatalf("Acquire() = %d pointers, error %v; want none without LFS", len(result.LFSPointers), err)
	}
	opts.LFS = true
	result, err = DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: fixture.ParentURL, Revision: "main"}, opts)
	if err != nil {
		t.Fatalf("Acquire(LFS) error = %v", err)
	}
	if len(result.LFSPointers) != 1 || result.LFSPointers[0].Path != "assets/blob.bin" {
		t.Fatalf("LFSPointers = %#v, want assets/blob.bin", result.LFSPointers)
	}
}

// TestDefaultGitAcquirerRefusesSubmoduleURLsThatReadLocalRepositories pins
// that a .gitmodules URL that git reads as ssh to the host "x y", and
// go-git as a local path, never reads the local repository it names.
func TestDefaultGitAcquirerRefusesSubmoduleURLsThatReadLocalRepositories(t *testing.T) {
	for _, prefix := range []string{"x y:", "git@x y:"} {
		parentURL := submoduletest.LocalPathURLFixture(t, prefix)
		opts := submoduleOptions(t.TempDir(), true, true)
		opts.AllowFileSubmoduleTransport = false
		result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: parentURL, Revision: "main"}, opts)
		if err == nil {
			data, _ := os.ReadFile(filepath.Join(result.Path, "sub", "app", "cm.yaml"))
			t.Fatalf("prefix %q: Acquire() error = nil, sub/app/cm.yaml = %q; want the URL refused", prefix, data)
		}
		if !strings.Contains(err.Error(), "drydock's Git client reads it as") {
			t.Fatalf("prefix %q: Acquire() error = %v, want the transport mismatch", prefix, err)
		}
	}
}

// TestDefaultGitAcquirerFetchesUnreachablePinnedCommit pins that a
// submodule commit that no branch or tag holds is fetched by its SHA, as
// `git submodule update` does.
func TestDefaultGitAcquirerFetchesUnreachablePinnedCommit(t *testing.T) {
	parentURL := submoduletest.UnreachableCommitFixture(t)
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: parentURL, Revision: "main"}, submoduleOptions(t.TempDir(), true, true))
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "sub", "app", "cm.yaml"))
	if err != nil || !strings.Contains(string(data), "name: feature") {
		t.Fatalf("sub/app/cm.yaml = %q, %v; want the pinned commit", data, err)
	}
}

// TestDefaultGitAcquirerFetchesSubmodulesOverGitDaemon runs drydock's own
// submodule fetch over the network: a git daemon serves the fixture, the
// superproject's relative submodule URLs resolve to git:// URLs, and the
// file transport stays refused. A second, offline run reads the cache.
func TestDefaultGitAcquirerFetchesSubmodulesOverGitDaemon(t *testing.T) {
	fixture := submoduletest.New(t)
	base := submoduletest.ServeGitDaemon(t, fixture.Dir)
	cacheDir := t.TempDir()
	request := GitRequest{URL: base + "/parent.git", Revision: "main"}
	online := GitOptions{AllowNetwork: true, CacheDir: cacheDir, Submodules: true}
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), request, online)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	requireFile(t, result.Path, "mods/child/deep/deep/cm.yaml")
	offline := online
	offline.AllowNetwork = false
	result, err = DefaultGitAcquirer{}.Acquire(context.Background(), request, offline)
	if err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	requireFile(t, result.Path, "mods/child/child/app/cm.yaml")
}
