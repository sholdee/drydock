package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/digestpath"
	"github.com/sholdee/drydock/internal/filedigest"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/render"
	"github.com/sholdee/drydock/internal/rendercache"
)

// SourceIdentity is one source's resolved identity in the persistent render
// cache key. Locator is a repository URL or chart repo+name, never a
// filesystem path. The provider root uses Kind "root" with an empty Locator.
type SourceIdentity struct {
	Kind     string `json:"kind"`
	Locator  string `json:"locator,omitempty"`
	Revision string `json:"revision,omitempty"`
	Digest   string `json:"digest,omitempty"`
}

const (
	sourceIdentityKindRoot           = "root"
	sourceIdentityKindGit            = "git"
	sourceIdentityKindChart          = "chart"
	sourceIdentityKindOCI            = "oci"
	sourceIdentityKindLocalInputs    = "local-inputs"
	sourceIdentityKindWorktreeInputs = "worktree-inputs"
)

type rootInputMode string

const (
	rootInputModeSnapshot rootInputMode = "snapshot"
	rootInputModeClean    rootInputMode = "clean"
	rootInputModeDirty    rootInputMode = "dirty"
	rootInputModeUnknown  rootInputMode = "unknown"
)

// persistentRenderCache is the run-scoped handle threaded through requests.
// Exactly one creator opens the store and owns the post-run eviction sweep.
type persistentRenderCache struct {
	store        *rendercache.Store
	fingerprint  rendercache.EngineFingerprint
	refresh      bool
	recordEvents bool

	mu                sync.Mutex
	pending           []cacheevent.Event
	changeSets        map[string]gitref.WorktreeChangeSetResult
	digests           map[string]gitref.PathDigestResult
	filesystemDigests map[string]filedigest.PathDigestResult
	contentDigests    *filedigest.ContentDigestCache
}

func (handle *persistentRenderCache) active() bool {
	return handle != nil && handle.store != nil
}

func (handle *persistentRenderCache) appendEvent(event cacheevent.Event) {
	if handle == nil || !handle.recordEvents {
		return
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	handle.pending = append(handle.pending, event)
}

// worktreeChangeSet returns the explicit Git worktree state and complete
// dirty-path enumeration for root. It is computed once per run per root;
// like the digest memos, the run-stability contract applies (mid-run edits
// after the first call are caught by store-time verification, not here).
func (handle *persistentRenderCache) worktreeChangeSet(ctx context.Context, root string) gitref.WorktreeChangeSetResult {
	if handle == nil {
		return gitref.WorktreeChangeSetResult{State: gitref.WorktreeStateUnknown}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return gitref.WorktreeChangeSetResult{State: gitref.WorktreeStateUnknown}
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.changeSets == nil {
		handle.changeSets = map[string]gitref.WorktreeChangeSetResult{}
	}
	if cached, ok := handle.changeSets[abs]; ok {
		return cached
	}
	result, err := gitref.WorktreeChangeSet(ctx, abs)
	if err != nil {
		result = gitref.WorktreeChangeSetResult{State: gitref.WorktreeStateUnknown}
	}
	if result.State == "" {
		result.State = gitref.WorktreeStateUnknown
	}
	handle.changeSets[abs] = result
	return result
}

func (handle *persistentRenderCache) committedPathDigest(ctx context.Context, repoPath, revision string, paths []gitref.PathDigestPath) (gitref.PathDigestResult, error) {
	if handle == nil {
		return gitref.PathDigestResult{}, fmt.Errorf("persistent render cache handle is nil")
	}
	key, normalized, err := committedPathDigestMemoKey(repoPath, revision, paths)
	if err != nil {
		return gitref.PathDigestResult{}, err
	}
	handle.mu.Lock()
	if result, ok := handle.digests[key]; ok {
		handle.mu.Unlock()
		return result, nil
	}
	handle.mu.Unlock()

	result, err := gitref.CommittedPathDigest(ctx, gitref.PathDigestInput{
		RepoPath: repoPath,
		Revision: revision,
		Paths:    normalized,
	})
	if err != nil {
		return gitref.PathDigestResult{}, err
	}

	handle.mu.Lock()
	if cached, ok := handle.digests[key]; ok {
		handle.mu.Unlock()
		return cached, nil
	}
	if handle.digests == nil {
		handle.digests = map[string]gitref.PathDigestResult{}
	}
	handle.digests[key] = result
	handle.mu.Unlock()
	return result, nil
}

func committedPathDigestMemoKey(repoPath, revision string, paths []gitref.PathDigestPath) (string, []gitref.PathDigestPath, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", nil, err
	}
	revision = strings.TrimSpace(revision)
	if revision == "" {
		revision = "HEAD"
	}
	byPath := make(map[string]bool, len(paths))
	for _, item := range paths {
		clean, err := digestpath.CanonicalGitPath(item.Path)
		if err != nil {
			return "", nil, err
		}
		if optional, ok := byPath[clean]; ok {
			byPath[clean] = optional && item.Optional
			continue
		}
		byPath[clean] = item.Optional
	}
	normalized := make([]gitref.PathDigestPath, 0, len(byPath))
	for clean, optional := range byPath {
		normalized = append(normalized, gitref.PathDigestPath{Path: clean, Optional: optional})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Path == normalized[j].Path {
			return !normalized[i].Optional && normalized[j].Optional
		}
		return normalized[i].Path < normalized[j].Path
	})
	var builder strings.Builder
	builder.WriteString(abs)
	builder.WriteByte(0)
	builder.WriteString(revision)
	for _, item := range normalized {
		builder.WriteByte(0)
		builder.WriteString(item.Path)
		if item.Optional {
			builder.WriteString(":optional")
		} else {
			builder.WriteString(":required")
		}
	}
	return builder.String(), normalized, nil
}

