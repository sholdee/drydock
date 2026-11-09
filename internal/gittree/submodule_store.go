package gittree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	formatconfig "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// ErrOfflineCacheMiss reports that a submodule store does not hold a
// commit and the network is off.
var ErrOfflineCacheMiss = errors.New("offline cache miss")

// errStoreOfOtherRepository marks an entry whose origin names another
// repository than the request.
var errStoreOfOtherRepository = errors.New("the cache entry is a clone of another repository")

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
	// SameRepository reports whether an origin URL of an existing entry
	// names the repository of URL, with the URL normalization of the
	// cache key. Nil compares the URLs exactly.
	SameRepository func(originURL string) bool
}

// sameRepository applies request.SameRepository, or exact equality with
// request.URL.
func (request StoreRequest) sameRepository(originURL string) bool {
	if request.SameRepository != nil {
		return request.SameRepository(originURL)
	}
	return originURL == request.URL
}

// OpenSubmoduleStore returns the bare clone at request.Path when it holds
// the commit. Otherwise, with the network, it clones URL there or fetches
// it: all branches and tags first, then the commit itself into
// refs/drydock/<sha>, as `git submodule update` fetches a pinned commit.
// fetched reports that it used the network. Without the network, a
// missing entry or commit fails with ErrOfflineCacheMiss. One entry per
// URL keeps a .gitmodules file with many gitlinks into one repository at
// one clone. An entry whose origin names another repository
// (OriginMatches) is not used: with the network, drydock clones URL into
// it again, and without it, the commit is an offline cache miss. Fetches
// go to URL, never to the origin URL of the entry.
func OpenSubmoduleStore(ctx context.Context, request StoreRequest) (*git.Repository, bool, error) {
	repo, err := git.PlainOpen(request.Path)
	if err == nil {
		if scrubErr := ScrubRemoteUserinfo(repo); scrubErr != nil {
			return nil, false, scrubErr
		}
		if !OriginMatches(repo, request.sameRepository) {
			repo, err = nil, errStoreOfOtherRepository
		} else if hasCommit(repo, request.Commit) {
			return repo, false, nil
		}
	}
	if !request.AllowNetwork {
		return nil, false, fmt.Errorf("%w: commit %s", ErrOfflineCacheMiss, request.Commit)
	}
	if err != nil {
		return cloneAndCheck(ctx, request)
	}
	return fetchIntoStore(ctx, repo, request)
}

// fetchIntoStore fetches a commit that the store at request.Path lacks.
// Only a store that holds the commit but lost an object of its tree, which
// every fetch answers with "already up to date", is cloned again. A commit
// that the remote does not have, such as an unpushed commit that a pull
// request pins, fails without touching the store.
func fetchIntoStore(ctx context.Context, repo *git.Repository, request StoreRequest) (*git.Repository, bool, error) {
	if err := fetchStore(ctx, repo, request); err != nil {
		return nil, true, err
	}
	if hasCommit(repo, request.Commit) {
		return repo, true, nil
	}
	fetchErr := fetchStoreCommit(ctx, repo, request)
	if hasCommit(repo, request.Commit) {
		return repo, true, nil
	}
	if _, err := repo.CommitObject(request.Commit); err == nil {
		return cloneAndCheck(ctx, request)
	}
	return nil, true, fmt.Errorf("commit %s not found: %w", request.Commit, errors.Join(fetchErr, plumbing.ErrObjectNotFound))
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
	err := repo.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", RemoteURL: request.URL, Auth: request.Auth, Tags: git.AllTags})
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
		RemoteURL:  request.URL,
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
	// Read the directory rather than glob the path: a "[" or "*" in the
	// cache directory would otherwise change what matches.
	children, err := os.ReadDir(filepath.Dir(entry))
	if err != nil {
		return
	}
	prefix := filepath.Base(entry) + ".clone-"
	for _, child := range children {
		if !strings.HasPrefix(child.Name(), prefix) {
			continue
		}
		match := filepath.Join(filepath.Dir(entry), child.Name())
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

// ScrubRemoteUserinfo removes the userinfo from the URLs of the
// configuration of repo, so a cache entry that an older drydock wrote with
// a token in its config loses it on its next use. It rewrites the raw
// values (scrubConfig), not go-git's view, which applies url.insteadOf
// rules. The new file replaces the old one through a rename in the same
// directory. It never follows a symlink and never shows the old URL.
func ScrubRemoteUserinfo(repo *git.Repository) error {
	storage, ok := repo.Storer.(*filesystem.Storage)
	if !ok {
		return nil
	}
	path := filepath.Join(storage.Filesystem().Root(), "config")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		// drydock never writes through a symlink.
		return fmt.Errorf("the Git config of the cache entry is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg := formatconfig.New()
	if err := formatconfig.NewDecoder(bytes.NewReader(data)).Decode(cfg); err != nil {
		return fmt.Errorf("read the Git config of the cache entry: %w", err)
	}
	if !scrubConfig(cfg) {
		return nil
	}
	return writeConfigAtomic(path, cfg, info.Mode().Perm())
}

// scrubConfig removes the userinfo from every URL of cfg and reports
// whether it changed any: the remote url and pushurl values, the submodule
// url values, and the url.<base>.insteadOf rules, whose base and values
// can hold the same token.
func scrubConfig(cfg *formatconfig.Config) bool {
	changed := false
	for _, section := range cfg.Sections {
		changed = scrubOptions(section.Options) || changed
		for _, sub := range section.Subsections {
			if clean, _, found := SplitURLUserinfo(sub.Name); found && section.IsName("url") {
				sub.Name = clean
				changed = true
			}
			changed = scrubOptions(sub.Options) || changed
		}
	}
	return changed
}

// scrubOptions removes the userinfo from every option value that is a URL
// with userinfo.
func scrubOptions(options formatconfig.Options) bool {
	changed := false
	for _, option := range options {
		if clean, _, found := SplitURLUserinfo(option.Value); found {
			option.Value = clean
			changed = true
		}
	}
	return changed
}

// writeConfigAtomic writes cfg to a temporary file next to path and
// renames it over path.
func writeConfigAtomic(path string, cfg *formatconfig.Config, mode os.FileMode) error {
	var buf bytes.Buffer
	if err := formatconfig.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "config.drydock-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(buf.Bytes()); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
