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
	opts := Options{CacheDir: cacheDir, AllowFileSubmoduleTransport: true}
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
	opts := Options{CacheDir: cacheDir, AllowFileSubmoduleTransport: true}
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
