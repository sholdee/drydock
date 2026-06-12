package gitref

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// WorktreePathsMatchRevision reports whether the worktree content of every
// requested repository-relative path matches the committed content at
// Revision: identical tracked file content under each path and no extra
// worktree files inside requested directories. Symlinks, submodules, and
// other unsupported modes fail closed (false). Errors mean the comparison
// could not be completed; callers must treat that as a mismatch.
func WorktreePathsMatchRevision(ctx context.Context, input PathDigestInput) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ref := strings.TrimSpace(input.Revision)
	if ref == "" {
		ref = "HEAD"
	}
	paths, err := normalizePathDigestPaths(ctx, input.Paths)
	if err != nil {
		return false, err
	}
	repo, displayRepoPath, err := openLocalRepository(input.RepoPath)
	if err != nil {
		return false, err
	}
	tree, err := treeForRef(repo, ref)
	if err != nil {
		return false, fmt.Errorf("resolve Git ref %q in %q: %w", ref, displayRepoPath, err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("open Git worktree in %q: %w", displayRepoPath, err)
	}
	root := worktree.Filesystem.Root()
	for _, requested := range paths {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		match, err := worktreePathMatchesTree(ctx, root, tree, requested.path)
		if err != nil || !match {
			return match, err
		}
	}
	return true, nil
}

func worktreePathMatchesTree(ctx context.Context, root string, tree *object.Tree, relPath string) (bool, error) {
	if relPath == "." {
		// Reject any gitlink in the root tree before iterating files.
		// go-git's FileIter skips filemode.Submodule entries entirely, so the
		// per-file guard below is unreachable for gitlinks; pre-check here.
		if err := rejectSubmodulesInDigestTree(ctx, ".", tree); err != nil {
			return false, err
		}
		return worktreeDirMatchesTree(ctx, root, tree, ".")
	}
	entry, err := tree.FindEntry(relPath)
	if err != nil {
		if !pathDigestEntryMissing(err) {
			return false, err
		}
		// Absent from the commit: the worktree must not have it either.
		// Optional/required is irrelevant at match time — presence equality
		// is the contract; an optional path that appears is a mismatch.
		_, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(relPath)))
		if os.IsNotExist(statErr) {
			return true, nil
		}
		if statErr != nil {
			return false, statErr
		}
		return false, nil
	}
	switch entry.Mode {
	case filemode.Dir:
		subtree, err := tree.Tree(relPath)
		if err != nil {
			return false, err
		}
		// Reject any gitlink in the subtree before iterating files.
		// go-git's FileIter skips filemode.Submodule entries entirely, so the
		// per-file guard in committedTreeFilesMatchWorktree is unreachable for
		// gitlinks; pre-check here ensures fail-closed behavior.
		if err := rejectSubmodulesInDigestTree(ctx, relPath, subtree); err != nil {
			return false, err
		}
		return worktreeDirMatchesTree(ctx, root, subtree, relPath)
	case filemode.Regular, filemode.Deprecated, filemode.Executable:
		file, err := tree.File(relPath)
		if err != nil {
			return false, err
		}
		changed, err := worktreeFileChanged(root, file)
		if err != nil {
			return false, err
		}
		return !changed, nil
	case filemode.Symlink, filemode.Empty:
		// Symlink targets are invisible to the committed digest; fail closed.
		return false, nil
	case filemode.Submodule:
		// Load-bearing guard: when a requested path directly names a gitlink
		// entry, FindEntry returns filemode.Submodule and this arm handles it.
		// rejectSubmodulesInDigestTree only covers "." and Dir entries; it does
		// not protect this code path. Fail closed.
		return false, nil
	default:
		return false, nil
	}
}

func worktreeDirMatchesTree(ctx context.Context, root string, tree *object.Tree, base string) (bool, error) {
	committed, match, err := committedTreeFilesMatchWorktree(ctx, root, tree, base)
	if err != nil || !match {
		return false, err
	}
	extra, err := worktreeDirHasExtraFiles(ctx, root, base, committed)
	if err != nil {
		return false, err
	}
	return !extra, nil
}

// committedTreeFilesMatchWorktree compares every committed file under base
// against the worktree and returns the committed name set for the extra-file
// sweep. A false match means a committed file changed, was deleted, or has an
// unverifiable mode (symlink, submodule).
func committedTreeFilesMatchWorktree(ctx context.Context, root string, tree *object.Tree, base string) (map[string]struct{}, bool, error) {
	committed := map[string]struct{}{}
	iter := tree.Files()
	defer iter.Close()
	for {
		file, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, err
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		name := file.Name
		if base != "." {
			name = path.Join(base, file.Name)
		}
		committed[name] = struct{}{}
		if file.Mode == filemode.Symlink {
			// Symlink targets are invisible to the committed digest; fail closed.
			// Note: filemode.Submodule (gitlinks) is never yielded by FileIter
			// (go-git skips them at the iteration layer); gitlinks are rejected
			// up front by rejectSubmodulesInDigestTree in the caller.
			return nil, false, nil
		}
		full := *file
		full.Name = name
		changed, err := worktreeFileChanged(root, &full)
		if err != nil {
			return nil, false, err
		}
		if changed {
			return nil, false, nil
		}
	}
	return committed, true, nil
}

// worktreeDirHasExtraFiles walks the worktree directory at base and reports
// whether it holds any file absent from the committed set — including nested
// .git directories, which fail closed as unverifiable content.
func worktreeDirHasExtraFiles(ctx context.Context, root, base string, committed map[string]struct{}) (bool, error) {
	dir := root
	if base != "." {
		dir = filepath.Join(root, filepath.FromSlash(base))
	}
	extra := false
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == gitDirName {
				if filepath.Dir(p) == root {
					return fs.SkipDir // repository metadata at the root only
				}
				// A nested .git directory is unverifiable content: the
				// committed tree cannot represent it, but a recursive
				// directory render reads it. Fail closed.
				extra = true
				return fs.SkipAll
			}
			return nil
		}
		if entry.Name() == gitDirName && filepath.Dir(p) == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if _, ok := committed[filepath.ToSlash(rel)]; !ok {
			extra = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return extra, nil
}
