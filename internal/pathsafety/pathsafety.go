package pathsafety

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func RelEscapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func SlashRelEscapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, "../")
}

func CleanRelative(raw string) (string, bool) {
	clean := filepath.Clean(raw)
	if filepath.IsAbs(clean) || clean == "." || RelEscapes(clean) {
		return "", false
	}
	return clean, true
}

func IsInsideAny(targetPath string, roots []string) (bool, string, error) {
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return false, "", err
	}
	absPath = filepath.Clean(absPath)
	resolvedPath, err := ResolveForContainment(absPath)
	if err != nil {
		return false, "", err
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return false, "", err
		}
		absRoot = filepath.Clean(absRoot)
		resolvedRoot, err := ResolveForContainment(absRoot)
		if err != nil {
			return false, "", err
		}
		rel, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err == nil && !RelEscapes(rel) {
			return true, absRoot, nil
		}
	}
	return false, "", nil
}

func ResolveForContainment(targetPath string) (string, error) {
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absPath)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for _, v := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, v)
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
