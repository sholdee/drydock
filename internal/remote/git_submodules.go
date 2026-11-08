package remote

import (
	"context"
	"fmt"
	"strings"

	git "github.com/go-git/go-git/v5"

	"github.com/sholdee/drydock/internal/gittree"
)

// syncGitSubmodules makes the worktree of the remote Git cache entry of
// result hold the submodule content of its commit, as `git submodule
// update --init --recursive` leaves kustomize's clone, when request asks
// for it, and an empty directory for each gitlink otherwise.
func (acquirer DefaultAcquirer) syncGitSubmodules(ctx context.Context, result Result, request Request, opts Options) error {
	repo, err := git.PlainOpen(result.Path)
	if err != nil {
		return fmt.Errorf("open remote Git repository %s: %s", RedactGitRepoURL(gitRequestURL(request)), redactGitError(err, gitRequestURL(request), opts.GitCredentials))
	}
	repoURL := gitRequestURL(request)
	err = gittree.SyncCacheSubmodules(ctx, gittree.CacheSubmoduleRequest{
		Root:               result.Path,
		Repo:               repo,
		Commit:             result.Revision,
		Enabled:            request.Submodules,
		RemoteURL:          repoURL,
		Opener:             acquirer.submoduleOpener(repoURL, opts),
		AllowFileTransport: opts.AllowFileSubmoduleTransport,
	})
	if err != nil {
		return fmt.Errorf("update submodules of remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, opts.GitCredentials))
	}
	return nil
}

func gitRequestURL(request Request) string {
	if repoURL := strings.TrimSpace(request.RepoURL); repoURL != "" {
		return repoURL
	}
	return strings.TrimSpace(request.URL)
}

// submoduleOpener opens the commit of a submodule from the remote Git
// cache, as the entry of the submodule URL at that commit, and fetches a
// missing entry unless drydock runs offline. HTTP credentials go only to a
// submodule on the host of superURL.
func (acquirer DefaultAcquirer) submoduleOpener(superURL string, opts Options) gittree.SubmoduleOpener {
	return gittree.SubmoduleOpenerFunc(func(ctx context.Context, ref gittree.SubmoduleRef) (*git.Repository, error) {
		request := Request{Kind: RequestGitRepo, URL: ref.URL, RepoURL: ref.URL, Revision: ref.Commit.String()}
		entryOpts := opts
		entryOpts.Refresh = false
		entryOpts.GitCredentials = submoduleGitCredentials(opts.GitCredentials, superURL, ref.URL)
		if opts.LockEntry != nil {
			unlock := opts.LockEntry(request, entryOpts)
			defer unlock()
		}
		result, err := acquirer.acquireGitRepo(ctx, request, entryOpts)
		if err != nil {
			return nil, fmt.Errorf("submodule %q (%s at %s): %w", ref.Path, RedactGitRepoURL(ref.URL), ref.Commit, err)
		}
		if result.Release != nil {
			defer result.Release()
		}
		return git.PlainOpen(result.Path)
	})
}

// submoduleGitCredentials returns the credentials of a submodule fetch: git
// runs kustomize's `git submodule update` with the credentials of its
// environment for every submodule, but drydock sends a username, password,
// or bearer token only to a submodule on the host of the superproject.
func submoduleGitCredentials(credentials GitCredentials, superURL, submoduleURL string) GitCredentials {
	superHost := gittree.SubmoduleURLHost(superURL)
	if superHost != "" && superHost == gittree.SubmoduleURLHost(submoduleURL) {
		return credentials
	}
	credentials.Username = ""
	credentials.Password = ""
	credentials.BearerToken = ""
	return credentials
}
