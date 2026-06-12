package filedigest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sholdee/drydock/internal/digestpath"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// ContentDigestCache memoizes per-file content digests for one run. It is
// safe for concurrent use. Callers own staleness: a run-scoped cache assumes
// worktree content is stable for the run — the same contract as the path-set
// memoization layered above it. Verification recomputes MUST pass a nil
// cache so they re-read disk.
type ContentDigestCache struct {
	mu      sync.Mutex
	digests map[string]string
}

func NewContentDigestCache() *ContentDigestCache {
	return &ContentDigestCache{digests: map[string]string{}}
}

func (c *ContentDigestCache) lookup(path string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	digest, ok := c.digests[path]
	return digest, ok
}

func (c *ContentDigestCache) record(path, digest string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.digests[path] = digest
}

type PathDigestInput struct {
	RepoRoot       string
	Paths          []PathDigestPath
	ForbiddenRoots []string
	ContentCache   *ContentDigestCache
}

type PathDigestPath struct {
	Path       string
	Optional   bool
	MarkerKind string
}

type PathDigestResult struct {
	Digest string
}

const pathDigestVersion = "drydock.filedigest.path-digest.v1"

// PathDigest returns a stable digest for repository-relative filesystem inputs.
// It reads the working tree as drydock renderers see it: untracked and ignored
// files under requested directories are included, while symlinks and special
// files are rejected so callers can fail closed.
func PathDigest(ctx context.Context, input PathDigestInput) (PathDigestResult, error) {
	if err := ctx.Err(); err != nil {
		return PathDigestResult{}, err
	}
	root, forbiddenRoots, err := normalizeDigestRoot(input.RepoRoot, input.ForbiddenRoots)
	if err != nil {
		return PathDigestResult{}, err
	}
	paths, err := normalizePathDigestInputs(ctx, input.Paths)
	if err != nil {
		return PathDigestResult{}, err
	}

	digest := sha256.New()
	writePathDigestRecord(digest, "version", pathDigestVersion)
	for _, requested := range paths {
		if err := ctx.Err(); err != nil {
			return PathDigestResult{}, err
		}
		if requested.markerKind != "" {
			writePathDigestRecord(digest, "marker", requested.path, requested.markerKind, optionalRecordValue(requested.optional))
			continue
		}
		if err := writeFilesystemPathDigestEntry(ctx, digest, root, forbiddenRoots, requested, input.ContentCache); err != nil {
			return PathDigestResult{}, fmt.Errorf("digest filesystem path %q: %w", requested.path, err)
		}
	}

	return PathDigestResult{Digest: hex.EncodeToString(digest.Sum(nil))}, nil
}

type normalizedPathDigestPath struct {
	path       string
	optional   bool
	markerKind string
}

func normalizePathDigestInputs(ctx context.Context, requested []PathDigestPath) ([]normalizedPathDigestPath, error) {
	type pathKey struct {
		path       string
		markerKind string
	}
	byPath := make(map[pathKey]bool, len(requested))
	for _, item := range requested {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clean, err := canonicalPathDigestPath(item.Path)
		if err != nil {
			return nil, err
		}
		if strings.Contains(item.MarkerKind, "\x00") {
			return nil, fmt.Errorf("marker kind contains nul")
		}
		key := pathKey{path: clean, markerKind: item.MarkerKind}
		if optional, ok := byPath[key]; ok {
			byPath[key] = optional && item.Optional
			continue
		}
		byPath[key] = item.Optional
	}

	paths := make([]normalizedPathDigestPath, 0, len(byPath))
	for key, optional := range byPath {
		paths = append(paths, normalizedPathDigestPath{
			path:       key.path,
			optional:   optional,
			markerKind: key.markerKind,
		})
	}
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].path == paths[j].path {
			return paths[i].markerKind < paths[j].markerKind
		}
		return paths[i].path < paths[j].path
	})
	return paths, nil
}

