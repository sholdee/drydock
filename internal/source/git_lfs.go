package source

import (
	"context"
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

// checkoutLFS lists the Git LFS pointer files of commit in repo when
// opts.LFS is set, outside submodules (gittree.ScanLFSPointers), and fills
// them in the checkout at cachePath (FillLFS). A pointer that it cannot
// fill holds the pointer blob again, also when an earlier run filled it.
func checkoutLFS(ctx context.Context, repo *git.Repository, cachePath, commit, repoURL string, opts GitOptions) ([]gittree.LFSPointer, gitlfs.Result, error) {
	if !opts.LFS {
		return nil, gitlfs.Result{}, nil
	}
	object, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, gitlfs.Result{}, err
	}
	tree, err := object.Tree()
	if err != nil {
		return nil, gitlfs.Result{}, err
	}
	pointers, err := gittree.ScanLFSPointers(ctx, tree)
	if err != nil || len(pointers) == 0 {
		return pointers, gitlfs.Result{}, err
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
