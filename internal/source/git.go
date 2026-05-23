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
)

type GitRequest struct {
	URL      string
	Revision string
}

type GitOptions struct {
	AllowNetwork bool
	CacheDir     string
	Refresh      bool
}

type GitResult struct {
	Path     string
	Revision string
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

	cacheDir := opts.CacheDir
	if cacheDir == "" {
		defaultDir, err := DefaultGitCacheDir()
		if err != nil {
			return GitResult{}, err
		}
		cacheDir = defaultDir
	}
	cachePath := filepath.Join(cacheDir, gitCacheKey(request.URL, request.Revision))
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return GitResult{}, err
	}

	repo, cloned, err := openOrCloneGitRepository(ctx, cachePath, request.URL)
	if err != nil {
		return GitResult{}, err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return GitResult{}, fmt.Errorf("open repository %s worktree: %s", RedactURL(request.URL), redactGitError(err, request.URL))
	}
	if opts.Refresh && !cloned {
		if isDefaultGitRevision(request.Revision) {
			if err := pullGitRepository(ctx, worktree, request.URL); err != nil {
				return GitResult{}, err
			}
		} else if err := fetchGitRepository(ctx, repo, request.URL); err != nil {
			return GitResult{}, err
		}
	}
	revision, err := checkoutGitRevision(repo, worktree, request.Revision)
	if err != nil && !cloned {
		if fetchErr := fetchGitRepository(ctx, repo, request.URL); fetchErr != nil {
			return GitResult{}, fetchErr
		}
		revision, err = checkoutGitRevision(repo, worktree, request.Revision)
	}
	if err != nil {
		return GitResult{}, fmt.Errorf("checkout repository %s revision %q: %s", RedactURL(request.URL), strings.TrimSpace(request.Revision), redactGitError(err, request.URL))
	}

	return GitResult{Path: cachePath, Revision: revision}, nil
}

func DefaultGitCacheDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "argocd-local", "git"), nil
}

func gitCacheKey(repoURL, revision string) string {
	sum := sha256.Sum256([]byte(NormalizeURL(repoURL) + "\n" + strings.TrimSpace(revision)))
	return hex.EncodeToString(sum[:])
}

func openOrCloneGitRepository(ctx context.Context, cachePath, repoURL string) (*git.Repository, bool, error) {
	repo, err := git.PlainOpen(cachePath)
	if err == nil {
		return repo, false, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, false, fmt.Errorf("open cached repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL))
	}

	if err := os.RemoveAll(cachePath); err != nil {
		return nil, false, err
	}
	repo, err = git.PlainCloneContext(ctx, cachePath, false, &git.CloneOptions{
		URL:  repoURL,
		Tags: git.AllTags,
	})
	if err != nil {
		_ = os.RemoveAll(cachePath)
		return nil, false, fmt.Errorf("clone repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL))
	}
	return repo, true, nil
}

func fetchGitRepository(ctx context.Context, repo *git.Repository, repoURL string) error {
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Tags:       git.AllTags,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return fmt.Errorf("fetch repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL))
}

func pullGitRepository(ctx context.Context, worktree *git.Worktree, repoURL string) error {
	err := worktree.PullContext(ctx, &git.PullOptions{
		RemoteName: "origin",
		Force:      true,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return fmt.Errorf("fetch repository %s: %s", RedactURL(repoURL), redactGitError(err, repoURL))
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

func redactGitError(err error, repoURL string) string {
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
	return message
}