func canonicalPathDigestPath(value string) (string, error) {
	return digestpath.CanonicalFilesystemPath(value)
}

func normalizeDigestRoot(repoRoot string, forbiddenRoots []string) (string, []string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return "", nil, fmt.Errorf("repository root must not be empty")
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", nil, fmt.Errorf("resolve repository root: %w", err)
	}
	resolvedRoot, err := pathsafety.ResolveForContainment(absRoot)
	if err != nil {
		return "", nil, fmt.Errorf("resolve repository root containment: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return "", nil, fmt.Errorf("stat repository root: %w", err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("repository root is not a directory")
	}

	normalizedForbidden := make([]string, 0, len(forbiddenRoots))
	for _, root := range forbiddenRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absForbidden, err := filepath.Abs(root)
		if err != nil {
			return "", nil, fmt.Errorf("resolve forbidden root %q: %w", root, err)
		}
		resolvedForbidden, err := pathsafety.ResolveForContainment(absForbidden)
		if err != nil {
			return "", nil, fmt.Errorf("resolve forbidden root %q containment: %w", root, err)
		}
		forbiddenInsideRoot, err := pathInsideRoot(resolvedForbidden, resolvedRoot)
		if err != nil {
			return "", nil, err
		}
		rootInsideForbidden, err := pathInsideRoot(resolvedRoot, resolvedForbidden)
		if err != nil {
			return "", nil, err
		}
		if forbiddenInsideRoot || rootInsideForbidden {
			normalizedForbidden = append(normalizedForbidden, resolvedForbidden)
		}
	}
	sort.Strings(normalizedForbidden)
	return resolvedRoot, normalizedForbidden, nil
}

func writeFilesystemPathDigestEntry(ctx context.Context, digest hash.Hash, repoRoot string, forbiddenRoots []string, requested normalizedPathDigestPath, cache *ContentDigestCache) error {
	fullPath, info, missing, err := resolveRequestedPath(repoRoot, forbiddenRoots, requested.path, requested.optional)
	if err != nil {
		return err
	}
	if missing {
		writePathDigestRecord(digest, "path", requested.path, "missing", "optional")
		return nil
	}
	return writeExistingFilesystemEntry(ctx, digest, requested.path, fullPath, info, forbiddenRoots, cache)
}

func resolveRequestedPath(repoRoot string, forbiddenRoots []string, relPath string, optional bool) (string, os.FileInfo, bool, error) {
	if relPath == "." {
		if err := rejectForbiddenPath(repoRoot, forbiddenRoots); err != nil {
			return "", nil, false, err
		}
		info, err := os.Lstat(repoRoot)
		if err != nil {
			return "", nil, false, fmt.Errorf("stat path: %w", err)
		}
		return repoRoot, info, false, nil
	}

	components := strings.Split(relPath, "/")
	current := repoRoot
	for i, component := range components {
		current = filepath.Join(current, component)
		if err := rejectForbiddenPath(current, forbiddenRoots); err != nil {
			return "", nil, false, err
		}
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				if optional {
					return filesystemPath(repoRoot, relPath), nil, true, nil
				}
				return "", nil, false, fmt.Errorf("required path is missing")
			}
			return "", nil, false, fmt.Errorf("stat path: %w", err)
		}
		if component == ".git" {
			return "", nil, false, fmt.Errorf("unsupported .git path")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, false, fmt.Errorf("unsupported symlink")
		}
		if i < len(components)-1 && !info.IsDir() {
			return "", nil, false, fmt.Errorf("path component %q is not a directory", path.Join(components[:i+1]...))
		}
		if i == len(components)-1 {
			return current, info, false, nil
		}
	}
	return filesystemPath(repoRoot, relPath), nil, true, nil
}

