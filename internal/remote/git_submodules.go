package remote

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/sholdee/drydock/internal/cache"

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

// submoduleStoreRevision is the revision part of the remote cache key of a
// submodule store, the one entry per submodule URL. A Git revision never
// holds a NUL, so no Kustomize ref shares the entry.
const submoduleStoreRevision = "\x00submodule-store"

// submoduleOpener opens the commit of a submodule from its store in the
// remote cache: one bare clone per submodule URL, which holds every commit
// drydock fetched for that URL (gittree.OpenSubmoduleStore). It fetches a
// missing commit unless drydock runs offline. HTTP credentials go only to a
// submodule in the credential scope of superURL.
func (acquirer DefaultAcquirer) submoduleOpener(superURL string, opts Options) gittree.SubmoduleOpener {
	return gittree.SubmoduleOpenerFunc(func(ctx context.Context, ref gittree.SubmoduleRef) (*git.Repository, error) {
		if ref.Transport == "git" {
			return nil, fmt.Errorf("submodule %q (%s): drydock does not fetch git:// URLs for remote Kustomize bases, which kustomize's git does; this is a recorded difference", ref.Path, RedactGitRepoURL(ref.URL))
		}
		credentials := submoduleGitCredentials(opts.GitCredentials, superURL, ref)
		repo, err := openRemoteSubmoduleStore(ctx, ref, opts, credentials)
		if err != nil {
			return nil, fmt.Errorf("submodule %q (%s at %s): %s", ref.Path, RedactGitRepoURL(ref.URL), ref.Commit, redactGitError(err, ref.URL, credentials))
		}
		return repo, nil
	})
}

// openRemoteSubmoduleStore opens the store of ref under the lock of its
// cache entry.
func openRemoteSubmoduleStore(ctx context.Context, ref gittree.SubmoduleRef, opts Options, credentials GitCredentials) (*git.Repository, error) {
	normalized, err := NormalizeGitRepoCacheURL(ref.URL)
	if err != nil {
		return nil, err
	}
	cacheDir, err := ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return nil, err
	}
	request := Request{Kind: RequestGitRepo, URL: ref.URL, RepoURL: ref.URL, Revision: submoduleStoreRevision}
	key, err := NewCacheKey(Request{Kind: RequestGitRepo, RepoURL: normalized, Revision: submoduleStoreRevision})
	if err != nil {
		return nil, err
	}
	path := cache.RemoteGitRepoPath(cacheDir, key)
	if err := rejectForbiddenCachePath(path, opts.ForbiddenRoots); err != nil {
		return nil, err
	}
	if opts.LockEntry != nil {
		unlock := opts.LockEntry(request, opts)
		defer unlock()
	}
	var auth transport.AuthMethod
	if !opts.Offline {
		if auth, _, err = gitAuthMethod(credentials, ref.URL); err != nil {
			return nil, err
		}
	}
	repo, fetched, err := gittree.OpenSubmoduleStore(ctx, gittree.StoreRequest{
		Path: path, URL: ref.URL, Commit: ref.Commit, Transport: ref.Transport, Auth: auth, AllowNetwork: !opts.Offline,
	})
	if err == nil && fetched {
		writeGitRepoMetadata(filepath.Dir(path), key, normalized, ref.Commit.String())
	}
	return repo, err
}

// submoduleGitCredentials returns the credentials of a submodule fetch: git
// runs kustomize's `git submodule update` with the credentials of its
// environment for every submodule, but drydock sends a username, password,
// or bearer token only to an http or https submodule on the host and port
// of the superproject, and never from https to http. Without them, the
// user and password that the resolved URL carried are used in that scope.
func submoduleGitCredentials(credentials GitCredentials, superURL string, ref gittree.SubmoduleRef) GitCredentials {
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

// cloneURLAndCredentials removes the userinfo of repoURL, so the remote
// cache entry stores a URL without a token (gittree.SplitURLUserinfo). The
// user and password become the credentials of an http or https URL unless
// explicit credentials are set.
func cloneURLAndCredentials(repoURL string, credentials GitCredentials) (string, GitCredentials) {
	clean, info, found := gittree.SplitURLUserinfo(repoURL)
	if !found {
		return repoURL, credentials
	}
	isHTTP := strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://")
	if isHTTP && credentials.Username == "" && credentials.Password == "" && credentials.BearerToken == "" {
		credentials.Username = info.Username
		credentials.Password = info.Password
	}
	return clean, credentials
}
