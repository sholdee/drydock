package app

import (
	"context"
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

// submoduleSnapshotCode identifies the warning for a snapshot of a Git ref
// that holds submodule content while reposerver.enable.git.submodule is
// off.
const submoduleSnapshotCode = "render.submodule-content-disabled"

// submoduleWorktreeCode identifies the warning for a source in a submodule
// that the working tree does not hold.
const submoduleWorktreeCode = "render.submodule-not-initialized"

// rootSubmodules describes the submodules of the tree a provider renders.
type rootSubmodules struct {
	root string
	// snapshotCheckedOut is the number of submodules whose content the
	// snapshot of a Git ref at root holds.
	snapshotCheckedOut int
	// worktree reports that root is a working tree, not a snapshot.
	worktree bool

	once  sync.Once
	empty []string
}

func newRootSubmodules(root string, request BuildRequest) *rootSubmodules {
	return &rootSubmodules{
		root:               root,
		snapshotCheckedOut: request.rootSnapshot.checkedOut,
		worktree:           strings.TrimSpace(request.rootRevision) == "",
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
			if module, ok := modules[link]; ok && strings.EqualFold(strings.TrimSpace(module.Update), "none") {
				continue
			}
			if entries, err := os.ReadDir(filepath.Join(s.root, filepath.FromSlash(link))); err != nil || len(entries) == 0 {
				s.empty = append(s.empty, link)
			}
		}
	})
	return s.empty
}

// submoduleRenderDiagnostics warns when the tree that source renders from
// holds other submodule content than the Argo CD repo-server's checkout:
//
//   - a snapshot of a Git ref always holds submodule content, because
//     drydock takes it before it reads argocd-cmd-params-cm; with
//     reposerver.enable.git.submodule off, the repo-server's checkout holds
//     each submodule as an empty directory;
//   - a working tree whose submodule is not initialized holds an empty
//     directory where the repo-server checks the content out. drydock
//     never writes to the working tree, so it renders it as it is.
func (p localProvider) submoduleRenderDiagnostics(ctx context.Context, source render.ResolvedSource) []diagnostic.Diagnostic {
	if p.submodules == nil || !p.isRepoRoot(source.RepoRoot) {
		return nil
	}
	if !p.gitSubmodules {
		if p.submodules.snapshotCheckedOut == 0 {
			return nil
		}
		return []diagnostic.Diagnostic{{
			Code:     submoduleSnapshotCode,
			Severity: diagnostic.SeverityWarning,
			Category: "render",
			Message:  "reposerver.enable.git.submodule is false, so the Argo CD repo-server renders each submodule as an empty directory, but drydock renders the submodule content of the Git ref snapshot, which it takes before it reads that setting",
		}}
	}
	if !p.submodules.worktree {
		return nil
	}
	link, ok := submoduleOfPath(p.submodules.emptySubmodules(ctx), source.Path)
	if !ok {
		return nil
	}
	return []diagnostic.Diagnostic{{
		Code:     submoduleWorktreeCode,
		Severity: diagnostic.SeverityWarning,
		Category: "render",
		Message:  fmt.Sprintf("source path %q is in submodule %q, which holds nothing in the working tree; the Argo CD repo-server checks out submodule content (reposerver.enable.git.submodule), so run git submodule update --init --recursive, or diff a Git ref with --ref", source.Path, link),
	}}
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
