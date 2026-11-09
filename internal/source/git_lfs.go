package source

import (
	"context"
	"os"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gitlfs"
	"github.com/sholdee/drydock/internal/gittree"
)

// LFSFill is one tree whose Git LFS pointer files FillLFS fills.
type LFSFill struct {
	// Root is the directory that holds the tree.
	Root string
	// RepoURL is the repository URL that the Argo CD repo-server fetches.
	// The Git LFS endpoint comes from it unless LFSConfig names one.
	RepoURL string
	// Pointers are the pointer files of the tree.
	Pointers []gittree.LFSPointer
	// LFSConfig is the .lfsconfig file of the tree's commit, or nil.
	LFSConfig []byte
	// LocalStores are .git/lfs directories to read before the drydock
	// cache, such as those of the local repository of a --ref snapshot.
	LocalStores []string
}

// FillLFS fills the Git LFS pointer files of fill, as `git lfs fetch` and
// `git lfs checkout` do in the Argo CD repo-server: from fill.LocalStores,
// then from the Git LFS store of the drydock Git cache, then, with
// opts.AllowNetwork, from the Git LFS server of the repository
// (gitlfs.Fill). The --git-* HTTP credentials go only to a batch endpoint
// that a --git-auth-host entry names, and a user and password in
// fill.RepoURL go only to the scheme, host, and port of that URL.
func FillLFS(ctx context.Context, fill LFSFill, opts GitOptions) (gitlfs.Result, error) {
	cacheDir, err := gitCacheDir(opts.CacheDir)
	if err != nil {
		cacheDir = ""
	}
	cleanURL, credentials := cloneURLAndCredentials(fill.RepoURL, opts.Credentials)
	endpoint, endpointErr := gitlfs.Endpoint(cleanURL, fill.LFSConfig)
	storeDir := ""
	if cacheDir != "" {
		storeDir = gitlfs.StoreDir(cacheDir)
	}
	return gitlfs.Fill(ctx, fill.Root, fill.Pointers, gitlfs.Options{
		LocalStores:  fill.LocalStores,
		CacheDir:     storeDir,
		AllowNetwork: opts.AllowNetwork,
		Endpoint:     endpoint,
		EndpointErr:  endpointErr,
		Credentials:  gitauth.Credentials(credentials),
	})
}

// lfsFilledMarker is the file in the .git directory of a cache checkout
// that says that a run with enableLfs may have filled its pointer files.
const lfsFilledMarker = "drydock-lfs-filled"

// checkoutLFS lists the Git LFS pointer files of commit in repo when
// opts.LFS is set, outside submodules (gittree.ScanLFSPointers), and fills
// them in the checkout at cachePath (FillLFS). A pointer that it cannot
// fill holds the pointer blob again, also when an earlier run filled it.
//
// Without opts.LFS, a checkout that an earlier run filled gets every
// pointer blob back, as the repo-server, which runs git with
// GIT_LFS_SKIP_SMUDGE=1, holds them. A checkout of HEAD is not checked out
// again, so its files would keep the content otherwise. The marker goes in
// place before drydock writes any content, so a run that stops halfway
// leaves it.
func checkoutLFS(ctx context.Context, repo *git.Repository, cachePath, commit, repoURL string, opts GitOptions) ([]gittree.LFSPointer, gitlfs.Result, error) {
	marker := filepath.Join(cachePath, git.GitDirName, lfsFilledMarker)
	_, markerErr := os.Lstat(marker)
	filled := markerErr == nil
	if !opts.LFS && !filled {
		return nil, gitlfs.Result{}, nil
	}
	tree, err := commitTree(repo, commit)
	if err != nil {
		return nil, gitlfs.Result{}, err
	}
	pointers, err := gittree.ScanLFSPointers(ctx, tree)
	if err != nil {
		return nil, gitlfs.Result{}, err
	}
	if !opts.LFS {
		if err := restorePointers(cachePath, tree, pointers, pointerPaths(pointers)); err != nil {
			return nil, gitlfs.Result{}, err
		}
		return nil, gitlfs.Result{}, os.Remove(marker)
	}
	if len(pointers) == 0 {
		return pointers, gitlfs.Result{}, nil
	}
	if !filled {
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			return nil, gitlfs.Result{}, err
		}
	}
	opts.CacheDir = filepath.Dir(cachePath)
	result, err := FillLFS(ctx, LFSFill{
		Root:      cachePath,
		RepoURL:   repoURL,
		Pointers:  pointers,
		LFSConfig: gittree.ReadLFSConfig(tree, gitlfs.MaxLFSConfigSize),
	}, opts)
	if err != nil {
		return nil, gitlfs.Result{}, err
	}
	return pointers, result, restorePointers(cachePath, tree, pointers, result.Missing)
}

func commitTree(repo *git.Repository, commit string) (*object.Tree, error) {
	object, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, err
	}
	return object.Tree()
}

func pointerPaths(pointers []gittree.LFSPointer) []string {
	out := make([]string, 0, len(pointers))
	for _, pointer := range pointers {
		out = append(out, pointer.Path)
	}
	return out
}

func restorePointers(root string, tree *object.Tree, pointers []gittree.LFSPointer, missing []string) error {
	byPath := make(map[string]gittree.LFSPointer, len(pointers))
	for _, pointer := range pointers {
		byPath[pointer.Path] = pointer
	}
	for _, path := range missing {
		if err := gittree.RestoreLFSPointer(root, tree, byPath[path]); err != nil {
			return err
		}
	}
	return nil
}
