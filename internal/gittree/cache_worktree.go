package gittree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// submoduleRecordName is the file in the Git directory of a drydock cache
// clone where drydock records the submodule content it wrote into the
// worktree. The Git directory belongs to drydock's cache, never to a user
// repository.
const submoduleRecordName = "drydock-submodules.json"

// submoduleRecord is what a cache clone's worktree holds besides the files
// of its commit: the gitlink directories that may hold submodule content.
type submoduleRecord struct {
	Commit   string   `json:"commit"`
	Enabled  bool     `json:"enabled"`
	Complete bool     `json:"complete"`
	Gitlinks []string `json:"gitlinks"`
}

func submoduleRecordPath(root string) string {
	return filepath.Join(root, ".git", submoduleRecordName)
}

func readSubmoduleRecord(root string) (submoduleRecord, bool) {
	data, err := os.ReadFile(submoduleRecordPath(root))
	if err != nil {
		return submoduleRecord{}, false
	}
	var record submoduleRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return submoduleRecord{}, false
	}
	return record, true
}

func writeSubmoduleRecord(root string, record submoduleRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(submoduleRecordPath(root), data, 0o644)
}

// ClearCacheSubmodules removes the submodule content that SyncCacheSubmodules
// wrote into the worktree of the drydock cache clone at root, unless it is
// the content of keep. A checkout of another commit then never meets it.
// A zero keep clears it always.
func ClearCacheSubmodules(root string, keep plumbing.Hash) error {
	record, ok := readSubmoduleRecord(root)
	if !ok {
		return nil
	}
	if !keep.IsZero() && record.Commit == keep.String() {
		return nil
	}
	for _, link := range record.Gitlinks {
		if err := removeGitlinkContent(root, link); err != nil {
			return err
		}
	}
	err := os.Remove(submoduleRecordPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// removeGitlinkContent removes the directory of the gitlink link below
// root, with everything in it. It never follows a symlink: when a
// component of the path is not a real directory, it leaves the path alone.
func removeGitlinkContent(root, link string) error {
	if !ValidRelPath(link) {
		return nil
	}
	current := root
	for component := range strings.SplitSeq(link, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() {
			return nil
		}
	}
	return os.RemoveAll(current)
}

// CacheSubmoduleRequest is the worktree of a drydock cache clone whose
// submodules SyncCacheSubmodules writes.
type CacheSubmoduleRequest struct {
	// Root is the worktree of the cache clone Repo.
	Root string
	Repo *git.Repository
	// Commit is the commit the worktree holds.
	Commit string
	// Enabled checks out submodule content. Without it, each gitlink is
	// an empty directory.
	Enabled bool
	// RemoteURL, Opener, and AllowFileTransport are the fields of
	// SubmoduleRequest.
	RemoteURL          string
	Opener             SubmoduleOpener
	AllowFileTransport bool
}

// SyncCacheSubmodules makes the worktree of a drydock cache clone hold the
// submodule content of its commit, as the Argo CD repo-server's checkout
// holds it: with Enabled, the content of every submodule
// (CheckoutSubmodules), and otherwise an empty directory for each gitlink.
// A worktree that already holds that content is left as it is.
func SyncCacheSubmodules(ctx context.Context, request CacheSubmoduleRequest) error {
	if record, ok := readSubmoduleRecord(request.Root); ok && record.Complete && record.Commit == request.Commit && record.Enabled == request.Enabled {
		return nil
	}
	if err := ClearCacheSubmodules(request.Root, plumbing.ZeroHash); err != nil {
		return err
	}
	commit, err := request.Repo.CommitObject(plumbing.NewHash(request.Commit))
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	links, err := Links(tree)
	if err != nil {
		return err
	}
	record := submoduleRecord{Commit: request.Commit, Enabled: request.Enabled}
	for _, link := range links {
		record.Gitlinks = append(record.Gitlinks, link.Path)
	}
	// Recorded before anything is written, so a run that stops halfway
	// leaves content that the next run clears.
	if err := writeSubmoduleRecord(request.Root, record); err != nil {
		return err
	}
	if err := MakeGitlinkDirs(request.Root, links); err != nil {
		return err
	}
	if request.Enabled {
		if _, err := CheckoutSubmodules(ctx, SubmoduleRequest{
			Root:                    request.Root,
			Tree:                    tree,
			RemoteURL:               request.RemoteURL,
			Opener:                  request.Opener,
			KeepOutOfBoundsSymlinks: true,
			AllowFileTransport:      request.AllowFileTransport,
		}); err != nil {
			return err
		}
	}
	record.Complete = true
	return writeSubmoduleRecord(request.Root, record)
}
