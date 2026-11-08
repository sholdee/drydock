package appset

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pathsafety"
	"golang.org/x/text/unicode/norm"
)

// gitRepositoryTree is the repository of a Git generator as the Argo CD
// repo-server's checkout holds it. A checkout holds the files of a commit,
// and the directory of each gitlink (submodule): never a .git entry below
// the top level, and never another empty directory. In a working tree it
// never holds a path that Git ignores and does not track. Paths are
// relative to root, with forward slashes. drydock never reads outside root
// or inside a .git directory.
type gitRepositoryTree struct {
	root     string
	worktree WorktreeFilter

	scanned bool
	scanErr error
	// files are the regular files and symlinks, in walk order.
	files []gitTreeFile
	// dirs are the directories of the checkout in walk order: the ones with
	// a file below them, and the gitlinks.
	dirs []gitTreeDir
	// contentDirs holds every directory of dirs.
	contentDirs map[string]bool
	// gitlinks are the gitlink directories: the ones the index or the
	// commit tree lists as a submodule (GitlinkLister), or, without a
	// lister, the ones that hold their own .git entry. `git ls-files` lists each one
	// as one entry and nothing in it, and the checkout holds each one as a
	// directory.
	gitlinks   []string
	gitlinkSet map[string]bool
	// submodules reports a .gitmodules file at the root. Without one, Argo
	// CD fetches no submodule content (util/git/client.go:812-817), so each
	// gitlink is an empty directory, and drydock does not look into it.
	submodules bool
	// childSet holds "dir/name" for every entry of children, so a lookup
	// matches the exact name, also on a file system that ignores case.
	childSet map[string]bool
	// children lists the files, symlinks, and directories of the checkout
	// in each directory, for the glob.
	children map[string][]gitTreeChild
}

// gitTreeChild is one entry of a scanned directory.
type gitTreeChild struct {
	name string
	kind gitTreeKind
}

type gitTreeKind int

const (
	gitTreeKindFile gitTreeKind = iota
	gitTreeKindSymlink
	gitTreeKindDir
)

type gitTreeFile struct {
	rel     string
	symlink bool
}

type gitTreeDir struct {
	rel string
}

func newGitRepositoryTree(root string, worktree WorktreeFilter) *gitRepositoryTree {
	return &gitRepositoryTree{root: pathsafety.ResolveSymlinkedRoot(root), worktree: worktree}
}

func (t *gitRepositoryTree) skip(rel string, isDir bool) bool {
	return t.worktree != nil && t.worktree.Skip(rel, isDir)
}

// scan walks the tree once. It never follows a symlink, skips every entry
// named .git and every path the worktree filter skips, and keeps regular
// files and symlinks, the entries Git tracks, and the gitlinks.
func (t *gitRepositoryTree) scan() error {
	if t.scanned {
		return t.scanErr
	}
	t.scanned = true
	t.contentDirs = map[string]bool{}
	t.gitlinkSet = map[string]bool{}
	if info, err := os.Lstat(filepath.Join(t.root, ".gitmodules")); err == nil && info.Mode().IsRegular() {
		t.submodules = true
	}
	if lister, ok := t.worktree.(GitlinkLister); ok {
		for _, link := range lister.Gitlinks() {
			t.addGitlink(link)
		}
	}
	t.scanErr = filepath.WalkDir(t.root, t.visit)
	if t.scanErr != nil {
		return t.scanErr
	}
	// The directories of the checkout: the ones with a file below them, and
	// the gitlinks with the directories above them, also when the index or
	// the commit lists a gitlink that is not on disk.
	for dir := range t.contentDirs {
		t.dirs = append(t.dirs, gitTreeDir{rel: dir})
	}
	// filepath.WalkDir visits a directory before what is in it, and the
	// entries of a directory by name, which is the order of path
	// components.
	slices.SortStableFunc(t.dirs, func(a, b gitTreeDir) int { return compareWalkOrder(a.rel, b.rel) })
	t.indexChildren()
	return nil
}

