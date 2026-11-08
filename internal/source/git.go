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
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gittree"
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
	// Submodules checks out the content of the submodules of the commit,
	// as the Argo CD repo-server does when reposerver.enable.git.submodule
	// is on (the default). Without it, each gitlink is an empty directory.
	Submodules bool
	// LockEntry, when set, locks the cache entry of a request for the
	// time drydock reads or writes it, and returns the unlock function.
	// Submodule fetches lock their own entries with it.
	LockEntry func(request GitRequest, opts GitOptions) (unlock func())
	// AllowFileSubmoduleTransport lets a submodule URL be a local path or a
	// file URL, which the Argo CD repo-server's git refuses. Only tests set
	// it.
	AllowFileSubmoduleTransport bool
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
		return acquireCachedGitRepository(ctx, cachePath, key, request, opts)
	}

	return acquireOnlineGitRepository(ctx, cachePath, key, request, opts)
}

// acquireOnlineGitRepository clones or opens the cache entry at cachePath,
// refreshes it when asked, checks out the revision with its submodules,
// and fetches what the entry lacks.
func acquireOnlineGitRepository(ctx context.Context, cachePath, key string, request GitRequest, opts GitOptions) (GitResult, error) {
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
	submoduleNetwork, err := syncCacheSubmodules(ctx, repo, cachePath, revision, request, opts)
	if err != nil {
		return GitResult{}, err
	}

	network := cloned || refreshed || fetched || submoduleNetwork
	writeGitMetadata(cachePath, key, request, revision)
	return GitResult{Path: cachePath, Revision: revision, LsRemoteRevision: LsRemoteRevision(repo, request.Revision, revision, request.URL), FromCache: !network, Network: network}, nil
}

