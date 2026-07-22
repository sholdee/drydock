package gitref

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
	"github.com/sholdee/drydock/internal/source"
)

func ChangedPathsBetweenRefs(ctx context.Context, repoPath, leftRef, rightRef string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, displayRepoPath, err := openLocalRepository(repoPath)
	if err != nil {
		return nil, err
	}
	leftTree, err := treeForRef(repo, leftRef)
	if err != nil {
		return nil, fmt.Errorf("resolve Git ref %q in %q: %w", leftRef, displayRepoPath, err)
	}
	rightTree, err := treeForRef(repo, rightRef)
	if err != nil {
		return nil, fmt.Errorf("resolve Git ref %q in %q: %w", rightRef, displayRepoPath, err)
	}
	return changedPathsBetweenTrees(ctx, leftTree, rightTree)
}

func ChangedPathsFromRefToWorktree(ctx context.Context, repoPath, leftRef string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, displayRepoPath, err := openLocalRepository(repoPath)
	if err != nil {
		return nil, err
	}
	leftTree, err := treeForRef(repo, leftRef)
	if err != nil {
		return nil, fmt.Errorf("resolve Git ref %q in %q: %w", leftRef, displayRepoPath, err)
	}
	headTree, err := treeForRef(repo, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve Git ref %q in %q: %w", "HEAD", displayRepoPath, err)
	}
	paths, err := changedPathsBetweenTrees(ctx, leftTree, headTree)
	if err != nil {
		return nil, err
	}
	index := stringSet(paths)
	if err := addTrackedWorktreeChanges(repo, headTree, index); err != nil {
		return nil, fmt.Errorf("detect Git worktree changes %q: %w", displayRepoPath, err)
	}
	return sortedStringSet(index), nil
}

func openLocalRepository(repoPath string) (*git.Repository, string, error) {
	repoPath = strings.TrimSpace(repoPath)
	displayRepoPath := source.RedactURL(repoPath)
	if repoPath == "" {
		return nil, displayRepoPath, fmt.Errorf("git ref snapshot repository path is required")
	}
	if looksLikeRemoteRepo(repoPath) {
		return nil, displayRepoPath, fmt.Errorf("git ref snapshot repository %q must be a local path; remote repository URLs are not supported for --repo yet", displayRepoPath)
	}
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, displayRepoPath, fmt.Errorf("open Git repository %q: %w", displayRepoPath, err)
	}
	return repo, displayRepoPath, nil
}

func treeForRef(repo *git.Repository, ref string) (*object.Tree, error) {
	hash, err := source.ResolveGitRevision(repo, ref)
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, err
	}
	return commit.Tree()
}

func changedPathsBetweenTrees(ctx context.Context, leftTree, rightTree *object.Tree) ([]string, error) {
	changes, err := object.DiffTreeContext(ctx, leftTree, rightTree)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			return nil, err
		}
		switch action {
		case merkletrie.Delete:
			paths[filepath.ToSlash(change.From.Name)] = struct{}{}
		case merkletrie.Insert, merkletrie.Modify:
			paths[filepath.ToSlash(change.To.Name)] = struct{}{}
		}
	}
	return sortedStringSet(paths), nil
}

func addTrackedWorktreeChanges(repo *git.Repository, headTree *object.Tree, paths map[string]struct{}) error {
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	files, err := headTreeFiles(headTree)
	if err != nil {
		return err
	}
	changed, err := changedWorktreeFiles(wt.Filesystem.Root(), files)
	if err != nil {
		return err
	}
	headPaths := recordChangedHeadPaths(paths, files, changed)
	return addIndexChanges(repo, headPaths, paths)
}

