package gitref

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// UntrackedIgnoredFilter reports whether a worktree path is one that Git
// ignores and does not track. No commit can hold such a path, so no Argo CD
// checkout holds it either.
type UntrackedIgnoredFilter struct {
	tracked     map[string]struct{}
	trackedDirs map[string]struct{}
	// submodules holds the gitlink paths of the index. The parent index
	// does not list the files of a submodule, and Argo CD checks out
	// submodules, so nothing at or under a gitlink is ever skipped.
	submodules map[string]struct{}
	matcher    gitignore.Matcher
}

// OpenUntrackedIgnoredFilter reads the index and the ignore rules of the Git
// worktree whose top level is root: .gitignore files, .git/info/exclude, and
// the core.excludesFile of the user and system Git configuration. When root
// is not the top level of a Git worktree, the filter skips nothing.
func OpenUntrackedIgnoredFilter(root string) (*UntrackedIgnoredFilter, error) {
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return &UntrackedIgnoredFilter{}, nil
		}
		return nil, fmt.Errorf("open Git repository: %w", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		if errors.Is(err, git.ErrIsBareRepository) {
			return &UntrackedIgnoredFilter{}, nil
		}
		return nil, fmt.Errorf("open Git worktree: %w", err)
	}
	index, err := repo.Storer.Index()
	if err != nil {
		return nil, fmt.Errorf("read Git index: %w", err)
	}
	filter := &UntrackedIgnoredFilter{
		tracked:     make(map[string]struct{}, len(index.Entries)),
		trackedDirs: map[string]struct{}{},
		submodules:  map[string]struct{}{},
	}
	for _, entry := range index.Entries {
		filter.tracked[entry.Name] = struct{}{}
		if entry.Mode == filemode.Submodule {
			filter.submodules[entry.Name] = struct{}{}
		}
		for dir := path.Dir(entry.Name); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if _, ok := filter.trackedDirs[dir]; ok {
				break
			}
			filter.trackedDirs[dir] = struct{}{}
		}
	}
	patterns, err := gitignore.ReadPatterns(worktree.Filesystem, nil)
	if err != nil {
		return nil, fmt.Errorf("read Git ignore rules: %w", err)
	}
	rootFS := osfs.New("/")
	// Unreadable user or system configuration adds no rules, as in
	// go-git's own status.
	system, _ := gitignore.LoadSystemPatterns(rootFS)
	global, _ := gitignore.LoadGlobalPatterns(rootFS)
	all := make([]gitignore.Pattern, 0, len(system)+len(global)+len(patterns))
	all = append(all, system...)
	all = append(all, global...)
	all = append(all, patterns...)
	filter.matcher = gitignore.NewMatcher(all)
	return filter, nil
}

// Skip reports whether rel, relative to the worktree root with forward
// slashes, is ignored and not tracked. A directory that holds a tracked
// path is never skipped, and neither is a path at or under a submodule.
func (f *UntrackedIgnoredFilter) Skip(rel string, isDir bool) bool {
	if f == nil || f.matcher == nil || rel == "" || rel == "." {
		return false
	}
	if f.inSubmodule(rel) {
		return false
	}
	if isDir {
		if _, ok := f.trackedDirs[rel]; ok {
			return false
		}
	} else if _, ok := f.tracked[rel]; ok {
		return false
	}
	return f.matcher.Match(strings.Split(rel, "/"), isDir)
}

// inSubmodule reports whether rel is a gitlink path or is under one.
func (f *UntrackedIgnoredFilter) inSubmodule(rel string) bool {
	if len(f.submodules) == 0 {
		return false
	}
	for candidate := rel; candidate != "." && candidate != "/" && candidate != ""; candidate = path.Dir(candidate) {
		if _, ok := f.submodules[candidate]; ok {
			return true
		}
	}
	return false
}
