package gittree

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// WriteOptions says where WriteTree writes a Git tree.
type WriteOptions struct {
	// Root is the directory that holds the checkout. Nothing is written
	// outside it.
	Root string
	// Prefix is the slash path below Root that the tree is written to:
	// empty for the top of a checkout, or the path of a submodule.
	Prefix string
	// KeepOutOfBoundsSymlinks writes a symlink whose target is absolute or
	// leaves Root as it is, instead of failing.
	KeepOutOfBoundsSymlinks bool
}

// WriteTree writes the files and symlinks of tree below Root/Prefix and
// creates an empty directory for each gitlink (submodule) of tree, as a
// checkout holds one before any submodule content is fetched. It returns
// the gitlinks with their paths relative to Root.
func WriteTree(ctx context.Context, tree *object.Tree, opts WriteOptions) ([]Gitlink, error) {
	files := make([]object.File, 0)
	if err := tree.Files().ForEach(func(file *object.File) error {
		copied := *file
		copied.Name = joinPrefix(opts.Prefix, file.Name)
		files = append(files, copied)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	if opts.Prefix != "" {
		if err := rejectGitPaths(files); err != nil {
			return nil, err
		}
	}
	if err := rejectOverlappingTreePaths(files); err != nil {
		return nil, err
	}
	if err := writeTreeFiles(ctx, files, opts.Root, opts.KeepOutOfBoundsSymlinks); err != nil {
		return nil, err
	}
	links, err := Links(tree)
	if err != nil {
		return nil, err
	}
	for i := range links {
		links[i].Path = joinPrefix(opts.Prefix, links[i].Path)
		if err := makeGitlinkDir(opts.Root, links[i].Path); err != nil {
			return nil, err
		}
	}
	return links, nil
}

// MakeGitlinkDirs creates the empty directory of each gitlink below root.
func MakeGitlinkDirs(root string, links []Gitlink) error {
	for _, link := range links {
		if err := makeGitlinkDir(root, link.Path); err != nil {
			return err
		}
	}
	return nil
}

func joinPrefix(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return path.Join(prefix, name)
}

// makeGitlinkDir creates the empty directory of the gitlink at the slash
// path rel below root.
func makeGitlinkDir(root, rel string) error {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || pathsafety.RelEscapes(clean) {
		return fmt.Errorf("unsafe Git tree path %q", rel)
	}
	target := filepath.Join(root, clean)
	if err := validateWriteParent(root, filepath.Dir(target)); err != nil {
		return err
	}
	return MkdirParents(root, target)
}

// ValidRelPath reports whether rel is a slash path that a Git tree can
// hold below the top of a checkout: relative, clean, not ".", and without
// a ".." or .git component (in any letter case).
func ValidRelPath(rel string) bool {
	if rel == "" || rel == "." || path.IsAbs(rel) || path.Clean(rel) != rel || strings.Contains(rel, `\`) {
		return false
	}
	for component := range strings.SplitSeq(rel, "/") {
		if component == ".." || strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}

func writeTreeFiles(ctx context.Context, files []object.File, root string, keepOutOfBoundsSymlinks bool) error {
	if len(files) == 0 {
		return ctx.Err()
	}
	workers := Parallelism(len(files))
	jobs := make(chan int)
	errs := make([]error, len(files))
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					errs[i] = err
					continue
				}
				errs[i] = writeTreeFile(root, &files[i], keepOutOfBoundsSymlinks)
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return firstError(errs)
}

func firstError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// MaxWorkers bounds the workers that write or compare the files of a tree.
const MaxWorkers = 16

// Parallelism returns how many workers process fileCount files of a tree.
func Parallelism(fileCount int) int {
	if fileCount <= 1 {
		return fileCount
	}
	workers := max(runtime.GOMAXPROCS(0), 1)
	workers = min(workers, MaxWorkers)
	workers = min(workers, fileCount)
	return workers
}

func writeTreeFile(root string, file *object.File, keepOutOfBoundsSymlinks bool) error {
	clean := filepath.Clean(file.Name)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("unsafe Git tree path %q", file.Name)
	}
	target := filepath.Join(root, clean)
	parent := filepath.Dir(target)
	if err := validateWriteParent(root, parent); err != nil {
		return err
	}
	if err := MkdirParents(root, parent); err != nil {
		return err
	}
	if err := validateWriteParent(root, parent); err != nil {
		return err
	}

	switch file.Mode {
	case filemode.Symlink:
		return writeTreeSymlink(root, target, file, keepOutOfBoundsSymlinks)
	case filemode.Regular, filemode.Deprecated, filemode.Executable:
		return WriteFile(target, file)
	case filemode.Empty, filemode.Dir, filemode.Submodule:
		return fmt.Errorf("unsupported Git tree file mode %s for %q", file.Mode, file.Name)
	default:
		return fmt.Errorf("unsupported Git tree file mode %s for %q", file.Mode, file.Name)
	}
}

func writeTreeSymlink(root, target string, file *object.File, keepOutOfBoundsSymlinks bool) error {
	body, err := file.Contents()
	if err != nil {
		return err
	}
	if body == "" {
		return fmt.Errorf("materialize symlink %q: symlink target must not be empty", file.Name)
	}
	if !keepOutOfBoundsSymlinks {
		if err := validateSymlinkTarget(root, target, body); err != nil {
			return fmt.Errorf("materialize symlink %q: %w", file.Name, err)
		}
	}
	return os.Symlink(body, target)
}

// rejectGitPaths fails for a submodule tree that names a path with a .git
// component, in any letter case. git refuses to check such a path out
// (verify_path), and a .git file would turn a directory of the checkout
// into a pointer to another repository.
func rejectGitPaths(files []object.File) error {
	for i := range files {
		for component := range strings.SplitSeq(files[i].Name, "/") {
			if strings.EqualFold(component, ".git") {
				return fmt.Errorf("unsafe Git tree path %q", files[i].Name)
			}
		}
	}
	return nil
}

// rejectOverlappingTreePaths fails for a malformed Git tree that names one
// path twice, or a path below another file. Git never writes such a tree.
// Written in parallel, a symlink entry could otherwise redirect the write of
// the other entry outside the checkout. files must be sorted by name.
func rejectOverlappingTreePaths(files []object.File) error {
	names := make(map[string]struct{}, len(files))
	for i := range files {
		name := files[i].Name
		if _, ok := names[name]; ok {
			return fmt.Errorf("git tree names path %q more than once", name)
		}
		names[name] = struct{}{}
	}
	for i := range files {
		for dir := path.Dir(files[i].Name); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if _, ok := names[dir]; ok {
				return fmt.Errorf("git tree path %q is below the file %q", files[i].Name, dir)
			}
		}
	}
	return nil
}

// WriteFile writes the content of file to target. It never writes through a
// symlink at target.
func WriteFile(target string, file *object.File) error {
	reader, err := file.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	mode := os.FileMode(0o644)
	if file.Mode == filemode.Executable {
		mode = 0o755
	}
	return WriteContent(target, reader, mode)
}

// WriteContent writes what reader holds to target with mode. O_NOFOLLOW
// fails on a symlink at target, so a write never goes through one. An
// existing regular file is truncated: on a case-insensitive filesystem,
// README.md and readme.md name one file, and the last write wins.
func WriteContent(target string, reader io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|openNoFollow, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, reader)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// MkdirParents creates dir and every directory between root and it, one
// component at a time. It refuses a component that exists as a symlink or
// as anything but a directory, so it never creates a directory through a
// symlink, outside root. os.MkdirAll follows symlinks, so on a
// case-insensitive filesystem a parallel worker that wrote the symlink "A"
// could make it create "a/x" outside root before any later check. os.Mkdir
// does not follow a symlink at its own path: it fails because the path
// exists, and the check that follows refuses it.
func MkdirParents(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return fmt.Errorf("snapshot destination %q escapes snapshot root %q", dir, root)
	}
	if rel == "." {
		return nil
	}
	current := root
	for component := range strings.SplitSeq(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("snapshot destination %q is not a directory inside snapshot root %q", current, root)
		}
	}
	return nil
}

func validateWriteParent(root, parent string) error {
	inside, _, err := pathsafety.IsInsideAny(parent, []string{root})
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("snapshot destination %q escapes snapshot root %q", parent, root)
	}
	return nil
}

func validateSymlinkTarget(root, linkPath, target string) error {
	if target == "" {
		return fmt.Errorf("symlink target must not be empty")
	}
	if filepath.IsAbs(target) {
		return fmt.Errorf("symlink target %q must be relative", target)
	}
	resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	inside, _, err := pathsafety.IsInsideAny(resolvedTarget, []string{root})
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("symlink target %q escapes snapshot root", target)
	}
	return nil
}
