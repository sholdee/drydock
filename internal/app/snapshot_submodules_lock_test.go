package app

import (
	"context"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// TestSnapshotSubmoduleOpenersShareTheEntryLock pins that the submodule
// openers of --ref snapshots lock the per-URL store as fetched sources do:
// the two sides of a diff, and fetched sources, fetch the same store at
// the same time, and an unlocked clone removes the store under the other.
func TestSnapshotSubmoduleOpenersShareTheEntryLock(t *testing.T) {
	fixture := submoduletest.New(t)
	base := submoduletest.ServeGitDaemon(t, fixture.Dir)
	cacheDir := t.TempDir()
	ref := gittree.SubmoduleRef{
		Path:       "mods/child",
		ModuleDirs: []string{"mods/child"},
		URL:        base + "/child.git",
		Commit:     plumbing.NewHash(fixture.ChildCommit),
		Transport:  "git",
	}
	const openers = 12
	errs := make([]error, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Go(func() {
			options := DiffRequest{GitCacheDir: cacheDir}.snapshotSubmoduleOptions(fixture.Work)
			_, errs[i] = options.Opener.OpenSubmodule(context.Background(), ref)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("opener %d error = %v, want every opener to wait for the store", i, err)
		}
	}
}
