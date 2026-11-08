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
	"time"

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
	tracked map[string]struct{}
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
// not in a Git worktree, when it is a subdirectory that the worktree does not
// track, or when Git ignores root itself.
func OpenUntrackedIgnoredFilter(ctx context.Context, root string) (*UntrackedIgnoredFilter, error) {
	// Git itself opens every repository it supports, such as one with
	// reftable refs or extensions.worktreeConfig, which go-git v5 refuses.
	if filter, ok := gitCLIUntrackedIgnoredFilter(ctx, root); ok {
		return filter, nil
	}
	return goGitOpenUntrackedIgnoredFilter(ctx, root)
}

// gitCLIUntrackedIgnoredFilter builds the filter with the git CLI alone: the
// top level and the prefix from git rev-parse, the tracked check of a
// subdirectory root from git ls-files, and the ignored set. It reports
// false when git is not on PATH or a call fails, so the caller falls back
// to go-git.
func gitCLIUntrackedIgnoredFilter(ctx context.Context, root string) (*UntrackedIgnoredFilter, bool) {
	out, ok := runGitCLI(ctx, root, "rev-parse", "--show-toplevel", "--show-prefix")
	if !ok {
		return nil, false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, false
	}
	top := lines[0]
	prefix := ""
	if len(lines) > 1 {
		prefix = strings.TrimSuffix(lines[1], "/")
	}
	// A root in a subdirectory must be a tree the worktree tracks. A
	// directory that only happens to sit inside some other worktree, such
	// as a temporary directory, is checked whole.
	if prefix != "" {
		tracked, ok := runGitCLI(ctx, top, "ls-files", "-z", "--cached", "--", prefix+"/")
		if !ok {
			return nil, false
		}
		if len(tracked) == 0 {
			return &UntrackedIgnoredFilter{}, true
		}
	}
	set, ok := gitCLIIgnoredSet(ctx, top)
	if !ok {
		return nil, false
	}
	filter := &UntrackedIgnoredFilter{git: set}
	// A root that Git ignores holds nothing a commit can hold. The user
	// still chose it as the repository, so check it whole.
	if filter.rootIgnored(prefix) {
		return &UntrackedIgnoredFilter{}, true
	}
	filter.prefix = prefix
	return filter, true
}

// goGitOpenUntrackedIgnoredFilter is OpenUntrackedIgnoredFilter without the
// git CLI.
func goGitOpenUntrackedIgnoredFilter(_ context.Context, root string) (*UntrackedIgnoredFilter, error) {
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
	// A root in a subdirectory must be a tree the worktree tracks. A
	// directory that only happens to sit inside some other worktree, such
	// as a temporary directory, is checked whole.
	if prefix != "" && !indexTracksDir(repo, prefix) {
		return &UntrackedIgnoredFilter{}, nil
	}
	filter, err := goGitUntrackedIgnoredFilter(repo, top)
	if err != nil {
		return nil, err
	}
	// A root that Git ignores holds nothing a commit can hold. The user
	// still chose it as the repository, so check it whole.
	if filter.rootIgnored(prefix) {
		return &UntrackedIgnoredFilter{}, nil
	}
	filter.prefix = prefix
	return filter, nil
}

// indexTracksDir reports whether the index of repo holds a path under dir.
func indexTracksDir(repo *git.Repository, dir string) bool {
	index, err := repo.Storer.Index()
	if err != nil {
		return false
	}
	for _, entry := range index.Entries {
		if strings.HasPrefix(entry.Name, dir+"/") {
			return true
		}
	}
	return false
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

// gitCLITimeout bounds the git CLI call. A FIFO named by core.excludesFile,
// or a lazy fetch in a partial clone, can block it.
var gitCLITimeout = 20 * time.Second

// gitLocalEnvVars are the variables that `git rev-parse --local-env-vars`
// lists (Git 2.56). Git sets some of them for hooks and for
// `git rebase --exec`, and they would point the call at another
// repository, index, or worktree than the one at top. -C does not override
// them.
var gitLocalEnvVars = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_INDEX_FILE":                   true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_PREFIX":                       true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_COMMON_DIR":                   true,
}

