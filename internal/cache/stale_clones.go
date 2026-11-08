package cache

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// staleCloneAge is how long a submodule store clone directory must go
// without a change before a prune removes it. A store clone
// (gittree.OpenSubmoduleStore) writes into "<entry>.clone-*" next to its
// entry and renames it into place when it is complete, so such a
// directory that nothing changed for this long belongs to a process that
// was killed. The cache commands run in their own process and cannot take
// the in-process entry locks of a render, so age is the only safe signal.
var staleCloneAge = time.Hour

// cloneSuffix marks a temporary store clone directory.
const cloneSuffix = ".clone-"

// staleClones lists the stale store clone directories of the Git cache at
// gitRoot and the remote cache at remoteRoot. keys, when not nil, limits
// the Git cache to the clones of those entry keys.
func staleClones(gitRoot, remoteRoot string, forbiddenRoots []string, keys map[string]bool, now time.Time) ([]string, error) {
	var out []string
	if root, ok, err := resolveCacheRoot(gitRoot, forbiddenRoots); err != nil {
		return nil, err
	} else if ok {
		found, err := staleClonesIn(root, func(name string) bool {
			key, _, isClone := strings.Cut(name, cloneSuffix)
			return isClone && isCacheKey(key) && (keys == nil || keys[key])
		}, now)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	if keys != nil {
		return out, nil
	}
	found, err := staleRemoteClones(remoteRoot, forbiddenRoots, now)
	return append(out, found...), err
}

// staleRemoteClones lists the stale "repo.clone-*" directories inside the
// remote Git entries.
func staleRemoteClones(remoteRoot string, forbiddenRoots []string, now time.Time) ([]string, error) {
	root, ok, err := resolveCacheRoot(remoteRoot, forbiddenRoots)
	if err != nil || !ok {
		return nil, err
	}
	children, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, child := range children {
		if !child.IsDir() || !isCacheKey(child.Name()) {
			continue
		}
		found, err := staleClonesIn(filepath.Join(root, child.Name()), func(name string) bool {
			return strings.HasPrefix(name, "repo"+cloneSuffix)
		}, now)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

// staleClonesIn lists the directories of dir whose name match accepts and
// that nothing below changed for staleCloneAge. It never follows a
// symlink.
func staleClonesIn(dir string, match func(string) bool, now time.Time) ([]string, error) {
	children, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, child := range children {
		if !child.IsDir() || !match(child.Name()) {
			continue
		}
		path := filepath.Join(dir, child.Name())
		newest, err := newestChange(path)
		if err != nil {
			continue
		}
		if now.Sub(newest) >= staleCloneAge {
			out = append(out, path)
		}
	}
	return out, nil
}

// newestChange returns the latest modification time of path and of
// everything below it, without following symlinks.
func newestChange(path string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, err
}

// removeStaleClones removes the stale clone directories of opts, or
// counts them on a dry run. keys limits the Git cache to those entries;
// nil sweeps every entry and the remote cache.
func removeStaleClones(opts OperationOptions, keys map[string]bool) (int, error) {
	enabled := enabledSources(opts.Sources)
	gitRoot, remoteRoot := "", ""
	if enabled[SourceGit] && (opts.Source == "" || opts.Source == SourceGit) {
		gitRoot = opts.GitCacheDir
	}
	if enabled[SourceRemote] && (opts.Source == "" || opts.Source == SourceRemote) {
		remoteRoot = opts.RemoteCacheDir
	}
	if keys != nil {
		remoteRoot = ""
	}
	if gitRoot == "" && remoteRoot == "" {
		return 0, nil
	}
	paths, err := staleClones(gitRoot, remoteRoot, opts.ForbiddenRoots, keys, time.Now())
	if err != nil || opts.DryRun {
		return len(paths), err
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return 0, err
		}
	}
	return len(paths), nil
}
