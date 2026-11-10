package acquisition

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sholdee/drydock/internal/gitlfs"
	"github.com/sholdee/drydock/internal/source"
)

// failingGitAcquirer fails every Acquire with err and counts the calls.
type failingGitAcquirer struct {
	err   error
	calls int
}

func (a *failingGitAcquirer) Acquire(context.Context, source.GitRequest, source.GitOptions) (source.GitResult, error) {
	a.calls++
	return source.GitResult{}, a.err
}

// TestSessionMemoizesGitLFSFetchFailures pins that a run acquires a
// repository and revision whose Git LFS fetch failed once, so each
// Application does not wait for the same failing fill again, and that it
// still retries any other failure.
func TestSessionMemoizesGitLFSFetchFailures(t *testing.T) {
	for _, tc := range []struct {
		err       error
		wantCalls int
	}{
		{fmt.Errorf("check out Git LFS content: %w: HTTP 403", gitlfs.ErrFetch), 1},
		{errors.New("clone repository: connection refused"), 2},
	} {
		delegate := &failingGitAcquirer{err: tc.err}
		acquirer := (Session{
			Locks:              NewTargetLocks(),
			SnapshotRoot:       t.TempDir(),
			SnapshotCacheReads: true,
			SnapshotCache:      NewSnapshotCache(),
		}).GitAcquirer(delegate)
		request := source.GitRequest{URL: "https://example.test/repo.git", Revision: "main"}
		opts := source.GitOptions{CacheDir: t.TempDir(), AllowNetwork: true, LFS: true}
		for range 2 {
			if _, err := acquirer.Acquire(context.Background(), request, opts); !errors.Is(err, tc.err) {
				t.Fatalf("Acquire() error = %v, want %v", err, tc.err)
			}
		}
		if delegate.calls != tc.wantCalls {
			t.Fatalf("%v: delegate calls = %d, want %d", tc.err, delegate.calls, tc.wantCalls)
		}
	}
}