// visit records one entry of the scan walk.
func (t *gitRepositoryTree) visit(abs string, entry fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if abs == t.root {
		return nil
	}
	rel, err := filepath.Rel(t.root, abs)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if strings.EqualFold(entry.Name(), ".git") {
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if t.skip(rel, entry.IsDir()) {
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	switch {
	case entry.IsDir():
		// A lister knows the gitlinks of the index or the commit. Without
		// one, a directory with its own .git is taken as a gitlink.
		if _, listed := t.worktree.(GitlinkLister); !listed {
			if _, err := os.Lstat(filepath.Join(abs, ".git")); err == nil {
				t.addGitlink(rel)
			}
		}
		if t.gitlinkSet[rel] && !t.submodules {
			// The checkout holds an empty directory here.
			return filepath.SkipDir
		}
	case entry.Type()&fs.ModeSymlink != 0:
		t.addFile(rel, true)
	case entry.Type().IsRegular():
		t.addFile(rel, false)
	}
	return nil
}

// compareWalkOrder orders two slash paths as filepath.WalkDir visits them:
// component by component, by name.
func compareWalkOrder(a, b string) int {
	return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
}

func (t *gitRepositoryTree) addGitlink(rel string) {
	if !validGitlinkPath(rel) || t.gitlinkSet[rel] {
		return
	}
	t.gitlinkSet[rel] = true
	t.gitlinks = append(t.gitlinks, rel)
	for dir := rel; dir != "."; dir = path.Dir(dir) {
		t.contentDirs[dir] = true
	}
}

// validGitlinkPath reports whether rel is a gitlink path that the index or
// a commit can hold: relative, clean, and without a ".." or .git part, as
// materializeGitlinks requires.
func validGitlinkPath(rel string) bool {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == "." {
		return false
	}
	for component := range strings.SplitSeq(rel, "/") {
		if component == ".." || strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}

// hasChild reports whether the scan holds name, exactly as spelled, in the
// directory dir.
func (t *gitRepositoryTree) hasChild(dir, name string) bool {
	return t.childSet[childKey(dir+"/"+name)]
}

// childKey is the key of a path in childSet. On macOS, a file system can
// hold a name decomposed (NFD), while Git commits it composed (NFC,
// core.precomposeUnicode) and the Linux checkout of the repo-server holds
// it composed, so both sides of a lookup compare as NFC there. The letter
// case stays exact.
func childKey(rel string) string {
	if runtime.GOOS == "darwin" {
		return norm.NFC.String(rel)
	}
	return rel
}

// indexChildren fills children from the scan: the files and symlinks, and
// the directories of the checkout.
func (t *gitRepositoryTree) indexChildren() {
	t.children = map[string][]gitTreeChild{}
	t.childSet = map[string]bool{}
	add := func(rel string, kind gitTreeKind) {
		parent := path.Dir(rel)
		t.children[parent] = append(t.children[parent], gitTreeChild{name: path.Base(rel), kind: kind})
		t.childSet[childKey(parent+"/"+path.Base(rel))] = true
	}
	for _, dir := range t.dirs {
		add(dir.rel, gitTreeKindDir)
	}
	for _, file := range t.files {
		kind := gitTreeKindFile
		if file.symlink {
			kind = gitTreeKindSymlink
		}
		add(file.rel, kind)
	}
}

// inGitlink reports whether rel is below one of the gitlinks.
func (t *gitRepositoryTree) inGitlink(rel string) bool {
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		if t.gitlinkSet[dir] {
			return true
		}
	}
	return false
}

func (t *gitRepositoryTree) addFile(rel string, symlink bool) {
	t.files = append(t.files, gitTreeFile{rel: rel, symlink: symlink})
	for dir := path.Dir(rel); dir != "." && !t.contentDirs[dir]; dir = path.Dir(dir) {
		t.contentDirs[dir] = true
	}
}

// hiddenPath reports whether a component of rel starts with ".", so the
// repo-server walk skips rel without hidden directories.
func hiddenPath(rel string) bool {
	for component := range strings.SplitSeq(rel, "/") {
		if strings.HasPrefix(component, ".") {
			return true
		}
	}
	return false
}

// hasSubmoduleConfig reports whether the tree has gitlinks and a
// .gitmodules file, so that Argo CD fetches submodule content.
func (t *gitRepositoryTree) hasSubmoduleConfig() bool {
	return len(t.gitlinks) > 0 && t.submodules
}

// directories mirrors the repo-server's walkDirectoryTree
// (reposerver/repository/utils.go:90-121 at v3.6.1) over the checkout: every
// directory in walk order, without hidden directories and their subtrees
// unless includeHidden, and never through a symlink. A checkout has a
// directory only when a file is below it, or when it is a gitlink. With
// includeHidden, the walk also lists the checkout's .git directory, so this
// lists ".git" too; it never lists what is in it.
func (t *gitRepositoryTree) directories(includeHidden bool) ([]string, error) {
	if err := t.scan(); err != nil {
		return nil, err
	}
	var out []string
	if includeHidden {
		out = append(out, ".git")
	}
	for _, dir := range t.dirs {
		if !includeHidden && hiddenPath(dir.rel) {
			continue
		}
		out = append(out, dir.rel)
	}
	if includeHidden {
		slices.SortStableFunc(out, compareWalkOrder)
	}
	return out, nil
}

// getFiles mirrors the repo-server's GetGitFiles
// (reposerver/repository/repository.go:3304-3380 at v3.6.1) for one
// pattern: an empty pattern means ".", LsFiles lists the files, and the
// repo-server reads each one, following symlinks. Errors that make the
// repo-server fail are generatorParamsError. inputs holds, for each file,
// the paths that its content comes from: the symlinks that the read
// followed and the file it read.
func (t *gitRepositoryTree) getFiles(manifestPath, pattern string, newGlobbing bool) (map[string][]byte, []diagnostic.Diagnostic, error) {
	out, _, diags, err := t.getFilesWithInputs(manifestPath, pattern, newGlobbing)
	return out, diags, err
}

func (t *gitRepositoryTree) getFilesWithInputs(manifestPath, pattern string, newGlobbing bool) (map[string][]byte, map[string][]string, []diagnostic.Diagnostic, error) {
	if pattern == "" {
		pattern = "."
	}
	if err := t.scan(); err != nil {
		return nil, nil, nil, fmt.Errorf("%s git generator: list files: %w", manifestPath, err)
	}
	var (
		listed []gitTreeFile
		diags  []diagnostic.Diagnostic
		err    error
	)
	if newGlobbing {
		listed, diags, err = t.globFiles(manifestPath, pattern)
	} else {
		listed, err = t.lsFiles(pattern)
	}
	if err != nil {
		return nil, nil, diags, err
	}
	out := make(map[string][]byte, len(listed))
	inputs := make(map[string][]string, len(listed))
	for _, file := range listed {
		data, paths, diag, err := t.readFile(manifestPath, pattern, file)
		if diag != nil {
			diags = append(diags, *diag)
		}
		if err != nil {
			return nil, nil, diags, err
		}
		if diag == nil {
			out[file.rel] = data
			inputs[file.rel] = paths
		}
	}
	return out, inputs, diags, nil
}

// lsFiles is `git ls-files --full-name -z -- <pattern>` in the top level of
// the checkout (util/git/client.go:763-770): the entries of the index whose
// path the pathspec matches. A gitlink is one entry, matched as a
// directory, and nothing in it is listed. A pathspec that git refuses fails
// the generator. One that drydock does not evaluate makes the
// ApplicationSet unsupported.
func (t *gitRepositoryTree) lsFiles(pattern string) ([]gitTreeFile, error) {
	spec, err := parseGitPathspec(pattern)
	if err != nil {
		if _, ok := errors.AsType[*gitPathspecError](err); ok {
			return nil, paramsError(fmt.Errorf("unable to list files. pattern %s: git ls-files: fatal: %w", pattern, err))
		}
		return nil, &generatorUnsupportedError{err: err}
	}
	var out []gitTreeFile
	for _, link := range t.gitlinks {
		if !t.inGitlink(link) && spec.matchesEntry(link, true) {
			out = append(out, gitTreeFile{rel: link})
		}
	}
	for _, file := range t.files {
		if !t.inGitlink(file.rel) && spec.matchesEntry(file.rel, false) {
			out = append(out, file)
		}
	}
	slices.SortFunc(out, func(a, b gitTreeFile) int { return strings.Compare(a.rel, b.rel) })
	return out, nil
}

// globFiles is LsFiles with applicationsetcontroller.enable.new.git.file.globbing
// (util/git/client.go:726-761): doublestar.FilepathGlob of the pattern
// joined to the checkout root, which matches directories too and follows
// symlinks, and keeps a match only when its symlinks resolve inside the
// checkout and fails only when it cannot resolve a match. drydock globs
// over a view of the checkout (gitGlobFS) that it builds as the glob asks,
// so it never lists a directory outside root or in .git. A match through a
// symlink out of the checkout is dropped, as Argo CD drops it. When a
// wildcard still has to match below such a symlink, the ApplicationSet is
// unsupported (gitGlobFS.checkVisited). A pattern that leaves root matches
// nothing here; in Argo CD it can only come back into the checkout through
// the checkout's random directory name.
func (t *gitRepositoryTree) globFiles(manifestPath, pattern string) ([]gitTreeFile, []diagnostic.Diagnostic, error) {
	// filepath.Join cleans the joined path, and an absolute pattern is
	// joined below the root too.
	const placeholderRoot = "/root"
	joined := path.Clean(placeholderRoot + "/" + filepath.ToSlash(pattern))
	var relPattern string
	switch {
	case joined == placeholderRoot:
		relPattern = "."
	case strings.HasPrefix(joined, placeholderRoot+"/"):
		relPattern = strings.TrimPrefix(joined, placeholderRoot+"/")
	default:
		diag := appsetDiagnostic(manifestPath, fmt.Sprintf("git files pattern %q leaves the repository; drydock matches it with nothing, and never lists files outside the repository", pattern))
		return nil, []diagnostic.Diagnostic{diag}, nil
	}
	if err := t.scan(); err != nil {
		return nil, nil, fmt.Errorf("%s git generator: list files: %w", manifestPath, err)
	}
	fsys := newGitGlobFS(t, relPattern)
	matches, err := doublestar.Glob(fsys, relPattern)
	if fsys.exhausted {
		return nil, nil, &generatorUnsupportedError{err: fmt.Errorf("git files pattern %q visits more than %d paths through the symlinks of the repository", pattern, maxGlobVisits)}
	}
	if err != nil {
		return nil, nil, paramsError(fmt.Errorf("unable to list files. pattern %s: %w", pattern, err))
	}
	if len(fsys.unsupported) > 0 {
		return nil, nil, &generatorUnsupportedError{err: fmt.Errorf("git files pattern %q %s", pattern, fsys.unsupported[0])}
	}
	out := make([]gitTreeFile, 0, len(matches))
	for _, match := range matches {
		node, err := fsys.lookup(match)
		if err == nil && node.outside {
			// LsFiles drops a match whose real path is outside the
			// checkout (util/git/client.go:750-758).
			continue
		}
		if err == nil && node.gitEscape {
			return nil, nil, &generatorUnsupportedError{err: fmt.Errorf("git files pattern %q matches %s, a symlink through the .git directory of the checkout, which drydock does not read", pattern, match)}
		}
		if err == nil && node.inGit {
			if node.dir {
				// Argo CD fails to read a directory of its .git directory.
				return nil, nil, paramsError(fmt.Errorf("unable to read files. pattern %s: read %s: is a directory", pattern, match))
			}
			return nil, nil, &generatorUnsupportedError{err: fmt.Errorf("git files pattern %q matches %s in the .git directory of the checkout, which Argo CD reads and drydock does not", pattern, match)}
		}
		out = append(out, gitTreeFile{rel: match})
	}
	return out, nil, nil
}

// checkoutGitPaths are the paths of the .git directory of an Argo CD
// checkout (git init, git fetch, git checkout). drydock never reads .git,
// so the glob sees these names only to tell that a pattern matches .git.
var checkoutGitPaths = []string{
	".git/FETCH_HEAD", ".git/HEAD", ".git/config", ".git/description", ".git/index", ".git/packed-refs",
	".git/hooks/", ".git/info/", ".git/info/exclude", ".git/logs/", ".git/logs/HEAD",
	".git/objects/", ".git/objects/info/", ".git/objects/pack/",
	".git/refs/", ".git/refs/heads/", ".git/refs/remotes/", ".git/refs/tags/",
}

// maxGlobVisits bounds the paths that one glob looks up, as a guard
// against symlinks that multiply the tree. The kernel stops a path after
// maxRepoSymlinkHops symlinks, but two symlinks to "." still give 2^40
// paths.
const maxGlobVisits = 1 << 17

// gitGlobFS is the checkout as FilepathGlob sees it. It answers each
// lookup from the scan, resolving symlinks one component at a time
// (resolveRepoPath), and caches the answer. A symlink that does not
// resolve, or resolves outside the checkout, is listed by ReadDir but does
// not exist for Stat, as the kernel reports. The .git directory of the
// checkout holds the names of checkoutGitPaths.
type gitGlobFS struct {
	tree *gitRepositoryTree
	// segments are the slash-separated parts of the pattern, and deep
	// reports a "**" or a brace group with a "/", which can match any
	// number of path parts.
	segments []string
	deep     bool
	// unsupported holds why the glob needs what drydock does not read.
	unsupported []string
	nodes       map[string]gitGlobNode
	gitDirs     map[string][]gitTreeChild
	visits      int
	exhausted   bool
}

type gitGlobNode struct {
	exists bool
	dir    bool
	// real is the resolved path relative to root, or the path in .git.
	real  string
	inGit bool
	// hops is the number of symlinks the path followed.
	hops int
	// outside reports a path whose symlinks lead out of the checkout.
	outside bool
	// gitEscape reports a path whose symlinks enter the top-level .git
	// directory and leave it again, which drydock does not follow.
	gitEscape bool
}

func newGitGlobFS(t *gitRepositoryTree, pattern string) *gitGlobFS {
	fsys := &gitGlobFS{tree: t, nodes: map[string]gitGlobNode{}, gitDirs: map[string][]gitTreeChild{}}
	fsys.segments = strings.Split(pattern, "/")
	for _, segment := range fsys.segments {
		if segment == "**" || strings.Contains(segment, "**") {
			fsys.deep = true
		}
	}
	if open := strings.IndexByte(pattern, '{'); open >= 0 && strings.Contains(pattern[open:], "/") && strings.Contains(pattern[open:], "}") {
		// A brace group can hold a "/", so the parts do not line up.
		for depth, i := 0, open; i < len(pattern); i++ {
			switch pattern[i] {
			case '{':
				depth++
			case '}':
				depth--
			case '/':
				if depth > 0 {
					fsys.deep = true
				}
			}
		}
	}
	fsys.gitDirs["."] = []gitTreeChild{{name: ".git", kind: gitTreeKindDir}}
	for _, gitPath := range checkoutGitPaths {
		kind := gitTreeKindFile
		if strings.HasSuffix(gitPath, "/") {
			kind = gitTreeKindDir
		}
		rel := strings.TrimSuffix(gitPath, "/")
		parent := path.Dir(rel)
		fsys.gitDirs[parent] = append(fsys.gitDirs[parent], gitTreeChild{name: path.Base(rel), kind: kind})
	}
	return fsys
}

var errGlobVisits = errors.New("too many paths")

// lookup resolves name, a slash path below the root.
func (f *gitGlobFS) lookup(name string) (gitGlobNode, error) {
	if node, ok := f.nodes[name]; ok {
		return node, nil
	}
	f.visits++
	if f.visits > maxGlobVisits {
		f.exhausted = true
		return gitGlobNode{}, errGlobVisits
	}
	node := f.resolve(name)
	f.nodes[name] = node
	return node, nil
}

// resolve resolves name from the node of its parent, as the kernel walks a
// path: one component at a time, following a symlink in it, and counting
// the symlinks of the whole path.
//
//nolint:gocyclo // One case per step of kernel path resolution.
func (f *gitGlobFS) resolve(name string) gitGlobNode {
	if name == "." {
		return gitGlobNode{exists: true, dir: true, real: "."}
	}
	parent, err := f.lookup(path.Dir(name))
	if err != nil || !parent.exists || !parent.dir {
		return gitGlobNode{}
	}
	component := path.Base(name)
	if parent.inGit {
		return f.gitNode(path.Join(parent.real, component))
	}
	if parent.real == "." && component == ".git" {
		return f.gitNode(".git")
	}
	candidate := path.Join(parent.real, component)
	// The exact name of the scan, also on a file system that ignores case.
	// A path that the scan left out, such as one in .git, in a gitlink
	// without .gitmodules, or one that Git ignores, does not exist.
	if !f.tree.hasChild(parent.real, component) {
		return gitGlobNode{}
	}
	if f.tree.contentDirs[candidate] {
		return gitGlobNode{exists: true, dir: true, real: candidate, hops: parent.hops}
	}
	info, err := os.Lstat(filepath.Join(f.tree.root, filepath.FromSlash(candidate)))
	if err != nil {
		return gitGlobNode{}
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if info.Mode().IsRegular() {
			return gitGlobNode{exists: true, real: candidate, hops: parent.hops}
		}
		// An empty directory, a FIFO, or a socket is no entry of a checkout.
		return gitGlobNode{}
	}
	resolution, err := f.tree.resolve(candidate)
	hops := parent.hops + resolution.hops
	if err != nil || hops > maxRepoSymlinkHops {
		return gitGlobNode{}
	}
	switch resolution.state {
	case repoSymlinkInside:
	case repoSymlinkOutside:
		return gitGlobNode{outside: true}
	case repoSymlinkInGit:
		if resolution.gitRest == "" {
			return gitGlobNode{gitEscape: true}
		}
		return f.gitNode(resolution.gitRest)
	}
	resolvedPath := resolution.path
	if resolvedPath == "." || f.tree.contentDirs[resolvedPath] {
		return gitGlobNode{exists: true, dir: true, real: resolvedPath, hops: hops}
	}
	info, err = os.Lstat(filepath.Join(f.tree.root, filepath.FromSlash(resolvedPath)))
	if err != nil || !info.Mode().IsRegular() {
		return gitGlobNode{}
	}
	return gitGlobNode{exists: true, real: resolvedPath, hops: hops}
}

// gitNode is the node of rel in the .git directory of the checkout.
func (f *gitGlobFS) gitNode(rel string) gitGlobNode {
	if rel == ".git" || slices.Contains(checkoutGitPaths, rel+"/") {
		return gitGlobNode{exists: true, dir: true, real: rel, inGit: true}
	}
	if slices.Contains(checkoutGitPaths, rel) {
		return gitGlobNode{exists: true, real: rel, inGit: true}
	}
	return gitGlobNode{}
}

// checkVisited records a node that the glob asks for and that drydock
// cannot answer for Argo CD. Argo CD's glob follows a symlink out of the
// checkout and drops the matches there, but it can enter a tree that holds
// its own checkout, such as a symlink to "..". So a symlink out of the
// checkout is unsupported when a wildcard part of the pattern still has to
// match below it, and needs nothing when only literal parts remain, which
// match nothing in the checkout. A symlink through .git is never followed.
func (f *gitGlobFS) checkVisited(name string, node gitGlobNode) {
	if node.gitEscape {
		f.unsupported = append(f.unsupported, fmt.Sprintf("goes through %s, a symlink through the .git directory of the checkout, which drydock does not read", name))
		return
	}
	if !node.outside {
		return
	}
	depth := len(strings.Split(name, "/"))
	if f.deep || (depth < len(f.segments) && hasGlobMeta(f.segments[depth:])) {
		f.unsupported = append(f.unsupported, fmt.Sprintf("goes through %s, a symlink to a target outside the repository, where a wildcard still has to match", name))
	}
}

// hasGlobMeta reports whether a pattern part holds a doublestar wildcard.
func hasGlobMeta(segments []string) bool {
	for _, segment := range segments {
		if strings.ContainsAny(segment, "*?[{") {
			return true
		}
	}
	return false
}

func (f *gitGlobFS) Open(name string) (fs.File, error) {
	info, err := f.Stat(name)
	if err != nil {
		return nil, err
	}
	return gitGlobFile{info: info}, nil
}

func (f *gitGlobFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	node, err := f.lookup(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	f.checkVisited(name, node)
	if !node.exists {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return gitGlobInfo{name: path.Base(name), dir: node.dir}, nil
}

func (f *gitGlobFS) ReadDir(name string) ([]fs.DirEntry, error) {
	node, err := f.lookup(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	if node.outside {
		f.unsupported = append(f.unsupported, fmt.Sprintf("lists %s, a symlink to a target outside the repository, which drydock does not read", name))
	}
	f.checkVisited(name, node)
	if !node.exists || !node.dir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	var children []gitTreeChild
	if node.inGit {
		children = f.gitDirs[node.real]
	} else {
		children = f.tree.children[node.real]
		if node.real == "." {
			children = append(append([]gitTreeChild(nil), f.gitDirs["."]...), children...)
		}
	}
	entries := make([]fs.DirEntry, 0, len(children))
	for _, child := range children {
		entries = append(entries, gitGlobEntry{child: child})
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

type gitGlobEntry struct {
	child gitTreeChild
}

func (e gitGlobEntry) Name() string { return e.child.name }
func (e gitGlobEntry) IsDir() bool  { return e.child.kind == gitTreeKindDir }
func (e gitGlobEntry) Type() fs.FileMode {
	switch e.child.kind {
	case gitTreeKindDir:
		return fs.ModeDir
	case gitTreeKindSymlink:
		return fs.ModeSymlink
	case gitTreeKindFile:
		return 0
	default:
		return 0
	}
}
func (e gitGlobEntry) Info() (fs.FileInfo, error) {
	return gitGlobInfo{name: e.child.name, dir: e.IsDir()}, nil
}

type gitGlobInfo struct {
	name string
	dir  bool
}

func (i gitGlobInfo) Name() string { return i.name }
func (i gitGlobInfo) Size() int64  { return 0 }
func (i gitGlobInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i gitGlobInfo) ModTime() time.Time { return time.Time{} }
func (i gitGlobInfo) IsDir() bool        { return i.dir }
func (i gitGlobInfo) Sys() any           { return nil }

type gitGlobFile struct {
	info fs.FileInfo
}

func (f gitGlobFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f gitGlobFile) Read([]byte) (int, error)   { return 0, io.EOF }
func (f gitGlobFile) Close() error               { return nil }

// readFile reads one listed path as the repo-server does, with
// os.ReadFile, which follows symlinks: a directory (a gitlink too), a
// missing target, a target that drydock skips (Options.Worktree), or one
// that is no regular file fails the generator. A symlink whose target
// drydock does not read, outside the repository or in .git, gives a
// warning and no content. The read goes through os.Root, so a symlink that
// appears after the check cannot lead outside the repository. paths are
// the symlinks the read followed and the file it read.
func (t *gitRepositoryTree) readFile(manifestPath, pattern string, file gitTreeFile) ([]byte, []string, *diagnostic.Diagnostic, error) {
	if t.gitlinkSet[file.rel] {
		return nil, nil, nil, paramsError(fmt.Errorf("unable to read files. pattern %s: read %s: is a directory", pattern, file.rel))
	}
	resolution, err := t.resolve(file.rel)
	if err != nil {
		return nil, nil, nil, paramsError(fmt.Errorf("unable to read files. pattern %s: %s: %w", pattern, file.rel, err))
	}
	switch resolution.state {
	case repoSymlinkInside:
	case repoSymlinkOutside:
		// Argo CD reads the target in its own file system, or fails when
		// it does not exist there.
		return nil, nil, nil, &generatorUnsupportedError{err: fmt.Errorf("git files path %q is a symlink to a target outside the repository, which drydock does not read", file.rel)}
	case repoSymlinkInGit:
		if resolution.gitRest == ".git" || slices.Contains(checkoutGitPaths, resolution.gitRest+"/") {
			return nil, nil, nil, paramsError(fmt.Errorf("unable to read files. pattern %s: read %s: is a directory", pattern, file.rel))
		}
		return nil, nil, nil, &generatorUnsupportedError{err: fmt.Errorf("git files path %q is a symlink into the .git directory of the checkout, which Argo CD reads and drydock does not", file.rel)}
	}
	resolved := resolution.path
	if t.gitlinkSet[resolved] || (resolved != "." && t.contentDirs[resolved] && !t.isOnDisk(resolved)) {
		return nil, nil, nil, paramsError(fmt.Errorf("unable to read files. pattern %s: read %s: is a directory", pattern, file.rel))
	}
	data, err := t.readRegular(resolved)
	if err != nil {
		if errors.Is(err, errIsDirectory) {
			return nil, nil, nil, paramsError(fmt.Errorf("unable to read files. pattern %s: read %s: is a directory", pattern, file.rel))
		}
		// A FIFO or socket is no file of a checkout, so the repo-server
		// finds no file there.
		if errors.Is(err, pathsafety.ErrNotRegular) {
			return nil, nil, nil, paramsError(fmt.Errorf("unable to read files. pattern %s: open %s: no such file or directory", pattern, file.rel))
		}
		return nil, nil, nil, fmt.Errorf("%s git generator: read %s: %w", manifestPath, file.rel, err)
	}
	paths := append(append([]string{file.rel}, resolution.followed...), resolved)
	slices.Sort(paths)
	return data, slices.Compact(paths), nil, nil
}

func (t *gitRepositoryTree) isOnDisk(rel string) bool {
	_, err := os.Lstat(filepath.Join(t.root, filepath.FromSlash(rel)))
	return err == nil
}

var errIsDirectory = errors.New("is a directory")

// readRegular reads the resolved path rel through os.Root, without
// blocking on a FIFO, and only when the open file is a regular file.
func (t *gitRepositoryTree) readRegular(rel string) ([]byte, error) {
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	handle, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errIsDirectory
	}
	if !info.Mode().IsRegular() {
		return nil, pathsafety.ErrNotRegular
	}
	return io.ReadAll(handle)
}

type repoSymlinkState int

const (
	repoSymlinkInside repoSymlinkState = iota
	repoSymlinkOutside
	repoSymlinkInGit
)

// maxRepoSymlinkHops is the symlink limit of Linux path resolution
// (MAXSYMLINKS is 40), where the Argo CD repo-server runs. It is a
// variable so a test can compare with the limit of its own kernel.
var maxRepoSymlinkHops = 40

// repoResolution is the result of resolveRepoPath.
type repoResolution struct {
	// path is the resolved path relative to root.
	path  string
	state repoSymlinkState
	// hops is the number of symlinks followed.
	hops int
	// followed are the symlinks followed, relative to root.
	followed []string
	// gitRest is, for a path that enters the top-level .git directory,
	// the clean path from ".git" on, or "" when it leaves .git again.
	gitRest string
}

// resolve is resolveRepoPath over the scan of t: a component must be an
// entry of the scan with that exact name.
func (t *gitRepositoryTree) resolve(rel string) (repoResolution, error) {
	return resolveRepoPath(t.root, rel, t.hasChild)
}

// resolveRepoPath resolves every symlink of rel, a path below root, one
// component at a time, as the kernel does. It never touches a path outside
// root or in .git: a symlink with an absolute target, or a ".." above
// root, gives repoSymlinkOutside, and the top-level .git directory gives
// repoSymlinkInGit, before anything there is read. An Argo CD checkout
// lives in a temporary directory, so an absolute target never points into
// it. A component after a file fails with ENOTDIR, a component that exists
// does not report with ENOENT, and more than maxRepoSymlinkHops symlinks
// with ELOOP, as os.ReadFile would. exists is asked for every component
// below root with the exact name, so a file system that ignores case does
// not change the answer.
//
//nolint:gocyclo // One case per step of kernel path resolution.
func resolveRepoPath(root, rel string, exists func(dir, name string) bool) (repoResolution, error) {
	pending := strings.Split(rel, "/")
	var resolved []string
	var result repoResolution
	afterFile := false
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		if afterFile {
			return result, &fs.PathError{Op: "open", Path: rel, Err: syscall.ENOTDIR}
		}
		switch component {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				result.state = repoSymlinkOutside
				return result, nil
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		if len(resolved) == 0 && component == ".git" {
			result.state = repoSymlinkInGit
			rest := path.Clean(path.Join(append([]string{".git"}, pending...)...))
			if rest == ".git" || strings.HasPrefix(rest, ".git/") {
				result.gitRest = rest
			}
			return result, nil
		}
		dir := path.Join(resolved...)
		if dir == "" {
			dir = "."
		}
		// Another .git entry is no path of a checkout, and the scan holds
		// none.
		if strings.EqualFold(component, ".git") || (exists != nil && !exists(dir, component)) {
			return result, &fs.PathError{Op: "open", Path: rel, Err: syscall.ENOENT}
		}
		current := path.Join(dir, component)
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(current)))
		if err != nil {
			return result, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, component)
			afterFile = !info.IsDir()
			continue
		}
		result.hops++
		if result.hops > maxRepoSymlinkHops {
			return result, &fs.PathError{Op: "open", Path: rel, Err: syscall.ELOOP}
		}
		result.followed = append(result.followed, current)
		target, err := os.Readlink(filepath.Join(root, filepath.FromSlash(current)))
		if err != nil {
			return result, err
		}
		target = filepath.ToSlash(target)
		if path.IsAbs(target) {
			result.state = repoSymlinkOutside
			return result, nil
		}
		pending = append(strings.Split(target, "/"), pending...)
	}
	result.path = path.Join(resolved...)
	if result.path == "" {
		result.path = "."
	}
	return result, nil
}
