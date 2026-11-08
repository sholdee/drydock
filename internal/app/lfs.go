package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// lfsPointerCode identifies the warning for Git LFS pointer files that
// drydock renders where the Argo CD repo-server renders their content.
const lfsPointerCode = "render.lfs-pointer"

// lfsRepositoryKeys returns the URL keys of the repository Secrets that
// set enableLfs. The Argo CD repo-server then fetches Git LFS objects and
// runs `git lfs checkout` after each checkout (util/git/client.go:710-719
// and 799-811), so the checkout holds the content of each pointer file.
func lfsRepositoryKeys(settings config.ArgoSettings) map[string]bool {
	var out map[string]bool
	for key, repository := range settings.HelmRepositories {
		if !repository.EnableLFS {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		url := repository.URL
		if url == "" {
			url = key
		}
		out[lfsURLKey(url)] = true
	}
	return out
}

// lfsURLKey compares repository URLs without letter case, scheme, user, or
// a .git suffix, as drydock compares a local remote with a repoURL.
func lfsURLKey(url string) string {
	return sourcepkg.CanonicalGitURLKey(strings.ToLower(strings.TrimSpace(url)))
}

// lfsEnabled reports whether the repository Secret of repoURL sets
// enableLfs.
func (p localProvider) lfsEnabled(repoURL string) bool {
	return len(p.lfsRepositories) > 0 && p.lfsRepositories[lfsURLKey(repoURL)]
}

// rootLFSState is the Git LFS state of the tree at a provider root whose
// repository sets enableLfs: the pointer files that it still holds where
// Argo CD holds their content.
type rootLFSState struct {
	snapshot bool
	missing  []string
	err      error
}

// snapshotLFSStates keeps, per snapshot root, the result of replacing its
// pointer files once, for every provider of the process that renders it.
var snapshotLFSStates sync.Map

type snapshotLFSEntry struct {
	once  sync.Once
	state rootLFSState
}

// prepareRootLFS checks the Git LFS pointer files of the tree at root when
// its repository sets enableLfs:
//
//   - a snapshot of a Git ref (drydock's own temporary tree) gets the
//     content of each pointer from the local repository's .git/lfs store,
//     as `git lfs checkout` does; a pointer whose object is missing stays,
//     and the render cache keys the root on the missing objects, so a later
//     `git lfs fetch` renders again;
//   - a working tree is the user's, which drydock never writes, so a file
//     that still holds the pointer of HEAD stays as it is.
func prepareRootLFS(ctx context.Context, root string, request BuildRequest, provider *localProvider) *rootLFSState {
	if len(provider.lfsRepositories) == 0 || !provider.rootRepositoryLFS() {
		return nil
	}
	if strings.TrimSpace(request.rootRevision) == "" {
		state := worktreeLFSState(ctx, root)
		return &state
	}
	if inside, _, err := pathsafety.IsInsideAny(root, []string{request.rootRepo}); err != nil || inside {
		return &rootLFSState{snapshot: true, err: fmt.Errorf("git ref snapshot %q is inside the repository", root)}
	}
	value, _ := snapshotLFSStates.LoadOrStore(filepath.Clean(root), &snapshotLFSEntry{})
	entry, _ := value.(*snapshotLFSEntry)
	entry.once.Do(func() {
		entry.state = snapshotLFSState(ctx, root, request.rootRepo, request.rootRevision)
	})
	if len(entry.state.missing) > 0 {
		provider.rootIdentity.Digest = lfsMissingDigest(entry.state.missing)
	}
	return &entry.state
}

// rootRepositoryLFS reports whether a remote of the root's repository
// names a repository that sets enableLfs.
func (p *localProvider) rootRepositoryLFS() bool {
	return slices.ContainsFunc(gitref.RemoteURLs(p.rootGitRepo), p.lfsEnabled)
}

func snapshotLFSState(ctx context.Context, root, repo, revision string) rootLFSState {
	state := rootLFSState{snapshot: true}
	pointers, err := gitref.LFSPointers(ctx, repo, revision)
	if err != nil {
		state.err = err
		return state
	}
	stores := gitref.LFSStoreDirs(repo)
	for _, pointer := range pointers {
		err := gittree.ReplaceLFSPointer(root, pointer, stores)
		switch {
		case errors.Is(err, gittree.ErrLFSObjectMissing):
			state.missing = append(state.missing, pointer.Path)
		case err != nil:
			state.err = err
			return state
		}
	}
	return state
}

func worktreeLFSState(ctx context.Context, root string) rootLFSState {
	var state rootLFSState
	pointers, err := gitref.LFSPointers(ctx, root, "HEAD")
	if err != nil {
		return state
	}
	for _, pointer := range pointers {
		target := filepath.Join(root, filepath.FromSlash(pointer.Path))
		// A symlinked parent directory could lead out of the tree.
		if inside, _, err := pathsafety.IsInsideAny(filepath.Dir(target), []string{root}); err != nil || !inside {
			continue
		}
		onDisk, ok := gittree.ReadLFSPointerFile(target)
		if ok && onDisk.OID == pointer.OID {
			state.missing = append(state.missing, pointer.Path)
		}
	}
	return state
}

// lfsMissingDigest keys a snapshot root on the pointer files it still
// holds.
func lfsMissingDigest(missing []string) string {
	sum := sha256.Sum256([]byte(strings.Join(missing, "\x00")))
	return "lfs-missing:" + hex.EncodeToString(sum[:])
}

// lfsRenderDiagnostics warns when the tree that source renders from holds
// Git LFS pointer files while its repository sets enableLfs, so that the
// Argo CD repo-server renders their content. It fails the source when
// drydock could not check the pointers.
func (p localProvider) lfsRenderDiagnostics(source render.ResolvedSource) ([]diagnostic.Diagnostic, error) {
	if !p.lfsEnabled(source.RepoURL) {
		return nil, nil
	}
	if p.isRepoRoot(source.RepoRoot) {
		return p.rootLFSDiagnostics(source)
	}
	if p.lfsFetched == nil {
		return nil, nil
	}
	value, ok := p.lfsFetched.Load(filepath.Clean(source.RepoRoot))
	if !ok {
		return nil, nil
	}
	pointers, _ := value.([]gittree.LFSPointer)
	paths := make([]string, 0, len(pointers))
	for _, pointer := range pointers {
		paths = append(paths, pointer.Path)
	}
	return lfsPointerDiagnostics(source.RepoURL, paths, "drydock does not download Git LFS objects"), nil
}

func (p localProvider) rootLFSDiagnostics(source render.ResolvedSource) ([]diagnostic.Diagnostic, error) {
	if p.rootLFS == nil {
		return nil, nil
	}
	if p.rootLFS.err != nil {
		return nil, fmt.Errorf("check Git LFS pointers of repository %s: %w", source.RepoURL, p.rootLFS.err)
	}
	hint := "run git lfs pull in the working tree"
	if p.rootLFS.snapshot {
		hint = "their objects are not in the repository's .git/lfs store; run git lfs fetch"
	}
	return lfsPointerDiagnostics(source.RepoURL, p.rootLFS.missing, hint), nil
}

func lfsPointerDiagnostics(repoURL string, paths []string, hint string) []diagnostic.Diagnostic {
	if len(paths) == 0 {
		return nil
	}
	shown := paths
	if len(shown) > 3 {
		shown = shown[:3]
	}
	return []diagnostic.Diagnostic{{
		Code:     lfsPointerCode,
		Severity: diagnostic.SeverityWarning,
		Category: "render",
		Message: fmt.Sprintf("repository %s sets enableLfs, so the Argo CD repo-server renders the content of its Git LFS files, but drydock renders %d of them as pointer files (%s); %s",
			sourcepkg.RedactURL(repoURL), len(paths), strings.Join(quoteStrings(shown), ", "), hint),
	}}
}

func quoteStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprintf("%q", value))
	}
	return out
}

// gitContentDiagnostics is the submodule and Git LFS warnings of source
// (submoduleRenderDiagnostics, lfsRenderDiagnostics).
func (p localProvider) gitContentDiagnostics(ctx context.Context, source render.ResolvedSource) ([]diagnostic.Diagnostic, error) {
	diags, err := p.submoduleRenderDiagnostics(ctx, source)
	if err != nil {
		return diags, err
	}
	lfsDiags, err := p.lfsRenderDiagnostics(source)
	return append(diags, lfsDiags...), err
}
