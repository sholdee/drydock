package change

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
		base, baseErr := os.ReadFile(filepath.Join(baseRoot, filepath.FromSlash(rel)))
		current, currentErr := os.ReadFile(filepath.Join(currentRoot, filepath.FromSlash(rel)))
		if baseErr != nil && !errors.Is(baseErr, fs.ErrNotExist) {
			return nil, baseErr
		}
		if currentErr != nil && !errors.Is(currentErr, fs.ErrNotExist) {
			return nil, currentErr
		}
		if baseErr != nil || currentErr != nil || !bytes.Equal(base, current) {
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
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
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
