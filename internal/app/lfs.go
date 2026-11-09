package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
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

// lfsRepositoryKeys maps the URL keys of the repository Secrets that set
// enableLfs to their URLs. The Argo CD repo-server then fetches Git LFS
// objects and runs `git lfs checkout` after each checkout
// (util/git/client.go:710-719 and 799-811), so the checkout holds the
// content of each pointer file. When two Secrets share a key, an http or
// https URL wins, then the first in order.
func lfsRepositoryKeys(settings config.ArgoSettings) map[string]string {
	var out map[string]string
	keys := make([]string, 0, len(settings.HelmRepositories))
	for key := range settings.HelmRepositories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		repository := settings.HelmRepositories[key]
		if !repository.EnableLFS {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		url := strings.TrimSpace(repository.URL)
		if url == "" {
			url = key
		}
		urlKey := lfsURLKey(url)
		if existing, ok := out[urlKey]; ok && (isHTTPURL(existing) || !isHTTPURL(url)) {
			continue
		}
		out[urlKey] = url
	}
	return out
}

func isHTTPURL(url string) bool {
	lower := strings.ToLower(url)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// lfsURLKey compares repository URLs without letter case, scheme, user, or
// a .git suffix, as drydock compares a local remote with a repoURL.
func lfsURLKey(url string) string {
	return sourcepkg.CanonicalGitURLKey(strings.ToLower(strings.TrimSpace(url)))
}

// lfsEnabled reports whether the repository Secret of repoURL sets
// enableLfs.
func (p localProvider) lfsEnabled(repoURL string) bool {
	if len(p.lfsRepositories) == 0 {
		return false
	}
	_, ok := p.lfsRepositories[lfsURLKey(repoURL)]
	return ok
}

// lfsMissing is the Git LFS state of a checkout whose repository sets
// enableLfs: the pointer files that it still holds where Argo CD holds
// their content, and why.
type lfsMissing struct {
	paths   []string
	reasons []string
}

// lfsMissingHint is the end of the render.lfs-pointer warning for the
// reasons of gitlfs.Result.
func lfsMissingHint(reasons []string) string {
	if len(reasons) == 0 {
		return "drydock could not get their Git LFS objects"
	}
	return "drydock could not get their Git LFS objects: " + strings.Join(reasons, "; ")
}

// rootLFSState is the Git LFS state of the tree at a provider root whose
// repository sets enableLfs: the pointer files that it still holds where
// Argo CD holds their content.
type rootLFSState struct {
	snapshot bool
	missing  []string
	reasons  []string
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
//     content of each pointer, as `git lfs fetch` and `git lfs checkout`
//     give it: from the local repository's .git/lfs store, then from the
//     Git LFS store of the drydock Git cache, then, unless drydock runs
//     offline, from the Git LFS server of the repository
//     (sourcepkg.FillLFS). A pointer whose object drydock cannot get
//     stays, and the render cache keys the root on the missing objects,
//     whatever store filled the others, so a later run that gets them
//     renders again;
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
		entry.state = snapshotLFSState(ctx, root, request, provider.rootLFSURL())
	})
	if len(entry.state.missing) > 0 {
		provider.rootIdentity.Digest = lfsMissingDigest(entry.state.missing)
	}
	return &entry.state
}

// rootRepositoryLFS reports whether a remote of the root's repository
// names a repository that sets enableLfs.
func (p *localProvider) rootRepositoryLFS() bool {
	return p.rootLFSURL() != ""
}

// rootLFSURL returns the URL of the repository Secret that sets enableLfs
// for a remote of the root's repository: origin first, then the remotes in
// order. The Argo CD repo-server fetches that URL, which the user's remote
// can name in another form, such as SSH for an https Secret. It returns ""
// when no remote matches.
func (p *localProvider) rootLFSURL() string {
	remotes := gitref.RemoteURLs(p.rootGitRepo)
	if origin := gitref.DefaultRemoteURL(p.rootGitRepo); origin != "" {
		remotes = append([]string{origin}, remotes...)
	}
	for _, remote := range remotes {
		if url, ok := p.lfsRepositories[lfsURLKey(remote)]; ok {
			return url
		}
	}
	return ""
}

func snapshotLFSState(ctx context.Context, root string, request BuildRequest, repoURL string) rootLFSState {
	state := rootLFSState{snapshot: true}
	repo, revision := request.rootRepo, request.rootRevision
	pointers, lfsconfig, err := gitref.LFSTree(ctx, repo, revision)
	if err != nil || len(pointers) == 0 {
		state.err = err
		return state
	}
	result, err := sourcepkg.FillLFS(ctx, sourcepkg.LFSFill{
		Root:        root,
		RepoURL:     repoURL,
		Pointers:    pointers,
		LFSConfig:   lfsconfig,
		LocalStores: gitref.LFSStoreDirs(repo),
	}, sourcepkg.GitOptions{
		AllowNetwork: !request.Offline,
		CacheDir:     request.GitCacheDir,
		Credentials:  request.GitCredentials,
	})
	if err != nil {
		state.err = err
		return state
	}
	state.missing, state.reasons = result.Missing, result.Reasons
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

// lfsMissingDigest keys a checkout on the pointer files it still holds. It
// depends on those paths alone, so an object from the network keys the
// same as one from a local store.
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
	missing, _ := value.(lfsMissing)
	return lfsPointerDiagnostics(source.RepoURL, missing.paths, lfsMissingHint(missing.reasons)), nil
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
		hint = lfsMissingHint(p.rootLFS.reasons)
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

// fetchedLFSDigest keys a fetched Git checkout that holds Git LFS pointer
// files on the ones that it still holds (lfsMissingDigest), also when it
// holds none: a key without it is one that drydock wrote before it
// downloaded Git LFS objects, for a render of every pointer as a pointer.
// A checkout without pointer files keeps its key.
func fetchedLFSDigest(acquired sourcepkg.GitResult) string {
	if len(acquired.LFSPointers) == 0 {
		return ""
	}
	return lfsMissingDigest(acquired.LFSMissing)
}
