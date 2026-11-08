package remote

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"

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
		if ref.Transport == "git" {
			return nil, fmt.Errorf("submodule %q (%s): drydock does not fetch git:// URLs for remote Kustomize bases, which kustomize's git does; this is a recorded difference", ref.Path, RedactGitRepoURL(ref.URL))
		}
		request := Request{Kind: RequestGitRepo, URL: ref.URL, RepoURL: ref.URL, Revision: ref.Commit.String()}
		entryOpts := opts
		entryOpts.Refresh = false
		entryOpts.RequireTransport = ref.Transport
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
// or bearer token only to a submodule on the host and port of the
// superproject, and never from https to http.
func submoduleGitCredentials(credentials GitCredentials, superURL, submoduleURL string) GitCredentials {
	if gittree.SameCredentialScope(superURL, submoduleURL) {
		return credentials
	}
	credentials.Username = ""
	credentials.Password = ""
	credentials.BearerToken = ""
	return credentials
}

// checkoutOrFetchCommit checks revision out, and when it is a full commit
// SHA that no fetched branch or tag holds, fetches that commit into
// refs/drydock/<sha> and tries again, as `git submodule update` fetches a
// pinned commit. It never fetches offline.
func checkoutOrFetchCommit(ctx context.Context, repo *git.Repository, worktree *git.Worktree, repoURL, revision string, opts Options) (string, error) {
	checkedOut, err := checkoutGitRevision(repo, worktree, revision)
	sha := strings.ToLower(strings.TrimSpace(revision))
	if err == nil || opts.Offline || !fullCommitSHA.MatchString(sha) {
		return checkedOut, err
	}
	auth, _, authErr := gitAuthMethod(opts.GitCredentials, repoURL)
	if authErr != nil {
		return "", err
	}
	if checkErr := checkFetchTransport(repo, repoURL, opts); checkErr != nil {
		return "", checkErr
	}
	fetchErr := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec(sha + ":refs/drydock/" + sha)},
	})
	if fetchErr != nil && !errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
		return "", err
	}
	return checkoutGitRevision(repo, worktree, revision)
}

var fullCommitSHA = regexp.MustCompile("^[0-9a-f]{40}$")

// checkFetchTransport fails unless go-git fetches repoURL, and every URL
// of the origin remote of repo when repo is set, over
// opts.RequireTransport.
func checkFetchTransport(repo *git.Repository, repoURL string, opts Options) error {
	if opts.RequireTransport == "" {
		return nil
	}
	urls := []string{repoURL}
	if repo != nil {
		remote, err := repo.Remote("origin")
		if err != nil {
			return err
		}
		urls = append(urls, remote.Config().URLs...)
	}
	for _, url := range urls {
		if err := gittree.CheckEndpointTransport(url, opts.RequireTransport); err != nil {
			return fmt.Errorf("remote Git repository %s: %w", RedactGitRepoURL(repoURL), err)
		}
	}
	return nil
}
