package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/render"
)

// submoduleWorktreeCode identifies the warning for a source in a submodule
// that the working tree does not hold.
const submoduleWorktreeCode = "render.submodule-not-initialized"

// rootSubmodules describes the submodules of the tree a provider renders.
type rootSubmodules struct {
	root string
	// fill is the submodule checkout of the snapshot of a Git ref at root,
	// or nil.
	fill *snapshotFill
	// worktree reports that root is a working tree, not a snapshot.
	worktree bool

	once  sync.Once
	empty []string
}

func newRootSubmodules(root string, request BuildRequest, fill *snapshotFill) *rootSubmodules {
	return &rootSubmodules{
		root:     root,
		fill:     fill,
		worktree: strings.TrimSpace(request.rootRevision) == "",
	}
}

// emptySubmodules lists the gitlinks of the index of a working tree that
// hold nothing on disk while Argo CD checks their content out: root has a
// .gitmodules file that does not skip them with update = none.
func (s *rootSubmodules) emptySubmodules(ctx context.Context) []string {
	s.once.Do(func() {
		data, err := pathsafety.ReadRegularFile(filepath.Join(s.root, ".gitmodules"))
		if err != nil {
			return
		}
		modules, err := gittree.ParseModules(data)
		if err != nil {
			modules = map[string]gittree.Module{}
		}
		for _, link := range gitref.IndexGitlinks(ctx, s.root) {
			if module, ok := modules[link]; ok && gittree.ModuleSkipped(module) {
				continue
			}
			if entries, err := os.ReadDir(filepath.Join(s.root, filepath.FromSlash(link))); err != nil || len(entries) == 0 {
				s.empty = append(s.empty, link)
			}
		}
	})
	return s.empty
}

// submoduleRenderDiagnostics checks the submodules of the tree that
// source renders from against the Argo CD repo-server's checkout:
//
//   - a snapshot of a Git ref whose submodule configuration git refuses
//     fails every source, as the repo-server's checkout fails;
//   - a source whose path is in a submodule of a snapshot that drydock
//     could not check out (offline, or unreachable) fails with the reason;
//   - a working tree whose submodule is not initialized holds an empty
//     directory where the repo-server checks the content out. drydock
//     never writes to the working tree, so it renders it as it is and
//     warns.
func (p localProvider) submoduleRenderDiagnostics(ctx context.Context, source render.ResolvedSource) ([]diagnostic.Diagnostic, error) {
	if p.submodules == nil || !p.isRepoRoot(source.RepoRoot) {
		return nil, nil
	}
	if fill := p.submodules.fill; fill != nil {
		if err := fill.checkState(p.gitSubmodules); err != nil {
			return nil, err
		}
		if !p.gitSubmodules {
			return nil, nil
		}
		return nil, fill.sourceError(source.Path)
	}
	if !p.gitSubmodules {
		return nil, nil
	}
	if !p.submodules.worktree {
		return nil, nil
	}
	link, ok := submoduleOfPath(p.submodules.emptySubmodules(ctx), source.Path)
	if !ok {
		return nil, nil
	}
	return []diagnostic.Diagnostic{{
		Code:     submoduleWorktreeCode,
		Severity: diagnostic.SeverityWarning,
		Category: "render",
		Message:  fmt.Sprintf("source path %q is in submodule %q, which holds nothing in the working tree; the Argo CD repo-server checks out submodule content (reposerver.enable.git.submodule), so run git submodule update --init --recursive, or diff a Git ref with --ref", source.Path, link),
	}}, nil
}

// sourceError fails a source of a snapshot whose submodules drydock could
// not check out: every source when git refuses the configuration, and a
// source whose path holds a missing submodule or is in one, since a
// recursive or plain directory source would otherwise drop what the
// submodule holds without a word.
func (f *snapshotFill) sourceError(sourcePath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return fmt.Errorf("check out the submodules of the Git ref snapshot: %w", f.err)
	}
	for _, missing := range f.result.Missing {
		if pathsOverlap(sourcePath, missing.Path) {
			return missingSubmoduleError(missing)
		}
	}
	return nil
}

// pathsOverlap reports whether the source path rel holds the slash path
// link, or is link, or is below it.
func pathsOverlap(rel, link string) bool {
	clean := path.Clean(filepath.ToSlash(rel))
	if clean == "." || clean == "" {
		return true
	}
	return clean == link || strings.HasPrefix(link, clean+"/") || strings.HasPrefix(clean, link+"/")
}

// renderError adds the missing submodules of the snapshot to the error of a
// source that failed: its inputs can be in one of them.
func (f *snapshotFill) renderError(err error) error {
	if f == nil || err == nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.filled || len(f.result.Missing) == 0 {
		return err
	}
	reasons := make([]error, 0, len(f.result.Missing))
	for _, missing := range f.result.Missing {
		reasons = append(reasons, missingSubmoduleError(missing))
	}
	return fmt.Errorf("%w; the snapshot lacks submodule content: %w", err, errors.Join(reasons...))
}

func missingSubmoduleError(missing gittree.MissingSubmodule) error {
	return fmt.Errorf("submodule %q could not be checked out: %w", missing.Path, missing.Err)
}

// submoduleOfPath returns the gitlink of links that holds the source path
// rel, or false.
func submoduleOfPath(links []string, rel string) (string, bool) {
	clean := path.Clean(filepath.ToSlash(rel))
	for _, link := range links {
		if clean == link || strings.HasPrefix(clean, link+"/") {
			return link, true
		}
	}
	return "", false
}
