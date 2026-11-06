package gitref

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	gitconfig "github.com/go-git/go-git/v5/plumbing/format/config"
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
// worktree whose top level is root: .gitignore files, info/exclude (also of
// the common Git directory of a linked worktree), and the file that
// core.excludesFile names, or the default user ignore file without one. When root
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
	// Lower priority first: core.excludesFile, then info/exclude of a linked
	// worktree, whose .git is a file, so ReadPatterns cannot find it, then
	// the patterns ReadPatterns read.
	all := readIgnorePatternFile(excludesFilePath(repo))
	all = append(all, readIgnorePatternFile(linkedWorktreeInfoExclude(root))...)
	all = append(all, patterns...)
	filter.matcher = gitignore.NewMatcher(all)
	return filter, nil
}

// excludesFilePath returns the file that core.excludesFile names, as Git
// reads it: the last value from the system, user, and repository
// configuration. Without one, Git reads $XDG_CONFIG_HOME/git/ignore, or
// ~/.config/git/ignore when XDG_CONFIG_HOME is not set. It returns "" when
// no file applies.
func excludesFilePath(repo *git.Repository) string {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	userConfig := filepath.Join(xdg, "git", "config")
	if xdg == "" && home != "" {
		userConfig = filepath.Join(home, ".config", "git", "config")
	}
	files := []string{"/etc/gitconfig", userConfig}
	if home != "" {
		files = append(files, filepath.Join(home, ".gitconfig"))
	}
	value := ""
	for _, file := range files {
		if excludes := configExcludesFile(file); excludes != "" {
			value = excludes
		}
	}
	if cfg, err := repo.Config(); err == nil {
		if excludes := cfg.Raw.Section("core").Options.Get("excludesfile"); excludes != "" {
			value = excludes
		}
	}
	if value == "" {
		switch {
		case xdg != "":
			return filepath.Join(xdg, "git", "ignore")
		case home != "":
			return filepath.Join(home, ".config", "git", "ignore")
		default:
			return ""
		}
	}
	if home != "" && (value == "~" || strings.HasPrefix(value, "~/")) {
		value = filepath.Join(home, strings.TrimPrefix(value, "~"))
	}
	return value
}

// configExcludesFile returns core.excludesFile from one Git configuration
// file, or "" when the file or the value is missing.
func configExcludesFile(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	cfg := gitconfig.New()
	if err := gitconfig.NewDecoder(file).Decode(cfg); err != nil {
		return ""
	}
	return cfg.Section("core").Options.Get("excludesfile")
}

// linkedWorktreeInfoExclude returns the info/exclude file of the common Git
// directory when root is a linked worktree, whose .git is a file that names
// its own Git directory. It returns "" otherwise.
func linkedWorktreeInfoExclude(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return ""
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
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
// apply from the worktree root. A missing or unreadable file adds none.
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
