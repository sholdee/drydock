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
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/sholdee/drydock/internal/cache"
)

//nolint:gocyclo // Git acquisition has distinct cache hit, refresh, offline, auth, clone, and checkout branches.
func (acquirer DefaultAcquirer) acquireGitRepo(ctx context.Context, request Request, opts Options) (Result, error) {
	repoURL := strings.TrimSpace(request.RepoURL)
	if repoURL == "" {
		repoURL = strings.TrimSpace(request.URL)
	}
	normalizedURL, err := NormalizeGitRepoCacheURL(repoURL)
	if err != nil {
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
			revision, err := checkoutGitRevision(repo, worktree, request.Revision)
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
	revision, err := checkoutGitRevision(repo, worktree, request.Revision)
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
		return repo, true, nil
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
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Tags:       git.AllTags,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return fmt.Errorf("fetch remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitError(err, repoURL, credentials))
}

func pullGitRepository(ctx context.Context, worktree *git.Worktree, repoURL string, auth transport.AuthMethod, credentials GitCredentials) error {
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

func gitAuthMethod(credentials GitCredentials, repoURL string) (transport.AuthMethod, bool, error) {
	if isSSHGitURL(repoURL) {
		auth, err := gitSSHAuthMethod(credentials, repoURL)
		return auth, auth != nil, err
	}
	if strings.TrimSpace(credentials.BearerToken) != "" {
		return &githttp.TokenAuth{Token: credentials.BearerToken}, true, nil
	}
	if strings.TrimSpace(credentials.Username) != "" || credentials.Password != "" {
		return &githttp.BasicAuth{Username: credentials.Username, Password: credentials.Password}, true, nil
	}
	return nil, false, nil
}

func gitSSHAuthMethod(credentials GitCredentials, repoURL string) (transport.AuthMethod, error) {
	if strings.TrimSpace(credentials.SSHPrivateKeyPath) == "" && strings.TrimSpace(credentials.SSHPrivateKey) == "" {
		return nil, fmt.Errorf("git SSH private key file is required for remote Git repository %s", RedactGitRepoURL(repoURL))
	}
	if strings.TrimSpace(credentials.SSHKnownHostsPath) == "" {
		return nil, fmt.Errorf("git SSH known_hosts file is required for remote Git repository %s", RedactGitRepoURL(repoURL))
	}
	user := sshGitUser(repoURL)
	var auth *gitssh.PublicKeys
	var err error
	if strings.TrimSpace(credentials.SSHPrivateKey) != "" {
		auth, err = gitssh.NewPublicKeys(user, []byte(credentials.SSHPrivateKey), credentials.SSHPassphrase)
	} else {
		auth, err = gitssh.NewPublicKeysFromFile(user, credentials.SSHPrivateKeyPath, credentials.SSHPassphrase)
	}
	if err != nil {
		return nil, fmt.Errorf("load git SSH private key for remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitCredentialError(err.Error(), credentials))
	}
	callback, err := gitssh.NewKnownHostsCallback(credentials.SSHKnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("load git SSH known_hosts file for remote Git repository %s: %s", RedactGitRepoURL(repoURL), redactGitCredentialError(err.Error(), credentials))
	}
	auth.HostKeyCallback = callback
	return auth, nil
}

func isSSHGitURL(repoURL string) bool {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return false
	}
	if strings.HasPrefix(repoURL, "ssh://") {
		return true
	}
	return isSCPStyleGitURL(repoURL)
}

func isSCPStyleGitURL(repoURL string) bool {
	if strings.Contains(repoURL, "://") || strings.HasPrefix(repoURL, "/") {
		return false
	}
	colon := strings.Index(repoURL, ":")
	if colon <= 0 {
		return false
	}
	if slash := strings.IndexAny(repoURL, `/\`); slash >= 0 && slash < colon {
		return false
	}
	return strings.Contains(repoURL[:colon], "@")
}

func sshGitUser(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if isSCPStyleGitURL(repoURL) {
		userHost, _, _ := strings.Cut(repoURL, ":")
		if user, _, ok := strings.Cut(userHost, "@"); ok && user != "" {
			return user
		}
		return "git"
	}
	if parsed, err := url.Parse(repoURL); err == nil && parsed.User != nil {
		if user := parsed.User.Username(); user != "" {
			return user
		}
	}
	return "git"
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

func gitRevisionCandidates(revision string) []plumbing.Revision {
	if branch, ok := strings.CutPrefix(revision, "refs/heads/"); ok {
		return []plumbing.Revision{
			plumbing.Revision(revision),
			plumbing.Revision("refs/remotes/origin/" + branch),
		}
	}
	if tag, ok := strings.CutPrefix(revision, "refs/tags/"); ok {
		return []plumbing.Revision{
			plumbing.Revision(revision),
			plumbing.Revision(tag),
		}
	}
	return []plumbing.Revision{
		plumbing.Revision(revision),
		plumbing.Revision("refs/heads/" + revision),
		plumbing.Revision("refs/remotes/origin/" + revision),
		plumbing.Revision("refs/tags/" + revision),
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
	message := err.Error()
	redacted := RedactGitRepoURL(repoURL)
	raw := strings.TrimSpace(repoURL)
	replacements := []string{raw, strings.TrimSuffix(raw, ".git")}
	if parsed, parseErr := url.Parse(raw); parseErr == nil && parsed.Scheme != "" {
		withoutFragment := *parsed
		withoutFragment.Fragment = ""
		replacements = append(replacements, withoutFragment.String())

		withoutQueryFragment := withoutFragment
		withoutQueryFragment.RawQuery = ""
		withoutQueryFragment.ForceQuery = false
		replacements = append(replacements, withoutQueryFragment.String())

		withoutUser := withoutQueryFragment
		withoutUser.User = nil
		replacements = append(replacements, withoutUser.String())

		if parsed.User != nil {
			username := parsed.User.Username()
			replacements = append(replacements, parsed.User.String()+"@")
			if username != "" {
				replacements = append(replacements, username+":***@")
				replacements = append(replacements, username+"@")
			}
		}
		if parsed.RawQuery != "" {
			replacements = append(replacements, parsed.RawQuery)
		}
		if parsed.Fragment != "" {
			replacements = append(replacements, parsed.Fragment)
		}
	}
	for _, replacement := range replacements {
		if replacement == "" || replacement == redacted {
			continue
		}
		message = strings.ReplaceAll(message, replacement, redacted)
	}
	return redactGitCredentialError(message, credentials)
}

func redactGitCredentialError(message string, credentials GitCredentials) string {
	return redactCredentialValues(message, Credentials{}, credentials)
}
