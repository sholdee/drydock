package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/change"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/render"
)

// sourceHydratorCode identifies the warning that a diff changes the dry
// source of an Application with spec.sourceHydrator. The Argo CD v3.6.1
// controller compares such an Application against its sync source only: the
// hydrated branch and path (ApplicationSpec.GetSources,
// pkg/apis/application/v1alpha1/types.go:278). drydock renders the same
// source at the same branch (resolveHydratorSyncSource). It cannot run the
// hydrator offline, so a change to the dry source shows only after the
// hydrator commits it to the sync branch. Only a diff gives the warning
// (hydratorDryChangeDiagnostics), and only when the dry source is in the
// local repository and the diff changes a file under its path. The code is
// strict-exempt (strictExemptDiagnostic): the render is the one Argo CD
// compares, so --strict has nothing to protect.
const sourceHydratorCode = "source.hydrator-sync-source"

// diffChangedFiles is the set of files that a diff changes, relative to the
// repository root. unknown is not empty when drydock could not list the
// files, and says why.
type diffChangedFiles struct {
	paths   []string
	unknown string
}

// hydratorDiffSide is one side of a diff for the dry source check: the
// build request of the side and the Applications that the side rendered.
type hydratorDiffSide struct {
	request      BuildRequest
	applications []argoappv1.Application
}

// hydratorDryChangeDiagnostics returns one sourceHydratorCode warning for
// each Application with spec.sourceHydrator that a diff renders, when its
// dry source is in the local repository on that side
// (hydratorDryIsLocalTree) and the diff changes a file under the dry source
// path. A dry source in another repository gives no warning, because its
// changes are not part of the diff. changedFiles is called at most once,
// and only when an Application has a local dry source. When it cannot list
// the files, the warning stays, so drydock never hides a dry source change.
func hydratorDryChangeDiagnostics(sides []hydratorDiffSide, changedFiles func() diffChangedFiles) []diagnostic.Diagnostic {
	var out []diagnostic.Diagnostic
	warned := map[string]struct{}{}
	var files *diffChangedFiles
	for _, side := range sides {
		var tree *selectionTree
		for _, application := range side.applications {
			hydrator := application.Spec.SourceHydrator
			if hydrator == nil {
				continue
			}
			if _, ok := warned[applicationDisplayName(application)]; ok {
				continue
			}
			if tree == nil {
				tree = newSelectionTree(side.request.Path, side.request.RepoMaps, side.request.selfRepo)
			}
			if !hydratorDryIsLocalTree(tree, hydrator.DrySource) {
				continue
			}
			if files == nil {
				listed := changedFiles()
				files = &listed
			}
			if diag, ok := hydratorDryChangeDiagnostic(application, *files); ok {
				warned[applicationDisplayName(application)] = struct{}{}
				out = append(out, diag)
			}
		}
	}
	return out
}

// hydratorDryIsLocalTree reports whether a dry source is the repository
// under analysis at a revision that tracks the local tree, by the self-repo
// rule that every source of the local repository follows. It mirrors
// hydratorSyncRendersFromRoot: a --repo-map to this tree names the local
// repository, and a map to another directory never does.
func hydratorDryIsLocalTree(tree *selectionTree, dry argoappv1.DrySource) bool {
	if toRoot, mapped := tree.repoMapped(dry.RepoURL); mapped {
		return toRoot && tree.selfRepo.tracksRevision(dry.TargetRevision)
	}
	return tree.selfRepo.matches(dry.RepoURL, dry.TargetRevision)
}

// hydratorDryChangeDiagnostic returns the warning for one Application when
// files lists a file under its dry source path, or when files is unknown.
// The dry source options (directory, helm, kustomize) do not matter: any
// change under the path can change what the hydrator commits.
func hydratorDryChangeDiagnostic(application argoappv1.Application, files diffChangedFiles) (diagnostic.Diagnostic, bool) {
	hydrator := application.Spec.SourceHydrator
	syncSource := hydrator.GetSyncSource()
	dryPath := normalizeSelectPath(hydrator.DrySource.Path)
	shownPath := dryPath
	if shownPath == "" {
		shownPath = "."
	}
	diag := diagnostic.Diagnostic{
		Code:       sourceHydratorCode,
		Severity:   diagnostic.SeverityWarning,
		Category:   "source",
		Provenance: diagnostic.Provenance{Path: applicationDisplayName(application)},
	}
	if files.unknown != "" {
		diag.Message = fmt.Sprintf("Application %s: drydock could not list the files that this diff changes (%s), so it cannot tell whether the dry source path %q changed; drydock does not run the source hydrator and rendered the sync source branch %q, path %q",
			applicationDisplayName(application), files.unknown, shownPath, syncSource.TargetRevision, syncSource.Path)
		return diag, true
	}
	for _, changed := range files.paths {
		if pathIntersects(dryPath, normalizeSelectPath(changed)) {
			diag.Message = fmt.Sprintf("Application %s: this diff changes the dry source path %q, but drydock does not run the source hydrator; it rendered the sync source branch %q, path %q, so the change shows only after the hydrator commits it",
				applicationDisplayName(application), shownPath, syncSource.TargetRevision, syncSource.Path)
			return diag, true
		}
	}
	return diagnostic.Diagnostic{}, false
}