func (handle *persistentRenderCache) filesystemPathDigest(ctx context.Context, repoRoot string, paths []filedigest.PathDigestPath, forbiddenRoots []string) (filedigest.PathDigestResult, error) {
	if handle == nil {
		return filedigest.PathDigestResult{}, fmt.Errorf("persistent render cache handle is nil")
	}
	key, normalizedPaths, normalizedForbiddenRoots, err := filesystemPathDigestMemoKey(repoRoot, paths, forbiddenRoots)
	if err != nil {
		return filedigest.PathDigestResult{}, err
	}
	handle.mu.Lock()
	if result, ok := handle.filesystemDigests[key]; ok {
		handle.mu.Unlock()
		return result, nil
	}
	handle.mu.Unlock()

	result, err := filedigest.PathDigest(ctx, filedigest.PathDigestInput{
		RepoRoot:       repoRoot,
		Paths:          normalizedPaths,
		ForbiddenRoots: normalizedForbiddenRoots,
		ContentCache:   handle.contentDigests,
	})
	if err != nil {
		return filedigest.PathDigestResult{}, err
	}

	handle.mu.Lock()
	if cached, ok := handle.filesystemDigests[key]; ok {
		handle.mu.Unlock()
		return cached, nil
	}
	if handle.filesystemDigests == nil {
		handle.filesystemDigests = map[string]filedigest.PathDigestResult{}
	}
	handle.filesystemDigests[key] = result
	handle.mu.Unlock()
	return result, nil
}

func filesystemPathDigestMemoKey(repoRoot string, paths []filedigest.PathDigestPath, forbiddenRoots []string) (string, []filedigest.PathDigestPath, []string, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", nil, nil, err
	}
	normalizedPaths, err := normalizeFilesystemPathDigestPaths(paths)
	if err != nil {
		return "", nil, nil, err
	}
	normalizedForbiddenRoots, err := normalizeFilesystemDigestForbiddenRoots(forbiddenRoots)
	if err != nil {
		return "", nil, nil, err
	}

	keyInput := struct {
		Version        string                      `json:"version"`
		RepoRoot       string                      `json:"repoRoot"`
		ForbiddenRoots []string                    `json:"forbiddenRoots,omitempty"`
		Paths          []filedigest.PathDigestPath `json:"paths,omitempty"`
	}{
		Version:        "drydock.app.filesystem-path-digest-memo-key.v1",
		RepoRoot:       filepath.Clean(absRoot),
		ForbiddenRoots: normalizedForbiddenRoots,
		Paths:          normalizedPaths,
	}
	data, err := json.Marshal(keyInput)
	if err != nil {
		return "", nil, nil, err
	}
	return string(data), normalizedPaths, normalizedForbiddenRoots, nil
}

func normalizeFilesystemPathDigestPaths(paths []filedigest.PathDigestPath) ([]filedigest.PathDigestPath, error) {
	type pathKey struct {
		path       string
		markerKind string
	}
	byPath := make(map[pathKey]bool, len(paths))
	for _, item := range paths {
		clean, err := canonicalFilesystemDigestPath(item.Path)
		if err != nil {
			return nil, err
		}
		if strings.Contains(item.MarkerKind, "\x00") {
			return nil, fmt.Errorf("marker kind contains nul")
		}
		key := pathKey{path: clean, markerKind: item.MarkerKind}
		if optional, ok := byPath[key]; ok {
			byPath[key] = optional && item.Optional
			continue
		}
		byPath[key] = item.Optional
	}
	normalized := make([]filedigest.PathDigestPath, 0, len(byPath))
	for key, optional := range byPath {
		normalized = append(normalized, filedigest.PathDigestPath{Path: key.path, Optional: optional, MarkerKind: key.markerKind})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Path == normalized[j].Path {
			if normalized[i].MarkerKind == normalized[j].MarkerKind {
				return !normalized[i].Optional && normalized[j].Optional
			}
			return normalized[i].MarkerKind < normalized[j].MarkerKind
		}
		return normalized[i].Path < normalized[j].Path
	})
	return normalized, nil
}

func canonicalFilesystemDigestPath(value string) (string, error) {
	return digestpath.CanonicalFilesystemPath(value)
}

func normalizeFilesystemDigestForbiddenRoots(forbiddenRoots []string) ([]string, error) {
	normalized := make([]string, 0, len(forbiddenRoots))
	for _, root := range forbiddenRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, filepath.Clean(abs))
	}
	sort.Strings(normalized)
	return normalized, nil
}

// release runs the post-run eviction sweep only when this run wrote at least
// one entry, then returns deferred observability events.
func (handle *persistentRenderCache) release() []cacheevent.Event {
	if handle == nil {
		return nil
	}
	handle.mu.Lock()
	events := append([]cacheevent.Event(nil), handle.pending...)
	handle.pending = nil
	handle.mu.Unlock()
	if !handle.active() || handle.store.Writes() == 0 {
		return events
	}
	result, err := handle.store.Sweep()
	if err != nil {
		if handle.recordEvents {
			events = append(events, cacheevent.Event{Source: cacheevent.SourceRender, Action: cacheevent.ActionError, Error: err.Error()})
		}
		return events
	}
	if handle.recordEvents {
		for _, key := range result.EvictedKeys {
			events = append(events, cacheevent.Event{Source: cacheevent.SourceRender, Action: cacheevent.ActionEvict, Revision: renderCacheKeyPrefix(key)})
		}
	}
	return events
}

func renderCacheKeyPrefix(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12]
}

// newPersistentRenderCache opens or initializes the run-scoped persistent render
// cache handle. Callers must pre-fold the acquisition refresh flags via
// renderCacheRefreshOptions; go through the ensure functions rather than calling
// this directly.
func newPersistentRenderCache(options RenderCacheOptions, recordEvents bool) *persistentRenderCache {
	if !options.RenderCacheEnabled {
		return nil
	}
	handle := &persistentRenderCache{
		fingerprint:       options.EngineFingerprint,
		refresh:           options.RefreshRenders,
		recordEvents:      recordEvents,
		changeSets:        map[string]gitref.WorktreeChangeSetResult{},
		digests:           map[string]gitref.PathDigestResult{},
		filesystemDigests: map[string]filedigest.PathDigestResult{},
		contentDigests:    filedigest.NewContentDigestCache(),
	}
	if !options.EngineFingerprint.Known() {
		handle.appendEvent(cacheevent.Event{Source: cacheevent.SourceRender, Action: cacheevent.ActionDisabled, Reason: "dev-build"})
		return handle
	}
	store, err := rendercache.Open(options.RenderCacheDir, options.RenderCacheMaxBytes)
	if err != nil {
		handle.appendEvent(cacheevent.Event{Source: cacheevent.SourceRender, Action: cacheevent.ActionError, Error: err.Error()})
		return handle
	}
	handle.store = store
	return handle
}

