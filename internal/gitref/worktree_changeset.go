package gitref

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/source"
)

// WorktreeChangeSetResult is WorktreeStatus plus the complete dirty-path
// enumeration. DirtyPaths lists every repository-relative path whose worktree
// state differs from HEAD: modified, deleted, mode-flipped, or type-changed
// tracked files, plus every extra file (untracked, ignored, and files under
// untracked directories). A nested .git directory is recorded as a single
// dirty path without descending into it. Sorted, deduplicated, and empty
// exactly when State is clean. Completeness is load-bearing: per-path-set
// cache keying serves committed-key hits for path sets that no dirty path
// touches, so a missed category here would become a stale cache hit.
type WorktreeChangeSetResult struct {
	State      WorktreeState
	Revision   string
	DirtyPaths []string
}

// WorktreeChangeSet classifies repoPath like WorktreeStatus and additionally
// enumerates every dirty path. Unlike WorktreeStatus it cannot short-circuit
// at the first difference: callers need the full set. Non-repository roots
// report unknown without failing.
func WorktreeChangeSet(ctx context.Context, repoPath string) (WorktreeChangeSetResult, error) {
	if err := ctx.Err(); err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, err
	}
	repo, displayRepoPath, err := openLocalRepository(repoPath)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return WorktreeChangeSetResult{State: WorktreeStateUnknown}, nil
		}
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, err
	}
	hash, err := source.ResolveGitRevision(repo, "HEAD")
	if err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, fmt.Errorf("resolve Git ref %q in %q: %w", "HEAD", displayRepoPath, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, fmt.Errorf("load Git commit %q in %q: %w", hash.String(), displayRepoPath, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, fmt.Errorf("load Git tree for %q in %q: %w", hash.String(), displayRepoPath, err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, fmt.Errorf("open Git worktree in %q: %w", displayRepoPath, err)
	}
	root := worktree.Filesystem.Root()

	files, err := headTreeFiles(tree)
	if err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, err
	}
	changed, err := changedWorktreeFiles(root, files)
	if err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, err
	}
	dirtySet := map[string]struct{}{}
	for i := range files {
		if changed[i] {
			dirtySet[filepath.ToSlash(files[i].Name)] = struct{}{}
		}
	}
	if err := addWorktreeExtraFiles(ctx, root, files, dirtySet); err != nil {
		return WorktreeChangeSetResult{State: WorktreeStateUnknown}, err
	}

	result := WorktreeChangeSetResult{Revision: hash.String()}
	if len(dirtySet) == 0 {
		result.State = WorktreeStateClean
		return result, nil
	}
	result.State = WorktreeStateDirty
	result.DirtyPaths = sortedStringSet(dirtySet)
	return result, nil
}

// addWorktreeExtraFiles records every worktree file that is not in the HEAD
// tree. Nested .git directories are recorded as a single dirty path and not
// descended into (their content is repository metadata the cache layers treat
// fail-closed). This deliberately diverges from worktreeHasOnlyHeadFiles,
// which silently skips all .git directories: here the fail-closed side is
// taken so that a nested .git always marks the worktree dirty even when
// WorktreeStatus considers it clean. The root .git is still skipped.
func addWorktreeExtraFiles(ctx context.Context, root string, files []object.File, dirtySet map[string]struct{}) error {
	headSet := make(map[string]struct{}, len(files))
	for i := range files {
		headSet[filepath.ToSlash(files[i].Name)] = struct{}{}
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if entry.Name() == gitDirName {
				if filepath.Dir(path) == root {
					return fs.SkipDir
				}
				dirtySet[rel] = struct{}{}
				return fs.SkipDir
			}
			return nil
		}
		if entry.Name() == gitDirName && filepath.Dir(path) == root {
			return nil
		}
		if _, ok := headSet[rel]; !ok {
			dirtySet[rel] = struct{}{}
		}
		return nil
	})
}
