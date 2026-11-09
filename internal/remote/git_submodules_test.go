package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// TestDefaultAcquirerChecksOutRemoteGitSubmodules pins that a remote
// Kustomize Git base holds the content of its submodules, nested ones
// included, as kustomize's `git submodule update --init --recursive`
// leaves its clone, and that ?submodules=false (Submodules false) leaves
// each gitlink an empty directory in the same cache entry.
func TestDefaultAcquirerChecksOutRemoteGitSubmodules(t *testing.T) {
	fixture := submoduletest.New(t)
	cacheDir := t.TempDir()
	request := Request{Kind: RequestGitRepo, RepoURL: fixture.ParentURL, Revision: "main", Submodules: true}
	opts := Options{AllowFileTransport: true, CacheDir: cacheDir, AllowFileSubmoduleTransport: true}
	result, err := DefaultAcquirer{}.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	for _, rel := range []string{"apps/top.yaml", "mods/child/child/app/cm.yaml", "mods/child/deep/deep/cm.yaml"} {
		if _, err := os.Stat(filepath.Join(result.Path, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("remote checkout lacks %s: %v", rel, err)
		}
	}

	offline := opts
	offline.Offline = true
	if _, err := (DefaultAcquirer{}).Acquire(context.Background(), request, offline); err != nil {
		t.Fatalf("offline Acquire() error = %v, want the cached submodules", err)
	}

	request.Submodules = false
	result, err = DefaultAcquirer{}.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("Acquire(no submodules) error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(result.Path, "mods", "child"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("mods/child = %d entries, %v; want an empty directory", len(entries), err)
	}
}

// TestDefaultAcquirerRemoteSubmodulesOfflineCacheMiss pins that an offline
// remote Git base whose submodule commit is not cached fails clearly.
func TestDefaultAcquirerRemoteSubmodulesOfflineCacheMiss(t *testing.T) {
	fixture := submoduletest.New(t)
	cacheDir := t.TempDir()
	request := Request{Kind: RequestGitRepo, RepoURL: fixture.ParentURL, Revision: "main"}
	opts := Options{AllowFileTransport: true, CacheDir: cacheDir, AllowFileSubmoduleTransport: true}
	if _, err := (DefaultAcquirer{}).Acquire(context.Background(), request, opts); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	request.Submodules = true
	opts.Offline = true
	_, err := DefaultAcquirer{}.Acquire(context.Background(), request, opts)
	if err == nil || !strings.Contains(err.Error(), "offline cache miss") || !strings.Contains(err.Error(), `submodule "mods/child"`) {
		t.Fatalf("offline Acquire() error = %v, want an offline cache miss for the submodule", err)
	}
}

// TestDefaultAcquirerRefusesRemoteSubmoduleURLsThatReadLocalRepositories
// pins the local path PoC for remote Kustomize bases: git reads
// "git@x y:/..." as ssh to the host "x y", and go-git as a local path.
func TestDefaultAcquirerRefusesRemoteSubmoduleURLsThatReadLocalRepositories(t *testing.T) {
	for _, prefix := range []string{"git@x y:", "x y:"} {
		parentURL := submoduletest.LocalPathURLFixture(t, prefix)
		result, err := DefaultAcquirer{}.Acquire(context.Background(), Request{Kind: RequestGitRepo, RepoURL: parentURL, Revision: "main", Submodules: true}, Options{AllowFileTransport: true, CacheDir: t.TempDir()})
		if err == nil {
			data, _ := os.ReadFile(filepath.Join(result.Path, "sub", "app", "cm.yaml"))
			t.Fatalf("prefix %q: Acquire() error = nil, sub/app/cm.yaml = %q; want the URL refused", prefix, data)
		}
		if !strings.Contains(err.Error(), "drydock's Git client reads it as") {
			t.Fatalf("prefix %q: Acquire() error = %v, want the transport mismatch", prefix, err)
		}
	}
}

// TestDefaultAcquirerFetchesUnreachablePinnedRemoteCommit pins that a
// remote Kustomize base fetches a submodule commit that no branch holds.
func TestDefaultAcquirerFetchesUnreachablePinnedRemoteCommit(t *testing.T) {
	parentURL := submoduletest.UnreachableCommitFixture(t)
	result, err := DefaultAcquirer{}.Acquire(context.Background(), Request{Kind: RequestGitRepo, RepoURL: parentURL, Revision: "main", Submodules: true}, Options{AllowFileTransport: true, CacheDir: t.TempDir(), AllowFileSubmoduleTransport: true})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "sub", "app", "cm.yaml"))
	if err != nil || !strings.Contains(string(data), "name: feature") {
		t.Fatalf("sub/app/cm.yaml = %q, %v; want the pinned commit", data, err)
	}
}

// TestDefaultAcquirerNamesTheGitTransportDifference pins that a git://
// submodule of a remote Kustomize base fails with an error that says
// drydock does not fetch it, which is a recorded difference.
func TestDefaultAcquirerNamesTheGitTransportDifference(t *testing.T) {
	parentURL := submoduletest.SubmoduleURLFixture(t, "git://git.example.invalid/child.git")
	_, err := DefaultAcquirer{}.Acquire(context.Background(), Request{Kind: RequestGitRepo, RepoURL: parentURL, Revision: "main", Submodules: true}, Options{AllowFileTransport: true, CacheDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "recorded difference") {
		t.Fatalf("Acquire() error = %v, want the git:// difference named", err)
	}
}

// TestDefaultAcquirerStoresNoUserinfo pins that a remote Git URL with
// userinfo is cloned without it, so the remote cache entry's .git/config
// never holds the token.
func TestDefaultAcquirerStoresNoUserinfo(t *testing.T) {
	fixture := submoduletest.New(t)
	const token = "SECRET-TOKEN-5678"
	withUserinfo := strings.Replace(fixture.ParentURL, "file://", "file://ci-token:"+token+"@", 1)
	cacheDir := t.TempDir()
	result, err := DefaultAcquirer{}.Acquire(context.Background(), Request{Kind: RequestGitRepo, RepoURL: withUserinfo, Revision: "main"}, Options{AllowFileTransport: true, CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) || strings.Contains(string(data), "ci-token") {
		t.Fatalf(".git/config holds the userinfo: %s", data)
	}
}

// TestDefaultAcquirerScrubsUserinfoFromOldEntries pins that a remote Git
// entry whose .git/config an older drydock wrote with userinfo loses it on
// its next use.
func TestDefaultAcquirerScrubsUserinfoFromOldEntries(t *testing.T) {
	fixture := submoduletest.New(t)
	const token = "SECRET-TOKEN-OLD-REMOTE"
	cacheDir := t.TempDir()
	request := Request{Kind: RequestGitRepo, RepoURL: fixture.ParentURL, Revision: "main"}
	result, err := DefaultAcquirer{}.Acquire(context.Background(), request, Options{AllowFileTransport: true, CacheDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(result.Path, ".git", "config")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(data), "file://", "file://ci-token:"+token+"@", 1)
	if err := os.WriteFile(config, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (DefaultAcquirer{}).Acquire(context.Background(), request, Options{AllowFileTransport: true, CacheDir: cacheDir, Offline: true}); err != nil {
		t.Fatalf("offline Acquire() error = %v", err)
	}
	if data, _ := os.ReadFile(config); strings.Contains(string(data), token) {
		t.Fatalf(".git/config still holds the userinfo: %s", data)
	}
}
