//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package filelock

import (
	"errors"
	"os"
)

// errUnsupported reports a platform without a file lock that drydock uses.
var errUnsupported = errors.New("file locks are not supported on this platform")

func openLockFile(path string, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, mode)
}

func tryLock(*os.File) (func(), bool, error) {
	return nil, false, errUnsupported
}