func headTreeFiles(headTree *object.Tree) ([]object.File, error) {
	files := make([]object.File, 0)
	if err := headTree.Files().ForEach(func(file *object.File) error {
		files = append(files, *file)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func changedWorktreeFiles(root string, files []object.File) ([]bool, error) {
	changed := make([]bool, len(files))
	errs := make([]error, len(files))
	workers := materializeTreeParallelism(len(files))
	if workers > 0 {
		jobs := make(chan int)
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				for i := range jobs {
					changed[i], errs[i] = worktreeFileChanged(root, &files[i])
				}
			}()
		}
		for i := range files {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}
	for i := range files {
		if errs[i] != nil {
			return nil, errs[i]
		}
	}
	return changed, nil
}

func recordChangedHeadPaths(paths map[string]struct{}, files []object.File, changed []bool) map[string]object.File {
	headPaths := make(map[string]object.File, len(files))
	for i := range files {
		name := filepath.ToSlash(files[i].Name)
		headPaths[name] = files[i]
		if changed[i] {
			paths[name] = struct{}{}
		}
	}
	return headPaths
}

func addIndexChanges(repo *git.Repository, headPaths map[string]object.File, paths map[string]struct{}) error {
	idx, err := repo.Storer.Index()
	if err != nil {
		return err
	}
	indexPaths := make(map[string]struct{}, len(idx.Entries))
	for _, entry := range idx.Entries {
		name := filepath.ToSlash(entry.Name)
		indexPaths[name] = struct{}{}
		headFile, ok := headPaths[name]
		if !ok || entry.Hash != headFile.Hash || !sameGitFileMode(entry.Mode, headFile.Mode) || entry.Stage != 0 || entry.IntentToAdd {
			paths[name] = struct{}{}
		}
	}
	for name := range headPaths {
		if _, ok := indexPaths[name]; !ok {
			paths[name] = struct{}{}
		}
	}
	return nil
}

func sameGitFileMode(left, right filemode.FileMode) bool {
	return comparableGitFileMode(left) == comparableGitFileMode(right)
}

func comparableGitFileMode(mode filemode.FileMode) filemode.FileMode {
	if mode == filemode.Deprecated {
		return filemode.Regular
	}
	return mode
}

func worktreeFileChanged(root string, file *object.File) (bool, error) {
	path := filepath.Join(root, filepath.FromSlash(file.Name))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	// ENOTDIR fires when a path component that was a directory in HEAD has been
	// replaced by a regular file in the worktree. The tracked file is
	// unquestionably gone; the replacing file is caught as an extra by the
	// walk in addWorktreeExtraFiles. Treat it as changed rather than hard-failing
	// the whole scan (errors.Is(syscall.ENOTDIR, os.ErrNotExist) is false in Go).
	if errors.Is(err, syscall.ENOTDIR) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	switch file.Mode {
	case filemode.Symlink:
		if info.Mode()&os.ModeSymlink == 0 {
			return true, nil
		}
		want, err := file.Contents()
		if err != nil {
			return false, err
		}
		got, err := os.Readlink(path)
		if err != nil {
			return false, err
		}
		return got != want, nil
	case filemode.Regular, filemode.Deprecated, filemode.Executable:
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
		return regularWorktreeFileChanged(path, file)
	case filemode.Empty, filemode.Dir, filemode.Submodule:
		return true, nil
	default:
		return true, nil
	}
}

func regularWorktreeFileChanged(path string, file *object.File) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if regularWorktreeModeChanged(info.Mode(), file.Mode) {
		return true, nil
	}
	if info.Size() != file.Size {
		return true, nil
	}
	// Hash the worktree file with git's blob framing and compare against the
	// known blob hash: exact equality without decompressing the blob.
	worktreeFile, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = worktreeFile.Close() }()
	hasher := plumbing.NewHasher(plumbing.BlobObject, info.Size())
	if _, err := io.Copy(hasher, worktreeFile); err != nil {
		return false, err
	}
	return hasher.Sum() != file.Hash, nil
}

func regularWorktreeModeChanged(worktreeMode os.FileMode, gitMode filemode.FileMode) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	executable := worktreeMode&0o111 != 0
	switch comparableGitFileMode(gitMode) {
	case filemode.Regular, filemode.Deprecated:
		return executable
	case filemode.Executable:
		return !executable
	case filemode.Empty, filemode.Dir, filemode.Symlink, filemode.Submodule:
		return true
	default:
		return true
	}
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