func noRenderCacheEvents() []cacheevent.Event { return nil }

// renderCacheRefreshOptions folds the acquisition refresh flags into the
// persistent tier's refresh bit. The in-memory key already varies on these
// flags; the persistent tier instead bypasses lookup and overwrites the entry,
// so an explicitly refreshed chart, git ref, or remote resource is always
// re-rendered. Offline is excluded: offline runs want cache hits.
func renderCacheRefreshOptions(options RenderCacheOptions, acquisition AcquisitionOptions) RenderCacheOptions {
	options.RefreshRenders = options.RefreshRenders ||
		acquisition.RefreshCharts ||
		acquisition.RefreshGit ||
		acquisition.RefreshRemoteResources
	return options
}

// ensurePersistentRenderCache opens or reuses the run-scoped persistent render
// cache handle. Only the creator's release sweeps and surfaces events.
func ensurePersistentRenderCache(request BuildRequest) (BuildRequest, func() []cacheevent.Event) {
	if request.persistentRenderCache != nil {
		return request, noRenderCacheEvents
	}
	handle := newPersistentRenderCache(renderCacheRefreshOptions(request.RenderCacheOptions, request.AcquisitionOptions), request.RecordCacheEvents)
	if handle == nil {
		return request, noRenderCacheEvents
	}
	request.persistentRenderCache = handle
	return request, handle.release
}

// ensureDiffPersistentRenderCache is the DiffRequest twin; buildRequest copies
// the handle into both side BuildRequests.
func ensureDiffPersistentRenderCache(request DiffRequest) (DiffRequest, func() []cacheevent.Event) {
	if request.persistentRenderCache != nil {
		return request, noRenderCacheEvents
	}
	handle := newPersistentRenderCache(renderCacheRefreshOptions(request.RenderCacheOptions, request.AcquisitionOptions), request.RecordCacheEvents)
	if handle == nil {
		return request, noRenderCacheEvents
	}
	request.persistentRenderCache = handle
	return request, handle.release
}

const (
	renderCacheReasonIneligibleSource     = "ineligible-source"
	renderCacheReasonDirtyWorktree        = "dirty-worktree"
	renderCacheReasonInputGraph           = "input-graph-unsupported"
	renderCacheReasonHelmGlobInputs       = "helm-glob-inputs"
	renderCacheReasonDuplicateApplication = "duplicate-application"
	renderCacheReasonInputDigest          = "input-digest-error"
	renderCacheReasonPinUnstable          = "pin-unstable"
	renderCacheReasonRecordsInactive      = "records-inactive"
	renderCacheReasonRefresh              = "refresh"
	renderCacheReasonInputsChanged        = "inputs-changed"
)

// collectSourceIdentities is the prepare phase: resolve every source and
// ref-only source through the real acquisition path and build the ordered
// identity vector: all plan.Sources by index, then plan.Refs sorted by ref key.
// A failure or empty identity makes the application persistence-ineligible;
// it never fails the render.
func collectSourceIdentities(ctx context.Context, handle *persistentRenderCache, provider localProvider, plan PlanResult) ([]SourceIdentity, string, bool) {
	byIndex := make(map[int]SourceIdentity, len(plan.Sources))
	var resolve func(SourcePlan) (SourceIdentity, string, bool)
	resolve = func(sourcePlan SourcePlan) (SourceIdentity, string, bool) {
		if identity, ok := byIndex[sourcePlan.Index]; ok {
			return identity, "", true
		}
		identity, reason, ok := sourceIdentityForPlan(ctx, handle, provider, plan, sourcePlan, resolve)
		if ok {
			byIndex[sourcePlan.Index] = identity
		}
		return identity, reason, ok
	}
	for _, sourcePlan := range plan.Sources {
		identity, reason, ok := resolve(sourcePlan)
		if !ok {
			if reason != "" {
				return nil, reason, false
			}
			if identity.Kind == sourceIdentityKindRoot {
				return nil, renderCacheReasonDirtyWorktree, false
			}
			return nil, renderCacheReasonIneligibleSource, false
		}
	}
	vector := make([]SourceIdentity, 0, len(plan.Sources)+len(plan.Refs))
	for _, sourcePlan := range plan.Sources {
		vector = append(vector, byIndex[sourcePlan.Index])
	}
	refKeys := make([]string, 0, len(plan.Refs))
	for refKey := range plan.Refs {
		refKeys = append(refKeys, refKey)
	}
	sort.Strings(refKeys)
	for _, refKey := range refKeys {
		vector = append(vector, byIndex[plan.Refs[refKey].Index])
	}
	return vector, "", true
}

func sourceIdentityForPlan(ctx context.Context, handle *persistentRenderCache, provider localProvider, plan PlanResult, sourcePlan SourcePlan, resolve func(SourcePlan) (SourceIdentity, string, bool)) (SourceIdentity, string, bool) {
	if sourcePlan.RefOnly {
		if candidate, ok := planSameRevisionRenderSource(plan, sourcePlan); ok {
			return resolve(candidate)
		}
	}
	_, identity, err := provider.resolveSourceRootIdentity(ctx, render.ResolvedSource{
		Path:           sourcePlan.Source.Path,
		Chart:          sourcePlan.Source.Chart,
		RepoURL:        sourcePlan.Source.RepoURL,
		TargetRevision: sourcePlan.Source.TargetRevision,
		ExplicitType:   sourcePlan.ExplicitType,
	})
	if err != nil {
		return identity, "", false
	}
	if identity.Kind != sourceIdentityKindRoot {
		if strings.TrimSpace(identity.Revision) == "" {
			return identity, "", false
		}
		return identity, "", true
	}
	return rootSourceIdentityForPlan(ctx, handle, provider, plan, sourcePlan, identity)
}

