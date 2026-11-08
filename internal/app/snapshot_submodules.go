package app

import (
	"github.com/sholdee/drydock/internal/gitref"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// allowFileSubmoduleTransport lets submodule URLs use the file transport,
// which the Argo CD repo-server's git refuses. Only tests set it, to serve
// submodules from local bare repositories.
var allowFileSubmoduleTransport = false

// snapshotSubmodules describes the submodules of a snapshot of a Git ref.
type snapshotSubmodules struct {
	// gitlinks are the gitlinks of the snapshot's commit, or nil when the
	// snapshot did not list them.
	gitlinks []string
	// checkedOut is the number of submodules whose content the snapshot
	// holds.
	checkedOut int
}

func snapshotSubmodulesOf(result gitref.Result) snapshotSubmodules {
	gitlinks := result.Gitlinks
	if gitlinks == nil {
		gitlinks = []string{}
	}
	return snapshotSubmodules{gitlinks: gitlinks, checkedOut: len(result.Submodules)}
}

// snapshotSubmoduleOptions says where a snapshot of a Git ref of the
// repository at repoPath gets submodule content. The Argo CD repo-server
// checks out submodules by default (reposerver.enable.git.submodule), and
// drydock takes the snapshot before it reads that setting from the tree,
// so a snapshot always holds submodule content: from the repository's own
// .git/modules store first, then from drydock's Git cache, then, unless
// drydock runs offline, from the submodule's remote into that cache. It
// never writes to the repository.
func (request DiffRequest) snapshotSubmoduleOptions(repoPath string) *gitref.SubmoduleOptions {
	remoteURL := gitref.DefaultRemoteURL(repoPath)
	return &gitref.SubmoduleOptions{
		RemoteURL: remoteURL,
		Opener: sourcepkg.SubmoduleCacheOpener(remoteURL, sourcepkg.GitOptions{
			AllowNetwork:                !request.Offline,
			CacheDir:                    request.GitCacheDir,
			Credentials:                 request.GitCredentials,
			AllowFileSubmoduleTransport: allowFileSubmoduleTransport,
		}, nil),
		AllowFileTransport: allowFileSubmoduleTransport,
	}
}
