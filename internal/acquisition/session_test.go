package acquisition

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/remote"
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
		SnapshotCache:      NewSnapshotCache(),
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
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

type countingGitAcquirer struct {
	path      string
	revision  string
	fromCache bool
	network   bool
	mu        sync.Mutex
	calls     int
}

func (a *countingGitAcquirer) Acquire(_ context.Context, _ source.GitRequest, _ source.GitOptions) (source.GitResult, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return source.GitResult{
		Path:      a.path,
		Revision:  a.revision,
		FromCache: a.fromCache,
		Network:   a.network,
	}, nil
}

func TestSessionGitSnapshotCacheHitReportsCacheReuse(t *testing.T) {
	cacheDir := t.TempDir()
	gitDir := filepath.Join(cacheDir, "repo")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "manifest.yaml"), []byte("kind: ConfigMap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delegate := &countingGitAcquirer{
		path:      gitDir,
		revision:  "abc123",
		fromCache: false,
		network:   true,
	}
	acquirer := (Session{
		Locks:              NewTargetLocks(),
		SnapshotRoot:       t.TempDir(),
		SnapshotCacheReads: true,
		SnapshotCache:      NewSnapshotCache(),
	}).GitAcquirer(delegate)
	request := source.GitRequest{URL: "https://example.test/repo.git", Revision: "main"}
	opts := source.GitOptions{CacheDir: cacheDir, AllowNetwork: true}

	first, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	second, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
	if first.FromCache || !first.Network {
		t.Fatalf("first flags = FromCache %t Network %t, want fresh network result", first.FromCache, first.Network)
	}
	if !second.FromCache || second.Network {
		t.Fatalf("second flags = FromCache %t Network %t, want cache reuse", second.FromCache, second.Network)
	}
	if first.Path != second.Path {
		t.Fatalf("snapshot paths differ: first %q second %q", first.Path, second.Path)
	}
}

type countingChartAcquirer struct {
	chartDir  string
	fromCache bool
	mu        sync.Mutex
	calls     int
}

func (a *countingChartAcquirer) Acquire(_ context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return chart.Result{
		ChartDir:   a.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  a.fromCache,
	}, nil
}

func TestSessionReusesChartSnapshotForSameTarget(t *testing.T) {
	cacheDir := t.TempDir()
	chartDir := filepath.Join(cacheDir, "demo")
	if err := os.MkdirAll(chartDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\nversion: 1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delegate := &countingChartAcquirer{chartDir: chartDir, fromCache: true}
	acquirer := (Session{
		Locks:              NewTargetLocks(),
		SnapshotRoot:       t.TempDir(),
		SnapshotCacheReads: true,
		SnapshotCache:      NewSnapshotCache(),
	}).ChartAcquirer(delegate)
	request := chart.Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       chart.RepositoryHTTP,
	}
	opts := chart.Options{CacheDir: cacheDir}

	first, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	second, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
	if first.ChartDir != second.ChartDir {
		t.Fatalf("snapshot paths differ: first %q second %q", first.ChartDir, second.ChartDir)
	}
	if first.ChartDir == chartDir {
		t.Fatalf("Acquire returned cache path %q, want snapshot path", first.ChartDir)
	}
	if _, err := os.Stat(filepath.Join(first.ChartDir, "Chart.yaml")); err != nil {
		t.Fatalf("snapshot missing Chart.yaml: %v", err)
	}
}

func TestSessionChartSnapshotCacheHitReportsCacheReuse(t *testing.T) {
	cacheDir := t.TempDir()
	chartDir := filepath.Join(cacheDir, "demo")
	if err := os.MkdirAll(chartDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\nversion: 1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delegate := &countingChartAcquirer{chartDir: chartDir, fromCache: false}
	acquirer := (Session{
		Locks:              NewTargetLocks(),
		SnapshotRoot:       t.TempDir(),
		SnapshotCacheReads: true,
		SnapshotCache:      NewSnapshotCache(),
	}).ChartAcquirer(delegate)
	request := chart.Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       chart.RepositoryHTTP,
	}
	opts := chart.Options{CacheDir: cacheDir}

	first, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	second, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
	if first.FromCache {
		t.Fatalf("first FromCache = true, want fresh result")
	}
	if !second.FromCache {
		t.Fatalf("second FromCache = false, want cache reuse")
	}
	if first.ChartDir != second.ChartDir {
		t.Fatalf("snapshot paths differ: first %q second %q", first.ChartDir, second.ChartDir)
	}
}

type blockingChartAcquirer struct {
	chartDir      string
	enteredFirst  chan struct{}
	enteredSecond chan struct{}
	releaseFirst  chan struct{}
	mu            sync.Mutex
	calls         int
}

func (a *blockingChartAcquirer) Acquire(_ context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
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

	return chart.Result{
		ChartDir:   a.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  true,
	}, nil
}

