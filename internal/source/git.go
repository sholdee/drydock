package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/giturl"
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
	Path     string
	Revision string
	// LsRemoteRevision is Revision in the form that `git ls-remote`
	// resolution in the Argo CD repo-server gives for the requested
	// revision (see LsRemoteRevision): a full or abbreviated SHA as
	// written, else the commit SHA.
	LsRemoteRevision string
	FromCache        bool
	Network          bool
}

type GitAcquirer interface {
	Acquire(ctx context.Context, request GitRequest, opts GitOptions) (GitResult, error)
}

type DefaultGitAcquirer struct{}

func (DefaultGitAcquirer) Acquire(ctx context.Context, request GitRequest, opts GitOptions) (GitResult, error) {
	if err := ctx.Err(); err != nil {
		return GitResult{}, err
	}

	cacheDir, err := gitCacheDir(opts.CacheDir)
	if err != nil {
		return GitResult{}, err
	}
	key := GitCacheKey(request.URL, request.Revision)
	cachePath := cache.GitEntryPath(cacheDir, key)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return GitResult{}, err
	}

	if !opts.AllowNetwork {
		result, err := acquireCachedGitRepository(cachePath, key, request)
		if err != nil {
			return GitResult{}, err
		}
		return result, nil
	}

	auth, _, err := gitAuthMethod(opts.Credentials, request.URL)
	if err != nil {
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
	writeGitMetadata(cachePath, key, request, revision)
	return GitResult{Path: cachePath, Revision: revision, LsRemoteRevision: LsRemoteRevision(repo, request.Revision, revision), FromCache: !network, Network: network}, nil
}

func acquireCachedGitRepository(cachePath, key string, request GitRequest) (GitResult, error) {
	repo, err := openExistingGitRepository(cachePath)
	if err != nil {
		return GitResult{}, fmt.Errorf("offline cache miss for Git repository %s", RedactURL(request.URL))
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return GitResult{}, fmt.Errorf("open cached repository %s worktree: %w", RedactURL(request.URL), err)
	}
	revision, err := checkoutGitRevision(repo, worktree, request.Revision)
	if err != nil {
		return GitResult{}, fmt.Errorf("checkout cached repository %s revision %q: %w", RedactURL(request.URL), strings.TrimSpace(request.Revision), err)
	}
	writeGitMetadata(cachePath, key, request, revision)
	return GitResult{Path: cachePath, Revision: revision, LsRemoteRevision: LsRemoteRevision(repo, request.Revision, revision), FromCache: true, Network: false}, nil
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

func GitCacheKey(repoURL, revision string) string {
	sum := sha256.Sum256([]byte(gitCacheTarget(repoURL) + "\n" + strings.TrimSpace(revision)))
	return hex.EncodeToString(sum[:])
}

func writeGitMetadata(cachePath, key string, request GitRequest, revision string) {
	_ = cache.WriteMetadata(cachePath, cache.Metadata{
		Source:   cache.SourceGit,
		Kind:     "git",
		Key:      key,
		Target:   gitCacheTarget(request.URL),
		Revision: revision,
	})
}

func gitCacheTarget(repoURL string) string {
	return NormalizeURL(cache.RedactedTarget(repoURL))
}

func openExistingGitRepository(cachePath string) (*git.Repository, error) {
	return git.PlainOpen(cachePath)
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

// gitAuthMethod resolves the go-git auth method for repoURL. GitCredentials
// mirrors gitauth.Credentials field for field, so the conversion is direct and
// the compiler catches any drift between the two layouts.
func gitAuthMethod(credentials GitCredentials, repoURL string) (transport.AuthMethod, bool, error) {
	auth, hasAuth, _, err := gitauth.AuthMethod(gitauth.Credentials(credentials), repoURL, newSSHEnvironment())
	if err != nil {
		return nil, false, fmt.Errorf("git SSH auth for repository %s: %s", RedactURL(repoURL), redactGitCredentialError(err.Error(), credentials))
	}
	return auth, hasAuth, nil
}

// newSSHEnvironment builds the ambient environment SSH identity resolution
// reads. Tests replace it so they never touch the developer's ~/.ssh or agent.
var newSSHEnvironment = gitauth.Default

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

	hash, err := resolveCheckoutRevision(repo, cleanRevision)
	if err != nil {
		return "", err
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: *hash, Force: true}); err != nil {
		return "", err
	}
	return hash.String(), nil
}

// resolveCheckoutRevision resolves the revision that a fetched Git source
// checks out. A hex revision that is also the name of a branch or tag
// resolves to that ref, as `git ls-remote` in the Argo CD v3.6.1 repo-server
// matches ref names before it takes a hex revision as an abbreviated SHA
// (util/git/client.go:991-1022). go-git prefers the object, so
// ResolveGitRevision alone would check out another commit.
//
// git reads hex digits in either case, and go-git only in lower case, so a
// hex revision that names no ref resolves in lower case.
func resolveCheckoutRevision(repo *git.Repository, revision string) (*plumbing.Hash, error) {
	if !abbreviatedCommitSHA.MatchString(revision) {
		return ResolveGitRevision(repo, revision)
	}
	if !fullCommitSHA.MatchString(revision) {
		if hash, ok := lsRemoteRefCommit(repo, revision); ok {
			return &hash, nil
		}
	}
	if hash, err := ResolveGitRevision(repo, revision); err == nil {
		return hash, nil
	}
	return ResolveGitRevision(repo, strings.ToLower(revision))
}

