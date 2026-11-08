package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/pathsafety"
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
	// options say where the submodule content of the snapshot comes from
	// once drydock reads the settings of the tree (fillSnapshotSubmodules).
	options *gitref.SubmoduleOptions
}

func snapshotSubmodulesOf(result gitref.Result, options *gitref.SubmoduleOptions) snapshotSubmodules {
	gitlinks := result.Gitlinks
	if gitlinks == nil {
		gitlinks = []string{}
	}
	if len(gitlinks) == 0 {
		options = nil
	}
	return snapshotSubmodules{gitlinks: gitlinks, options: options}
}

// snapshotSubmoduleOptions says where a snapshot of a Git ref of the
// repository at repoPath gets submodule content: from the repository's own
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

// snapshotFill is the submodule checkout of one snapshot root.
type snapshotFill struct {
	once   sync.Once
	result gittree.SubmoduleResult
	err    error
}

// snapshotFills keeps, per snapshot root, the one submodule checkout of
// the process.
var snapshotFills sync.Map

// fillSnapshotSubmodules checks out the submodules of the snapshot root of
// request once settings are known, as the Argo CD repo-server does when
// reposerver.enable.git.submodule is on. With the setting off, it checks
// out nothing, and each gitlink stays an empty directory. It returns nil
// when there is nothing to check out. A submodule that drydock cannot find
// (offline, or unreachable) stays empty and is listed in result.Missing.
// The snapshot is drydock's own temporary tree, never the repository.
func fillSnapshotSubmodules(ctx context.Context, root string, request BuildRequest, settings config.ArgoSettings) *snapshotFill {
	options := request.rootSnapshot.options
	if options == nil || strings.TrimSpace(request.rootRevision) == "" {
		return nil
	}
	if !config.ParseEnableGitSubmodules(settings.EnableGitSubmodules.Value) {
		return nil
	}
	value, _ := snapshotFills.LoadOrStore(filepath.Clean(root), &snapshotFill{})
	fill, _ := value.(*snapshotFill)
	fill.once.Do(func() {
		if inside, _, err := pathsafety.IsInsideAny(root, []string{request.rootRepo}); err != nil || inside {
			fill.err = fmt.Errorf("git ref snapshot %q is inside the repository", root)
			return
		}
		fill.result, fill.err = gitref.FillSnapshotSubmodules(ctx, gitref.FillRequest{
			Root:                    root,
			Repo:                    request.rootRepo,
			Revision:                request.rootRevision,
			Submodules:              *options,
			KeepOutOfBoundsSymlinks: true,
		})
	})
	return fill
}

// missingDigest keys a snapshot root on the submodules it could not check
// out, so a later run that finds them renders again.
func (f *snapshotFill) missingDigest() string {
	if f == nil || len(f.result.Missing) == 0 {
		return ""
	}
	paths := make([]string, 0, len(f.result.Missing))
	for _, missing := range f.result.Missing {
		paths = append(paths, missing.Path)
	}
	sum := sha256.Sum256([]byte(strings.Join(paths, "\x00")))
	return "submodules-missing:" + hex.EncodeToString(sum[:])
}

// joinDigestMarkers joins the non-empty markers that key a snapshot root
// on content it lacks.
func joinDigestMarkers(markers ...string) string {
	var out []string
	for _, marker := range markers {
		if marker != "" {
			out = append(out, marker)
		}
	}
	return strings.Join(out, ",")
}
