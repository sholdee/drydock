package source

import (
	"context"
	"fmt"
	"sync/atomic"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/sholdee/drydock/internal/gittree"
)

// syncCacheSubmodules makes the worktree of the cache clone repo at root
// hold the submodule content of commit (gittree.SyncCacheSubmodules), with
// opts.Submodules. It reports whether a submodule fetch used the network.
func syncCacheSubmodules(ctx context.Context, repo *git.Repository, root, commit string, request GitRequest, opts GitOptions) (bool, error) {
	var network atomic.Bool
	err := gittree.SyncCacheSubmodules(ctx, gittree.CacheSubmoduleRequest{
		Root:               root,
		Repo:               repo,
		Commit:             commit,
		Enabled:            opts.Submodules,
		RemoteURL:          request.URL,
		Opener:             SubmoduleCacheOpener(request.URL, opts, &network),
		AllowFileTransport: opts.AllowFileSubmoduleTransport,
	})
	if err != nil {
		return network.Load(), fmt.Errorf("update submodules of repository %s: %s", RedactURL(request.URL), redactGitError(err, request.URL, opts.Credentials))
	}
	return network.Load(), nil
}

// SubmoduleCacheOpener opens the commit of a submodule from drydock's Git
// cache, as an entry for the submodule URL at that commit: the entry that
// a Git source of that URL and commit uses too. Unless opts forbid the
// network, it fetches a missing entry. Without the network, a missing
// entry fails as an offline cache miss. network, when not nil, is set when
// a fetch used the network. superURL is the URL of the top superproject:
// HTTP credentials go only to a submodule on the same host
// (submoduleCredentials).
func SubmoduleCacheOpener(superURL string, opts GitOptions, network *atomic.Bool) gittree.SubmoduleOpener {
	return gittree.SubmoduleOpenerFunc(func(ctx context.Context, ref gittree.SubmoduleRef) (*git.Repository, error) {
		request := GitRequest{URL: ref.URL, Revision: ref.Commit.String()}
		entryOpts := GitOptions{
			AllowNetwork:                opts.AllowNetwork,
			CacheDir:                    opts.CacheDir,
			Credentials:                 submoduleCredentials(opts.Credentials, superURL, ref.URL),
			LockEntry:                   opts.LockEntry,
			AllowFileSubmoduleTransport: opts.AllowFileSubmoduleTransport,
			RequireTransport:            ref.Transport,
		}
		if opts.LockEntry != nil {
			unlock := opts.LockEntry(request, entryOpts)
			defer unlock()
		}
		result, err := (DefaultGitAcquirer{}).Acquire(ctx, request, entryOpts)
		if err != nil {
			return nil, fmt.Errorf("submodule %q (%s at %s): %w", ref.Path, RedactURL(ref.URL), ref.Commit, err)
		}
		if result.Network && network != nil {
			network.Store(true)
		}
		return git.PlainOpen(result.Path)
	})
}

// submoduleCredentials returns the credentials drydock fetches a submodule
// with. The Argo CD repo-server runs `git submodule update` with the
// credentials of the superproject's repository (runCredentialedCmd,
// util/git/client.go:783-789): the same askpass username and password, and
// the same SSH key, for every submodule. drydock sends a username,
// password, or bearer token only to a submodule on the host and port of
// the superproject, and never from https to http, so a .gitmodules file
// cannot send them elsewhere or in clear text.
// SSH keys are kept for every host: SSH never sends the private key.
func submoduleCredentials(credentials GitCredentials, superURL, submoduleURL string) GitCredentials {
	if gittree.SameCredentialScope(superURL, submoduleURL) {
		return credentials
	}
	credentials.Username = ""
	credentials.Password = ""
	credentials.BearerToken = ""
	return credentials
}

// lfsPointersOf lists the Git LFS pointer files of commit in repo when
// opts.LFS is set (gittree.ScanLFSPointers).
func lfsPointersOf(ctx context.Context, repo *git.Repository, commit string, opts GitOptions) ([]gittree.LFSPointer, error) {
	if !opts.LFS {
		return nil, nil
	}
	object, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, err
	}
	tree, err := object.Tree()
	if err != nil {
		return nil, err
	}
	return gittree.ScanLFSPointers(ctx, tree)
}
