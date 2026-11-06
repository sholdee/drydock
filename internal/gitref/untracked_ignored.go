package gitref

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// UntrackedIgnoredFilter reports whether a worktree path is one that Git
// ignores and does not track. No commit can hold such a path, so no Argo CD
// checkout holds it either. The filter only ever skips a path that Git
// itself ignores: it asks the git CLI when it can, and otherwise applies
// only the ignore rules that it reads exactly as Git does.
type UntrackedIgnoredFilter struct {
	// prefix is the filter root relative to the worktree top level, with
	// forward slashes, or "" when the root is the top level.
	prefix string
	// git holds the answer of the git CLI. nil means the go-git fallback.
	git *gitIgnoredSet

	// The go-git fallback.
	tracked     map[string]struct{}
	trackedDirs map[string]struct{}
	// submodules holds the gitlink paths of the index. The parent index
	// does not list the files of a submodule, and Argo CD checks out
	// submodules, so nothing at or under a gitlink is ever skipped.
	submodules map[string]struct{}
	matcher    gitignore.Matcher
}

// gitIgnoredSet is the untracked ignored set that
// `git ls-files --others --ignored --exclude-standard --directory` lists.
type gitIgnoredSet struct {
	files map[string]struct{}
	dirs  map[string]struct{}
}

// OpenUntrackedIgnoredFilter returns the filter of the Git worktree that
// holds root, at its top level or in a subdirectory of it. When the git
// CLI is on PATH, the filter asks Git for its untracked ignored set, with
// every ignore rule Git applies. Otherwise it reads the index, the
// .gitignore files, and info/exclude (also of the common Git directory of a
// linked worktree) with go-git. That fallback leaves out core.excludesFile
// and the user ignore file, whose lookup has rules go-git does not follow,
// so it skips fewer paths than Git. The filter skips nothing when root is
// not in a Git worktree, or when Git ignores root itself.
func OpenUntrackedIgnoredFilter(ctx context.Context, root string) (*UntrackedIgnoredFilter, error) {
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
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
	top := worktree.Filesystem.Root()
	prefix, ok := worktreePrefix(top, root)
	if !ok {
		return &UntrackedIgnoredFilter{}, nil
	}
	var filter *UntrackedIgnoredFilter
	if set, ok := gitCLIIgnoredSet(ctx, top); ok {
		filter = &UntrackedIgnoredFilter{git: set}
	} else {
		filter, err = goGitUntrackedIgnoredFilter(repo, worktree.Filesystem.Root())
		if err != nil {
			return nil, err
		}
	}
	// A root that Git ignores holds nothing a commit can hold. The user
	// still chose it as the repository, so check it whole.
	if prefix != "" && filter.skipFull(prefix, true) {
		return &UntrackedIgnoredFilter{}, nil
	}
	filter.prefix = prefix
	return filter, nil
}

// worktreePrefix returns root relative to the worktree top level, with
// forward slashes. It reports false when root is not under top.
func worktreePrefix(top, root string) (string, bool) {
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return "", false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(realTop, realRoot)
	if err != nil || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return filepath.ToSlash(rel), true
}

// gitCLIIgnoredSet asks the git CLI for the untracked ignored paths of the
// worktree at top. It reports false when git is not on PATH or fails, such
// as for a repository that Git refuses as unsafe. The command reads the
// index and the worktree only: it takes no lock and runs no fsmonitor hook.
func gitCLIIgnoredSet(ctx context.Context, top string) (*gitIgnoredSet, bool) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, false
	}
	cmd := exec.CommandContext(ctx, gitBin,
		"-c", "core.fsmonitor=false",
		"-C", top,
		"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	set := &gitIgnoredSet{files: map[string]struct{}{}, dirs: map[string]struct{}{}}
	for entry := range strings.SplitSeq(stdout.String(), "\x00") {
		switch {
		case entry == "":
		case strings.HasSuffix(entry, "/"):
			set.dirs[strings.TrimSuffix(entry, "/")] = struct{}{}
		default:
			set.files[entry] = struct{}{}
		}
	}
	return set, true
}

// goGitUntrackedIgnoredFilter is the fallback without the git CLI.
func goGitUntrackedIgnoredFilter(repo *git.Repository, top string) (*UntrackedIgnoredFilter, error) {
	worktree, err := repo.Worktree()
	if err != nil {
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
	// info/exclude of a linked worktree is in the common Git directory. Its
	// .git is a file there, so ReadPatterns does not find it.
	all := readIgnorePatternFile(linkedWorktreeInfoExclude(top))
	all = append(all, patterns...)
	filter.matcher = gitignore.NewMatcher(all)
	return filter, nil
}

// Skip reports whether rel, relative to the filter root with forward
// slashes, is ignored and not tracked.
func (f *UntrackedIgnoredFilter) Skip(rel string, isDir bool) bool {
	if f == nil || rel == "" || rel == "." {
		return false
	}
	full := rel
	if f.prefix != "" {
		full = f.prefix + "/" + rel
	}
	return f.skipFull(full, isDir)
}

// skipFull is Skip for a path relative to the worktree top level.
func (f *UntrackedIgnoredFilter) skipFull(full string, isDir bool) bool {
	if f.git != nil {
		return f.git.ignored(full, isDir)
	}
	if f.matcher == nil {
		return false
	}
	if f.inSubmodule(full) {
		return false
	}
	if isDir {
		if _, ok := f.trackedDirs[full]; ok {
			return false
		}
	} else if _, ok := f.tracked[full]; ok {
		return false
	}
	return f.matcher.Match(strings.Split(full, "/"), isDir)
}

// ignored reports whether Git listed full, or a directory above it, as
// untracked and ignored. A directory that Git lists alone is ignored as a
// whole: it holds no tracked path.
func (s *gitIgnoredSet) ignored(full string, isDir bool) bool {
	if isDir {
		if _, ok := s.dirs[full]; ok {
			return true
		}
	} else if _, ok := s.files[full]; ok {
		return true
	}
	for dir := path.Dir(full); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if _, ok := s.dirs[dir]; ok {
			return true
		}
	}
	return false
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

// linkedWorktreeInfoExclude returns the info/exclude file of the common Git
// directory when top is a linked worktree, whose .git is a file that names
// its own Git directory. It returns "" otherwise.
func linkedWorktreeInfoExclude(top string) string {
	data, err := os.ReadFile(filepath.Join(top, ".git"))
	if err != nil {
		return ""
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(top, gitDir)
	}
	commonDir := gitDir
	if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = strings.TrimSpace(string(common))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	}
	return filepath.Join(commonDir, "info", "exclude")
}

// readIgnorePatternFile reads the patterns of one ignore file whose patterns
// apply from the worktree top level. A missing or unreadable file adds none.
func readIgnorePatternFile(path string) []gitignore.Pattern {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var patterns []gitignore.Pattern
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	return patterns
}
