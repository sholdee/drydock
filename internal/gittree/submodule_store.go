package gittree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ErrOfflineCacheMiss reports that a submodule store does not hold a
// commit and the network is off.
var ErrOfflineCacheMiss = errors.New("offline cache miss")

// StoreRequest is one commit of a submodule repository that
// OpenSubmoduleStore finds in a drydock cache entry.
type StoreRequest struct {
	// Path is the cache entry: a bare clone of URL that holds every
	// commit drydock fetched for that URL.
	Path string
	// URL is the submodule URL without userinfo, which the entry stores.
	URL    string
	Commit plumbing.Hash
	// Transport is the only go-git transport protocol the fetch may use.
	Transport string
	Auth      transport.AuthMethod
	// AllowNetwork lets OpenSubmoduleStore clone or fetch.
	AllowNetwork bool
}

// OpenSubmoduleStore returns the bare clone at request.Path when it holds
// the commit. Otherwise, with the network, it clones URL there or fetches
// it: all branches and tags first, then the commit itself into
// refs/drydock/<sha>, as `git submodule update` fetches a pinned commit.
// fetched reports that it used the network. Without the network, a
// missing entry or commit fails with ErrOfflineCacheMiss. One entry per
// URL keeps a .gitmodules file with many gitlinks into one repository at
// one clone.
func OpenSubmoduleStore(ctx context.Context, request StoreRequest) (*git.Repository, bool, error) {
	repo, err := git.PlainOpen(request.Path)
	if err == nil && hasCommit(repo, request.Commit) {
		return repo, false, nil
	}
	if !request.AllowNetwork {
		return nil, false, fmt.Errorf("%w: commit %s", ErrOfflineCacheMiss, request.Commit)
	}
	if err != nil {
		return cloneAndCheck(ctx, request)
	}
	if err := ScrubRemoteUserinfo(repo); err != nil {
		return nil, false, err
	}
	if err := fetchStore(ctx, repo, request); err != nil {
		return nil, true, err
	}
	if hasCommit(repo, request.Commit) {
		return repo, true, nil
	}
	if fetchStoreCommit(ctx, repo, request) == nil && hasCommit(repo, request.Commit) {
		return repo, true, nil
	}
	// A store that holds the commit but lost an object of its tree
	// answers every fetch with "already up to date": clone it again.
	return cloneAndCheck(ctx, request)
}

// cloneAndCheck clones the store again and fetches the commit when no
// branch or tag holds it.
func cloneAndCheck(ctx context.Context, request StoreRequest) (*git.Repository, bool, error) {
	repo, err := cloneStore(ctx, request)
	if err != nil {
		return nil, true, err
	}
	if hasCommit(repo, request.Commit) {
		return repo, true, nil
	}
	if err := fetchStoreCommit(ctx, repo, request); err != nil || !hasCommit(repo, request.Commit) {
		return nil, true, fmt.Errorf("commit %s not found: %w", request.Commit, errors.Join(err, plumbing.ErrObjectNotFound))
	}
	return repo, true, nil
}

// hasCommit reports whether repo holds commit and its tree. A store that
// lost an object is fetched again rather than trusted.
func hasCommit(repo *git.Repository, commit plumbing.Hash) bool {
	object, err := repo.CommitObject(commit)
	if err != nil {
		return false
	}
	_, err = object.Tree()
	return err == nil
}

func cloneStore(ctx context.Context, request StoreRequest) (*git.Repository, error) {
	if err := CheckEndpointTransport(request.URL, request.Transport); err != nil {
		return nil, err
	}
	parent := filepath.Dir(request.Path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	removeStaleClones(request.Path, time.Now())
	// The clone goes to a temporary directory next to the entry and moves
	// into place only when it is complete, so a clone that fails or stops
	// halfway never leaves a store that a later run trusts.
	temp, err := os.MkdirTemp(parent, filepath.Base(request.Path)+".clone-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if _, err := git.PlainCloneContext(ctx, temp, true, &git.CloneOptions{URL: request.URL, Auth: request.Auth, Tags: git.AllTags}); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(request.Path); err != nil {
		return nil, err
	}
	if err := os.Rename(temp, request.Path); err != nil {
		return nil, err
	}
	return git.PlainOpen(request.Path)
}

func fetchStore(ctx context.Context, repo *git.Repository, request StoreRequest) error {
	if err := checkStoreOrigin(repo, request.Transport); err != nil {
		return err
	}
	err := repo.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", Auth: request.Auth, Tags: git.AllTags})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

func fetchStoreCommit(ctx context.Context, repo *git.Repository, request StoreRequest) error {
	if err := checkStoreOrigin(repo, request.Transport); err != nil {
		return err
	}
	sha := request.Commit.String()
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       request.Auth,
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec(sha + ":refs/drydock/" + sha)},
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

// checkStoreOrigin fails unless go-git fetches every URL of the origin
// remote of repo over protocol.
func checkStoreOrigin(repo *git.Repository, protocol string) error {
	remote, err := repo.Remote("origin")
	if err != nil {
		return err
	}
	for _, url := range remote.Config().URLs {
		if err := CheckEndpointTransport(url, protocol); err != nil {
			return err
		}
	}
	return nil
}

// StaleCloneAge is how long a "<entry>.clone-*" directory must go without
// a change before drydock takes it for the leftover of a killed process.
// The cache commands use the same age.
const StaleCloneAge = time.Hour

// removeStaleClones removes the "<entry>.clone-*" siblings of the store at
// entry that nothing changed for StaleCloneAge. The caller holds the entry
// lock, so a clone of this process is not among them, and a clone of
// another live process changed within the hour.
func removeStaleClones(entry string, now time.Time) {
	matches, err := filepath.Glob(entry + ".clone-*")
	if err != nil {
		return
	}
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil || !info.IsDir() {
			continue
		}
		if newest, err := NewestChange(match); err == nil && now.Sub(newest) >= StaleCloneAge {
			_ = os.RemoveAll(match)
		}
	}
}

// NewestChange returns the latest modification time of path and of
// everything below it, without following symlinks.
func NewestChange(path string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, err
}

// ScrubRemoteUserinfo removes the userinfo from every remote URL of repo,
// so a cache entry that an older drydock wrote with a token in its
// .git/config loses it on its next use. It never shows the old URL.
func ScrubRemoteUserinfo(repo *git.Repository) error {
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	changed := false
	for _, remote := range cfg.Remotes {
		for i, url := range remote.URLs {
			if clean, _, found := SplitURLUserinfo(url); found {
				remote.URLs[i] = clean
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("remove userinfo from the cached remote URL: %w", err)
	}
	return nil
}
