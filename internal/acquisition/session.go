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
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/source"
)

type Session struct {
	Locks              *TargetLocks
	SnapshotRoot       string
	SnapshotCacheReads bool
	SnapshotCache      *SnapshotCache
	// PreserveGitDirInSnapshots keeps .git directories in Git source
	// snapshots for plugin-enabled runs whose trusted plugins may inspect
	// repository metadata. Built-in renderers use only worktree files.
	PreserveGitDirInSnapshots bool
}

// SnapshotSession owns one snapshot root and cache shared by every provider
// in a single command invocation.
type SnapshotSession struct {
	Root      string
	Cache     *SnapshotCache
	closeOnce sync.Once
}

type TargetLocks struct {
	mu    sync.Mutex
	locks map[string]*targetLock
}

type SnapshotCache struct {
	mu          sync.Mutex
	gits        map[string]source.GitResult
	charts      map[string]chart.Result
	ociResolves map[string]string
	ociExtracts map[string]string
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
		gits:        map[string]source.GitResult{},
		charts:      map[string]chart.Result{},
		ociResolves: map[string]string{},
		ociExtracts: map[string]string{},
	}
}

func NewSnapshotSession(prefix string) (*SnapshotSession, error) {
	root, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, err
	}
	return &SnapshotSession{Root: root, Cache: NewSnapshotCache()}, nil
}

func (session *SnapshotSession) Close() {
	if session == nil {
		return
	}
	session.closeOnce.Do(func() { _ = os.RemoveAll(session.Root) })
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

func (cache *SnapshotCache) ociResolve(key string) (string, bool) {
	if cache == nil {
		return "", false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	digest, ok := cache.ociResolves[key]
	return digest, ok
}

func (cache *SnapshotCache) storeOCIResolve(key, digest string) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ociResolves[key] = digest
}

func (cache *SnapshotCache) ociExtract(key string) (string, bool) {
	if cache == nil {
		return "", false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	dir, ok := cache.ociExtracts[key]
	return dir, ok
}

func (cache *SnapshotCache) storeOCIExtract(key, dir string) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ociExtracts[key] = dir
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
		delegate:       delegate,
		locks:          session.Locks,
		snapshotRoot:   session.SnapshotRoot,
		snapshot:       session.SnapshotCacheReads,
		snapshotCache:  session.SnapshotCache,
		preserveGitDir: session.PreserveGitDirInSnapshots,
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

// OCIArtifactAcquirer decorates delegate with the session memo required by
// the render-cache prepare phase: collectSourceIdentities re-invokes source
// resolution before every render, so without memoization every OCI source
// would resolve and extract twice with duplicated acquisition events.
// Extractions are copied into the session snapshot area and released with the
// session.
func (session Session) OCIArtifactAcquirer(delegate ociartifact.Acquirer) ociartifact.Acquirer {
	if delegate == nil {
		delegate = ociartifact.DefaultAcquirer{}
	}
	if session.Locks == nil {
		return delegate
	}
	return cacheSafeOCIArtifactAcquirer{
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
	delegate       source.GitAcquirer
	locks          *TargetLocks
	snapshotRoot   string
	snapshot       bool
	snapshotCache  *SnapshotCache
	preserveGitDir bool
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
	gitCopyOptions := snapshotCopyOptions{}
	if !acquirer.preserveGitDir {
		gitCopyOptions.skipDirNames = map[string]struct{}{".git": {}}
	}
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "git", result.Path, gitCopyOptions)
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
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "chart", result.ChartDir, snapshotCopyOptions{linkRegularFiles: true})
	if err != nil {
		return chart.Result{}, err
	}
	result.ChartDir = snapshot
	acquirer.snapshotCache.storeChart(key, result)
	return result, nil
}

type cacheSafeOCIArtifactAcquirer struct {
	delegate      ociartifact.Acquirer
	locks         *TargetLocks
	snapshotRoot  string
	snapshot      bool
	snapshotCache *SnapshotCache
}

func (acquirer cacheSafeOCIArtifactAcquirer) Resolve(ctx context.Context, repoURL, revision string, opts ociartifact.Options) (string, error) {
	key := "oci-resolve:" + ociartifact.NormalizeURL(repoURL) + "\x00" + strings.TrimSpace(revision)
	unlock := acquirer.locks.lock(key)
	defer unlock()

	if acquirer.snapshot {
		if digest, ok := acquirer.snapshotCache.ociResolve(key); ok {
			return digest, nil
		}
	}
	digest, err := acquirer.delegate.Resolve(ctx, repoURL, revision, opts)
	if err != nil || !acquirer.snapshot {
		return digest, err
	}
	acquirer.snapshotCache.storeOCIResolve(key, digest)
	return digest, nil
}

func (acquirer cacheSafeOCIArtifactAcquirer) Extract(ctx context.Context, repoURL, digest string, opts ociartifact.Options) (string, func(), error) {
	// An empty snapshot root with snapshot reads on would make
	// snapshotCachePath pass the extraction dir through unchanged, the
	// delegate release below would delete it, and the deleted path would be
	// memoized and returned — fail loudly instead.
	if acquirer.snapshot && strings.TrimSpace(acquirer.snapshotRoot) == "" {
		return "", nil, fmt.Errorf("oci artifact session snapshot root is empty for %s digest %s", ociartifact.RedactURL(repoURL), digest)
	}
	key, keyErr := ociCacheLockKey(repoURL, digest, opts)
	if keyErr != nil {
		return acquirer.delegate.Extract(ctx, repoURL, digest, opts)
	}
	unlock := acquirer.locks.lock(key)
	defer unlock()

	if acquirer.snapshot {
		if dir, ok := acquirer.snapshotCache.ociExtract(key); ok {
			return dir, func() {}, nil
		}
	}

	dir, release, err := acquirer.delegate.Extract(ctx, repoURL, digest, opts)
	if err != nil || !acquirer.snapshot {
		return dir, release, err
	}
	// Defense in depth ahead of the snapshot copy, which recreates symlinks
	// verbatim: an extracted tree holding an out-of-bounds symlink never
	// enters the session area (upstream CheckOutOfBoundsSymlinks parity,
	// behind the extractor's own tar guards).
	if err := rejectOutOfBoundsSymlinks(dir); err != nil {
		if release != nil {
			release()
		}
		return "", nil, fmt.Errorf("oci artifact %s digest %s: %w", ociartifact.RedactURL(repoURL), digest, err)
	}
	// Copy into the session snapshot area, then close argo's extraction
	// closer immediately: the extraction lives under os.TempDir, so the copy
	// never renames across the temp boundary (EXDEV) and the snapshot is
	// released with the session.
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "oci", dir, snapshotCopyOptions{})
	if release != nil {
		release()
	}
	if err != nil {
		return "", nil, err
	}
	acquirer.snapshotCache.storeOCIExtract(key, snapshot)
	return snapshot, func() {}, nil
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
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "remote", result.Path, snapshotCopyOptions{})
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
	cacheDir, err := chart.ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return "", err
	}
	key, err := chart.NewCacheKey(request)
	if err != nil {
		return "", err
	}
	return absoluteCacheLockKey("chart", filepath.Join(cacheDir, string(request.Kind), key))
}

