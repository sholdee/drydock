package gitref

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/sholdee/drydock/internal/gittree"
)

// snapshotSubmodules checks out the submodules of a snapshot when the
// request asks for them. Without that, it only lists the gitlinks.
func snapshotSubmodules(ctx context.Context, request Request, tree *object.Tree, root string) (gittree.SubmoduleResult, error) {
	if request.Submodules == nil {
		links, err := gittree.Links(tree)
		if err != nil {
			return gittree.SubmoduleResult{}, err
		}
		result := gittree.SubmoduleResult{}
		for _, link := range links {
			result.Gitlinks = append(result.Gitlinks, link.Path)
		}
		return result, nil
	}
	options := request.Submodules
	remoteURL := options.RemoteURL
	if strings.TrimSpace(remoteURL) == "" {
		remoteURL = DefaultRemoteURL(request.Repo)
	}
	return gittree.CheckoutSubmodules(ctx, gittree.SubmoduleRequest{
		Root:                    root,
		Tree:                    tree,
		RemoteURL:               remoteURL,
		Opener:                  gittree.ChainOpener(localModuleOpener(request.Repo), options.Opener),
		KeepOutOfBoundsSymlinks: request.KeepOutOfBoundsSymlinks,
		AllowFileTransport:      options.AllowFileTransport,
	})
}

// DefaultRemoteURL returns the URL of the remote origin of the repository
// at repoPath, or of its only remote, or "". The Argo CD repo-server
// resolves relative submodule URLs against origin, which holds the
// repoURL of the Application.
func DefaultRemoteURL(repoPath string) string {
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return ""
	}
	remotes, err := repo.Remotes()
	if err != nil {
		return ""
	}
	var only string
	for _, remote := range remotes {
		config := remote.Config()
		if len(config.URLs) == 0 {
			continue
		}
		if config.Name == "origin" {
			return config.URLs[0]
		}
		only = config.URLs[0]
	}
	if len(remotes) == 1 {
		return only
	}
	return ""
}

// localModuleOpener opens a submodule from the submodule store of the
// local repository at repoPath: .git/modules/<name>, and
// .git/modules/<name>/modules/<nested name> for a nested submodule, in the
// Git directory of the worktree and in the common Git directory. It reads
// object stores only and never runs git, so it works offline.
func localModuleOpener(repoPath string) gittree.SubmoduleOpener {
	dirs := localGitDirs(repoPath)
	return gittree.SubmoduleOpenerFunc(func(_ context.Context, ref gittree.SubmoduleRef) (*git.Repository, error) {
		rel, ok := gittree.ModuleStorePath(ref)
		if !ok {
			return nil, fmt.Errorf("submodule %q: unsafe module name", ref.Path)
		}
		for _, dir := range dirs {
			repo, ok := openModuleStore(filepath.Join(dir, "modules", filepath.FromSlash(rel)))
			if !ok {
				continue
			}
			if _, err := repo.CommitObject(ref.Commit); err == nil {
				return repo, nil
			}
		}
		return nil, fmt.Errorf("submodule %q: commit %s is not in the local submodule store", ref.Path, ref.Commit)
	})
}

// openModuleStore opens the Git directory dir as a repository without a
// worktree. dir must be a real directory, not a symlink.
func openModuleStore(dir string) (*git.Repository, bool) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	storage := filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())
	repo, err := git.Open(storage, nil)
	if err != nil {
		return nil, false
	}
	return repo, true
}

// localGitDirs returns the Git directory of the worktree at repoPath and
// its common Git directory (the main repository's for a linked worktree),
// without duplicates.
func localGitDirs(repoPath string) []string {
	gitDir := resolveGitDir(repoPath)
	if gitDir == "" {
		return nil
	}
	dirs := []string{gitDir}
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		dirs = append(dirs, filepath.Clean(common))
	}
	if len(dirs) == 2 && dirs[0] == dirs[1] {
		dirs = dirs[:1]
	}
	return dirs
}

// resolveGitDir returns the Git directory of the repository at repoPath:
// repoPath/.git when it is a directory, the target of a "gitdir:" file,
// or repoPath itself for a bare repository.
func resolveGitDir(repoPath string) string {
	dotGit := filepath.Join(repoPath, ".git")
	info, err := os.Lstat(dotGit)
	switch {
	case err == nil && info.IsDir():
		return dotGit
	case err == nil && info.Mode().IsRegular():
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return ""
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return ""
		}
		target = strings.TrimSpace(target)
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoPath, target)
		}
		return filepath.Clean(target)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "objects")); err == nil {
		return repoPath
	}
	return ""
}
