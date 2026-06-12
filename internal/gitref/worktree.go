package gitref

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
	"sync/atomic"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/source"
)

type WorktreeState string

const (
	WorktreeStateClean   WorktreeState = "clean"
	WorktreeStateDirty   WorktreeState = "dirty"
	WorktreeStateUnknown WorktreeState = "unknown"
)

type WorktreeStatusResult struct {
	State    WorktreeState
	Revision string
}

// WorktreeStatus reports whether repoPath is a clean Git worktree, a dirty
// Git worktree, or an unknown/non-Git root. Clean and dirty Git states include
// the resolved HEAD revision. Non-repository roots report unknown without
// failing; other resolution problems are returned as errors with unknown state.
func WorktreeStatus(ctx context.Context, repoPath string) (WorktreeStatusResult, error) {
	return worktreeStatus(ctx, repoPath, true)
}

// WorktreeHeadIdentity reports the HEAD commit SHA when the worktree at
// repoPath matches the HEAD tree exactly: identical file set and content,
// counting untracked and ignored files as dirt. Any difference yields ("",
// nil). Errors mean the identity could not be determined; callers treat that
// the same as dirt. Checked-out submodules read dirty because their files are
// not in the HEAD tree. No shellouts.
func WorktreeHeadIdentity(ctx context.Context, repoPath string) (string, error) {
	status, err := worktreeStatus(ctx, repoPath, false)
	if err != nil {
		return "", err
	}
	if status.State != WorktreeStateClean {
		return "", nil
	}
	return status.Revision, nil
}

func worktreeStatus(ctx context.Context, repoPath string, nonRepositoryIsUnknown bool) (WorktreeStatusResult, error) {
	if err := ctx.Err(); err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, err
	}
	repo, displayRepoPath, err := openLocalRepository(repoPath)
	if err != nil {
		if nonRepositoryIsUnknown && errors.Is(err, git.ErrRepositoryNotExists) {
			return WorktreeStatusResult{State: WorktreeStateUnknown}, nil
		}
		return WorktreeStatusResult{State: WorktreeStateUnknown}, err
	}
	hash, err := source.ResolveGitRevision(repo, "HEAD")
	if err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, fmt.Errorf("resolve Git ref %q in %q: %w", "HEAD", displayRepoPath, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, fmt.Errorf("load Git commit %q in %q: %w", hash.String(), displayRepoPath, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, fmt.Errorf("load Git tree for %q in %q: %w", hash.String(), displayRepoPath, err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, fmt.Errorf("open Git worktree in %q: %w", displayRepoPath, err)
	}
	root := worktree.Filesystem.Root()

	files, err := headTreeFiles(tree)
	if err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, err
	}
	dirtyFiles, err := anyWorktreeFileDiffers(ctx, root, files)
	if err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, err
	}
	if dirtyFiles {
		return WorktreeStatusResult{State: WorktreeStateDirty, Revision: hash.String()}, nil
	}
	clean, err := worktreeHasOnlyHeadFiles(ctx, root, files)
	if err != nil {
		return WorktreeStatusResult{State: WorktreeStateUnknown}, err
	}
	if !clean {
		return WorktreeStatusResult{State: WorktreeStateDirty, Revision: hash.String()}, nil
	}
	return WorktreeStatusResult{State: WorktreeStateClean, Revision: hash.String()}, nil
}

// anyWorktreeFileDiffers reports whether any tracked file differs from its
// committed blob, stopping all workers at the first detected difference.
func anyWorktreeFileDiffers(ctx context.Context, root string, files []object.File) (bool, error) {
	workers := materializeTreeParallelism(len(files))
	if workers == 0 {
		return false, nil
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var dirty atomic.Bool
	errs := make([]error, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range jobs {
				worktreeDifferWorker(root, files, i, &dirty, errs, cancel, scanCtx)
			}
		}()
	}
	for i := range files {
		if scanCtx.Err() != nil {
			break
		}
		select {
		case <-scanCtx.Done():
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	if dirty.Load() {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, err := range errs {
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

func worktreeDifferWorker(root string, files []object.File, i int, dirty *atomic.Bool, errs []error, cancel context.CancelFunc, scanCtx context.Context) {
	if dirty.Load() || scanCtx.Err() != nil {
		return
	}
	changed, err := worktreeFileChanged(root, &files[i])
	if err != nil {
		errs[i] = err
		cancel()
		return
	}
	if changed {
		dirty.Store(true)
		cancel()
	}
}

const gitDirName = ".git"

// worktreeHasOnlyHeadFiles reports false when any file exists that is not in
// the HEAD tree: untracked, ignored, or a submodule working file. Directories
// named .git are skipped as repository metadata.
func worktreeHasOnlyHeadFiles(ctx context.Context, root string, files []object.File) (bool, error) {
	headSet := make(map[string]struct{}, len(files))
	for i := range files {
		headSet[filepath.ToSlash(files[i].Name)] = struct{}{}
	}
	extra := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == gitDirName {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Name() == gitDirName && filepath.Dir(path) == root {
			// A .git file at the root (linked worktree layout) is repository
			// metadata, not content.
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, ok := headSet[filepath.ToSlash(rel)]; !ok {
			extra = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return !extra, nil
}
