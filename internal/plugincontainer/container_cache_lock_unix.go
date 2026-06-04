//go:build !windows

package plugincontainer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type containerCacheLock struct {
	file *os.File
}

func lockContainerCacheDirectory(ctx context.Context, dir string) (*containerCacheLock, error) {
	file, err := os.OpenFile(containerCacheLockPath(dir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open cache lock for %q: %w", dir, err)
	}
	if err := os.Chmod(file.Name(), 0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("chmod cache lock for %q: %w", dir, err)
	}
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &containerCacheLock{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock cache directory %q: %w", dir, err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, fmt.Errorf("lock cache directory %q: %w", dir, ctx.Err())
		case <-timer.C:
		}
	}
}

func (l *containerCacheLock) Close() {
	if l == nil || l.file == nil {
		return
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	_ = l.file.Close()
}