// diffChangedFiles lists the files that the diff changes: the changed-only
// set when the diff already has it, the Git changes between --ref-orig and
// --ref (or the working tree), or else a compare of the two side trees, as
// changed-only does for a path diff (filteredChangedOnlyPaths). The list is
// not filtered by the changed-only include and ignore globs.
func (request DiffRequest) diffChangedFiles(ctx context.Context) diffChangedFiles {
	if request.changedPaths != nil {
		return diffChangedFiles{paths: request.changedPaths}
	}
	paths, ok, err := gitRefChangedPaths(ctx, request, request.Repo)
	if err == nil && !ok {
		paths, err = change.Detect(request.LeftPath, request.RightPath)
	}
	if err != nil {
		return diffChangedFiles{unknown: err.Error()}
	}
	return diffChangedFiles{paths: paths}
}

// hydratorDryChangeDiagnostics returns the dry source warnings for the two
// rendered sides of the diff (see the function of the same name).
func (request DiffRequest) hydratorDryChangeDiagnostics(ctx context.Context, leftRequest, rightRequest BuildRequest, leftBuild, rightBuild BuildResult) []diagnostic.Diagnostic {
	return hydratorDryChangeDiagnostics([]hydratorDiffSide{
		{request: rightRequest, applications: rightBuild.Applications},
		{request: leftRequest, applications: leftBuild.Applications},
	}, func() diffChangedFiles {
		return request.diffChangedFiles(ctx)
	})
}

// resolveHydratorSyncSource resolves the tree of a source hydrator sync
// source. Argo CD reads syncSource.path at syncSource.targetBranch, so
// drydock reads that branch too, never the local tree only because the
// path exists there:
//
//   - A --repo-map target renders as it is when the branch is the one
//     checked out there. When the target is the top of a Git repository
//     with another branch checked out, drydock renders a snapshot of the
//     branch. Any other target fails, because drydock cannot read the
//     branch from it.
//   - The local repository renders from the tree of this side when the
//     branch is one that the tree tracks, by the rule that every source of
//     the local repository follows (selfRepoMatcher.matches): HEAD, a
//     --ref or --ref-orig name of a diff, or the default branch of the
//     repository. Both sides of a diff then read their own tree, so a
//     change to the hydrated path shows in the diff.
//   - Any other source comes from the Git cache or a fetch, and --offline
//     fails on a cache miss.
func (p localProvider) resolveHydratorSyncSource(ctx context.Context, source render.ResolvedSource) (string, SourceIdentity, error) {
	branch := strings.TrimSpace(source.TargetRevision)
	if p.hydratorSyncIsLocalTree(source) {
		p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionLocal, Target: source.RepoURL, Revision: source.TargetRevision})
		return p.repoRoot, p.rootIdentity, nil
	}
	selfMapped := p.hydratorSelfMapped(source.RepoURL)
	if p.sourceResolver != nil && !selfMapped {
		if mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL); ok {
			return p.resolveMappedHydratorBranch(ctx, source, mappedPath, branch)
		}
		if _, err := p.sourceResolver.Resolve(source.RepoURL, source.TargetRevision); err != nil {
			return "", SourceIdentity{}, fmt.Errorf("source hydrator sync branch %q: %w", branch, err)
		}
	}
	root, identity, err := p.acquireGitSource(ctx, source)
	if err != nil {
		return "", SourceIdentity{}, fmt.Errorf("source hydrator sync branch %q: %w", branch, err)
	}
	return root, identity, nil
}

// hydratorSelfMapped reports a --repo-map of the sync repository to the tree
// of this side: a map to the checkout of a build, or a map to the
// repository under a --ref diff, which the diff rewrites to each side's
// snapshot (DiffRequest.buildRequest). Such a map names the repository
// under analysis, so the sync source then behaves as if it had no map.
func (p localProvider) hydratorSelfMapped(repoURL string) bool {
	if p.sourceResolver == nil {
		return false
	}
	mappedPath, ok := p.sourceResolver.MappedPath(repoURL)
	return ok && sameLocalPath(mappedPath, p.repoRoot)
}

