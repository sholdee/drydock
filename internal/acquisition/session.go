package acquisition

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/filecopy"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/source"
)

type Session struct {
	Locks              *TargetLocks
	SnapshotRoot       string
	SnapshotCacheReads bool
	SnapshotCache      *SnapshotCache
}

type TargetLocks struct {
	mu    sync.Mutex
	locks map[string]*targetLock
}

type SnapshotCache struct {
	mu     sync.Mutex
	gits   map[string]source.GitResult
	charts map[string]chart.Result
}

type targetLock struct {
	mu   sync.Mutex
	refs int
}

func NewTargetLocks() *TargetLocks {
	return &TargetLocks{locks: map[string]*targetLock{}}
}

func NewSnapshotCache() *SnapshotCache {
	return &SnapshotCache{
		gits:   map[string]source.GitResult{},
		charts: map[string]chart.Result{},
	}
}

func (cache *SnapshotCache) git(key string) (source.GitResult, bool) {
	if cache == nil {
		return source.GitResult{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	result, ok := cache.gits[key]
	return result, ok
}

func (cache *SnapshotCache) storeGit(key string, result source.GitResult) {
	if cache == nil {
		return
	}
	result.FromCache = true
	result.Network = false
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.gits[key] = result
}

func (cache *SnapshotCache) chart(key string) (chart.Result, bool) {
	if cache == nil {
		return chart.Result{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	result, ok := cache.charts[key]
	return result, ok
}

func (cache *SnapshotCache) storeChart(key string, result chart.Result) {
	if cache == nil {
		return
	}
	result.FromCache = true
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.charts[key] = result
}

func (locks *TargetLocks) lock(key string) func() {
	if locks == nil || key == "" {
		return func() {}
	}
	locks.mu.Lock()
	lock, ok := locks.locks[key]
	if !ok {
		lock = &targetLock{}
		locks.locks[key] = lock
	}
	lock.refs++
	locks.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		locks.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(locks.locks, key)
		}
		locks.mu.Unlock()
	}
}

func (session Session) GitAcquirer(delegate source.GitAcquirer) source.GitAcquirer {
	if delegate == nil {
		delegate = source.DefaultGitAcquirer{}
	}
	if session.Locks == nil {
		return delegate
	}
	return cacheSafeGitAcquirer{
		delegate:      delegate,
		locks:         session.Locks,
		snapshotRoot:  session.SnapshotRoot,
		snapshot:      session.SnapshotCacheReads,
		snapshotCache: session.SnapshotCache,
	}
}

func (session Session) ChartAcquirer(delegate chart.Acquirer) chart.Acquirer {
	if delegate == nil {
		delegate = chart.DefaultAcquirer{}
	}
	if session.Locks == nil {
		return delegate
	}
	return cacheSafeChartAcquirer{
		delegate:      delegate,
		locks:         session.Locks,
		snapshotRoot:  session.SnapshotRoot,
		snapshot:      session.SnapshotCacheReads,
		snapshotCache: session.SnapshotCache,
	}
}

func (session Session) RemoteAcquirer(delegate remote.Acquirer) remote.Acquirer {
	if delegate == nil {
		delegate = remote.DefaultAcquirer{}
	}
	if session.Locks == nil {
		return delegate
	}
	return cacheSafeRemoteAcquirer{
		delegate:     delegate,
		locks:        session.Locks,
		snapshotRoot: session.SnapshotRoot,
		snapshot:     session.SnapshotCacheReads,
	}
}

type cacheSafeGitAcquirer struct {
	delegate      source.GitAcquirer
	locks         *TargetLocks
	snapshotRoot  string
	snapshot      bool
	snapshotCache *SnapshotCache
}

func (acquirer cacheSafeGitAcquirer) Acquire(ctx context.Context, request source.GitRequest, opts source.GitOptions) (source.GitResult, error) {
	key, keyErr := gitCacheLockKey(request, opts)
	if keyErr != nil {
		return acquirer.delegate.Acquire(ctx, request, opts)
	}
	unlock := acquirer.locks.lock(key)
	defer unlock()

	if acquirer.snapshot {
		if result, ok := acquirer.snapshotCache.git(key); ok {
			return result, nil
		}
	}

	result, err := acquirer.delegate.Acquire(ctx, request, opts)
	if err != nil || !acquirer.snapshot {
		return result, err
	}
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "git", result.Path, false)
	if err != nil {
		return source.GitResult{}, err
	}
	result.Path = snapshot
	acquirer.snapshotCache.storeGit(key, result)
	return result, nil
}

type cacheSafeChartAcquirer struct {
	delegate      chart.Acquirer
	locks         *TargetLocks
	snapshotRoot  string
	snapshot      bool
	snapshotCache *SnapshotCache
}

func (acquirer cacheSafeChartAcquirer) Acquire(ctx context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	key, keyErr := chartCacheLockKey(request, opts)
	if keyErr != nil {
		return acquirer.delegate.Acquire(ctx, request, opts)
	}
	unlock := acquirer.locks.lock(key)
	defer unlock()

	if acquirer.snapshot {
		if result, ok := acquirer.snapshotCache.chart(key); ok {
			return result, nil
		}
	}

	result, err := acquirer.delegate.Acquire(ctx, request, opts)
	if err != nil || !acquirer.snapshot {
		return result, err
	}
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "chart", result.ChartDir, true)
	if err != nil {
		return chart.Result{}, err
	}
	result.ChartDir = snapshot
	acquirer.snapshotCache.storeChart(key, result)
	return result, nil
}

