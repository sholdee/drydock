package gitref

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sholdee/drydock/internal/gitlfs"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/source"
)

// LFSPointers lists the Git LFS pointer files of the commit that revision
// names in the local repository at repoPath, outside submodules
// (gittree.ScanLFSPointers). It reads Git objects only.
func LFSPointers(ctx context.Context, repoPath, revision string) ([]gittree.LFSPointer, error) {
	repo, displayRepoPath, err := openLocalRepository(repoPath)
	if err != nil {
		return nil, err
	}
	hash, err := source.ResolveGitRevision(repo, revision)
	if err != nil {
		return nil, fmt.Errorf("resolve Git ref %q in %q: %w", revision, displayRepoPath, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	return gittree.ScanLFSPointers(ctx, tree)
}

// LFSStoreDirs returns the Git LFS stores of the local repository at
// repoPath: the lfs directory of its Git directory and of its common Git
// directory. git-lfs keeps objects in lfs/objects there.
func LFSStoreDirs(repoPath string) []string {
	dirs := localGitDirs(repoPath)
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, filepath.Join(dir, "lfs"))
	}
	return out
}

// LFSTree lists the Git LFS pointer files of the commit that revision
// names in the local repository at repoPath, as LFSPointers does, and
// returns the .lfsconfig file of that commit, or nil.
func LFSTree(ctx context.Context, repoPath, revision string) ([]gittree.LFSPointer, []byte, error) {
	repo, displayRepoPath, err := openLocalRepository(repoPath)
	if err != nil {
		return nil, nil, err
	}
	hash, err := source.ResolveGitRevision(repo, revision)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve Git ref %q in %q: %w", revision, displayRepoPath, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, nil, err
	}
	pointers, err := gittree.ScanLFSPointers(ctx, tree)
	if err != nil {
		return nil, nil, err
	}
	return pointers, gittree.ReadLFSConfig(tree, gitlfs.MaxLFSConfigSize), nil
}