// hydratorSyncIsLocalTree reports whether a source hydrator sync source
// renders from the local tree: its repository is the repository under
// analysis, by its remote or by a --repo-map to this tree, and the
// self-repo rule tracks its branch (resolveHydratorSyncSource).
func (p localProvider) hydratorSyncIsLocalTree(source render.ResolvedSource) bool {
	branch := strings.TrimSpace(source.TargetRevision)
	if branch == "" {
		return false
	}
	if p.sourceResolver != nil {
		if _, mapped := p.sourceResolver.MappedPath(source.RepoURL); mapped {
			// A map to another tree always wins, as for every source.
			return p.hydratorSelfMapped(source.RepoURL) && selfRepoMatcher{revisions: p.selfRepoRevisions}.tracksRevision(branch)
		}
	}
	return p.isSelfRepoRef(source.RepoURL, branch)
}

// hydratorSnapshot is one snapshot of a branch of a --repo-map Git
// repository, made once per run.
type hydratorSnapshot struct {
	once     sync.Once
	path     string
	revision string
	err      error
}

func (p localProvider) resolveMappedHydratorBranch(ctx context.Context, source render.ResolvedSource, mappedPath, branch string) (string, SourceIdentity, error) {
	abs, err := filepath.Abs(mappedPath)
	if err != nil {
		return "", SourceIdentity{}, err
	}
	p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionMapped, Target: source.RepoURL, Revision: source.TargetRevision})
	if branch != "" && gitref.CurrentBranch(abs) == branch {
		return abs, SourceIdentity{}, nil
	}
	if gitref.HeadRevision(abs) == "" {
		return "", SourceIdentity{}, fmt.Errorf("source hydrator sync branch %q: --repo-map target %q is not the top of a Git repository, so drydock cannot read the branch from it", branch, abs)
	}
	snapshot := p.hydratorBranchSnapshot(ctx, abs, branch)
	if snapshot.err != nil {
		return "", SourceIdentity{}, fmt.Errorf("source hydrator sync branch %q: %w", branch, snapshot.err)
	}
	return snapshot.path, SourceIdentity{}, nil
}

// hydratorBranchSnapshot snapshots branch of the Git repository at repo
// into the run's snapshot directory, once per run.
func (p localProvider) hydratorBranchSnapshot(ctx context.Context, repo, branch string) *hydratorSnapshot {
	memo := p.hydratorSnapshots
	if memo == nil {
		memo = &sync.Map{}
	}
	value, _ := memo.LoadOrStore(repo+"\x00"+branch, &hydratorSnapshot{})
	snapshot, ok := value.(*hydratorSnapshot)
	if !ok {
		return &hydratorSnapshot{err: fmt.Errorf("snapshot memo holds %T", value)}
	}
	snapshot.once.Do(func() {
		root := strings.TrimSpace(p.acquisition.SnapshotRoot)
		if root == "" {
			snapshot.err = fmt.Errorf("no snapshot directory for the branch of %q", repo)
			return
		}
		// A local branch wins. A clone that never checked the branch out
		// holds it only as a remote-tracking branch of origin.
		var result gitref.Result
		var err error
		for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch} {
			result, err = gitref.Snapshot(ctx, gitref.Request{
				Repo:                    repo,
				Ref:                     ref,
				ForbiddenRoots:          []string{p.repoRoot, repo},
				KeepOutOfBoundsSymlinks: true,
			})
			if err == nil {
				break
			}
		}
		if err != nil {
			snapshot.err = err
			return
		}
		target, err := os.MkdirTemp(root, "hydrator-*")
		if err == nil {
			err = os.Remove(target)
		}
		if err == nil {
			err = os.Rename(result.Path, target)
		}
		if err != nil {
			_ = result.Cleanup()
			snapshot.err = err
			return
		}
		snapshot.path = target
		snapshot.revision = result.Revision
	})
	return snapshot
}

// hydratorSnapshotRevision returns the commit of the branch snapshot that a
// --repo-map source hydrator sync source renders, for ARGOCD_APP_REVISION.
func (p localProvider) hydratorSnapshotRevision(source render.ResolvedSource, root string) (string, bool) {
	if !source.HydratorSync || p.hydratorSnapshots == nil || p.sourceResolver == nil {
		return "", false
	}
	mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL)
	if !ok {
		return "", false
	}
	abs, err := filepath.Abs(mappedPath)
	if err != nil {
		return "", false
	}
	value, ok := p.hydratorSnapshots.Load(abs + "\x00" + strings.TrimSpace(source.TargetRevision))
	if !ok {
		return "", false
	}
	snapshot, ok := value.(*hydratorSnapshot)
	if !ok || snapshot.err != nil || snapshot.path != root {
		return "", false
	}
	return snapshot.revision, true
}
