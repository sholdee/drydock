//go:build windows

package plugincontainer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type containerCacheLock struct {
	file       *os.File
	overlapped windows.Overlapped
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
	lock := &containerCacheLock{file: file}
	for {
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&lock.overlapped,
		)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	_ = l.file.Close()
}
