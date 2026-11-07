//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package filelock

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// openLockFile opens the lock file without following a symlink. When a
// read-write open is not allowed, it opens the file read-only: flock locks
// a read-only open too.
func openLockFile(path string, mode os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, mode)
	if errors.Is(err, fs.ErrPermission) {
		return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	}
	return file, err
}

// tryLock takes the flock of file without waiting. busy reports that
// another holder has it.
func tryLock(file *os.File) (func(), bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, false, nil
	}
	busy := errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
	return nil, busy, err
}
