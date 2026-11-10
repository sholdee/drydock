package cache

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KindLFS is the kind of a Git LFS object of the Git cache. drydock keeps
// the objects that it downloads in <git cache>/lfs/objects/<oid[0:2]>/
// <oid[2:4]>/<oid>, one file per object, shared by every repository. Each
// object is an entry keyed by its SHA-256, and its modification time is
// its last use, because drydock touches it on every read.
const KindLFS = "lfs"

// lfsDirName is the directory of the Git LFS store in the Git cache. Its
// name is not a cache key, so the Git entry lister skips it.
const lfsDirName = "lfs"

// listLFSEntries lists the objects of the Git LFS store of the Git cache
// at gitRoot.
func listLFSEntries(gitRoot string, forbiddenRoots []string) ([]Entry, error) {
	root, ok, err := resolveCacheRoot(gitRoot, forbiddenRoots)
	if err != nil || !ok {
		return nil, err
	}
	objects := filepath.Join(root, lfsDirName, "objects")
	var entries []Entry
	for _, first := range readDirNames(objects) {
		for _, second := range readDirNames(filepath.Join(objects, first)) {
			dir := filepath.Join(objects, first, second)
			for _, name := range readDirNames(dir) {
				entry, ok := lfsEntry(root, dir, first, second, name)
				if ok {
					entries = append(entries, entry)
				}
			}
		}
	}
	return entries, nil
}

func lfsEntry(root, dir, first, second, oid string) (Entry, bool) {
	if !isCacheKey(oid) || oid[0:2] != first || oid[2:4] != second {
		return Entry{}, false
	}
	path := filepath.Join(dir, oid)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Entry{}, false
	}
	return Entry{
		Source:     SourceGit,
		Kind:       KindLFS,
		Key:        oid,
		Path:       path,
		SizeBytes:  info.Size(),
		ModifiedAt: info.ModTime(),
		cacheRoot:  root,
	}, true
}

// readDirNames returns the names in dir, or nil.
func readDirNames(dir string) []string {
	children, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(children))
	for _, child := range children {
		names = append(names, child.Name())
	}
	return names
}

// staleLFSTemps lists the temporary download files of the Git LFS store
// of the Git cache at gitRoot that nothing changed for staleCloneAge. A
// download writes "<oid>.*.tmp" in lfs/tmp and renames it into place when
// it is complete, so such a file belongs to a process that was killed.
func staleLFSTemps(gitRoot string, forbiddenRoots []string, now time.Time) ([]string, error) {
	root, ok, err := resolveCacheRoot(gitRoot, forbiddenRoots)
	if err != nil || !ok {
		return nil, err
	}
	dir := filepath.Join(root, lfsDirName, "tmp")
	var out []string
	for _, name := range readDirNames(dir) {
		if !strings.HasSuffix(name, ".tmp") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) >= staleCloneAge {
			out = append(out, path)
		}
	}
	return out, nil
}

// removeStaleLFSTemps removes the stale temporary files of the Git LFS
// store (staleLFSTemps) when opts covers the Git cache, or counts them on
// a dry run.
func removeStaleLFSTemps(opts OperationOptions) (int, error) {
	if !enabledSources(opts.Sources)[SourceGit] || (opts.Source != "" && opts.Source != SourceGit) {
		return 0, nil
	}
	paths, err := staleLFSTemps(opts.GitCacheDir, opts.ForbiddenRoots, time.Now())
	if err != nil || opts.DryRun {
		return len(paths), err
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	}
	return len(paths), nil
}

// removeStaleFiles removes the stale clones (removeStaleClones) and the
// stale Git LFS downloads (removeStaleLFSTemps) of opts, and counts them in
// result.
func removeStaleFiles(opts OperationOptions, keys map[string]bool, result *OperationResult) error {
	var err error
	if result.StaleClonesRemoved, err = removeStaleClones(opts, keys); err != nil {
		return err
	}
	result.StaleLFSTempsRemoved, err = removeStaleLFSTemps(opts)
	return err
}