// gitCLIEnv is the environment of the git CLI call: the process
// environment without gitLocalEnvVars, with optional locks, terminal
// prompts, and lazy fetches off.
func gitCLIEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if gitLocalEnvVars[name] {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
}

// gitCLIIgnoredSet asks the git CLI for the untracked ignored paths of the
// worktree at top. It reports false when git is not on PATH, fails, or
// takes longer than gitCLITimeout, such as for a repository that Git
// refuses as unsafe. The command reads the index and the worktree only: it
// takes no lock, runs no fsmonitor hook, and prompts for nothing.
func gitCLIIgnoredSet(ctx context.Context, top string) (*gitIgnoredSet, bool) {
	out, ok := runGitCLI(ctx, top, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if !ok {
		return nil, false
	}
	set := &gitIgnoredSet{files: map[string]struct{}{}, dirs: map[string]struct{}{}}
	for entry := range strings.SplitSeq(string(out), "\x00") {
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

// runGitCLI runs git in dir with the clean environment of gitCLIEnv,
// without the fsmonitor hook, and within gitCLITimeout. It reports false
// when git is not on PATH or the call fails.
func runGitCLI(ctx context.Context, dir string, args ...string) ([]byte, bool) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, gitCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBin, append([]string{"-c", "core.fsmonitor=false", "-C", dir}, args...)...)
	cmd.Env = gitCLIEnv()
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	return stdout.Bytes(), true
}

// goGitUntrackedIgnoredFilter is the fallback without the git CLI.
func goGitUntrackedIgnoredFilter(repo *git.Repository, top string) (*UntrackedIgnoredFilter, error) {
	index, err := repo.Storer.Index()
	if err != nil {
		return nil, fmt.Errorf("read Git index: %w", err)
	}
	filter := &UntrackedIgnoredFilter{
		tracked:    make(map[string]struct{}, len(index.Entries)),
		submodules: map[string]struct{}{},
	}
	for _, entry := range index.Entries {
		filter.tracked[entry.Name] = struct{}{}
		if entry.Mode == filemode.Submodule {
			filter.submodules[entry.Name] = struct{}{}
		}
	}
	// info/exclude is in the common Git directory, which for a linked
	// worktree is not top/.git. The .gitignore files follow, lower
	// directories last, so they take priority.
	infoExclude := filepath.Join(top, ".git", "info", "exclude")
	if linked := linkedWorktreeInfoExclude(top); linked != "" {
		infoExclude = linked
	}
	all := readIgnorePatternFile(infoExclude, nil)
	all = append(all, readGitignoreTree(top, nil, all)...)
	filter.matcher = gitignore.NewMatcher(all)
	return filter, nil
}

// readGitignoreTree reads the .gitignore files of the directory dir, given
// as path components below top, and of the directories below it, as Git
// does: it does not read a .gitignore that is a symlink, and it does not
// enter a directory that is ignored, that is a symlink, or that holds its
// own .git, such as a nested repository or a submodule. go-git's
// ReadPatterns does all three, so it can ignore more than Git.
func readGitignoreTree(top string, dir []string, inherited []gitignore.Pattern) []gitignore.Pattern {
	full := filepath.Join(append([]string{top}, dir...)...)
	patterns := readIgnorePatternFile(filepath.Join(full, ".gitignore"), dir)
	active := append(append([]gitignore.Pattern(nil), inherited...), patterns...)
	entries, err := os.ReadDir(full)
	if err != nil {
		return patterns
	}
	matcher := gitignore.NewMatcher(active)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".git" {
			continue
		}
		child := append(append([]string(nil), dir...), entry.Name())
		if matcher.Match(child, true) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(full, entry.Name(), ".git")); err == nil {
			continue
		}
		sub := readGitignoreTree(top, child, active)
		patterns = append(patterns, sub...)
		active = append(active, sub...)
	}
	return patterns
}

