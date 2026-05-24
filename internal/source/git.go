package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

type GitRequest struct {
	URL      string
	Revision string
}

type GitOptions struct {
	AllowNetwork bool
	CacheDir     string
	Refresh      bool
	Credentials  GitCredentials
}

type GitCredentials struct {
	Username          string
	Password          string
	BearerToken       string
	SSHPrivateKeyPath string
	SSHPrivateKey     string
	SSHPassphrase     string
	SSHKnownHostsPath string
}

type GitResult struct {
	Path      string
	Revision  string
	FromCache bool
	Network   bool
}

type GitAcquirer interface {
	Acquire(ctx context.Context, request GitRequest, opts GitOptions) (GitResult, error)
}

type DefaultGitAcquirer struct{}

func (DefaultGitAcquirer) Acquire(ctx context.Context, request GitRequest, opts GitOptions) (GitResult, error) {
	if !opts.AllowNetwork {
		return GitResult{}, fmt.Errorf("repository %s requires --allow-network for Git fetching", RedactURL(request.URL))
	}
	if err := ctx.Err(); err != nil {
		return GitResult{}, err
	}
	auth, _, err := gitAuthMethod(opts.Credentials, request.URL)
	if err != nil {
		return GitResult{}, err
	}

	cacheDir, err := gitCacheDir(opts.CacheDir)
	if err != nil {
		return GitResult{}, err
	}
	cachePath := filepath.Join(cacheDir, gitCacheKey(request.URL, request.Revision))
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return GitResult{}, err
	}

	repo, cloned, err := openOrCloneGitRepository(ctx, cachePath, request.URL, auth, opts.Credentials)
	if err != nil {
		return GitResult{}, err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return GitResult{}, fmt.Errorf("open repository %s worktree: %s", RedactURL(request.URL), redactGitError(err, request.URL, opts.Credentials))
	}
	refreshed := false
	if opts.Refresh && !cloned {
		if err := refreshGitRepository(ctx, repo, worktree, request, auth, opts.Credentials); err != nil {
			return GitResult{}, err
		}
		refreshed = true
	}
	revision, fetched, err := checkoutAcquiredGitRepository(ctx, repo, worktree, request, cloned, auth, opts.Credentials)
	if err != nil {
		return GitResult{}, err
	}

	network := cloned || refreshed || fetched
	return GitResult{Path: cachePath, Revision: revision, FromCache: !network, Network: network}, nil
}

func gitCacheDir(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	return DefaultGitCacheDir()
}

func DefaultGitCacheDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "drydock", "git"), nil
}

func gitCacheKey(repoURL, revision string) string {
	sum := sha256.Sum256([]byte(NormalizeURL(repoURL) + "\n" + strings.TrimSpace(revision)))
	return hex.EncodeToString(sum[:])
}

func openOrCloneGitRepository(ctx context.Context, cachePath, repoURL string, auth transport.AuthMethod, credentials GitCredentials) (*git.Repository, bool, error) {
	repo, err := git.PlainOpen(cachePath)
	if err == nil {
		return repo, false, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, false, fmt.Errorf("open cached repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL, credentials))
	}

	if err := os.RemoveAll(cachePath); err != nil {
		return nil, false, err
	}
	repo, err = git.PlainCloneContext(ctx, cachePath, false, &git.CloneOptions{
		URL:  repoURL,
		Auth: auth,
		Tags: git.AllTags,
	})
	if err != nil {
		_ = os.RemoveAll(cachePath)
		return nil, false, fmt.Errorf("clone repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL, credentials))
	}
	return repo, true, nil
}

func refreshGitRepository(ctx context.Context, repo *git.Repository, worktree *git.Worktree, request GitRequest, auth transport.AuthMethod, credentials GitCredentials) error {
	if isDefaultGitRevision(request.Revision) {
		return pullGitRepository(ctx, worktree, request.URL, auth, credentials)
	}
	return fetchGitRepository(ctx, repo, request.URL, auth, credentials)
}

func checkoutAcquiredGitRepository(ctx context.Context, repo *git.Repository, worktree *git.Worktree, request GitRequest, cloned bool, auth transport.AuthMethod, credentials GitCredentials) (string, bool, error) {
	revision, err := checkoutGitRevision(repo, worktree, request.Revision)
	if err == nil {
		return revision, false, nil
	}
	fetched := false
	if !cloned {
		if fetchErr := fetchGitRepository(ctx, repo, request.URL, auth, credentials); fetchErr != nil {
			return "", false, fetchErr
		}
		fetched = true
		revision, err = checkoutGitRevision(repo, worktree, request.Revision)
	}
	if err != nil {
		return "", fetched, fmt.Errorf("checkout repository %s revision %q: %s", RedactURL(request.URL), strings.TrimSpace(request.Revision), redactGitError(err, request.URL, credentials))
	}
	return revision, fetched, nil
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
	return fmt.Errorf("fetch repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL, credentials))
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
	return fmt.Errorf("fetch repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL, credentials))
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
		return nil, fmt.Errorf("git SSH private key file is required for repository %s", RedactURL(repoURL))
	}
	if strings.TrimSpace(credentials.SSHKnownHostsPath) == "" {
		return nil, fmt.Errorf("git SSH known_hosts file is required for repository %s", RedactURL(repoURL))
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
		return nil, fmt.Errorf("load git SSH private key for repository %s: %s", RedactURL(repoURL), redactGitCredentialError(err.Error(), credentials))
	}
	callback, err := gitssh.NewKnownHostsCallback(credentials.SSHKnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("load git SSH known_hosts file for repository %s: %s", RedactURL(repoURL), redactGitCredentialError(err.Error(), credentials))
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

func redactGitError(err error, repoURL string, credentials GitCredentials) string {
	message := err.Error()
	redacted := RedactURL(repoURL)
	raw := strings.TrimSpace(repoURL)
	replacements := []string{raw, strings.TrimSuffix(raw, ".git"), redacted}
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
	for _, secret := range []string{
		credentials.Password,
		credentials.BearerToken,
		credentials.SSHPrivateKey,
		credentials.SSHPassphrase,
	} {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	if credentials.SSHPrivateKey != "" {
		for _, line := range strings.Split(credentials.SSHPrivateKey, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			message = strings.ReplaceAll(message, line, "[redacted]")
		}
	}
	for _, marker := range []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----END OPENSSH PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----END RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----END EC PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		"-----END PRIVATE KEY-----",
	} {
		message = strings.ReplaceAll(message, marker, "[redacted]")
	}
	return message
}

func RedactGitCredentialError(message string, credentials GitCredentials) string {
	return redactGitCredentialError(message, credentials)
}