func acquireCachedGitRepository(ctx context.Context, cachePath, key string, request GitRequest, opts GitOptions) (GitResult, error) {
	repo, err := openExistingGitRepository(cachePath)
	if err != nil {
		return GitResult{}, fmt.Errorf("offline cache miss for Git repository %s", RedactURL(request.URL))
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return GitResult{}, fmt.Errorf("open cached repository %s worktree: %w", RedactURL(request.URL), err)
	}
	revision, err := checkoutGitRevision(repo, worktree, request.Revision, request.URL)
	if err != nil {
		return GitResult{}, fmt.Errorf("checkout cached repository %s revision %q: %w", RedactURL(request.URL), strings.TrimSpace(request.Revision), err)
	}
	if _, err := syncCacheSubmodules(ctx, repo, cachePath, revision, request, opts); err != nil {
		return GitResult{}, err
	}
	writeGitMetadata(cachePath, key, request, revision)
	return GitResult{Path: cachePath, Revision: revision, LsRemoteRevision: LsRemoteRevision(repo, request.Revision, revision, request.URL), FromCache: true, Network: false}, nil
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
	revision, err := checkoutGitRevision(repo, worktree, request.Revision, request.URL)
	if err == nil {
		return revision, false, nil
	}
	fetched := false
	if !cloned {
		if fetchErr := fetchGitRepository(ctx, repo, request.URL, auth, credentials); fetchErr != nil {
			return "", false, fetchErr
		}
		fetched = true
		revision, err = checkoutGitRevision(repo, worktree, request.Revision, request.URL)
	}
	if err != nil {
		return "", fetched, fmt.Errorf("checkout repository %s revision %q: %s", RedactURL(request.URL), strings.TrimSpace(request.Revision), redactGitError(err, request.URL, credentials))
	}
	return revision, fetched, nil
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
	return fmt.Errorf("fetch repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL, credentials))
}

func pullGitRepository(ctx context.Context, worktree *git.Worktree, repoURL string, auth transport.AuthMethod, credentials GitCredentials) error {
	if err := gittree.ClearCacheSubmodules(worktree.Filesystem.Root(), plumbing.ZeroHash); err != nil {
		return fmt.Errorf("clear submodule content of repository %s: %w", RedactURL(repoURL), err)
	}
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

func checkoutGitRevision(repo *git.Repository, worktree *git.Worktree, revision, repoURL string) (string, error) {
	cleanRevision := strings.TrimSpace(revision)
	if isDefaultGitRevision(cleanRevision) {
		head, err := repo.Head()
		if err != nil {
			return "", err
		}
		return head.Hash().String(), nil
	}

	hash, err := resolveCheckoutRevision(repo, cleanRevision, repoURL)
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

// resolveCheckoutRevision resolves the revision that a fetched Git source
// checks out, in the order of `git ls-remote` in the Argo CD v3.6.1
// repo-server (util/git/client.go:961-1027):
//
//   - a full SHA is taken as it is;
//   - a branch or tag with that name wins next, also for a name of hex
//     digits such as `2024` or `cafe`, because ls-remote matches ref names
//     first. go-git prefers an object whose SHA starts with the name, so
//     ResolveGitRevision alone could check out another commit. A branch
//     resolves from the remote-tracking ref of the cache clone first: a
//     fetch updates refs/remotes/<remote>/<branch>, but never the local
//     branch that the clone created for the default branch, which would
//     stay at the commit of clone time after --refresh-git;
//   - a hex revision of 7 or more digits is then an abbreviated SHA, which
//     must name a single commit or tag (resolveAbbreviatedCommit);
//   - a shorter all-hex name that names no ref does not resolve, as in
//     ls-remote;
//   - any other revision resolves through ResolveGitRevision.
func resolveCheckoutRevision(repo *git.Repository, revision, repoURL string) (*plumbing.Hash, error) {
	if fullCommitSHA.MatchString(revision) {
		return ResolveGitRevision(repo, strings.ToLower(revision))
	}
	if hash, ok := lsRemoteRefCommit(repo, revision, repoURL, true); ok {
		return &hash, nil
	}
	if abbreviatedCommitSHA.MatchString(revision) {
		hash, found, err := resolveAbbreviatedCommit(repo, revision)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("unable to resolve %q to a commit SHA", revision)
		}
		return &hash, nil
	}
	if shortHexRevision.MatchString(revision) {
		return nil, fmt.Errorf("unable to resolve %q to a commit SHA", revision)
	}
	return ResolveGitRevision(repo, revision)
}

// resolveAbbreviatedCommit resolves an abbreviated SHA as `git checkout`
// does. git gives the name no type hint there, so every object whose SHA
// starts with the abbreviation counts, of any type: more than one fails
// like git ("short object ID ... is ambiguous"), also when only one of
// them is a commit. A single commit resolves to itself, and a single
// annotated tag to the commit it points to. A single tree or blob is no
// commit, which fails too. found is false when no object matches.
func resolveAbbreviatedCommit(repo *git.Repository, abbreviated string) (plumbing.Hash, bool, error) {
	abbreviated = strings.ToLower(abbreviated)
	matches := hashesWithPrefix(repo, abbreviated)
	switch len(matches) {
	case 0:
		return plumbing.ZeroHash, false, nil
	case 1:
	default:
		return plumbing.ZeroHash, false, fmt.Errorf("short object ID %s is ambiguous", abbreviated)
	}
	if commit, err := repo.CommitObject(matches[0]); err == nil {
		return commit.Hash, true, nil
	}
	if tag, err := repo.TagObject(matches[0]); err == nil {
		if commit, err := tag.Commit(); err == nil {
			return commit.Hash, true, nil
		}
	}
	return plumbing.ZeroHash, false, fmt.Errorf("short object ID %s does not name a commit", abbreviated)
}

// hashesWithPrefix lists the SHAs of the objects of repo that start with the
// lower-case hex prefix, as go-git's own prefix lookup does.
func hashesWithPrefix(repo *git.Repository, prefix string) []plumbing.Hash {
	even, err := hex.DecodeString(prefix[:len(prefix)&^1])
	if err != nil {
		return nil
	}
	var candidates []plumbing.Hash
	type prefixLister interface {
		HashesWithPrefix(prefix []byte) ([]plumbing.Hash, error)
	}
	if lister, ok := repo.Storer.(prefixLister); ok {
		candidates, err = lister.HashesWithPrefix(even)
		if err != nil {
			return nil
		}
	} else {
		iter, err := repo.Storer.IterEncodedObjects(plumbing.AnyObject)
		if err != nil {
			return nil
		}
		_ = iter.ForEach(func(object plumbing.EncodedObject) error {
			hash := object.Hash()
			if strings.HasPrefix(hash.String(), prefix[:len(prefix)&^1]) {
				candidates = append(candidates, hash)
			}
			return nil
		})
	}
	var out []plumbing.Hash
	seen := map[plumbing.Hash]struct{}{}
	for _, hash := range candidates {
		if _, ok := seen[hash]; ok || !strings.HasPrefix(hash.String(), prefix) {
			continue
		}
		seen[hash] = struct{}{}
		out = append(out, hash)
	}
	return out
}

// LsRemoteRevision returns the revision that the Argo CD v3.6.1
// repo-server passes on for a source of a multi-source Application, where it
// uses the `git ls-remote` result as it is (nativeGitClient.lsRemote,
// util/git/client.go:961-1027, and runRepoOperation,
// reposerver/repository/repository.go:516-518). commit is the full commit
// SHA that drydock checked out for revision in repo, and repoURL the
// repository URL of the source. The result is:
//
//   - revision as written when it is a full 40-digit SHA;
//   - revision as written when it is an abbreviated SHA (7 or more hex
//     digits) that names no branch or tag, as ls-remote returns it
//     (util/git/client.go:1019-1022), also when it abbreviates an
//     annotated tag object that the checkout peeled to commit;
//   - commit in every other case: HEAD, a branch, a lightweight tag, and an
//     annotated tag, because listRemote peels each tag that the server
//     advertises with a peeled SHA (util/git/workaround.go:86-96).
//
// drydock reads the refs of repo, not of the remote.
func LsRemoteRevision(repo *git.Repository, revision, commit, repoURL string) string {
	revision = strings.TrimSpace(revision)
	if fullCommitSHA.MatchString(revision) {
		return revision
	}
	if repo == nil || !abbreviatedCommitSHA.MatchString(revision) {
		return commit
	}
	if _, ok := lsRemoteRefCommit(repo, revision, repoURL, true); ok {
		return commit
	}
	if hash, found, err := resolveAbbreviatedCommit(repo, revision); err == nil && found && hash.String() == commit {
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
// not hex (7 or more digits), so it names a branch, a tag, or HEAD. repoURL
// is the repository URL of the source.
//
//   - A full SHA gives the SHA in lower case for single and as written for
//     multi. repo may be nil.
//   - A hex revision that names a branch or a tag of repo gives the commit
//     of that ref for both, because ls-remote matches ref names first.
//   - Any other abbreviated SHA stays as written for multi. single is the
//     commit that the abbreviation names in repo, an abbreviated annotated
//     tag object peeled to its commit, or "" when no object in repo matches.
//   - An abbreviation that matches more than one object of any type, or a
//     single object that is no commit or tag, fails, as `git checkout`
//     fails for it in the repo-server (resolveAbbreviatedCommit).
func PinnedRevision(repo *git.Repository, revision, repoURL string) (single, multi string, ok bool, err error) {
	revision = strings.TrimSpace(revision)
	if fullCommitSHA.MatchString(revision) {
		return strings.ToLower(revision), revision, true, nil
	}
	if !abbreviatedCommitSHA.MatchString(revision) {
		return "", "", false, nil
	}
	if repo == nil {
		return "", revision, true, nil
	}
	if hash, ok := lsRemoteRefCommit(repo, revision, repoURL, false); ok {
		return hash.String(), hash.String(), true, nil
	}
	hash, found, err := resolveAbbreviatedCommit(repo, revision)
	if err != nil {
		return "", "", true, err
	}
	if !found {
		return "", revision, true, nil
	}
	return hash.String(), revision, true, nil
}

// IsHexRevision reports whether revision is a full or abbreviated commit SHA
// in the sense of the Argo CD repo-server (7 or more hex digits).
func IsHexRevision(revision string) bool {
	return abbreviatedCommitSHA.MatchString(strings.TrimSpace(revision))
}

var (
	fullCommitSHA        = regexp.MustCompile("^[0-9A-Fa-f]{40}$")
	abbreviatedCommitSHA = regexp.MustCompile("^[0-9A-Fa-f]{7,}$")
	shortHexRevision     = regexp.MustCompile("^[0-9A-Fa-f]{1,6}$")
)

// lsRemoteRefCommit returns the commit of the branch or tag named name in
// repo, peeled to its commit. name is a short name, or a full
// refs/heads/<branch> or refs/tags/<tag> name. A branch is
// refs/heads/<branch> or refs/remotes/<remote>/<branch>, where the remote
// is the one whose URL is repoURL, or every remote in name order when no
// remote has that URL. preferRemote looks at the remote-tracking ref first,
// which is the current remote commit in a drydock Git cache clone. Without
// it the local branch wins, which is what the user has in a local tree. A
// branch wins over a tag. ls-remote takes the first match in a ref list
// that has no fixed order, so for a name that is both a branch and a tag
// Argo CD can pick either.
func lsRemoteRefCommit(repo *git.Repository, name, repoURL string, preferRemote bool) (plumbing.Hash, bool) {
	var names []plumbing.ReferenceName
	switch {
	case strings.HasPrefix(name, "refs/tags/"):
		names = []plumbing.ReferenceName{plumbing.ReferenceName(name)}
	case strings.HasPrefix(name, "refs/heads/"):
		names = lsRemoteBranchRefNames(repo, strings.TrimPrefix(name, "refs/heads/"), repoURL, preferRemote)
	case strings.HasPrefix(name, "refs/"):
		return plumbing.ZeroHash, false
	default:
		names = append(lsRemoteBranchRefNames(repo, name, repoURL, preferRemote), plumbing.NewTagReferenceName(name))
	}
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

// lsRemoteBranchRefNames lists the ref names that hold branch in repo, in
// the order lsRemoteRefCommit tries them.
func lsRemoteBranchRefNames(repo *git.Repository, branch, repoURL string, preferRemote bool) []plumbing.ReferenceName {
	var remote []plumbing.ReferenceName
	for _, name := range lsRemoteRemoteNames(repo, repoURL) {
		remote = append(remote, plumbing.NewRemoteReferenceName(name, branch))
	}
	local := plumbing.NewBranchReferenceName(branch)
	if preferRemote {
		return append(remote, local)
	}
	return append([]plumbing.ReferenceName{local}, remote...)
}

// remoteURLKey is the key that lsRemoteRemoteNames compares remote URLs
// by: CanonicalGitURLKey of the URL in lower case. Lowering first also
// strips a `.GIT` suffix, as NormalizeGitURL does (util/git/git.go:74-91).
func remoteURLKey(url string) string {
	return CanonicalGitURLKey(strings.ToLower(url))
}

// lsRemoteRemoteNames returns the remote of repo whose URL is repoURL, or
// every remote in name order when none is.
func lsRemoteRemoteNames(repo *git.Repository, repoURL string) []string {
	remotes, err := repo.Remotes()
	if err != nil {
		return nil
	}
	// Argo CD compares repository URLs without letter case
	// (NormalizeGitURL lowercases the whole URL, util/git/git.go:74-91).
	// The scheme and user are dropped too, because a local remote may use
	// SSH where the Application uses HTTPS.
	want := remoteURLKey(repoURL)
	var all []string
	for _, remote := range remotes {
		config := remote.Config()
		all = append(all, config.Name)
		if want == "" {
			continue
		}
		for _, url := range config.URLs {
			if remoteURLKey(url) == want {
				return []string{config.Name}
			}
		}
	}
	sort.Strings(all)
	return all
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
