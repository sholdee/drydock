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

// RelEntersGit reports whether any component of the OS-relative path rel is
// ".git", compared case-insensitively: on case-insensitive filesystems
// (macOS, Windows) ".GIT/config" reaches the same file as ".git/config".
// A .git directory holds repository metadata, credentials included
// (actions/checkout persists its token in .git/config by default), never
// render input.
func RelEntersGit(rel string) bool {
	for component := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if strings.EqualFold(component, ".git") {
			return true
		}
	}
	return false
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

// ResolveSymlinkedRoot returns root with its symlinks resolved when root
// itself is a symlink, and root unchanged otherwise. A walk that starts at
// a symlink stops there, so drydock resolves such a repository root once
// at entry. An Argo CD checkout is never a symlink. A root that only has a
// symlink above it, such as a macOS temporary directory, walks normally and
// keeps its spelling, so its paths in output do not change. A relative
// symlink keeps a relative root relative.
func ResolveSymlinkedRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		return root
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return root
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return root
	}
	return resolved
}