func writeExistingFilesystemEntry(ctx context.Context, digest hash.Hash, relPath, fullPath string, info os.FileInfo, forbiddenRoots []string, cache *ContentDigestCache) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if info.Name() == ".git" {
		return fmt.Errorf("unsupported .git path")
	}
	if err := rejectForbiddenPath(fullPath, forbiddenRoots); err != nil {
		return err
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return fmt.Errorf("unsupported symlink")
	case mode.IsDir():
		return writeDirectoryDigestEntry(ctx, digest, relPath, fullPath, forbiddenRoots, cache)
	case mode.IsRegular():
		return writeRegularFileDigestEntry(ctx, digest, relPath, fullPath, mode, cache)
	default:
		return fmt.Errorf("unsupported filesystem mode %s", mode.String())
	}
}

func writeDirectoryDigestEntry(ctx context.Context, digest hash.Hash, relPath, fullPath string, forbiddenRoots []string, cache *ContentDigestCache) error {
	writePathDigestRecord(digest, "path", relPath, "present", "directory")
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return fmt.Errorf("read directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := writeDirectoryChildDigestEntry(ctx, digest, relPath, fullPath, entry, forbiddenRoots, cache); err != nil {
			return err
		}
	}
	return nil
}

func writeDirectoryChildDigestEntry(ctx context.Context, digest hash.Hash, relPath, fullPath string, entry os.DirEntry, forbiddenRoots []string, cache *ContentDigestCache) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.Name() == ".git" {
		return fmt.Errorf("unsupported .git path")
	}
	childRel := path.Join(relPath, entry.Name())
	if relPath == "." {
		childRel = entry.Name()
	}
	childPath := filepath.Join(fullPath, entry.Name())
	if err := rejectForbiddenPath(childPath, forbiddenRoots); err != nil {
		return err
	}
	childInfo, err := entry.Info()
	if err != nil {
		return fmt.Errorf("stat child %q: %w", childRel, err)
	}
	if err := writeExistingFilesystemEntry(ctx, digest, childRel, childPath, childInfo, forbiddenRoots, cache); err != nil {
		return fmt.Errorf("%s: %w", childRel, err)
	}
	return nil
}

func writeRegularFileDigestEntry(ctx context.Context, digest hash.Hash, relPath, fullPath string, mode os.FileMode, cache *ContentDigestCache) error {
	contentDigest, ok := cache.lookup(fullPath)
	if !ok {
		computed, err := fileContentDigest(ctx, fullPath)
		if err != nil {
			return err
		}
		contentDigest = computed
		cache.record(fullPath, contentDigest)
	}
	writePathDigestRecord(digest, "path", relPath, "present", modeClass(mode), "sha256", contentDigest)
	return nil
}

func fileContentDigest(ctx context.Context, filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	defer func() { _ = file.Close() }()

	digest := sha256.New()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := digest.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			break
		}
		return "", fmt.Errorf("read file: %w", readErr)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func filesystemPath(repoRoot, relPath string) string {
	if relPath == "." {
		return repoRoot
	}
	return filepath.Join(repoRoot, filepath.FromSlash(relPath))
}

func rejectForbiddenPath(target string, forbiddenRoots []string) error {
	if len(forbiddenRoots) == 0 {
		return nil
	}
	cleanTarget := filepath.Clean(target)
	for _, root := range forbiddenRoots {
		inside, err := pathInsideRoot(cleanTarget, root)
		if err != nil {
			return err
		}
		if inside {
			return fmt.Errorf("path is inside forbidden root %q", root)
		}
	}
	return nil
}

func pathInsideRoot(target, root string) (bool, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return !pathsafety.RelEscapes(rel), nil
}

func modeClass(mode os.FileMode) string {
	if mode&0o111 != 0 {
		return "executable"
	}
	return "regular"
}

func optionalRecordValue(optional bool) string {
	if optional {
		return "optional"
	}
	return "required"
}

func writePathDigestRecord(digest hash.Hash, fields ...string) {
	digestpath.WriteRecord(digest, fields...)
}
