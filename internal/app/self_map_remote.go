package app

import (
	"context"

	"github.com/sholdee/drydock/internal/remote"
)

// selfMapRemote wraps the remote acquirer so self-referential kustomize git
// bases (git::<ownURL>//dir?ref=HEAD) resolve to the active diff side root
// instead of the shared remote worktree. Returns the delegate unchanged when
// no self keys are configured — provably dead outside diffs.
func (p localProvider) selfMapRemote(delegate remote.Acquirer) remote.Acquirer {
	if len(p.selfRepoURLKeys) == 0 {
		return delegate
	}
	return selfMapRemoteAcquirer{delegate: delegate, p: p}
}

type selfMapRemoteAcquirer struct {
	delegate remote.Acquirer
	p        localProvider
}

func (a selfMapRemoteAcquirer) Acquire(ctx context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	if request.Kind == remote.RequestGitRepo && a.p.isSelfRepoRef(request.RepoURL, request.Revision) {
		// Truthful result: FromCache=true makes recordRemoteCacheEvent emit
		// Network=false and Action=hit — no phantom fetch. Revision carries
		// the side snapshot SHA when known. Pin-stability is unchanged:
		// acquisitionsPinStable gates RemoteGit on RequestedRevision, which
		// stays symbolic, so such apps remain persistent-cache-ineligible
		// exactly as today.
		return remote.Result{
			Path:      a.p.repoRoot,
			URL:       request.URL,
			Revision:  a.p.rootIdentity.Revision, // side SHA; may be "" for path diffs
			FromCache: true,
			Release:   func() {}, // idempotent no-op; side root is command-lifetime, read-only
		}, nil
	}
	return a.delegate.Acquire(ctx, request, opts)
}
