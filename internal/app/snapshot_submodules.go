package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/pathsafety"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// submoduleMissingCode identifies the run-level warning for submodules that
// a Git ref snapshot could not check out.
const submoduleMissingCode = "source.submodule-missing"

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
			// The fetched sources of every run lock the same entries.
			LockEntry: processCacheTargetLocks.LockGitEntry,
		}, nil),
		AllowFileTransport: allowFileSubmoduleTransport,
	}
}

// snapshotFill is the submodule checkout of one snapshot root. filled
// reports that the root holds submodule content: the settings of the last
// provider or discovery run had reposerver.enable.git.submodule on.
type snapshotFill struct {
	mu     sync.Mutex
	filled bool
	result gittree.SubmoduleResult
	err    error
}

// snapshotFills keeps, per snapshot root, the submodule checkout of the
// process.
var snapshotFills sync.Map

// fillSnapshotSubmodules makes the snapshot root of request hold the
// submodule content that the settings ask for, as the Argo CD repo-server
// does: with reposerver.enable.git.submodule on, the content of every
// submodule, and with it off, an empty directory for each gitlink. The
// settings can change while discovery runs, for example when a rendered
// argocd-cmd-params-cm turns the key off, so each call applies the value
// it gets: it fills an empty root, or empties a filled one. After a
// change, the out-of-bounds symlink checks of the root run again. It
// returns the checkout of the root, or nil when the root is no snapshot
// with gitlinks. A submodule that drydock cannot find (offline, or unreachable) stays empty
// and is listed in result.Missing. The snapshot is drydock's own temporary
// tree, never the repository.
func fillSnapshotSubmodules(ctx context.Context, root string, request BuildRequest, settings config.ArgoSettings) *snapshotFill {
	options := request.rootSnapshot.options
	if options == nil || strings.TrimSpace(request.rootRevision) == "" {
		return nil
	}
	enabled := config.ParseEnableGitSubmodules(settings.EnableGitSubmodules.Value)
	value, _ := snapshotFills.LoadOrStore(filepath.Clean(root), &snapshotFill{})
	fill, _ := value.(*snapshotFill)
	if fill.apply(ctx, root, request, *options, enabled) {
		request.symlinkBounds.invalidateUnder(root)
	}
	return fill
}

// checkState fails when the root no longer holds the submodule content
// that a provider built with enabled expects: another provider of the
// process changed it. drydock then fails the source loudly instead of
// reading a tree that changes under it.
func (f *snapshotFill) checkState(enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.filled != enabled {
		return fmt.Errorf("the submodule content of the Git ref snapshot changed while drydock rendered from it (reposerver.enable.git.submodule %t, the snapshot follows %t)", enabled, f.filled)
	}
	return nil
}

// apply fills or empties the submodules of root to match enabled, and
// reports whether it changed the tree.
func (f *snapshotFill) apply(ctx context.Context, root string, request BuildRequest, options gitref.SubmoduleOptions, enabled bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.filled == enabled {
		return false
	}
	if !enabled {
		f.err = gittree.ClearGitlinkDirs(root, request.rootSnapshot.gitlinks)
		f.result = gittree.SubmoduleResult{}
		f.filled = false
		return true
	}
	f.filled = true
	if inside, _, err := pathsafety.IsInsideAny(root, []string{request.rootRepo}); err != nil || inside {
		f.err = fmt.Errorf("git ref snapshot %q is inside the repository", root)
		return false
	}
	f.result, f.err = gitref.FillSnapshotSubmodules(ctx, gitref.FillRequest{
		Root:                    root,
		Repo:                    request.rootRepo,
		Revision:                request.rootRevision,
		Submodules:              options,
		KeepOutOfBoundsSymlinks: true,
	})
	return true
}

// missingDiagnostic is the run-level warning that lists the submodules
// that the snapshot could not check out, or false when there is none.
func (f *snapshotFill) missingDiagnostic() (diagnostic.Diagnostic, bool) {
	if f == nil {
		return diagnostic.Diagnostic{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.result.Missing) == 0 {
		return diagnostic.Diagnostic{}, false
	}
	paths := make([]string, 0, len(f.result.Missing))
	for _, missing := range f.result.Missing {
		paths = append(paths, strconv.Quote(missing.Path))
	}
	return diagnostic.Diagnostic{
		Code:     submoduleMissingCode,
		Severity: diagnostic.SeverityWarning,
		Category: "source",
		Message:  fmt.Sprintf("the Git ref snapshot lacks submodule %s, which drydock could not check out; every source whose path holds or is in one of them fails", strings.Join(paths, ", ")),
	}, true
}

// missingDigest keys a snapshot root on the submodules it could not check
// out, so a later run that finds them renders again.
func (f *snapshotFill) missingDigest() string {
	if f == nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.result.Missing) == 0 {
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
