package source

import (
	"context"
	"fmt"
	"sync/atomic"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/sholdee/drydock/internal/cache"

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

// SubmoduleStoreRevision is the revision part of the Git cache key of a
// submodule store, the one cache entry per submodule URL. A Git revision
// never holds a NUL, so no source shares the entry.
const SubmoduleStoreRevision = "\x00submodule-store"

// SubmoduleCacheOpener opens the commit of a submodule from its store in
// drydock's Git cache: one bare clone per submodule URL, which holds every
// commit drydock fetched for that URL (gittree.OpenSubmoduleStore). Unless
// opts forbid the network, it fetches a missing commit. Without the
// network, a missing commit fails as an offline cache miss. network, when
// not nil, is set when a fetch used the network. superURL is the URL of
// the top superproject: HTTP credentials go only to a submodule in its
// credential scope (submoduleCredentials).
func SubmoduleCacheOpener(superURL string, opts GitOptions, network *atomic.Bool) gittree.SubmoduleOpener {
	return gittree.SubmoduleOpenerFunc(func(ctx context.Context, ref gittree.SubmoduleRef) (*git.Repository, error) {
		credentials := submoduleCredentials(opts.Credentials, superURL, ref)
		repo, fetched, err := openSubmoduleStore(ctx, ref, opts, credentials)
		if fetched && network != nil {
			network.Store(true)
		}
		if err != nil {
			return nil, fmt.Errorf("submodule %q (%s at %s): %s", ref.Path, RedactURL(ref.URL), ref.Commit, redactGitError(err, ref.URL, credentials))
		}
		return repo, nil
	})
}

// openSubmoduleStore opens the store of ref under the lock of its cache
// entry.
func openSubmoduleStore(ctx context.Context, ref gittree.SubmoduleRef, opts GitOptions, credentials GitCredentials) (*git.Repository, bool, error) {
	cacheDir, err := gitCacheDir(opts.CacheDir)
	if err != nil {
		return nil, false, err
	}
	request := GitRequest{URL: ref.URL, Revision: SubmoduleStoreRevision}
	if opts.LockEntry != nil {
		unlock := opts.LockEntry(request, opts)
		defer unlock()
	}
	key := GitCacheKey(ref.URL, SubmoduleStoreRevision)
	path := cache.GitEntryPath(cacheDir, key)
	var auth transport.AuthMethod
	if opts.AllowNetwork {
		if auth, _, err = gitAuthMethod(credentials, ref.URL); err != nil {
			return nil, false, err
		}
	}
	repo, fetched, err := gittree.OpenSubmoduleStore(ctx, gittree.StoreRequest{
		Path: path, URL: ref.URL, Commit: ref.Commit, Transport: ref.Transport, Auth: auth, AllowNetwork: opts.AllowNetwork,
	})
	if err != nil {
		return nil, fetched, err
	}
	if fetched {
		writeGitMetadata(path, key, GitRequest{URL: ref.URL}, ref.Commit.String())
	}
	return repo, fetched, nil
}

// submoduleCredentials returns the credentials drydock fetches a submodule
// with. The Argo CD repo-server runs `git submodule update` with the
// credentials of the superproject's repository (runCredentialedCmd,
// util/git/client.go:783-789): the same askpass username and password, and
// the same SSH key, for every submodule. drydock sends a username,
// password, or bearer token only to an http or https submodule on the host
// and port of the superproject, and never from https to http, so a
// .gitmodules file cannot send them elsewhere or in clear text. Without
// such credentials, the user and password that the resolved URL carried
// (ref.Userinfo) are used within the same scope. SSH keys are kept for
// every host: SSH never sends the private key.
func submoduleCredentials(credentials GitCredentials, superURL string, ref gittree.SubmoduleRef) GitCredentials {
	httpScope := (ref.Transport == "http" || ref.Transport == "https") && gittree.SameCredentialScope(superURL, ref.URL)
	if !httpScope {
		credentials.Username = ""
		credentials.Password = ""
		credentials.BearerToken = ""
		return credentials
	}
	if credentials.Username == "" && credentials.Password == "" && credentials.BearerToken == "" {
		credentials.Username = ref.Userinfo.Username
		credentials.Password = ref.Userinfo.Password
	}
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
