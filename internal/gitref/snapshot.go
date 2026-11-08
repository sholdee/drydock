package gitref

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/gittree"
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
	// Submodules, when set, checks out the content of the submodules of
	// the commit, as the Argo CD repo-server does by default
	// (CheckoutSubmodules). Without it, each gitlink stays an empty
	// directory.
	Submodules *SubmoduleOptions
}

// SubmoduleOptions says where a snapshot gets submodule content. A
// submodule commit comes from the local repository's own submodule
// store (.git/modules) first, which needs no network, and else from
// Opener.
type SubmoduleOptions struct {
	// RemoteURL is the URL that relative submodule URLs resolve against.
	// Empty means the URL of the remote origin of the repository, or of
	// its only remote.
	RemoteURL string
	// Opener finds a submodule commit that the local store does not hold:
	// drydock's Git cache, and the network unless drydock runs offline.
	Opener gittree.SubmoduleOpener
	// AllowFileTransport is gittree.SubmoduleRequest.AllowFileTransport.
	AllowFileTransport bool
}

type Result struct {
	Path     string
	Revision string
	Cleanup  func() error
	// Gitlinks are the gitlinks of the commit, relative to Path with
	// forward slashes, with or without submodule content.
	Gitlinks []string
	// SubmodulesConfigured reports that the commit has a .gitmodules file.
	SubmodulesConfigured bool
	// Submodules are the submodules the snapshot checked out.
	Submodules []gittree.CheckedOutSubmodule
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

	result := Result{Path: root, Revision: hash.String(), Cleanup: cleanup}
	if err := materializeTree(ctx, tree, root, request.KeepOutOfBoundsSymlinks); err != nil {
		_ = cleanup()
		return Result{}, err
	}
	submodules, err := snapshotSubmodules(ctx, request, tree, root)
	if err != nil {
		_ = cleanup()
		return Result{}, fmt.Errorf("check out the submodules of %q at %s: %w", displayRepoPath, hash.String(), err)
	}
	result.Gitlinks = submodules.Gitlinks
	result.SubmodulesConfigured = submodules.Configured
	result.Submodules = submodules.Submodules
	return result, nil
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
	_, err := gittree.WriteTree(ctx, tree, gittree.WriteOptions{Root: root, KeepOutOfBoundsSymlinks: keepOutOfBoundsSymlinks})
	return err
}

// mkdirSnapshotParents and writeSnapshotFile keep the names the tests of
// this package use.
var (
	mkdirSnapshotParents = gittree.MkdirParents
	writeSnapshotFile    = gittree.WriteFile
)

// SnapshotGitlinks returns the gitlinks of a snapshot at root that holds no
// submodule content, relative to root with forward slashes: its empty
// directories. A Git tree cannot hold another empty directory. A snapshot
// with submodule content lists them in Result.Gitlinks.
func SnapshotGitlinks(root string) []string {
	var out []string
	collectEmptyDirectories(root, "", &out)
	return out
}

func collectEmptyDirectories(dir, rel string, out *[]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if len(entries) == 0 && rel != "" {
		*out = append(*out, rel)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		collectEmptyDirectories(filepath.Join(dir, entry.Name()), path.Join(rel, entry.Name()), out)
	}
}
