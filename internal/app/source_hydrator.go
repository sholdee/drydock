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
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// sourceHydratorCode identifies the advisory for an Application with
// spec.sourceHydrator. The Argo CD v3.6.1 controller compares such an
// Application against its sync source only: the hydrated branch and path
// (ApplicationSpec.GetSources, pkg/apis/application/v1alpha1/types.go:278).
// drydock renders the same source at the same branch
// (resolveHydratorSyncSource), so its output matches the comparison whether
// or not the advisory fires. It cannot run the hydrator offline, so
// a change to the dry source shows only after the hydrator commits it to
// the sync branch. The code is strict-exempt (strictExemptDiagnostic): the
// render is the one Argo CD compares, so --strict has nothing to protect.
const sourceHydratorCode = "source.hydrator-sync-source"

// sourceHydratorDiagnostic reports the advisory for an Application with
// spec.sourceHydrator.
func sourceHydratorDiagnostic(application argoappv1.Application) (diagnostic.Diagnostic, bool) {
	hydrator := application.Spec.SourceHydrator
	if hydrator == nil {
		return diagnostic.Diagnostic{}, false
	}
	syncSource := hydrator.GetSyncSource()
	return diagnostic.Diagnostic{
		Code:     sourceHydratorCode,
		Severity: diagnostic.SeverityWarning,
		Category: "source",
		Message: fmt.Sprintf("Application %s uses spec.sourceHydrator: drydock renders the sync source %s at branch %q, path %q, as Argo CD compares it, and ignores spec.source and spec.sources; it reads the branch from the local tree only when the tree holds that branch, else from a --repo-map Git repository, the Git cache, or a fetch; drydock does not run the hydrator, so changes to the dry source %q show only after the hydrator commits them",
			applicationDisplayName(application), remote.RedactGitRepoURL(syncSource.RepoURL), syncSource.TargetRevision, syncSource.Path, hydrator.DrySource.Path),
		Provenance: diagnostic.Provenance{Path: applicationDisplayName(application)},
	}, true
}

// hydratorBranchForRequest is the branch whose content the tree of request
// holds (localProvider.hydratorBranch): the --ref or --ref-orig name of a
// diff side that renders a Git ref, else the branch checked out at root.
func hydratorBranchForRequest(root string, request BuildRequest) string {
	if strings.TrimSpace(request.rootRevision) != "" {
		name := strings.TrimSpace(request.rootRefName)
		if sourcepkg.IsCommitSHA(name) {
			return ""
		}
		return name
	}
	return gitref.CurrentBranch(root)
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
//   - The local repository renders from the local tree when the branch is
//     the one the tree holds (localProvider.hydratorBranch).
//   - Any other source comes from the Git cache or a fetch, and --offline
//     fails on a cache miss.
func (p localProvider) resolveHydratorSyncSource(ctx context.Context, source render.ResolvedSource) (string, SourceIdentity, error) {
	branch := strings.TrimSpace(source.TargetRevision)
	if p.sourceResolver != nil {
		if mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL); ok {
			return p.resolveMappedHydratorBranch(ctx, source, mappedPath, branch)
		}
	}
	if p.hydratorSyncIsLocalTree(source) {
		p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionLocal, Target: source.RepoURL, Revision: source.TargetRevision})
		return p.repoRoot, p.rootIdentity, nil
	}
	if p.sourceResolver != nil {
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

// hydratorSyncIsLocalTree reports whether a source hydrator sync source
// renders from the local tree: its repository is the local repository and
// its branch is the one the tree holds.
func (p localProvider) hydratorSyncIsLocalTree(source render.ResolvedSource) bool {
	branch := strings.TrimSpace(source.TargetRevision)
	if branch == "" || branch != p.hydratorBranch || strings.TrimSpace(source.RepoURL) == "" {
		return false
	}
	_, ok := p.selfRepoURLKeys[sourcepkg.CanonicalGitURLKey(source.RepoURL)]
	return ok
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
	snapshot := value.(*hydratorSnapshot)
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
	snapshot := value.(*hydratorSnapshot)
	if snapshot.err != nil || snapshot.path != root {
		return "", false
	}
	return snapshot.revision, true
}