// LsRemoteRevision returns the revision that the Argo CD v3.6.1
// repo-server passes on for a source of a multi-source Application, where it
// uses the `git ls-remote` result as it is (nativeGitClient.lsRemote,
// util/git/client.go:961-1027, and runRepoOperation,
// reposerver/repository/repository.go:516-518). commit is the full commit
// SHA that drydock checked out for revision in repo. The result is:
//
//   - revision as written when it is a full 40-digit SHA;
//   - revision as written when it is an abbreviated SHA (7 or more hex
//     digits) that names no branch or tag and is a prefix of commit;
//   - commit in every other case: HEAD, a branch, a lightweight tag, and an
//     annotated tag, because listRemote peels each tag that the server
//     advertises with a peeled SHA (util/git/workaround.go:86-96).
//
// drydock reads the refs of repo, not of the remote.
func LsRemoteRevision(repo *git.Repository, revision, commit string) string {
	revision = strings.TrimSpace(revision)
	if fullCommitSHA.MatchString(revision) {
		return revision
	}
	if repo == nil || !abbreviatedCommitSHA.MatchString(revision) {
		return commit
	}
	if _, ok := lsRemoteRefCommit(repo, revision); ok {
		return commit
	}
	if strings.HasPrefix(commit, strings.ToLower(revision)) {
		return revision
	}
	return commit
}

// PinnedRevision returns what the Argo CD v3.6.1 repo-server reports as
// ARGOCD_APP_REVISION for a targetRevision that pins a commit by SHA, when
// drydock renders a local tree and not a checkout of that revision. single
// is the value of a single-source Application (`git rev-parse HEAD` after
// the checkout) and multi the value of a source of a multi-source
// Application (the ls-remote result as it is). ok is false when revision is
// not hex, so it names a branch, a tag, or HEAD.
//
//   - A full SHA gives the SHA in lower case for single and as written for
//     multi. repo may be nil.
//   - A hex revision that names a branch or a tag of repo gives the commit
//     of that ref for both, because ls-remote matches ref names first.
//   - Any other abbreviated SHA stays as written for multi. single is the
//     commit in repo that the abbreviation names, or "" when repo has no
//     such single commit.
func PinnedRevision(repo *git.Repository, revision string) (single, multi string, ok bool) {
	revision = strings.TrimSpace(revision)
	if fullCommitSHA.MatchString(revision) {
		return strings.ToLower(revision), revision, true
	}
	if !abbreviatedCommitSHA.MatchString(revision) {
		return "", "", false
	}
	if repo == nil {
		return "", revision, true
	}
	if hash, ok := lsRemoteRefCommit(repo, revision); ok {
		return hash.String(), hash.String(), true
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(strings.ToLower(revision)))
	if err != nil || !strings.HasPrefix(hash.String(), strings.ToLower(revision)) {
		return "", revision, true
	}
	return hash.String(), revision, true
}

// IsHexRevision reports whether revision is a full or abbreviated commit SHA
// in the sense of the Argo CD repo-server (7 or more hex digits).
func IsHexRevision(revision string) bool {
	return abbreviatedCommitSHA.MatchString(strings.TrimSpace(revision))
}

var (
	fullCommitSHA        = regexp.MustCompile("^[0-9A-Fa-f]{40}$")
	abbreviatedCommitSHA = regexp.MustCompile("^[0-9A-Fa-f]{7,}$")
)

// lsRemoteRefCommit returns the commit of the branch or tag named name in
// repo: refs/heads/<name>, refs/remotes/<remote>/<name> for every remote,
// or refs/tags/<name>, peeled to its commit. A branch wins over a tag.
// ls-remote takes the first match in a ref list that has no fixed order,
// so for a name that is both a branch and a tag Argo CD can pick either.
func lsRemoteRefCommit(repo *git.Repository, name string) (plumbing.Hash, bool) {
	names := []plumbing.ReferenceName{plumbing.NewBranchReferenceName(name)}
	if remotes, err := repo.Remotes(); err == nil {
		for _, remote := range remotes {
			names = append(names, plumbing.NewRemoteReferenceName(remote.Config().Name, name))
		}
	}
	names = append(names, plumbing.NewTagReferenceName(name))
	for _, refName := range names {
		if _, err := repo.Reference(refName, true); err != nil {
			continue
		}
		hash, err := repo.ResolveRevision(plumbing.Revision(refName.String()))
		if err != nil {
			continue
		}
		return *hash, true
	}
	return plumbing.ZeroHash, false
}

func isDefaultGitRevision(revision string) bool {
	cleanRevision := strings.TrimSpace(revision)
	return cleanRevision == "" || cleanRevision == "HEAD"
}

func ResolveGitRevision(repo *git.Repository, revision string) (*plumbing.Hash, error) {
	candidates := GitRevisionCandidates(revision)
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

func GitRevisionCandidates(revision string) []plumbing.Revision {
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
	message := giturl.RedactInMessage(err.Error(), repoURL, RedactURL(repoURL))
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
		for line := range strings.SplitSeq(credentials.SSHPrivateKey, "\n") {
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
