package gitref

import (
	"context"
	"fmt"
	"io"
	"net/url"
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
	"github.com/sholdee/drydock/internal/source"
)

type Request struct {
	Repo           string
	Ref            string
	ForbiddenRoots []string
	// KeepOutOfBoundsSymlinks writes a symlink whose target is absolute or
	// leaves the snapshot as it is, instead of failing the snapshot. A
	// rendered snapshot needs it: the Argo CD repo-server fails each
	// Application that renders from such a tree, not the whole run, and
	// drydock's renderers check the tree and never read through such a
	// symlink. Nothing is written through it: a Git tree cannot hold a path
	// below a symlink.
	KeepOutOfBoundsSymlinks bool
}

type Result struct {
	Path     string
	Revision string
	Cleanup  func() error
}

func Snapshot(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	ref := strings.TrimSpace(request.Ref)
	if ref == "" {
		ref = "HEAD"
	}

	repo, displayRepoPath, err := openLocalRepository(request.Repo)
	if err != nil {
		return Result{}, err
	}
	hash, err := source.ResolveGitRevision(repo, ref)
	if err != nil {
		return Result{}, fmt.Errorf("resolve Git ref %q in %q: %w", ref, displayRepoPath, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return Result{}, fmt.Errorf("load Git commit %q in %q: %w", hash.String(), displayRepoPath, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return Result{}, fmt.Errorf("load Git tree for %q in %q: %w", hash.String(), displayRepoPath, err)
	}

	root, err := os.MkdirTemp("", "drydock-gitref-*")
	if err != nil {
		return Result{}, err
	}
	cleanup := func() error { return os.RemoveAll(root) }
	if inside, matchedRoot, err := pathsafety.IsInsideAny(root, request.ForbiddenRoots); err != nil {
		_ = cleanup()
		return Result{}, fmt.Errorf("validate Git ref snapshot temp directory %q: %w", root, err)
	} else if inside {
		_ = cleanup()
		return Result{}, fmt.Errorf("git ref snapshot temp directory %q is inside protected root %q", root, matchedRoot)
	}

	if err := materializeTree(ctx, tree, root, request.KeepOutOfBoundsSymlinks); err != nil {
		_ = cleanup()
		return Result{}, err
	}
	return Result{Path: root, Revision: hash.String(), Cleanup: cleanup}, nil
}

func looksLikeRemoteRepo(value string) bool {
	if strings.Contains(value, "://") {
		return true
	}
	if looksLikeSCPRemote(value) {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	if parsed.Host != "" {
		return true
	}
	switch strings.ToLower(parsed.Scheme) {
	case "file", "git", "git+ssh", "http", "https", "oci", "ssh":
		return true
	default:
		return false
	}
}

func looksLikeSCPRemote(value string) bool {
	if strings.Contains(value, "://") || strings.HasPrefix(value, "/") {
		return false
	}
	colon := strings.Index(value, ":")
	if colon <= 0 {
		return false
	}
	if slash := strings.IndexAny(value, `/\`); slash >= 0 && slash < colon {
		return false
	}
	hostPart := value[:colon]
	if user, host, ok := strings.Cut(hostPart, "@"); ok {
		return user != "" && host != ""
	}
	return strings.Contains(hostPart, ".")
}

func materializeTree(ctx context.Context, tree *object.Tree, root string, keepOutOfBoundsSymlinks bool) error {
	files := make([]object.File, 0)
	if err := tree.Files().ForEach(func(file *object.File) error {
		files = append(files, *file)
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	if err := rejectOverlappingTreePaths(files); err != nil {
		return err
	}
	return materializeTreeFiles(ctx, files, root, keepOutOfBoundsSymlinks)
}

func materializeTreeFiles(ctx context.Context, files []object.File, root string, keepOutOfBoundsSymlinks bool) error {
	if len(files) == 0 {
		return ctx.Err()
	}
	workers := materializeTreeParallelism(len(files))
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
				errs[i] = materializeTreeFile(root, &files[i], keepOutOfBoundsSymlinks)
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

const maxMaterializeTreeWorkers = 16

func materializeTreeParallelism(fileCount int) int {
	if fileCount <= 1 {
		return fileCount
	}
	workers := max(runtime.GOMAXPROCS(0), 1)
	workers = min(workers, maxMaterializeTreeWorkers)
	workers = min(workers, fileCount)
	return workers
}

func materializeTreeFile(root string, file *object.File, keepOutOfBoundsSymlinks bool) error {
	path := filepath.Clean(file.Name)
	if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("unsafe Git tree path %q", file.Name)
	}
	target := filepath.Join(root, path)
	parent := filepath.Dir(target)
	if err := validateSnapshotWriteParent(root, parent); err != nil {
		return err
	}
	if err := mkdirSnapshotParents(root, parent); err != nil {
		return err
	}
	if err := validateSnapshotWriteParent(root, parent); err != nil {
		return err
	}

	switch file.Mode {
	case filemode.Symlink:
		body, err := file.Contents()
		if err != nil {
			return err
		}
		if body == "" {
			return fmt.Errorf("materialize symlink %q: symlink target must not be empty", file.Name)
		}
		if !keepOutOfBoundsSymlinks {
			if err := validateSnapshotSymlinkTarget(root, target, body); err != nil {
				return fmt.Errorf("materialize symlink %q: %w", file.Name, err)
			}
		}
		return os.Symlink(body, target)
	case filemode.Regular, filemode.Deprecated, filemode.Executable:
		return writeSnapshotFile(target, file)
	case filemode.Empty, filemode.Dir, filemode.Submodule:
		return fmt.Errorf("unsupported Git tree file mode %s for %q", file.Mode, file.Name)
	default:
		return fmt.Errorf("unsupported Git tree file mode %s for %q", file.Mode, file.Name)
	}
}

// rejectOverlappingTreePaths fails for a malformed Git tree that names one
// path twice, or a path below another file. Git never writes such a tree.
// Written in parallel, a symlink entry could otherwise redirect the write of
// the other entry outside the snapshot. files must be sorted by name.
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

func writeSnapshotFile(target string, file *object.File) error {
	reader, err := file.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	mode := os.FileMode(0o644)
	if file.Mode == filemode.Executable {
		mode = 0o755
	}
	// O_EXCL fails on any existing path, a symlink included, so a write
	// never goes through a symlink.
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
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

// mkdirSnapshotParents creates dir and every directory between root and it,
// one component at a time. It refuses a component that exists as a symlink
// or as anything but a directory, so it never creates a directory through a
// symlink, outside the snapshot. os.MkdirAll follows symlinks, so on a
// case-insensitive filesystem a parallel worker that wrote the symlink "A"
// could make it create "a/x" outside the snapshot before any later check.
// os.Mkdir does not follow a symlink at its own path: it fails because the
// path exists, and the check that follows refuses it.
func mkdirSnapshotParents(root, dir string) error {
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

func validateSnapshotWriteParent(root, parent string) error {
	inside, _, err := pathsafety.IsInsideAny(parent, []string{root})
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("snapshot destination %q escapes snapshot root %q", parent, root)
	}
	return nil
}

func validateSnapshotSymlinkTarget(root, linkPath, target string) error {
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