// IndexGitlinks returns the gitlinks (submodules) of the index of the Git
// worktree that holds root and that are at or below root, relative to root
// with forward slashes. It asks the git CLI when it can and reads the index
// with go-git otherwise. It returns nothing when root is not in a Git
// worktree.
func IndexGitlinks(ctx context.Context, root string) []string {
	if out, ok := runGitCLI(ctx, root, "ls-files", "-s", "-z"); ok {
		var links []string
		for entry := range strings.SplitSeq(string(out), "\x00") {
			meta, name, found := strings.Cut(entry, "\t")
			if found && strings.HasPrefix(meta, "160000 ") {
				links = append(links, name)
			}
		}
		return links
	}
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if err != nil {
		return nil
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return nil
	}
	prefix, ok := worktreePrefix(worktree.Filesystem.Root(), root)
	if !ok {
		return nil
	}
	index, err := repo.Storer.Index()
	if err != nil {
		return nil
	}
	var links []string
	for _, entry := range index.Entries {
		if entry.Mode != filemode.Submodule {
			continue
		}
		name := entry.Name
		if prefix != "" {
			rest, found := strings.CutPrefix(name, prefix+"/")
			if !found {
				continue
			}
			name = rest
		}
		links = append(links, name)
	}
	return links
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
	// The fallback never skips a whole directory: a later "!" pattern can
	// bring back a file below a directory that an earlier pattern matches,
	// such as foo/** with !foo/keep.yaml. It decides file by file, so when
	// Git ignores a whole directory, it can check more than Git, never less.
	if isDir || f.inSubmodule(full) {
		return false
	}
	if _, ok := f.tracked[full]; ok {
		return false
	}
	return f.matcher.Match(strings.Split(full, "/"), false)
}

// rootIgnored reports whether Git ignores the filter root at prefix,
// relative to the worktree top level. For the go-git fallback, a match of
// the directory counts: the root is then checked whole, which is the
// stricter side.
func (f *UntrackedIgnoredFilter) rootIgnored(prefix string) bool {
	if prefix == "" {
		return false
	}
	if f.git != nil {
		return f.git.ignored(prefix, true)
	}
	if f.matcher == nil {
		return false
	}
	return f.matcher.Match(strings.Split(prefix, "/"), true)
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

// requireOnePartAfterTrailingDoubleStar rewrites a pattern that ends in
// "/**" or "/**/" so that go-git needs one more path part after the "**",
// as Git does: "foo/**" matches everything inside foo, but not foo itself.
// go-git lets a final "**" match no part at all, so it would also match a
// file named foo, and treat the directory foo as ignored.
func requireOnePartAfterTrailingDoubleStar(line string) string {
	trimmed := trimTrailingSpaces(line)
	switch {
	case strings.HasSuffix(trimmed, "/**"):
		return trimmed + "/*"
	case strings.HasSuffix(trimmed, "/**/"):
		return strings.TrimSuffix(trimmed, "/") + "/*/"
	default:
		return line
	}
}

// trimTrailingSpaces removes the trailing spaces of a pattern line as Git
// does (trim_trailing_spaces in dir.c): only spaces, not tabs, and not a
// space escaped with a backslash.
func trimTrailingSpaces(line string) string {
	end := len(line)
	for end > 0 && line[end-1] == ' ' {
		if end > 1 && line[end-2] == '\\' {
			break
		}
		end--
	}
	return line[:end]
}

// readIgnorePatternFile reads the patterns of one ignore file whose
// patterns apply below domain, path components from the worktree top
// level. Git does not read an ignore file that is a symlink, so neither
// does this. A missing or unreadable file adds none.
func readIgnorePatternFile(path string, domain []string) []gitignore.Pattern {
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
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
		// Git negates a bracket expression with "[!", but go-git passes the
		// pattern to filepath.Match, which reads "!" there as a plain
		// character and negates with "[^".
		line = strings.ReplaceAll(line, "[!", "[^")
		line = requireOnePartAfterTrailingDoubleStar(line)
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
	}
	return patterns
}