func rootSourceIdentityForPlan(ctx context.Context, handle *persistentRenderCache, provider localProvider, plan PlanResult, sourcePlan SourcePlan, identity SourceIdentity) (SourceIdentity, string, bool) {
	switch provider.rootInputMode {
	case rootInputModeSnapshot:
		if strings.TrimSpace(identity.Revision) == "" {
			return identity, renderCacheReasonIneligibleSource, false
		}
		return identity, "", true
	case rootInputModeClean:
		if strings.TrimSpace(identity.Revision) == "" {
			return identity, renderCacheReasonDirtyWorktree, false
		}
		localIdentity, reason, ok := localInputSourceIdentity(ctx, handle, provider, plan, sourcePlan, identity.Revision)
		return localIdentity, reason, ok
	case rootInputModeDirty:
		localIdentity, reason, ok := worktreeInputSourceIdentity(ctx, handle, provider, plan, sourcePlan)
		return localIdentity, reason, ok
	case rootInputModeUnknown:
		return identity, renderCacheReasonIneligibleSource, false
	default:
		return identity, renderCacheReasonIneligibleSource, false
	}
}

func localInputSourceIdentity(ctx context.Context, handle *persistentRenderCache, provider localProvider, plan PlanResult, sourcePlan SourcePlan, revision string) (SourceIdentity, string, bool) {
	// Clean mode: globs are safe because Helm expansion happens against
	// committed-equal content and any expansion change rotates the path set
	// (and therefore the cache key) via the committed digest.
	paths, _, err := localInputDigestPathsForSource(ctx, plan, sourcePlan, provider.repoRoot)
	if err != nil {
		return SourceIdentity{}, renderCacheReasonInputGraph, false
	}
	return committedSourceIdentityForPaths(ctx, handle, provider, revision, paths)
}

