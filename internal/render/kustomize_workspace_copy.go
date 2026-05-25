package render

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func copyAcquiredRemoteKustomizeResource(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source path %q is a symlink", src)
	}
	if info.IsDir() {
		return copyRegularTree(src, dst)
	}
	if info.Mode().IsRegular() {
		return copyRegularFile(src, dst)
	}
	return fmt.Errorf("source path %q is not a regular file or directory", src)
}

func copyRegularTree(srcRoot, dstRoot string) error {
	return copyTree(srcRoot, dstRoot, false)
}

func copyWorkspaceTree(srcRoot, dstRoot string) error {
	return copyTree(srcRoot, dstRoot, true)
}

//nolint:gocyclo // Tree copy handles regular files, directories, optional symlinks, and parent safety.
func copyTree(srcRoot, dstRoot string, preserveSymlinks bool) error {
	srcRoot = filepath.Clean(srcRoot)
	dstRoot = filepath.Clean(dstRoot)

	return filepath.WalkDir(srcRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy source path %q escapes source root %q", path, srcRoot)
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dstPath := filepath.Clean(filepath.Join(dstRoot, rel))
		dstRel, err := filepath.Rel(dstRoot, dstPath)
		if err != nil || dstRel == ".." || strings.HasPrefix(dstRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy destination path %q escapes destination root %q", dstPath, dstRoot)
		}

		if entry.Type()&os.ModeSymlink != 0 {
			if !preserveSymlinks {
				return fmt.Errorf("copy source path %q is a symlink", path)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, dstPath)
		}

		if entry.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("copy source path %q is not a regular file", path)
		}
		return copyRegularFile(path, dstPath)
	})
}

func copyRegularFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source path %q is a symlink", src)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source path %q is not a regular file", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}