func ociCacheLockKey(repoURL, digest string, opts ociartifact.Options) (string, error) {
	cacheDir, err := ociartifact.ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return "", err
	}
	return absoluteCacheLockKey("oci", ociartifact.EntryPath(cacheDir, repoURL, digest))
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

// rejectOutOfBoundsSymlinks fails when the tree under root contains a symlink
// that escapes it: absolute targets, or relative targets any traversal step
// of which leaves root (argo-cd CheckOutOfBoundsSymlinks parity,
// util/app/path/path.go). Callers run it before a snapshot copy so an escape
// is never recreated inside the session area.
func rejectOutOfBoundsSymlinks(root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(absRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			return fmt.Errorf("out-of-bounds symlink %q -> %q", relPath, target)
		}
		// Walk each target component so intermediate ".." escapes are caught
		// even when the joined path collapses back inside root.
		currentDir := filepath.Dir(path)
		for part := range strings.SplitSeq(target, string(os.PathSeparator)) {
			currentDir = filepath.Join(currentDir, part)
			rel, err := filepath.Rel(absRoot, currentDir)
			if err != nil {
				return err
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("out-of-bounds symlink %q -> %q", relPath, target)
			}
		}
		return nil
	})
}

type snapshotCopyOptions struct {
	linkRegularFiles bool
	skipDirNames     map[string]struct{}
}

func snapshotCachePath(root, prefix, sourcePath string, opts snapshotCopyOptions) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(sourcePath) == "" {
		return sourcePath, nil
	}
	snapshotRoot, err := os.MkdirTemp(root, prefix+"-*")
	if err != nil {
		return "", err
	}
	snapshotPath := filepath.Join(snapshotRoot, filepath.Base(sourcePath))
	if err := copyCachePath(sourcePath, snapshotPath, opts); err != nil {
		_ = os.RemoveAll(snapshotRoot)
		return "", err
	}
	return snapshotPath, nil
}

func copyCachePath(src, dst string, opts snapshotCopyOptions) error {
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
			if entry.IsDir() {
				if _, skip := opts.skipDirNames[entry.Name()]; skip {
					continue
				}
			}
			if err := copyCachePath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), opts); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyRegularCacheFile(src, dst, info.Mode().Perm(), opts.linkRegularFiles)
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