func TestSessionReusesChartSnapshotForSameTargetConcurrently(t *testing.T) {
	type acquireResult struct {
		result chart.Result
		err    error
	}

	cacheDir := t.TempDir()
	chartDir := filepath.Join(cacheDir, "demo")
	if err := os.MkdirAll(chartDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\nversion: 1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delegate := &blockingChartAcquirer{
		chartDir:      chartDir,
		enteredFirst:  make(chan struct{}),
		enteredSecond: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	acquirer := (Session{
		Locks:              NewTargetLocks(),
		SnapshotRoot:       t.TempDir(),
		SnapshotCacheReads: true,
		SnapshotCache:      NewSnapshotCache(),
	}).ChartAcquirer(delegate)
	request := chart.Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       chart.RepositoryHTTP,
	}
	opts := chart.Options{CacheDir: cacheDir}

	done := make(chan acquireResult, 2)
	acquire := func() {
		result, err := acquirer.Acquire(context.Background(), request, opts)
		done <- acquireResult{result: result, err: err}
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(delegate.releaseFirst)
		})
	}
	defer release()

	go acquire()
	select {
	case <-delegate.enteredFirst:
	case completed := <-done:
		t.Fatalf("first Acquire() completed before entering delegate: result %#v err %v", completed.result, completed.err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first acquisition to enter delegate")
	}
	go acquire()
	select {
	case <-delegate.enteredSecond:
		t.Fatal("second acquisition entered delegate before first snapshot was cached")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	results := make([]chart.Result, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case completed := <-done:
			if completed.err != nil {
				t.Fatalf("Acquire() error = %v", completed.err)
			}
			results = append(results, completed.result)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for acquisition")
		}
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls = %d, want 1", delegate.calls)
	}
	if results[0].ChartDir != results[1].ChartDir {
		t.Fatalf("snapshot paths differ: first %q second %q", results[0].ChartDir, results[1].ChartDir)
	}
	if results[0].ChartDir == chartDir {
		t.Fatalf("Acquire returned cache path %q, want snapshot path", results[0].ChartDir)
	}
}

type blockingRemoteGitAcquirer struct {
	cacheDir      string
	enteredFirst  chan struct{}
	enteredSecond chan struct{}
	releaseFirst  chan struct{}
	mu            sync.Mutex
	calls         int
}

func (a *blockingRemoteGitAcquirer) Acquire(_ context.Context, request remote.Request, _ remote.Options) (remote.Result, error) {
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

	path := filepath.Join(a.cacheDir, "repo")
	if err := os.MkdirAll(path, 0o755); err != nil {
		return remote.Result{}, err
	}
	if err := os.WriteFile(filepath.Join(path, "kustomization.yaml"), []byte("resources: []\n"), 0o644); err != nil {
		return remote.Result{}, err
	}
	return remote.Result{Path: path, URL: request.RepoURL, Revision: request.Revision, FromCache: true}, nil
}

func TestSessionHoldsRemoteGitLockUntilRelease(t *testing.T) {
	cacheDir := t.TempDir()
	delegate := &blockingRemoteGitAcquirer{
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
	acquirer := session.RemoteAcquirer(delegate)
	request := remote.Request{
		Kind:     remote.RequestGitRepo,
		RepoURL:  "https://example.test/repo.git",
		Revision: "main",
	}

	firstDone := make(chan error, 1)
	var first remote.Result
	go func() {
		result, err := acquirer.Acquire(context.Background(), request, remote.Options{CacheDir: cacheDir})
		if err == nil {
			first = result
		}
		firstDone <- err
	}()
	<-delegate.enteredFirst
	close(delegate.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.Release == nil {
		t.Fatal("first Release is nil, want lock release callback")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := acquirer.Acquire(context.Background(), request, remote.Options{CacheDir: cacheDir})
		secondDone <- err
	}()
	select {
	case <-delegate.enteredSecond:
		t.Fatal("second acquisition entered before first release")
	case <-time.After(50 * time.Millisecond):
	}

	first.Release()
	first.Release()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Acquire() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second acquisition did not proceed after first release")
	}
}

type releasingRemoteGitAcquirer struct {
	path    string
	release func()
}

func (a releasingRemoteGitAcquirer) Acquire(_ context.Context, request remote.Request, _ remote.Options) (remote.Result, error) {
	return remote.Result{
		Path:      a.path,
		URL:       request.RepoURL,
		Revision:  request.Revision,
		FromCache: true,
		Release:   a.release,
	}, nil
}

func TestSessionChainsRemoteGitDelegateRelease(t *testing.T) {
	cacheDir := t.TempDir()
	repoDir := filepath.Join(cacheDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var releaseCalls int
	acquirer := (Session{
		Locks:              NewTargetLocks(),
		SnapshotRoot:       t.TempDir(),
		SnapshotCacheReads: true,
	}).RemoteAcquirer(releasingRemoteGitAcquirer{
		path: repoDir,
		release: func() {
			releaseCalls++
		},
	})
	result, err := acquirer.Acquire(context.Background(), remote.Request{
		Kind:     remote.RequestGitRepo,
		RepoURL:  "https://example.test/repo.git",
		Revision: "main",
	}, remote.Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.Release == nil {
		t.Fatal("Release is nil")
	}
	result.Release()
	result.Release()
	if releaseCalls != 1 {
		t.Fatalf("delegate release calls = %d, want 1", releaseCalls)
	}
}
