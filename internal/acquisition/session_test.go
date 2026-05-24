package acquisition

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/source"
)

type blockingGitAcquirer struct {
	cacheDir      string
	enteredFirst  chan struct{}
	enteredSecond chan struct{}
	releaseFirst  chan struct{}
	mu            sync.Mutex
	calls         int
}

func (a *blockingGitAcquirer) Acquire(_ context.Context, request source.GitRequest, _ source.GitOptions) (source.GitResult, error) {
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()

	switch call {
	case 1:
		close(a.enteredFirst)
		<-a.releaseFirst
	case 2:
		close(a.enteredSecond)
	}

	path := filepath.Join(a.cacheDir, source.GitCacheKey(request.URL, request.Revision))
	if err := os.MkdirAll(path, 0o755); err != nil {
		return source.GitResult{}, err
	}
	if err := os.WriteFile(filepath.Join(path, "manifest.yaml"), []byte("kind: ConfigMap\n"), 0o644); err != nil {
		return source.GitResult{}, err
	}
	return source.GitResult{Path: path, Revision: request.Revision, FromCache: true}, nil
}

func TestSessionSerializesSameGitCacheTarget(t *testing.T) {
	cacheDir := t.TempDir()
	delegate := &blockingGitAcquirer{
		cacheDir:      cacheDir,
		enteredFirst:  make(chan struct{}),
		enteredSecond: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	session := Session{
		Locks:              NewTargetLocks(),
		SnapshotRoot:       t.TempDir(),
		SnapshotCacheReads: true,
	}
	acquirer := session.GitAcquirer(delegate)
	request := source.GitRequest{URL: "https://example.test/repo.git", Revision: "main"}
	opts := source.GitOptions{CacheDir: cacheDir, AllowNetwork: true}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	acquire := func() {
		defer wg.Done()
		result, err := acquirer.Acquire(context.Background(), request, opts)
		if err != nil {
			errs <- err
			return
		}
		if result.Path == filepath.Join(cacheDir, source.GitCacheKey(request.URL, request.Revision)) {
			errs <- fmt.Errorf("Acquire returned cache path %q, want snapshot path", result.Path)
			return
		}
		if _, err := os.Stat(filepath.Join(result.Path, "manifest.yaml")); err != nil {
			errs <- fmt.Errorf("snapshot missing manifest: %w", err)
		}
	}

	wg.Add(1)
	go acquire()
	<-delegate.enteredFirst

	wg.Add(1)
	go acquire()
	select {
	case <-delegate.enteredSecond:
		t.Fatal("second acquisition entered before first acquisition released")
	case <-time.After(50 * time.Millisecond):
	}

	close(delegate.releaseFirst)
	wg.Wait()
	close(errs)
	if delegate.calls != 2 {
		t.Fatalf("delegate calls = %d, want 2", delegate.calls)
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
