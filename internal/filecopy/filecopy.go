package filecopy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func CopyRegularFile(src, dst string, mode os.FileMode) error {
	info, err := regularSourceInfo(src)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func LinkOrCopyRegularFile(src, dst string, mode os.FileMode) error {
	info, err := regularSourceInfo(src)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode().Perm() == mode {
		if err := os.Link(src, dst); err == nil {
			return nil
		}
	}
	return CopyRegularFile(src, dst, mode)
}

func regularSourceInfo(src string) (os.FileInfo, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("source path %q is a symlink", src)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source path %q is not a regular file", src)
	}
	return info, nil
}
