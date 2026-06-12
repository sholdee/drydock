package gitref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/digestpath"
	"github.com/sholdee/drydock/internal/source"
)

type PathDigestInput struct {
	RepoPath string
	Revision string
	Paths    []PathDigestPath
}

type PathDigestPath struct {
	Path     string
	Optional bool
}

type PathDigestResult struct {
	Digest   string
	Revision string
}

const committedPathDigestVersion = "drydock.gitref.committed-path-digest.v1"

// CommittedPathDigest returns a stable digest for repository-relative paths in
// a committed Git tree. It reads Git object identity only, not worktree files.
func CommittedPathDigest(ctx context.Context, input PathDigestInput) (PathDigestResult, error) {
	if err := ctx.Err(); err != nil {
		return PathDigestResult{}, err
	}
	ref := strings.TrimSpace(input.Revision)
	if ref == "" {
		ref = "HEAD"
	}
	paths, err := normalizePathDigestPaths(ctx, input.Paths)
	if err != nil {
		return PathDigestResult{}, err
	}

	repo, displayRepoPath, err := openLocalRepository(input.RepoPath)
	if err != nil {
		return PathDigestResult{}, err
	}
	hash, err := source.ResolveGitRevision(repo, ref)
	if err != nil {
		return PathDigestResult{}, fmt.Errorf("resolve Git ref %q in %q: %w", ref, displayRepoPath, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return PathDigestResult{}, fmt.Errorf("load Git commit %q in %q: %w", hash.String(), displayRepoPath, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return PathDigestResult{}, fmt.Errorf("load Git tree for %q in %q: %w", hash.String(), displayRepoPath, err)
	}
	if err := ctx.Err(); err != nil {
		return PathDigestResult{}, err
	}

	digest := sha256.New()
	writePathDigestRecord(digest, "version", committedPathDigestVersion)
	for _, requested := range paths {
		if err := ctx.Err(); err != nil {
			return PathDigestResult{}, err
		}
		if err := writeCommittedPathDigestEntry(ctx, digest, tree, requested); err != nil {
			return PathDigestResult{}, fmt.Errorf("digest Git path %q at %s: %w", requested.path, hash.String(), err)
		}
	}

	return PathDigestResult{
		Digest:   hex.EncodeToString(digest.Sum(nil)),
		Revision: hash.String(),
	}, nil
}

type normalizedPathDigestPath struct {
	path     string
	optional bool
}

func normalizePathDigestPaths(ctx context.Context, requested []PathDigestPath) ([]normalizedPathDigestPath, error) {
	byPath := make(map[string]bool, len(requested))
	for _, item := range requested {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clean, err := canonicalPathDigestPath(item.Path)
		if err != nil {
			return nil, err
		}
		if optional, ok := byPath[clean]; ok {
			byPath[clean] = optional && item.Optional
			continue
		}
		byPath[clean] = item.Optional
	}

	paths := make([]normalizedPathDigestPath, 0, len(byPath))
	for clean, optional := range byPath {
		paths = append(paths, normalizedPathDigestPath{path: clean, optional: optional})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].path < paths[j].path })
	return paths, nil
}

func canonicalPathDigestPath(value string) (string, error) {
	return digestpath.CanonicalGitPath(value)
}

func writeCommittedPathDigestEntry(ctx context.Context, digest hash.Hash, tree *object.Tree, requested normalizedPathDigestPath) error {
	if requested.path == "." {
		if err := rejectSubmodulesInDigestTree(ctx, requested.path, tree); err != nil {
			return err
		}
		writePathDigestRecord(digest, "path", requested.path, "present", filemode.Dir.String(), "tree", tree.Hash.String())
		return nil
	}

	entry, err := tree.FindEntry(requested.path)
	if err != nil {
		if !pathDigestEntryMissing(err) {
			return err
		}
		if !requested.optional {
			return fmt.Errorf("required Git path is missing")
		}
		writePathDigestRecord(digest, "path", requested.path, "missing")
		return nil
	}

	switch entry.Mode {
	case filemode.Dir:
		subtree, err := tree.Tree(requested.path)
		if err != nil {
			return err
		}
		if err := rejectSubmodulesInDigestTree(ctx, requested.path, subtree); err != nil {
			return err
		}
		writePathDigestRecord(digest, "path", requested.path, "present", entry.Mode.String(), "tree", entry.Hash.String())
	case filemode.Regular, filemode.Deprecated, filemode.Executable, filemode.Symlink:
		writePathDigestRecord(digest, "path", requested.path, "present", entry.Mode.String(), "blob", entry.Hash.String())
	case filemode.Submodule:
		return fmt.Errorf("unsupported gitlink/submodule")
	case filemode.Empty:
		return fmt.Errorf("unsupported empty git tree file mode")
	default:
		return fmt.Errorf("unsupported git tree file mode %s", entry.Mode)
	}
	return nil
}

func pathDigestEntryMissing(err error) bool {
	return errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound)
}

func rejectSubmodulesInDigestTree(ctx context.Context, base string, tree *object.Tree) error {
	for _, entry := range tree.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		entryPath := entry.Name
		if base != "." {
			entryPath = path.Join(base, entry.Name)
		}
		switch entry.Mode {
		case filemode.Submodule:
			return fmt.Errorf("unsupported gitlink/submodule at %q", entryPath)
		case filemode.Dir:
			subtree, err := tree.Tree(entry.Name)
			if err != nil {
				return err
			}
			if err := rejectSubmodulesInDigestTree(ctx, entryPath, subtree); err != nil {
				return err
			}
		case filemode.Empty, filemode.Regular, filemode.Deprecated, filemode.Executable, filemode.Symlink:
		}
	}
	return nil
}

func writePathDigestRecord(digest hash.Hash, fields ...string) {
	digestpath.WriteRecord(digest, fields...)
}
