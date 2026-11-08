package gittree

import (
	"context"
	"fmt"
	"path"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// MaxSubmoduleDepth bounds how deep drydock follows submodules of
// submodules. git has no such limit; a deeper tree fails.
const MaxSubmoduleDepth = 8

// MaxSubmodules bounds how many submodules drydock checks out for one
// checkout, nested ones included. A larger tree fails.
const MaxSubmodules = 256

// SubmoduleRef names one submodule that a checkout needs.
type SubmoduleRef struct {
	// Path is the slash path of the submodule below the top of the
	// checkout.
	Path string
	// ModuleDirs are the submodule names from the top superproject down to
	// this submodule. git keeps the repository of a submodule in
	// .git/modules/<name>, and of a nested one in
	// .git/modules/<name>/modules/<nested name>.
	ModuleDirs []string
	// URL is the resolved URL of the submodule.
	URL string
	// Commit is the commit that the superproject pins.
	Commit plumbing.Hash
}

// SubmoduleOpener returns a repository that holds the commit of ref. It
// fails when it has none.
type SubmoduleOpener interface {
	OpenSubmodule(ctx context.Context, ref SubmoduleRef) (*git.Repository, error)
}

// SubmoduleOpenerFunc adapts a function to SubmoduleOpener.
type SubmoduleOpenerFunc func(ctx context.Context, ref SubmoduleRef) (*git.Repository, error)

// OpenSubmodule calls f.
func (f SubmoduleOpenerFunc) OpenSubmodule(ctx context.Context, ref SubmoduleRef) (*git.Repository, error) {
	return f(ctx, ref)
}

// SubmoduleRequest is a checkout whose submodules CheckoutSubmodules
// writes.
type SubmoduleRequest struct {
	// Root is the directory of the checkout. The tree is already written
	// there (WriteTree).
	Root string
	// Tree is the tree of the checked-out commit of the superproject.
	Tree *object.Tree
	// RemoteURL is the URL that relative submodule URLs resolve against.
	RemoteURL string
	// Opener finds the repository of each submodule.
	Opener SubmoduleOpener
	// KeepOutOfBoundsSymlinks is WriteOptions.KeepOutOfBoundsSymlinks.
	KeepOutOfBoundsSymlinks bool
	// AllowFileTransport lets a submodule URL use the file transport or a
	// local path. The Argo CD repo-server's git refuses it; only tests set
	// it.
	AllowFileTransport bool
}

// CheckedOutSubmodule is a submodule that CheckoutSubmodules wrote.
type CheckedOutSubmodule struct {
	Path   string
	URL    string
	Commit plumbing.Hash
}

// SubmoduleResult is what CheckoutSubmodules did.
type SubmoduleResult struct {
	// Gitlinks are the gitlinks of the top-level tree, with or without
	// content.
	Gitlinks []string
	// Submodules are the submodules it wrote, nested ones included, in
	// the order it wrote them.
	Submodules []CheckedOutSubmodule
	// Configured reports that the top-level tree has a .gitmodules file.
	Configured bool
}

// CheckoutSubmodules writes the content of the submodules of a checkout, as
// `git submodule update --init --recursive` does in the Argo CD repo-server
// (util/git/client.go:783-789, run from Checkout at
// util/git/client.go:812-818 when the checkout has a .gitmodules file):
//
//   - a tree without a .gitmodules file gets no submodule content, and each
//     gitlink stays an empty directory;
//   - every gitlink needs a module with its path and a URL in .gitmodules,
//     or the update fails ("No url found for submodule path");
//   - a module with update = none is skipped and stays empty;
//   - a relative URL resolves against the URL of the superproject, and a
//     nested submodule's against the URL of its parent submodule;
//   - the file transport and every transport but http, https, git, and ssh
//     fail, as git refuses them for submodules;
//   - each submodule is checked out at the commit its superproject pins, and
//     its own submodules follow, to MaxSubmoduleDepth levels and
//     MaxSubmodules submodules.
//
// The superproject's tree must already be written to Root. Git LFS content
// of a submodule is never fetched: Argo CD runs the update with
// GIT_LFS_SKIP_SMUDGE=1 (util/git/client.go:1897) and `git lfs checkout`
// only in the superproject.
func CheckoutSubmodules(ctx context.Context, request SubmoduleRequest) (SubmoduleResult, error) {
	links, err := Links(request.Tree)
	if err != nil {
		return SubmoduleResult{}, err
	}
	result := SubmoduleResult{Gitlinks: make([]string, 0, len(links))}
	for _, link := range links {
		result.Gitlinks = append(result.Gitlinks, link.Path)
	}
	modules, found, err := ReadModules(request.Tree)
	if err != nil {
		return result, err
	}
	result.Configured = found
	if !found {
		return result, nil
	}
	walker := submoduleWalker{request: request, result: &result}
	err = walker.checkoutLevel(ctx, submoduleLevel{links: links, modules: modules, remoteURL: request.RemoteURL})
	return result, err
}

type submoduleWalker struct {
	request SubmoduleRequest
	result  *SubmoduleResult
}

// submoduleLevel is one superproject: the top one, or a submodule with its
// own submodules.
type submoduleLevel struct {
	links      []Gitlink
	modules    map[string]Module
	remoteURL  string
	prefix     string
	moduleDirs []string
	depth      int
}

func (w submoduleWalker) checkoutLevel(ctx context.Context, level submoduleLevel) error {
	if level.depth >= MaxSubmoduleDepth {
		return fmt.Errorf("submodule %q is nested more than %d levels deep", level.prefix, MaxSubmoduleDepth)
	}
	for _, link := range level.links {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.checkoutLink(ctx, level, link); err != nil {
			return err
		}
	}
	return nil
}

func (w submoduleWalker) checkoutLink(ctx context.Context, level submoduleLevel, link Gitlink) error {
	fullPath := joinPrefix(level.prefix, link.Path)
	module, ok := level.modules[link.Path]
	if !ok || module.URL == "" {
		return fmt.Errorf("no url found for submodule path %q in .gitmodules", fullPath)
	}
	if strings.EqualFold(strings.TrimSpace(module.Update), "none") {
		return nil
	}
	if !ValidRelPath(fullPath) {
		return fmt.Errorf("unsafe submodule path %q", fullPath)
	}
	resolved, err := ResolveSubmoduleURL(level.remoteURL, module.URL)
	if err != nil {
		return fmt.Errorf("submodule %q: %w", fullPath, err)
	}
	if err := w.checkTransport(fullPath, resolved); err != nil {
		return err
	}
	if len(w.result.Submodules) >= MaxSubmodules {
		return fmt.Errorf("checkout has more than %d submodules", MaxSubmodules)
	}
	ref := SubmoduleRef{
		Path:       fullPath,
		ModuleDirs: append(append([]string(nil), level.moduleDirs...), module.Name),
		URL:        resolved,
		Commit:     link.Commit,
	}
	tree, err := w.openTree(ctx, ref)
	if err != nil {
		return err
	}
	if _, err := WriteTree(ctx, tree, WriteOptions{Root: w.request.Root, Prefix: fullPath, KeepOutOfBoundsSymlinks: w.request.KeepOutOfBoundsSymlinks}); err != nil {
		return fmt.Errorf("check out submodule %q: %w", fullPath, err)
	}
	w.result.Submodules = append(w.result.Submodules, CheckedOutSubmodule{Path: fullPath, URL: resolved, Commit: link.Commit})
	return w.checkoutNested(ctx, level, ref, tree)
}

// checkoutNested checks out the submodules of the submodule ref, whose
// tree is tree.
func (w submoduleWalker) checkoutNested(ctx context.Context, level submoduleLevel, ref SubmoduleRef, tree *object.Tree) error {
	links, err := Links(tree)
	if err != nil || len(links) == 0 {
		return err
	}
	modules, found, err := ReadModules(tree)
	if err != nil {
		return fmt.Errorf("submodule %q: %w", ref.Path, err)
	}
	if !found {
		return nil
	}
	return w.checkoutLevel(ctx, submoduleLevel{
		links:      links,
		modules:    modules,
		remoteURL:  ref.URL,
		prefix:     ref.Path,
		moduleDirs: ref.ModuleDirs,
		depth:      level.depth + 1,
	})
}

func (w submoduleWalker) checkTransport(fullPath, resolved string) error {
	transport := SubmoduleTransport(resolved)
	if AllowedSubmoduleTransport(transport) {
		return nil
	}
	if transport == "file" && w.request.AllowFileTransport {
		return nil
	}
	return fmt.Errorf("submodule %q: transport %q not allowed", fullPath, transport)
}

func (w submoduleWalker) openTree(ctx context.Context, ref SubmoduleRef) (*object.Tree, error) {
	if w.request.Opener == nil {
		return nil, fmt.Errorf("submodule %q: no source for submodule content", ref.Path)
	}
	repo, err := w.request.Opener.OpenSubmodule(ctx, ref)
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(ref.Commit)
	if err != nil {
		return nil, fmt.Errorf("submodule %q: commit %s of %s: %w", ref.Path, ref.Commit, ref.URL, err)
	}
	return commit.Tree()
}

// SubmodulePathsBelow reports whether rel lies in one of the gitlinks.
func SubmodulePathsBelow(gitlinks []string, rel string) bool {
	for _, link := range gitlinks {
		if rel == link || strings.HasPrefix(rel, link+"/") {
			return true
		}
	}
	return false
}

// ChainOpener tries each opener in turn and returns the first repository
// that holds the commit. When none does, it fails with the error of the
// last one.
func ChainOpener(openers ...SubmoduleOpener) SubmoduleOpener {
	return SubmoduleOpenerFunc(func(ctx context.Context, ref SubmoduleRef) (*git.Repository, error) {
		var lastErr error
		for _, opener := range openers {
			if opener == nil {
				continue
			}
			repo, err := opener.OpenSubmodule(ctx, ref)
			if err == nil {
				if _, commitErr := repo.CommitObject(ref.Commit); commitErr == nil {
					return repo, nil
				}
				err = fmt.Errorf("submodule %q: commit %s not found", ref.Path, ref.Commit)
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("submodule %q: no source for commit %s", ref.Path, ref.Commit)
		}
		return nil, lastErr
	})
}

// cleanModulePath joins module names into a slash path, or returns false
// when a name would leave the directory.
func cleanModulePath(names []string) (string, bool) {
	parts := make([]string, 0, 2*len(names))
	for i, name := range names {
		clean := path.Clean("/" + strings.ReplaceAll(name, `\`, "/"))
		if clean == "/" || !validModuleName(name) {
			return "", false
		}
		if i > 0 {
			parts = append(parts, "modules")
		}
		parts = append(parts, strings.TrimPrefix(clean, "/"))
	}
	return path.Join(parts...), true
}

// ModuleStorePath returns the slash path of the repository of ref below a
// .git/modules directory, or false when a module name is unsafe.
func ModuleStorePath(ref SubmoduleRef) (string, bool) {
	return cleanModulePath(ref.ModuleDirs)
}
