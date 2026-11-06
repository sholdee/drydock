package render

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/pathsafety"
)

// maxTracedSymlinks bounds one symlink trace, as the kernel and
// filepath.EvalSymlinks bound symlink resolution.
const maxTracedSymlinks = 255

// DirectorySymlinkInputPaths returns the repository paths outside dirs that
// the symlinks under dirs read or would read: every symlink that resolving
// each one passes through, and the path it ends at, even when that path is
// missing. The directory renderer and the Jsonnet importer follow file
// symlinks in the repository, so these paths are render inputs of a
// directory source: a change to any of them can change the render. dirs are
// repository-relative paths with forward slashes. Paths outside the
// repository are left out: the renderer never reads them, and the
// repository-wide out-of-bounds symlink check fails such a repository
// unless reposerver.allow.oob.symlinks is true. A directory under dirs that
// is missing or unreadable adds nothing. The result is sorted, with forward
// slashes.
func DirectorySymlinkInputPaths(repoRoot string, dirs []string) []string {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil
	}
	covered := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		clean := filepath.Clean(filepath.FromSlash(dir))
		if filepath.IsAbs(clean) || pathsafety.RelEscapes(clean) {
			continue
		}
		covered = append(covered, clean)
	}
	found := map[string]struct{}{}
	for _, dir := range covered {
		collectDirectorySymlinkInputs(realRoot, dir, covered, found)
	}
	out := make([]string, 0, len(found))
	for rel := range found {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// collectDirectorySymlinkInputs adds to found the repository paths outside
// covered that the symlinks under dir reach.
func collectDirectorySymlinkInputs(realRoot, dir string, covered []string, found map[string]struct{}) {
	start := filepath.Join(realRoot, dir)
	_ = filepath.WalkDir(start, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == start {
				return err
			}
			return nil
		}
		if entry.IsDir() {
			if path != start && (entry.Name() == drydockCacheMetadataDirName || strings.EqualFold(entry.Name(), ".git")) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		visited, final := traceSymlinkPaths(path)
		if final != "" {
			visited = append(visited, final)
		}
		for _, item := range visited {
			rel, err := filepath.Rel(realRoot, item)
			if err != nil || rel == "." || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) || relUnderAny(rel, covered) {
				continue
			}
			found[filepath.ToSlash(rel)] = struct{}{}
		}
		return nil
	})
}

func relUnderAny(rel string, dirs []string) bool {
	for _, dir := range dirs {
		if dir == "." || rel == dir || strings.HasPrefix(rel, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// traceSymlinkPaths resolves the absolute path start one component at a
// time, as filepath.EvalSymlinks does, and returns every symlink it passes
// through and the path it ends at. When a component is missing, the end is
// the path the rest of the components would name below the last existing
// directory. A symlink loop or an unreadable link ends the trace with no end
// path.
func traceSymlinkPaths(start string) (visited []string, final string) {
	volume := filepath.VolumeName(start)
	dest := volume + string(filepath.Separator)
	rest := splitPathComponents(start[len(volume):])
	links := 0
	for len(rest) > 0 {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			dest = filepath.Dir(dest)
			continue
		}
		next := filepath.Join(dest, part)
		info, err := os.Lstat(next)
		if err != nil {
			return visited, filepath.Join(append([]string{next}, rest...)...)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.IsDir() && len(rest) > 0 {
				// A file in the middle of a path does not resolve.
				return visited, filepath.Join(append([]string{next}, rest...)...)
			}
			dest = next
			continue
		}
		links++
		if links > maxTracedSymlinks {
			return visited, ""
		}
		visited = append(visited, next)
		target, err := os.Readlink(next)
		if err != nil {
			return visited, ""
		}
		if filepath.IsAbs(target) {
			targetVolume := filepath.VolumeName(target)
			dest = targetVolume + string(filepath.Separator)
			target = target[len(targetVolume):]
		}
		rest = append(splitPathComponents(target), rest...)
	}
	return visited, dest
}

func splitPathComponents(path string) []string {
	return strings.Split(path, string(filepath.Separator))
}
