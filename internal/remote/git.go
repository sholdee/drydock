package remote

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/giturl"
)

// acquireGitRepo checks the Git repository of request out in the remote
// cache, with the content of its submodules when request asks for it.
func (acquirer DefaultAcquirer) acquireGitRepo(ctx context.Context, request Request, opts Options) (Result, error) {
	result, err := acquirer.checkoutGitRepo(ctx, request, opts)
	if err != nil {
		return result, err
	}
	if err := acquirer.syncGitSubmodules(ctx, result, request, opts); err != nil {
		if result.Release != nil {
			result.Release()
		}
		return Result{}, err
	}
	return result, nil
}

//nolint:gocyclo // Git acquisition has distinct cache hit, refresh, offline, auth, clone, and checkout branches.
func (acquirer DefaultAcquirer) checkoutGitRepo(ctx context.Context, request Request, opts Options) (Result, error) {
	repoURL := strings.TrimSpace(request.RepoURL)
	if repoURL == "" {
		repoURL = strings.TrimSpace(request.URL)
	}
	normalizedURL, err := NormalizeGitRepoCacheURL(repoURL)
	if err != nil {
		return Result{}, err
	}
	if err := rejectLocalGitTransport(repoURL, opts.AllowFileTransport); err != nil {
		return Result{}, err
	}
	cacheDir, err := ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return Result{}, err
	}
	key, err := NewCacheKey(Request{Kind: RequestGitRepo, RepoURL: normalizedURL, Revision: request.Revision})
	if err != nil {
		return Result{}, err
	}
	cachePath := cache.RemoteGitRepoPath(cacheDir, key)
	if err := rejectForbiddenCachePath(cachePath, opts.ForbiddenRoots); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// The cache key already ignores userinfo. The clone must not store it.
	repoURL, opts.GitCredentials = cloneURLAndCredentials(repoURL, opts.GitCredentials)

	repo, cacheHit, err := openCachedGitRepository(cachePath, repoURL, opts.GitCredentials)
	if err != nil {
		return Result{}, err
	}
	if cacheHit {
		worktree, err := repo.Worktree()
		if err != nil {
			return Result{}, fmt.Errorf("open remote Git repository %s worktree: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, opts.GitCredentials))
		}
		if opts.Offline {
			revision, err := checkoutGitRevision(repo, worktree, request.Revision)
			if err != nil {
				return Result{}, fmt.Errorf("checkout remote Git repository %s revision %q: %s", RedactGitRepoURL(repoURL), strings.TrimSpace(request.Revision), redactGitError(err, repoURL, opts.GitCredentials))
			}
			writeGitRepoMetadata(filepath.Dir(cachePath), key, normalizedURL, revision)
			return Result{Path: cachePath, URL: normalizedURL, Revision: revision, FromCache: true}, nil
		}
		if !opts.Refresh {
			revision, err := checkoutOrFetchCommit(ctx, repo, worktree, repoURL, request.Revision, opts)
			if err == nil {
				writeGitRepoMetadata(filepath.Dir(cachePath), key, normalizedURL, revision)
				return Result{Path: cachePath, URL: normalizedURL, Revision: revision, FromCache: true}, nil
			}
			return Result{}, fmt.Errorf("checkout cached remote Git repository %s revision %q: %s", RedactGitRepoURL(repoURL), strings.TrimSpace(request.Revision), redactGitError(err, repoURL, opts.GitCredentials))
		}
		auth, _, err := gitAuthMethod(opts.GitCredentials, repoURL)
		if err != nil {
			return Result{}, err
		}
		if err := refreshGitRepository(ctx, repo, worktree, repoURL, request.Revision, auth, opts.GitCredentials); err != nil {
			return Result{}, err
		}
		revision, err := checkoutGitRevision(repo, worktree, request.Revision)
		if err != nil {
			return Result{}, fmt.Errorf("checkout remote Git repository %s revision %q: %s", RedactGitRepoURL(repoURL), strings.TrimSpace(request.Revision), redactGitError(err, repoURL, opts.GitCredentials))
		}
		writeGitRepoMetadata(filepath.Dir(cachePath), key, normalizedURL, revision)
		return Result{Path: cachePath, URL: normalizedURL, Revision: revision}, nil
	}
	if opts.Offline {
		return Result{}, fmt.Errorf("offline cache miss for remote Git repository %s", RedactGitRepoURL(repoURL))
	}
	if err := rejectForbiddenCachePath(cachePath, opts.ForbiddenRoots); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return Result{}, fmt.Errorf("create remote Git cache %s: %w", filepath.Dir(cachePath), err)
	}
	auth, _, err := gitAuthMethod(opts.GitCredentials, repoURL)
	if err != nil {
		return Result{}, err
	}
	repo, err = cloneGitRepository(ctx, cachePath, repoURL, auth, opts.GitCredentials)
	if err != nil {
		return Result{}, err
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return Result{}, fmt.Errorf("open remote Git repository %s worktree: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, opts.GitCredentials))
	}
	revision, err := checkoutOrFetchCommit(ctx, repo, worktree, repoURL, request.Revision, opts)
	if err != nil {
		return Result{}, fmt.Errorf("checkout remote Git repository %s revision %q: %s", RedactGitRepoURL(repoURL), strings.TrimSpace(request.Revision), redactGitError(err, repoURL, opts.GitCredentials))
	}
	writeGitRepoMetadata(filepath.Dir(cachePath), key, normalizedURL, revision)
	return Result{Path: cachePath, URL: normalizedURL, Revision: revision}, nil
}