// committedSourceIdentityForPaths digests paths at revision and records the
// committed store-time verification. Shared by clean mode and by dirty
// mode's untouched-path-set shortcut.
func committedSourceIdentityForPaths(ctx context.Context, handle *persistentRenderCache, provider localProvider, revision string, paths []gitref.PathDigestPath) (SourceIdentity, string, bool) {
	result, err := handle.committedPathDigest(ctx, provider.repoRoot, revision, paths)
	if err != nil {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	if strings.TrimSpace(result.Digest) == "" {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	provider.inputVerifier.addCommitted(provider, revision, paths)
	return SourceIdentity{
		Kind:   sourceIdentityKindLocalInputs,
		Digest: result.Digest,
	}, "", true
}

func worktreeInputSourceIdentity(ctx context.Context, handle *persistentRenderCache, provider localProvider, plan PlanResult, sourcePlan SourcePlan) (SourceIdentity, string, bool) {
	paths, helmGlobs, err := localInputDigestPathsForSource(ctx, plan, sourcePlan, provider.repoRoot)
	if err != nil {
		return SourceIdentity{}, renderCacheReasonInputGraph, false
	}
	if !helmGlobs && dirtyWorktreeCommittedShortcut(provider, paths) {
		// No dirty path touches this source's digested inputs, so its
		// worktree content provably matches the commit: reuse the committed
		// key (identical to clean mode after Revision normalization) and the
		// committed store-time verification.
		return committedSourceIdentityForPaths(ctx, handle, provider, provider.rootIdentity.Revision, paths)
	}
	if helmGlobs {
		// Dirty mode: globs are rejected because the expansion set depends on
		// the current worktree and is not provable from committed content
		// alone — a new untracked file matching the glob would be invisible
		// to the path-set intersection above.
		return SourceIdentity{}, renderCacheReasonHelmGlobInputs, false
	}
	result, err := handle.filesystemPathDigest(ctx, provider.repoRoot, filesystemInputDigestPaths(paths), filesystemDigestForbiddenRoots(provider))
	if err != nil {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	if strings.TrimSpace(result.Digest) == "" {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	provider.inputVerifier.addFilesystem(provider, paths, result.Digest)
	return SourceIdentity{
		Kind:   sourceIdentityKindWorktreeInputs,
		Digest: result.Digest,
	}, "", true
}

func helmDigestInputsHaveGlob(valueFiles []string, fileParameters []argoappv1.HelmFileParameter) bool {
	if slices.ContainsFunc(valueFiles, helmInputPathHasGlob) {
		return true
	}
	for _, parameter := range fileParameters {
		if helmInputPathHasGlob(parameter.Path) {
			return true
		}
	}
	return false
}

func helmInputPathHasGlob(inputPath string) bool {
	return strings.ContainsAny(inputPath, "*?[")
}

func filesystemInputDigestPaths(paths []gitref.PathDigestPath) []filedigest.PathDigestPath {
	if len(paths) == 0 {
		return nil
	}
	out := make([]filedigest.PathDigestPath, 0, len(paths))
	for _, item := range paths {
		out = append(out, filedigest.PathDigestPath{Path: item.Path, Optional: item.Optional})
	}
	return out
}

// dirtyPathsTouch reports whether any dirty repository-relative path equals
// or falls under any digested path. Digested directory paths cover their
// subtree; dirty entries are file paths (plus nested-.git directory
// markers). sortedDirtyPaths must be sorted. Canonicalization failures and
// "." fail closed (touched). If the enumeration is ever optimized to record
// untracked DIRECTORIES as single markers, this helper must additionally
// match ancestors of each digested path — today only nested .git uses a
// directory marker and digest paths cannot live under .git.
// Note: on case-insensitive filesystems a case-only rename yields a dirty
// set containing only the on-disk casing; committed-name intersection may
// not match. This is correct today (content is byte-identical on such
// filesystems) and consistent with the committed-digest behavior.
func dirtyPathsTouch(sortedDirtyPaths []string, paths []gitref.PathDigestPath) bool {
	if len(sortedDirtyPaths) == 0 {
		return false
	}
	for _, item := range paths {
		clean, err := digestpath.CanonicalGitPath(item.Path)
		if err != nil {
			return true
		}
		if clean == "." {
			return true
		}
		i := sort.SearchStrings(sortedDirtyPaths, clean)
		if i < len(sortedDirtyPaths) && sortedDirtyPaths[i] == clean {
			return true
		}
		j := sort.SearchStrings(sortedDirtyPaths, clean+"/")
		if j < len(sortedDirtyPaths) && strings.HasPrefix(sortedDirtyPaths[j], clean+"/") {
			return true
		}
	}
	return false
}

// dirtyWorktreeCommittedShortcut reports whether a dirty-mode path set may
// use the committed-digest flow: the dirty enumeration must be present
// (empty means a hand-built provider without enumeration — fail safe), the
// root revision known, and no dirty path touching the set.
func dirtyWorktreeCommittedShortcut(provider localProvider, paths []gitref.PathDigestPath) bool {
	if len(provider.rootDirtyPaths) == 0 {
		return false
	}
	if strings.TrimSpace(provider.rootIdentity.Revision) == "" {
		return false
	}
	return !dirtyPathsTouch(provider.rootDirtyPaths, paths)
}

func filesystemDigestForbiddenRoots(provider localProvider) []string {
	candidates := append([]string(nil), provider.chartForbiddenRoots...)
	candidates = append(candidates, provider.remoteResourceForbiddenRoots...)
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strictPathInsideRoot(provider.repoRoot, candidate) {
			roots = appendUniqueString(roots, candidate)
		}
	}
	return roots
}

// renderInputVerification re-checks one digest path set after the render.
// Dirty mode records the filesystem baseline digest behind the key; clean
// mode records the committed revision and path set so store() can prove the
// worktree still matches the commit the key was derived from.
type renderInputVerification struct {
	repoRoot       string
	revision       string
	committedPaths []gitref.PathDigestPath
	paths          []filedigest.PathDigestPath
	forbiddenRoots []string
	digest         string
}

type renderInputVerifier struct {
	entries []renderInputVerification
}

func (v *renderInputVerifier) addFilesystem(provider localProvider, paths []gitref.PathDigestPath, digest string) {
	if v == nil {
		return
	}
	v.entries = append(v.entries, renderInputVerification{
		repoRoot:       provider.repoRoot,
		paths:          filesystemInputDigestPaths(paths),
		forbiddenRoots: filesystemDigestForbiddenRoots(provider),
		digest:         digest,
	})
}

func (v *renderInputVerifier) addCommitted(provider localProvider, revision string, paths []gitref.PathDigestPath) {
	if v == nil {
		return
	}
	v.entries = append(v.entries, renderInputVerification{
		repoRoot:       provider.repoRoot,
		revision:       revision,
		committedPaths: append([]gitref.PathDigestPath(nil), paths...),
	})
}

func strictPathInsideRoot(root, candidate string) bool {
	root = strings.TrimSpace(root)
	candidate = strings.TrimSpace(candidate)
	if root == "" || candidate == "" {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(absRoot), filepath.Clean(absCandidate))
	if err != nil {
		return false
	}
	return rel != "." && !pathsafety.RelEscapes(rel) && !filepath.IsAbs(rel)
}

func localInputDigestPathsForSource(ctx context.Context, plan PlanResult, sourcePlan SourcePlan, repoRoot string) ([]gitref.PathDigestPath, bool, error) {
	sourcePath := strings.TrimSpace(sourcePlan.Source.Path)
	if sourcePath == "" {
		return nil, false, fmt.Errorf("local source path is empty")
	}
	clean, err := cleanLocalSourcePath(sourcePath)
	if err != nil {
		return nil, false, err
	}
	paths, helmGlobs, err := localToolInputDigestPaths(ctx, plan, sourcePlan, repoRoot, clean)
	if err != nil {
		return nil, false, err
	}
	// PrepareSource merges these override files into the source spec before
	// rendering, so their content is a render input for every tool. Optional:
	// absence is itself a digest record, so adding or deleting one rotates
	// the key.
	return append(paths, argocdSourceOverrideDigestPaths(clean, plan.Application.Name)...), helmGlobs, nil
}

func argocdSourceOverrideDigestPaths(sourcePath, appName string) []gitref.PathDigestPath {
	sourcePath = filepath.ToSlash(sourcePath)
	paths := []gitref.PathDigestPath{{Path: path.Join(sourcePath, repoSourceFile), Optional: true}}
	if strings.TrimSpace(appName) != "" {
		paths = append(paths, gitref.PathDigestPath{Path: path.Join(sourcePath, fmt.Sprintf(appSourceFile, appName)), Optional: true})
	}
	return paths
}

// localToolInputDigestPaths picks the per-tool input enumerator using the
// same classification the render path uses (selectLocalRenderer), so the
// digested inputs cannot diverge from the files the render actually reads.
// The returned bool reports whether any Helm input path contains glob
// metacharacters.
func localToolInputDigestPaths(ctx context.Context, plan PlanResult, sourcePlan SourcePlan, repoRoot, clean string) ([]gitref.PathDigestPath, bool, error) {
	switch sourcePlan.ExplicitType {
	case argoappv1.ApplicationSourceTypeDirectory, argoappv1.ApplicationSourceTypeHelm, argoappv1.ApplicationSourceTypeKustomize, "":
	case argoappv1.ApplicationSourceTypePlugin:
		return nil, false, fmt.Errorf("source type %q is not persistent-cache eligible", sourcePlan.ExplicitType)
	default:
		return nil, false, fmt.Errorf("source type %q is not persistent-cache eligible", sourcePlan.ExplicitType)
	}
	renderer, err := selectLocalRenderer(render.ResolvedSource{
		RepoRoot:     repoRoot,
		Path:         clean,
		ExplicitType: sourcePlan.ExplicitType,
	})
	if err != nil {
		return nil, false, err
	}
	switch renderer.(type) {
	case render.DirectoryRenderer:
		paths, err := localDirectoryInputDigestPaths(plan, sourcePlan, repoRoot)
		return paths, false, err
	case render.HelmRenderer:
		return localHelmInputDigestPaths(plan, sourcePlan, repoRoot)
	case render.KustomizeRenderer:
		paths, err := localKustomizeInputDigestPaths(ctx, plan, sourcePlan, repoRoot)
		return paths, false, err
	default:
		return nil, false, fmt.Errorf("renderer %T is not persistent-cache eligible", renderer)
	}
}

func localKustomizeInputDigestPaths(ctx context.Context, plan PlanResult, sourcePlan SourcePlan, repoRoot string) ([]gitref.PathDigestPath, error) {
	opts, err := renderOptions(plan.Application, sourcePlan.Source, CapabilityOptions{})
	if err != nil {
		return nil, err
	}
	source := resolvedSourceForPlan(sourcePlan)
	if source.RepoRoot == "" {
		source.RepoRoot = repoRoot
	}
	return render.KustomizeInputDigestPaths(ctx, source, opts)
}

func localDirectoryInputDigestPaths(plan PlanResult, sourcePlan SourcePlan, repoRoot string) ([]gitref.PathDigestPath, error) {
	opts, err := renderOptions(plan.Application, sourcePlan.Source, CapabilityOptions{})
	if err != nil {
		return nil, err
	}
	source := resolvedSourceForPlan(sourcePlan)
	if source.RepoRoot == "" {
		source.RepoRoot = repoRoot
	}
	return render.DirectoryInputDigestPaths(source, opts)
}

func localHelmInputDigestPaths(plan PlanResult, sourcePlan SourcePlan, repoRoot string) ([]gitref.PathDigestPath, bool, error) {
	opts, err := renderOptions(plan.Application, sourcePlan.Source, CapabilityOptions{})
	if err != nil {
		return nil, false, err
	}
	valueFiles, fileParameters, err := localHelmDigestInputPaths(plan, sourcePlan, opts)
	if err != nil {
		return nil, false, err
	}
	helmGlobs := helmDigestInputsHaveGlob(valueFiles, fileParameters)
	opts.ValueFiles = valueFiles
	opts.HelmFileParameters = fileParameters
	refRoots, _, err := renderRefsForSource(plan, sourcePlan, helmRefInputPaths(opts))
	if err != nil {
		return nil, helmGlobs, err
	}
	opts.RefRoots = mergeRefRoots(opts.RefRoots, refRoots)
	collected, err := render.CollectHelmLocalInputPaths(render.HelmLocalInputOptions{
		RepoRoot: repoRoot,
		Source:   resolvedSourceForPlan(sourcePlan),
		Options:  opts,
	})
	if err != nil {
		return nil, helmGlobs, err
	}
	paths := make([]gitref.PathDigestPath, 0, len(collected))
	for _, item := range collected {
		paths = append(paths, gitref.PathDigestPath{Path: item.Path, Optional: item.Optional})
	}
	return paths, helmGlobs, nil
}

func localHelmDigestInputPaths(plan PlanResult, sourcePlan SourcePlan, opts render.RenderOptions) ([]string, []argoappv1.HelmFileParameter, error) {
	valueFiles := make([]string, 0, len(opts.ValueFiles))
	for _, valueFile := range opts.ValueFiles {
		keep, err := localHelmDigestShouldCollectPath(plan, sourcePlan, valueFile)
		if err != nil {
			return nil, nil, err
		}
		if keep {
			valueFiles = append(valueFiles, valueFile)
		}
	}
	fileParameters := make([]argoappv1.HelmFileParameter, 0, len(opts.HelmFileParameters))
	for _, parameter := range opts.HelmFileParameters {
		keep, err := localHelmDigestShouldCollectPath(plan, sourcePlan, parameter.Path)
		if err != nil {
			return nil, nil, err
		}
		if keep {
			fileParameters = append(fileParameters, parameter)
		}
	}
	return valueFiles, fileParameters, nil
}

func localHelmDigestShouldCollectPath(plan PlanResult, sourcePlan SourcePlan, inputPath string) (bool, error) {
	trimmed := strings.TrimSpace(inputPath)
	if trimmed == "" {
		return true, nil
	}
	if !strings.HasPrefix(trimmed, "$") {
		return true, nil
	}
	refKey, ok, err := helmValueFileRefKey(trimmed)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("unprovable Helm ref path %q", inputPath)
	}
	refSource, ok := plan.Refs[refKey]
	if !ok {
		return false, fmt.Errorf("unknown Helm ref path %q", inputPath)
	}
	return isSameSourceRevision(refSource.Source, sourcePlan.Source), nil
}

func planSameRevisionRenderSource(plan PlanResult, refPlan SourcePlan) (SourcePlan, bool) {
	for _, candidate := range plan.Sources {
		if candidate.RefOnly || candidate.Index == refPlan.Index {
			continue
		}
		if isSameSourceRevision(candidate.Source, refPlan.Source) {
			return candidate, true
		}
	}
	return SourcePlan{}, false
}

// persistentRenderKeyInput mirrors applicationRenderCacheKey's input with
// path-typed and run-ephemeral fields replaced by resolved source identities.
type persistentRenderKeyInput struct {
	FormatVersion           int                           `json:"formatVersion"`
	Application             applicationRenderCacheInput   `json:"application"`
	ApplicationInputs       SourceIdentity                `json:"applicationInputs,omitempty"`
	SettingsSignature       string                        `json:"settingsSignature"`
	PluginTimeout           string                        `json:"pluginTimeout,omitempty"`
	EnableAVPCompat         bool                          `json:"enableAVPCompat"`
	EnableKSOPSCompat       bool                          `json:"enableKSOPSCompat"`
	EnablePlugins           bool                          `json:"enablePlugins"`
	PluginPolicyFingerprint string                        `json:"pluginPolicyFingerprint,omitempty"`
	HasInjectedPluginRender bool                          `json:"hasInjectedPluginRender"`
	TrackingOptions         TrackingOptions               `json:"trackingOptions"`
	Sources                 []SourceIdentity              `json:"sources"`
	Engine                  rendercache.EngineFingerprint `json:"engine"`
	KubeVersion             string                        `json:"kubeVersion,omitempty"`
	APIVersions             []string                      `json:"apiVersions,omitempty"`
}

func persistentRenderCacheKey(input persistentRenderKeyInput) (string, error) {
	input.FormatVersion = rendercache.FormatVersion
	input.Sources = normalizePersistentSourceIdentities(input.Sources)
	input.ApplicationInputs = normalizePersistentSourceIdentity(input.ApplicationInputs)
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("fingerprint persistent render cache key: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func normalizePersistentSourceIdentities(input []SourceIdentity) []SourceIdentity {
	if len(input) == 0 {
		return nil
	}
	out := append([]SourceIdentity(nil), input...)
	for i := range out {
		out[i] = normalizePersistentSourceIdentity(out[i])
	}
	return out
}

func normalizePersistentSourceIdentity(input SourceIdentity) SourceIdentity {
	if input.Kind == sourceIdentityKindLocalInputs || input.Kind == sourceIdentityKindWorktreeInputs {
		input.Revision = ""
	}
	return input
}

// persistentRenderOptions are the unexported persistent-tier inputs threaded
// into RenderApplicationWithOptions. Exported direct callers leave them zero,
// which keeps persistence off.
type persistentRenderOptions struct {
	cache                      *persistentRenderCache
	settingsSignature          string
	hasInjectedPluginRender    bool
	applicationInputPaths      []string
	applicationInputsKnown     bool
	applicationInputsDuplicate bool
	kubeVersion                string
	apiVersions                []string
}

// persistentRenderState carries one application render's persistent-tier
// decision through prepare, lookup, render, and store phases.
type persistentRenderState struct {
	enabled       bool
	key           string
	handle        *persistentRenderCache
	refresh       bool
	recorder      *cacheevent.Recorder
	acquisitions  *cacheevent.AcquisitionCollector
	appName       string
	verifications []renderInputVerification
}

func (state *persistentRenderState) event(action cacheevent.Action, reason string, errorText string) {
	if state.recorder == nil {
		return
	}
	state.recorder.Record(cacheevent.Event{
		Source:   cacheevent.SourceRender,
		Action:   action,
		Target:   state.appName,
		Revision: renderCacheKeyPrefix(state.key),
		Reason:   reason,
		Error:    errorText,
	})
}

func (state *persistentRenderState) skip(reason string) {
	if !state.enabled {
		return
	}
	state.event(cacheevent.ActionSkipped, reason, "")
	state.enabled = false
}

// preparePersistentRender is the prepare + eligibility phase. It never fails
// the render: every problem degrades to "not enabled".
func preparePersistentRender(ctx context.Context, application argoappv1.Application, plan PlanResult, provider render.Provider, options ApplicationRenderOptions) persistentRenderState {
	state := persistentRenderState{appName: renderEventTarget(application)}
	handle := options.persistent.cache
	if !handle.active() {
		return state
	}
	local, ok := provider.(localProvider)
	if !ok {
		return state
	}
	state.handle = handle
	state.refresh = handle.refresh
	state.recorder = local.cacheEvents
	state.acquisitions = local.acquisitions
	verifier := &renderInputVerifier{}
	local.inputVerifier = verifier
	if applicationUsesPluginSource(application) || planUsesPluginSource(plan) {
		state.event(cacheevent.ActionSkipped, renderCacheReasonIneligibleSource, "")
		return state
	}
	identities, reason, ok := collectSourceIdentities(ctx, handle, local, plan)
	if !ok {
		state.event(cacheevent.ActionSkipped, reason, "")
		return state
	}
	inputIdentity, reason, ok := collectApplicationInputIdentity(ctx, handle, local, options.persistent.applicationInputPaths, options.persistent.applicationInputsKnown, options.persistent.applicationInputsDuplicate)
	if !ok {
		state.event(cacheevent.ActionSkipped, reason, "")
		return state
	}
	key, err := persistentRenderCacheKey(persistentRenderKeyInput{
		Application:             newApplicationRenderCacheInput(application),
		ApplicationInputs:       inputIdentity,
		SettingsSignature:       options.persistent.settingsSignature,
		PluginTimeout:           options.PluginOptions.PluginTimeout.String(),
		EnableAVPCompat:         options.PluginOptions.EnableAVPCompat,
		EnableKSOPSCompat:       options.PluginOptions.EnableKSOPSCompat,
		EnablePlugins:           options.PluginOptions.EnablePlugins,
		PluginPolicyFingerprint: options.PluginOptions.pluginPolicyFingerprint,
		HasInjectedPluginRender: options.persistent.hasInjectedPluginRender,
		TrackingOptions:         normalizeTrackingOptions(options.TrackingOptions),
		Sources:                 identities,
		Engine:                  handle.fingerprint,
		KubeVersion:             options.persistent.kubeVersion,
		APIVersions:             options.persistent.apiVersions,
	})
	if err != nil {
		state.event(cacheevent.ActionError, "", err.Error())
		return state
	}
	state.key = key
	state.verifications = verifier.entries
	state.enabled = true
	return state
}

func collectApplicationInputIdentity(ctx context.Context, handle *persistentRenderCache, provider localProvider, inputPaths []string, known bool, duplicate bool) (SourceIdentity, string, bool) {
	if !known {
		return SourceIdentity{}, "", true
	}
	if duplicate {
		return SourceIdentity{}, renderCacheReasonDuplicateApplication, false
	}
	switch provider.rootInputMode {
	case rootInputModeSnapshot:
		return SourceIdentity{}, "", true
	case rootInputModeUnknown:
		return SourceIdentity{}, renderCacheReasonIneligibleSource, false
	case rootInputModeClean, rootInputModeDirty:
	default:
		return SourceIdentity{}, renderCacheReasonIneligibleSource, false
	}
	paths, err := applicationInputDigestPaths(inputPaths)
	if err != nil {
		return SourceIdentity{}, renderCacheReasonInputGraph, false
	}
	if len(paths) == 0 {
		return SourceIdentity{}, renderCacheReasonInputGraph, false
	}
	switch provider.rootInputMode {
	case rootInputModeClean:
		return committedApplicationInputIdentity(ctx, handle, provider, paths)
	case rootInputModeDirty:
		if dirtyWorktreeCommittedShortcut(provider, paths) {
			return committedApplicationInputIdentity(ctx, handle, provider, paths)
		}
		return filesystemApplicationInputIdentity(ctx, handle, provider, paths)
	case rootInputModeSnapshot:
		return SourceIdentity{}, "", true
	case rootInputModeUnknown:
		return SourceIdentity{}, renderCacheReasonIneligibleSource, false
	default:
		return SourceIdentity{}, renderCacheReasonIneligibleSource, false
	}
}

func committedApplicationInputIdentity(ctx context.Context, handle *persistentRenderCache, provider localProvider, paths []gitref.PathDigestPath) (SourceIdentity, string, bool) {
	if provider.rootIdentity.Kind != sourceIdentityKindRoot || strings.TrimSpace(provider.rootIdentity.Revision) == "" {
		return SourceIdentity{}, renderCacheReasonDirtyWorktree, false
	}
	result, err := handle.committedPathDigest(ctx, provider.repoRoot, provider.rootIdentity.Revision, paths)
	if err != nil {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	if strings.TrimSpace(result.Digest) == "" {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	provider.inputVerifier.addCommitted(provider, provider.rootIdentity.Revision, paths)
	return SourceIdentity{
		Kind:   sourceIdentityKindLocalInputs,
		Digest: result.Digest,
	}, "", true
}

func filesystemApplicationInputIdentity(ctx context.Context, handle *persistentRenderCache, provider localProvider, paths []gitref.PathDigestPath) (SourceIdentity, string, bool) {
	result, err := handle.filesystemPathDigest(ctx, provider.repoRoot, filesystemInputDigestPaths(paths), filesystemDigestForbiddenRoots(provider))
	if err != nil {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	if strings.TrimSpace(result.Digest) == "" {
		return SourceIdentity{}, renderCacheReasonInputDigest, false
	}
	provider.inputVerifier.addFilesystem(provider, paths, result.Digest)
	return SourceIdentity{
		Kind:   sourceIdentityKindWorktreeInputs,
		Digest: result.Digest,
	}, "", true
}

func applicationInputDigestPaths(inputPaths []string) ([]gitref.PathDigestPath, error) {
	unique := uniqueStrings(inputPaths)
	paths := make([]gitref.PathDigestPath, 0, len(unique))
	for _, inputPath := range unique {
		clean, err := cleanLocalSourcePath(inputPath)
		if err != nil {
			return nil, err
		}
		paths = append(paths, gitref.PathDigestPath{Path: clean})
	}
	return paths, nil
}

func planUsesPluginSource(plan PlanResult) bool {
	for _, sourcePlan := range plan.Sources {
		if sourcePlan.Source.Plugin != nil {
			return true
		}
	}
	return false
}

func (state *persistentRenderState) lookup() (RenderResult, bool) {
	if !state.enabled {
		return RenderResult{}, false
	}
	if state.refresh {
		state.event(cacheevent.ActionMiss, renderCacheReasonRefresh, "")
		return RenderResult{}, false
	}
	payload, hit, err := state.handle.store.Get(state.key)
	if err != nil {
		state.event(cacheevent.ActionError, "", err.Error())
		return RenderResult{}, false
	}
	if !hit {
		state.event(cacheevent.ActionMiss, "", "")
		return RenderResult{}, false
	}
	result, err := unmarshalRenderResultPayload(payload)
	if err != nil {
		_ = state.handle.store.Delete(state.key)
		state.event(cacheevent.ActionError, "", err.Error())
		return RenderResult{}, false
	}
	state.event(cacheevent.ActionHit, "", "")
	return result, true
}

func (state *persistentRenderState) store(ctx context.Context, result RenderResult) {
	if !state.enabled || ctx.Err() != nil {
		return
	}
	if state.acquisitions == nil {
		state.event(cacheevent.ActionSkipped, renderCacheReasonRecordsInactive, "")
		return
	}
	if !acquisitionsPinStable(state.acquisitions.Records()) {
		state.event(cacheevent.ActionSkipped, renderCacheReasonPinUnstable, "")
		return
	}
	if unchanged, detail := state.renderInputsUnchanged(ctx); !unchanged {
		state.event(cacheevent.ActionSkipped, renderCacheReasonInputsChanged, detail)
		return
	}
	payload, err := marshalRenderResultPayload(result)
	if err != nil {
		state.event(cacheevent.ActionError, "", err.Error())
		return
	}
	if err := state.handle.store.Put(state.key, payload, rendercache.EntryMeta{
		Version: state.handle.fingerprint.Version,
		Commit:  state.handle.fingerprint.Commit,
	}); err != nil {
		state.event(cacheevent.ActionError, "", err.Error())
		return
	}
	state.event(cacheevent.ActionStore, "", "")
}

// renderInputsUnchanged re-checks every input path set after the render.
// Dirty sets are re-digested via a direct filedigest call (bypassing the
// run-scoped memo); clean sets are compared against the committed content at
// the key's revision. Any error fails closed (skip the store, never the
// render).
func (state *persistentRenderState) renderInputsUnchanged(ctx context.Context) (bool, string) {
	for _, verification := range state.verifications {
		if verification.revision != "" {
			match, err := gitref.WorktreePathsMatchRevision(ctx, gitref.PathDigestInput{
				RepoPath: verification.repoRoot,
				Revision: verification.revision,
				Paths:    verification.committedPaths,
			})
			if err != nil {
				return false, err.Error()
			}
			if !match {
				return false, ""
			}
			continue
		}
		result, err := filedigest.PathDigest(ctx, filedigest.PathDigestInput{
			RepoRoot:       verification.repoRoot,
			Paths:          verification.paths,
			ForbiddenRoots: verification.forbiddenRoots,
		})
		if err != nil {
			return false, err.Error()
		}
		if result.Digest != verification.digest {
			return false, ""
		}
	}
	return true, ""
}

func acquisitionsPinStable(records []cacheevent.AcquisitionRecord) bool {
	for _, record := range records {
		switch record.Kind {
		case cacheevent.AcquisitionGit:
			// Git source revisions are already key inputs.
		case cacheevent.AcquisitionOCI:
			// The resolved digest is already a key input
			// (SourceIdentity.Revision), so moved tags rotate the key.
		case cacheevent.AcquisitionChart:
			if strings.TrimSpace(record.RequestedRevision) == "" {
				return false
			}
		case cacheevent.AcquisitionRemoteGit:
			if !isFullCommitSHA(record.RequestedRevision) {
				return false
			}
		case cacheevent.AcquisitionRemoteHTTP:
			return false
		default:
			return false
		}
	}
	return true
}

func isFullCommitSHA(revision string) bool {
	if len(revision) != 40 {
		return false
	}
	for _, r := range revision {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
