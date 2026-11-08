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

// ReplaceFile writes data to dst as a new file. A tree that
// LinkOrCopyRegularFile built can hold dst as a hard link to a file outside
// the tree, and a write in place (os.WriteFile, O_TRUNC, append) goes
// through the link into that file. So ReplaceFile first removes a regular
// file at dst, which breaks the link, and then creates dst with
// O_CREATE|O_EXCL. It refuses a symlink or any other non-regular entry at
// dst. The exclusive create also fails when something appears at dst
// between the remove and the create, a symlink included.
func ReplaceFile(dst string, data []byte, mode os.FileMode) error {
	info, err := os.Lstat(dst)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path %q is a symlink", dst)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("destination path %q is not a regular file", dst)
		}
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
	case !os.IsNotExist(err):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
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
