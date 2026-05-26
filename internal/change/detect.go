package change

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/sholdee/drydock/internal/streamcmp"
)

func Detect(baseRoot, currentRoot string) ([]string, error) {
	paths := make(map[string]struct{})
	if err := collectFiles(baseRoot, paths); err != nil {
		return nil, err
	}
	if err := collectFiles(currentRoot, paths); err != nil {
		return nil, err
	}

	var changed []string
	for rel := range paths {
		basePath := filepath.Join(baseRoot, filepath.FromSlash(rel))
		currentPath := filepath.Join(currentRoot, filepath.FromSlash(rel))
		baseInfo, baseErr := os.Lstat(basePath)
		if baseErr != nil && !errors.Is(baseErr, fs.ErrNotExist) {
			return nil, baseErr
		}
		currentInfo, currentErr := os.Lstat(currentPath)
		if currentErr != nil && !errors.Is(currentErr, fs.ErrNotExist) {
			return nil, currentErr
		}
		if baseErr != nil || currentErr != nil {
			changed = append(changed, rel)
			continue
		}
		fileChanged, err := pathsChanged(basePath, currentPath, baseInfo, currentInfo)
		if err != nil {
			return nil, err
		}
		if fileChanged {
			changed = append(changed, rel)
		}
	}

	sort.Strings(changed)
	return changed, nil
}

func collectFiles(root string, paths map[string]struct{}) error {
	return filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		paths[filepath.ToSlash(rel)] = struct{}{}
		return nil
	})
}

func isRegularFile(info fs.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0
}

func isSymlink(info fs.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0
}

func pathsChanged(basePath, currentPath string, baseInfo, currentInfo fs.FileInfo) (bool, error) {
	switch {
	case isSymlink(baseInfo) && isSymlink(currentInfo):
		return symlinksChanged(basePath, currentPath)
	case !isRegularFile(baseInfo) || !isRegularFile(currentInfo):
		return true, nil
	default:
		return regularFilesChanged(basePath, currentPath, baseInfo, currentInfo)
	}
}

func symlinksChanged(basePath, currentPath string) (bool, error) {
	baseTarget, err := os.Readlink(basePath)
	if err != nil {
		return false, err
	}
	currentTarget, err := os.Readlink(currentPath)
	if err != nil {
		return false, err
	}
	return baseTarget != currentTarget, nil
}

func regularFilesChanged(basePath, currentPath string, baseInfo, currentInfo fs.FileInfo) (bool, error) {
	if baseInfo.Size() != currentInfo.Size() {
		return true, nil
	}
	base, err := os.Open(basePath)
	if err != nil {
		return false, err
	}
	defer base.Close()
	current, err := os.Open(currentPath)
	if err != nil {
		return false, err
	}
	defer current.Close()
	equal, err := streamcmp.Equal(base, current)
	if err != nil {
		return false, err
	}
	return !equal, nil
}