type cacheSafeRemoteAcquirer struct {
	delegate     remote.Acquirer
	locks        *TargetLocks
	snapshotRoot string
	snapshot     bool
}

func (acquirer cacheSafeRemoteAcquirer) Acquire(ctx context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	key, keyErr := remoteCacheLockKey(request, opts)
	if keyErr != nil {
		return acquirer.delegate.Acquire(ctx, request, opts)
	}
	unlock := acquirer.locks.lock(key)

	result, err := acquirer.delegate.Acquire(ctx, request, opts)
	if err != nil {
		unlock()
		return result, err
	}
	if request.Kind == remote.RequestGitRepo {
		delegateRelease := result.Release
		var once sync.Once
		result.Release = func() {
			once.Do(func() {
				if delegateRelease != nil {
					delegateRelease()
				}
				unlock()
			})
		}
		return result, nil
	}
	defer unlock()
	if !acquirer.snapshot {
		return result, nil
	}
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "remote", result.Path, false)
	if err != nil {
		return remote.Result{}, err
	}
	result.Path = snapshot
	return result, nil
}

func gitCacheLockKey(request source.GitRequest, opts source.GitOptions) (string, error) {
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = source.DefaultGitCacheDir()
		if err != nil {
			return "", err
		}
	}
	return absoluteCacheLockKey("git", filepath.Join(cacheDir, source.GitCacheKey(request.URL, request.Revision)))
}

func chartCacheLockKey(request chart.Request, opts chart.Options) (string, error) {
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = chart.DefaultCacheDir()
		if err != nil {
			return "", err
		}
	}
	key, err := chart.NewCacheKey(request)
	if err != nil {
		return "", err
	}
	return absoluteCacheLockKey("chart", filepath.Join(cacheDir, string(request.Kind), key))
}

func remoteCacheLockKey(request remote.Request, opts remote.Options) (string, error) {
	cacheDir, err := remote.ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return "", err
	}
	key, err := remote.NewCacheKey(request)
	if err != nil {
		return "", err
	}
	if request.Kind == remote.RequestGitRepo {
		return absoluteCacheLockKey("remote-git", filepath.Join(cacheDir, key, "repo"))
	}
	return absoluteCacheLockKey("remote", remote.CachePath(cacheDir, key))
}

func absoluteCacheLockKey(prefix, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return prefix + ":" + filepath.Clean(abs), nil
}

func snapshotCachePath(root, prefix, sourcePath string, linkRegularFiles bool) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(sourcePath) == "" {
		return sourcePath, nil
	}
	snapshotRoot, err := os.MkdirTemp(root, prefix+"-*")
	if err != nil {
		return "", err
	}
	snapshotPath := filepath.Join(snapshotRoot, filepath.Base(sourcePath))
	if err := copyCachePath(sourcePath, snapshotPath, linkRegularFiles); err != nil {
		_ = os.RemoveAll(snapshotRoot)
		return "", err
	}
	return snapshotPath, nil
}

func copyCachePath(src, dst string, linkRegularFiles bool) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyCachePath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), linkRegularFiles); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyRegularCacheFile(src, dst, info.Mode().Perm(), linkRegularFiles)
	default:
		return fmt.Errorf("cache path %q is not a regular file, directory, or symlink", src)
	}
}

func copyRegularCacheFile(src, dst string, mode os.FileMode, linkRegularFiles bool) error {
	if linkRegularFiles {
		return filecopy.LinkOrCopyRegularFile(src, dst, mode)
	}
	return filecopy.CopyRegularFile(src, dst, mode)
}