func writeGitRepoMetadata(entryRoot, key, target, revision string) {
	_ = cache.WriteMetadata(entryRoot, cache.Metadata{
		Source:   cache.SourceRemote,
		Kind:     "git-repo",
		Key:      key,
		Target:   cache.RedactedTarget(target),
		Revision: revision,
	})
}

func openCachedGitRepository(cachePath, repoURL string, credentials GitCredentials) (*git.Repository, bool, error) {
	repo, err := git.PlainOpen(cachePath)
	if err == nil {
		// An entry that an older drydock cloned from a URL with userinfo
		// loses it now.
		return repo, true, gittree.ScrubRemoteUserinfo(repo)
	}
	if errors.Is(err, git.ErrRepositoryNotExists) || errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("open cached remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, credentials))
}

func cloneGitRepository(ctx context.Context, cachePath, repoURL string, auth transport.AuthMethod, credentials GitCredentials) (*git.Repository, error) {
	if err := os.RemoveAll(cachePath); err != nil {
		return nil, err
	}
	repo, err := git.PlainCloneContext(ctx, cachePath, false, &git.CloneOptions{
		URL:  repoURL,
		Auth: auth,
		Tags: git.AllTags,
	})
	if err != nil {
		_ = os.RemoveAll(cachePath)
		return nil, fmt.Errorf("clone remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, credentials))
	}
	return repo, nil
}

func refreshGitRepository(ctx context.Context, repo *git.Repository, worktree *git.Worktree, repoURL, revision string, auth transport.AuthMethod, credentials GitCredentials) error {
	if isDefaultGitRevision(revision) {
		return pullGitRepository(ctx, worktree, repoURL, auth, credentials)
	}
	return fetchGitRepository(ctx, repo, repoURL, auth, credentials)
}

func fetchGitRepository(ctx context.Context, repo *git.Repository, repoURL string, auth transport.AuthMethod, credentials GitCredentials) error {
	// Prune drops the remote-tracking refs of branches that the remote
	// deleted, so a deleted branch stops resolving after a refresh, as
	// ls-remote in the Argo CD repo-server no longer finds it.
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Tags:       git.AllTags,
		Prune:      true,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return fmt.Errorf("fetch remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, credentials))
}

func pullGitRepository(ctx context.Context, worktree *git.Worktree, repoURL string, auth transport.AuthMethod, credentials GitCredentials) error {
	if err := gittree.ClearCacheSubmodules(worktree.Filesystem.Root(), plumbing.ZeroHash); err != nil {
		return fmt.Errorf("clear submodule content of remote Git repository %s: %w", RedactGitRepoURL(repoURL), err)
	}
	err := worktree.PullContext(ctx, &git.PullOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      true,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return fmt.Errorf("fetch remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, credentials))
}

// gitAuthMethod resolves the go-git auth method for a remote Kustomize Git
// ref. The shared builder lives in gitauth; this package's GitCredentials is a
// field-for-field mirror of gitauth.Credentials, which is what makes the
// struct conversion below legal (and compile-checked against drift).
func gitAuthMethod(credentials GitCredentials, repoURL string) (transport.AuthMethod, bool, error) {
	auth, hasAuth, _, err := gitauth.AuthMethod(gitauth.Credentials(credentials), repoURL, newSSHEnvironment())
	if err != nil {
		return nil, false, fmt.Errorf("git SSH auth for remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitCredentialError(err.Error(), credentials))
	}
	return auth, hasAuth, nil
}

// newSSHEnvironment builds the ambient environment SSH identity resolution
// reads. Tests replace it so they never touch the developer's ~/.ssh or agent.
var newSSHEnvironment = gitauth.Default

// rejectLocalGitTransport fails when repoURL names the file transport, or
// when go-git would read it as a local path, unless allowFile is set. The
// error never quotes the parse error, which can hold a password.
func rejectLocalGitTransport(repoURL string, allowFile bool) error {
	if allowFile {
		return nil
	}
	if parsed, err := url.Parse(repoURL); err == nil && parsed.Scheme == "file" {
		return localGitTransportError(repoURL)
	}
	endpoint, err := transport.NewEndpoint(repoURL)
	if err != nil {
		return fmt.Errorf("remote Git repository URL %q cannot be parsed by drydock's Git client", RedactGitRepoURL(repoURL))
	}
	if endpoint.Protocol == "file" {
		return localGitTransportError(repoURL)
	}
	return nil
}

func localGitTransportError(repoURL string) error {
	return fmt.Errorf("remote Git repository URL %q uses the local file transport; drydock fetches remote Git repositories only over http, https, or ssh", RedactGitRepoURL(repoURL))
}

func isSCPStyleGitURL(repoURL string) bool {
	return giturl.IsSCPStyle(repoURL)
}

func checkoutGitRevision(repo *git.Repository, worktree *git.Worktree, revision string) (string, error) {
	cleanRevision := strings.TrimSpace(revision)
	if isDefaultGitRevision(cleanRevision) {
		head, err := repo.Head()
		if err != nil {
			return "", err
		}
		return head.Hash().String(), nil
	}

	hash, err := resolveGitRevision(repo, cleanRevision)
	if err != nil {
		return "", err
	}
	// Submodule content of another commit must not meet the checkout.
	if err := gittree.ClearCacheSubmodules(worktree.Filesystem.Root(), *hash); err != nil {
		return "", err
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: *hash, Force: true}); err != nil {
		return "", err
	}
	return hash.String(), nil
}

func isDefaultGitRevision(revision string) bool {
	cleanRevision := strings.TrimSpace(revision)
	return cleanRevision == "" || cleanRevision == "HEAD"
}

func resolveGitRevision(repo *git.Repository, revision string) (*plumbing.Hash, error) {
	candidates := gitRevisionCandidates(revision)
	var lastErr error
	for _, candidate := range candidates {
		hash, err := repo.ResolveRevision(candidate)
		if err == nil {
			return hash, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// gitRevisionCandidates lists the refs that a remote Kustomize Git ref
// resolves through, in order. A branch resolves from the remote-tracking ref
// of the clone first: a fetch with --refresh-remote-resources updates
// refs/remotes/origin/<branch>, but never the local branch that the clone
// created for the default branch, which would stay at the commit of clone
// time. kustomize fetches the ref from the remote, which gives the remote's
// current commit.
func gitRevisionCandidates(revision string) []plumbing.Revision {
	if branch, ok := strings.CutPrefix(revision, "refs/heads/"); ok {
		return []plumbing.Revision{
			plumbing.Revision("refs/remotes/origin/" + branch),
			plumbing.Revision(revision),
		}
	}
	if tag, ok := strings.CutPrefix(revision, "refs/tags/"); ok {
		return []plumbing.Revision{
			plumbing.Revision(revision),
			plumbing.Revision(tag),
		}
	}
	return []plumbing.Revision{
		plumbing.Revision("refs/remotes/origin/" + revision),
		plumbing.Revision("refs/heads/" + revision),
		plumbing.Revision("refs/tags/" + revision),
		plumbing.Revision(revision),
	}
}

func RedactGitRepoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if isSCPStyleGitURL(raw) {
		if before, _, ok := strings.Cut(raw, "#"); ok {
			raw = before
		}
		if before, _, ok := strings.Cut(raw, "?"); ok {
			raw = before
		}
		userHost, repoPath, ok := strings.Cut(raw, ":")
		if !ok {
			return "[invalid-url]"
		}
		if _, host, ok := strings.Cut(userHost, "@"); ok && host != "" {
			return host + ":" + repoPath
		}
		return raw
	}
	return RedactURL(raw)
}

func redactGitError(err error, repoURL string, credentials GitCredentials) string {
	message := giturl.RedactInMessage(err.Error(), repoURL, RedactGitRepoURL(repoURL))
	return redactGitCredentialError(message, credentials)
}

func redactGitCredentialError(message string, credentials GitCredentials) string {
	return redactCredentialValues(message, Credentials{}, credentials)
}
