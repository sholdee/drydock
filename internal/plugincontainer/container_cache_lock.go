package plugincontainer

import (
	"context"
	"fmt"

	"github.com/sholdee/drydock/internal/filelock"
)

// containerCacheLock is the lock on one container cache directory.
type containerCacheLock struct {
	lock *filelock.Lock
}

// lockContainerCacheDirectory takes the lock file of the container cache
// directory dir, with mode 0600, and waits until it holds it, or until ctx
// ends.
func lockContainerCacheDirectory(ctx context.Context, dir string) (*containerCacheLock, error) {
	lock, err := filelock.AcquireWithMode(ctx, containerCacheLockPath(dir), 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock cache directory %q: %w", dir, err)
	}
	return &containerCacheLock{lock: lock}, nil
}

// Close releases the lock.
func (l *containerCacheLock) Close() {
	if l == nil {
		return
	}
	l.lock.Release()
}
