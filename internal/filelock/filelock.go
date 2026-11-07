package filelock

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Lock is an exclusive lock on a lock file.
type Lock struct {
	file   *os.File
	unlock func()
}

// retryDelay is how long Acquire waits between tries while another holder
// keeps the lock.
const retryDelay = 25 * time.Millisecond

// Acquire takes an exclusive lock on the lock file at path and waits until
// it holds it, or until ctx ends. It creates a missing lock file with mode
// 0644 before the umask, so a cache that a group shares can be locked by
// each member. When the file exists but only allows reading, it locks a
// read-only open of it. It refuses a lock file that is a symlink.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	return acquire(ctx, path, 0o644, false)
}

// AcquireWithMode is Acquire, and also sets the mode of the lock file to
// mode, so its holder decides who can open it.
func AcquireWithMode(ctx context.Context, path string, mode os.FileMode) (*Lock, error) {
	return acquire(ctx, path, mode, true)
}

func acquire(ctx context.Context, path string, mode os.FileMode, setMode bool) (*Lock, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("lock file %q is a symlink", path)
	}
	file, err := openLockFile(path, mode)
	if err != nil {
		return nil, fmt.Errorf("open lock file %q: %w", path, err)
	}
	if setMode {
		if err := file.Chmod(mode); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("chmod lock file %q: %w", path, err)
		}
	}
	for {
		unlock, busy, err := tryLock(file)
		if err == nil {
			return &Lock{file: file, unlock: unlock}, nil
		}
		if !busy {
			_ = file.Close()
			return nil, fmt.Errorf("lock %q: %w", path, err)
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, fmt.Errorf("lock %q: %w", path, ctx.Err())
		case <-timer.C:
		}
	}
}

// Release releases the lock.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	if l.unlock != nil {
		l.unlock()
	}
	_ = l.file.Close()
}
