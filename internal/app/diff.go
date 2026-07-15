package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/rendercache"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

type DiffRequest struct {
	LeftPath  string
	RightPath string
	Repo      string
	Ref       string
	RefOrig   string
	DiscoveryOptions
	ChangedOnly             bool
	StrictChangedOnly       bool
	ChangedOnlyIncludeGlobs []string
	ChangedOnlyIgnoreGlobs  []string
	Strict                  bool
	ProjectDiagnosticsMode  diagnostic.ProjectDiagnosticsMode
	Unified                 int
	StripAttrs              []string
	ShowIgnoredFields       bool
	AcquisitionOptions
	RenderCacheOptions
	PluginOptions
	ExecutionOptions
	FilterOptions
	ApplicationSetOptions
	CapabilityOptions
	CRDScopeOptions

	changedPaths          []string
	persistentRenderCache *persistentRenderCache
	leftPathRevision      string
	rightPathRevision     string
	selfRepo              selfRepoRefs
}

type DiffAppRequest struct {
	DiffRequest
	Name string
}

type DiffResult struct {
	Results     []diff.Result
	Diagnostics []diagnostic.Diagnostic
	CacheEvents []cacheevent.Event
}

type ImageDiffResult struct {
	Added       []string
	Removed     []string
	Unchanged   []string
	Diagnostics []diagnostic.Diagnostic
	CacheEvents []cacheevent.Event
}

func (o Orchestrator) DiffApps(ctx context.Context, request DiffRequest) (result DiffResult, err error) {
	request, cleanup, err := resolveDiffRequestPaths(ctx, request, true)
	if err != nil {
		return DiffResult{}, err
	}
	defer func() {
		// Git ref snapshots live under OS temp; cleanup must not turn a valid diff
		// result into a command failure.
		_ = cleanup()
	}()

	if err := validateDiffPaths(request); err != nil {
		return DiffResult{}, err
	}
	if err := request.ProjectDiagnosticsMode.Validate(); err != nil {
		return DiffResult{}, err
	}
	if err := validateDiffRenderCacheRoot(request); err != nil {
		return DiffResult{}, err
	}
	loadedRequest, policyDiags, policyCleanup, err := ensureDiffPluginPolicy(ctx, request)
	defer policyCleanup()
	if err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(policyDiags)}, err
	}
	request = loadedRequest
	request, releaseRenderCache := ensureDiffPersistentRenderCache(request)
	defer func() {
		result.CacheEvents = append(result.CacheEvents, releaseRenderCache()...)
	}()

	leftBuild, rightBuild, diagnostics, err := o.buildDiffSides(ctx, request)
	diagnostics = request.filterProjectDiagnostics(append(policyDiags, diagnostics...))
	cacheEvents := cacheEventsFromBuilds(leftBuild, rightBuild)
	buildErr := err
	if buildErr != nil && !hasRenderedDiffInput(leftBuild, rightBuild) {
		return DiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}

	results, diffErr := diffBuildResults(leftBuild, rightBuild, diff.Options{
		Unified:           request.Unified,
		StripAttrs:        request.StripAttrs,
		ShowIgnoredFields: request.ShowIgnoredFields,
	})
	if err := errors.Join(buildErr, diffErr); err != nil {
		return DiffResult{Results: results, Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}
	return DiffResult{Results: results, Diagnostics: diagnostics, CacheEvents: cacheEvents}, nil
}

func (o Orchestrator) DiffApp(ctx context.Context, request DiffAppRequest) (result DiffResult, err error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return DiffResult{}, fmt.Errorf("application name is required")
	}
	diffRequest, cleanup, err := resolveDiffRequestPaths(ctx, request.DiffRequest, false)
	if err != nil {
		return DiffResult{}, err
	}
	request.DiffRequest = diffRequest
	defer func() {
		// Git ref snapshots live under OS temp; cleanup must not turn a valid diff
		// result into a command failure.
		_ = cleanup()
	}()

	if err := validateDiffPaths(request.DiffRequest); err != nil {
		return DiffResult{}, err
	}
	if err := request.ProjectDiagnosticsMode.Validate(); err != nil {
		return DiffResult{}, err
	}
	if err := validateDiffRenderCacheRoot(request.DiffRequest); err != nil {
		return DiffResult{}, err
	}
	loadedRequest, policyDiags, policyCleanup, err := ensureDiffPluginPolicy(ctx, request.DiffRequest)
	defer policyCleanup()
	if err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(policyDiags)}, err
	}
	preparedDiffRequest, releaseRenderCache := ensureDiffPersistentRenderCache(loadedRequest)
	request.DiffRequest = preparedDiffRequest
	defer func() {
		result.CacheEvents = append(result.CacheEvents, releaseRenderCache()...)
	}()

	forbiddenRoots := diffForbiddenRoots(request.DiffRequest)
	if err := validateDiffCacheRoots(request.DiffRequest, forbiddenRoots); err != nil {
		return DiffResult{}, err
	}

	leftBuildRequest := request.buildRequest(request.LeftPath, forbiddenRoots)
	rightBuildRequest := request.buildRequest(request.RightPath, forbiddenRoots)
	leftBuildRequest.rootRevision = request.leftPathRevision
	rightBuildRequest.rootRevision = request.rightPathRevision
	parallelism, err := normalizeParallelism(request.Parallelism)
	if err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(policyDiags)}, err
	}
	leftParallelism, rightParallelism, concurrent := splitSideParallelism(parallelism)
	leftBuildRequest.Parallelism = leftParallelism
	rightBuildRequest.Parallelism = rightParallelism
	snapshotSession, err := acquisition.NewSnapshotSession("drydock-cache-snapshots-*")
	if err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(policyDiags)}, err
	}
	defer snapshotSession.Close()
	leftBuildRequest.snapshotSession = snapshotSession
	rightBuildRequest.snapshotSession = snapshotSession

	diagnostics := append([]diagnostic.Diagnostic(nil), policyDiags...)
	leftList, rightList := runDiffSidePair(ctx, concurrent, o.ListApplications, leftBuildRequest, rightBuildRequest)
	diagnostics = append(diagnostics, leftList.result.Diagnostics...)
	diagnostics = append(diagnostics, rightList.result.Diagnostics...)
	if err := errors.Join(leftList.err, rightList.err); err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(diagnostics)}, err
	}
	leftBuildRequest.PluginOptions = leftList.result.pluginOptions
	leftBuildRequest.renderCache = leftList.result.renderCache
	leftBuildRequest.renderSettingsSignature = leftList.result.renderSettingsSignature
	leftBuildRequest.discovered = leftList.result.discovered
	rightBuildRequest.PluginOptions = rightList.result.pluginOptions
	rightBuildRequest.renderCache = rightList.result.renderCache
	rightBuildRequest.renderSettingsSignature = rightList.result.renderSettingsSignature
	rightBuildRequest.discovered = rightList.result.discovered

	leftApp, leftOK, rightApp, rightOK, err := selectDiffAppApplications(leftList.result.Applications, rightList.result.Applications, name)
	if err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(diagnostics)}, err
	}

	leftBuildRequest.Applications = selectedApplications(leftApp, leftOK)
	rightBuildRequest.Applications = selectedApplications(rightApp, rightOK)

	leftBuild, rightBuild := runDiffSidePair(ctx, concurrent, o.Build, leftBuildRequest, rightBuildRequest)
	leftBuild.result.CacheEvents = append(append([]cacheevent.Event(nil), leftList.result.CacheEvents...), leftBuild.result.CacheEvents...)
	rightBuild.result.CacheEvents = append(append([]cacheevent.Event(nil), rightList.result.CacheEvents...), rightBuild.result.CacheEvents...)
	diagnostics = append(diagnostics, leftBuild.result.Diagnostics...)
	diagnostics = append(diagnostics, rightBuild.result.Diagnostics...)
	cacheEvents := cacheEventsFromBuilds(leftBuild.result, rightBuild.result)
	if err := errors.Join(leftBuild.err, rightBuild.err); err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(diagnostics), CacheEvents: cacheEvents}, err
	}

	results, err := diffBuildResults(leftBuild.result, rightBuild.result, diff.Options{
		Unified:           request.Unified,
		StripAttrs:        request.StripAttrs,
		ShowIgnoredFields: request.ShowIgnoredFields,
	})
	if err != nil {
		return DiffResult{Diagnostics: request.filterProjectDiagnostics(diagnostics), CacheEvents: cacheEvents}, err
	}
	return DiffResult{Results: results, Diagnostics: request.filterProjectDiagnostics(diagnostics), CacheEvents: cacheEvents}, nil
}

func (o Orchestrator) DiffImages(ctx context.Context, request DiffRequest) (result ImageDiffResult, err error) {
	request, cleanup, err := resolveDiffRequestPaths(ctx, request, true)
	if err != nil {
		return ImageDiffResult{}, err
	}
	defer func() {
		// Git ref snapshots live under OS temp; cleanup must not turn a valid diff
		// result into a command failure.
		_ = cleanup()
	}()

	if err := validateDiffPaths(request); err != nil {
		return ImageDiffResult{}, err
	}
	if err := request.ProjectDiagnosticsMode.Validate(); err != nil {
		return ImageDiffResult{}, err
	}
	if err := validateDiffRenderCacheRoot(request); err != nil {
		return ImageDiffResult{}, err
	}
	loadedRequest, policyDiags, policyCleanup, err := ensureDiffPluginPolicy(ctx, request)
	defer policyCleanup()
	if err != nil {
		return ImageDiffResult{Diagnostics: request.filterProjectDiagnostics(policyDiags)}, err
	}
	request = loadedRequest
	request, releaseRenderCache := ensureDiffPersistentRenderCache(request)
	defer func() {
		result.CacheEvents = append(result.CacheEvents, releaseRenderCache()...)
	}()

	leftBuild, rightBuild, diagnostics, err := o.buildDiffSides(ctx, request)
	diagnostics = request.filterProjectDiagnostics(append(policyDiags, diagnostics...))
	cacheEvents := cacheEventsFromBuilds(leftBuild, rightBuild)
	buildErr := err
	if buildErr != nil && !hasRenderedDiffInput(leftBuild, rightBuild) {
		return ImageDiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}

	leftDocs, err := diffDocuments(leftBuild)
	if err != nil {
		return ImageDiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}
	rightDocs, err := diffDocuments(rightBuild)
	if err != nil {
		return ImageDiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}

	added, removed, unchanged := compareStringSets(diff.ExtractImages(leftDocs), diff.ExtractImages(rightDocs))
	return ImageDiffResult{
		Added:       added,
		Removed:     removed,
		Unchanged:   unchanged,
		Diagnostics: diagnostics,
		CacheEvents: cacheEvents,
	}, buildErr
}

func cacheEventsFromBuilds(leftBuild, rightBuild BuildResult) []cacheevent.Event {
	out := make([]cacheevent.Event, 0, len(leftBuild.CacheEvents)+len(rightBuild.CacheEvents))
	out = append(out, leftBuild.CacheEvents...)
	out = append(out, rightBuild.CacheEvents...)
	return out
}

func compareStringSets(left, right []string) (added, removed, unchanged []string) {
	leftIndex := 0
	rightIndex := 0
	for leftIndex < len(left) && rightIndex < len(right) {
		switch {
		case left[leftIndex] == right[rightIndex]:
			unchanged = append(unchanged, left[leftIndex])
			leftIndex++
			rightIndex++
		case left[leftIndex] < right[rightIndex]:
			removed = append(removed, left[leftIndex])
			leftIndex++
		default:
			added = append(added, right[rightIndex])
			rightIndex++
		}
	}
	removed = append(removed, left[leftIndex:]...)
	added = append(added, right[rightIndex:]...)
	return added, removed, unchanged
}

func resolveDiffRequestPaths(ctx context.Context, request DiffRequest, computeChangedPaths bool) (DiffRequest, func() error, error) {
	var cleanups []func() error
	cleanup := func() error {
		var err error
		for _, v := range slices.Backward(cleanups) {
			err = errors.Join(err, v())
		}
		return err
	}

	repoPath := strings.TrimSpace(request.Repo)
	if repoPath == "" {
		repoPath = request.RightPath
	}
	hasRef := strings.TrimSpace(request.Ref) != "" || strings.TrimSpace(request.RefOrig) != ""
	if err := validateDiffRefOptions(request, hasRef); err != nil {
		return request, cleanup, err
	}
	if hasRef {
		request.Repo = repoPath
	}

	if computeChangedPaths && request.ChangedOnly {
		resolved, done, err := resolveEmptyChangedOnlyRefDiff(ctx, request, repoPath)
		if err != nil {
			return request, cleanup, err
		}
		request = resolved
		if done {
			return request, cleanup, nil
		}
	}

	forbiddenRoots := []string{request.LeftPath, request.RightPath, repoPath}
	if strings.TrimSpace(request.RefOrig) != "" {
		result, err := gitref.Snapshot(ctx, gitref.Request{
			Repo:           repoPath,
			Ref:            request.RefOrig,
			ForbiddenRoots: forbiddenRoots,
		})
		if err != nil {
			return request, cleanup, err
		}
		request.LeftPath = result.Path
		request.leftPathRevision = result.Revision
		cleanups = append(cleanups, result.Cleanup)
		forbiddenRoots = append(forbiddenRoots, result.Path)
	}
	if strings.TrimSpace(request.Ref) != "" {
		result, err := gitref.Snapshot(ctx, gitref.Request{
			Repo:           repoPath,
			Ref:            request.Ref,
			ForbiddenRoots: forbiddenRoots,
		})
		if err != nil {
			return request, cleanup, errors.Join(err, cleanup())
		}
		request.RightPath = result.Path
		request.rightPathRevision = result.Revision
		cleanups = append(cleanups, result.Cleanup)
	}
	request.selfRepo = detectSelfRepoRefs(request, repoPath)
	return request, cleanup, nil
}

func resolveEmptyChangedOnlyRefDiff(ctx context.Context, request DiffRequest, repoPath string) (DiffRequest, bool, error) {
	changedPaths, ok, err := gitRefChangedPaths(ctx, request, repoPath)
	if err != nil || !ok {
		return request, false, err
	}
	request.changedPaths = changedPaths
	filtered, err := filteredChangedOnlyPaths(request)
	if err != nil {
		return request, false, err
	}
	if len(filtered) > 0 {
		return request, false, nil
	}
	// Nothing relevant changed between the refs. Point both sides at the
	// repository tree and skip snapshot materialization; buildDiffSides returns
	// before any discovery or render.
	request.LeftPath = repoPath
	request.RightPath = repoPath
	return request, true, nil
}

func validateDiffRefOptions(request DiffRequest, hasRef bool) error {
	if strings.TrimSpace(request.Repo) != "" && !hasRef && strings.TrimSpace(request.PluginPolicyRef) == "" {
		return fmt.Errorf("--repo requires --ref or --ref-orig")
	}
	if strings.TrimSpace(request.RefOrig) != "" && strings.TrimSpace(request.LeftPath) != "" {
		return fmt.Errorf("--ref-orig cannot be combined with --path-orig")
	}
	return nil
}

func gitRefChangedPaths(ctx context.Context, request DiffRequest, repoPath string) ([]string, bool, error) {
	refOrig := strings.TrimSpace(request.RefOrig)
	if refOrig == "" {
		return nil, false, nil
	}
	ref := strings.TrimSpace(request.Ref)
	if ref != "" {
		changedPaths, err := gitref.ChangedPathsBetweenRefs(ctx, repoPath, refOrig, ref)
		return changedPaths, true, err
	}
	if !sameLocalPath(repoPath, request.RightPath) {
		return nil, false, nil
	}
	changedPaths, err := gitref.ChangedPathsFromRefToWorktree(ctx, repoPath, refOrig)
	return changedPaths, true, err
}

func sameLocalPath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(leftAbs); err == nil {
		leftAbs = resolved
	}
	if resolved, err := filepath.EvalSymlinks(rightAbs); err == nil {
		rightAbs = resolved
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func (request DiffRequest) buildRequest(path string, forbiddenRoots []string) BuildRequest {
	options := request.buildAcquisitionOptions(forbiddenRoots)
	// design.md: "PR diff roots are authoritative for mapped repositories."
	// An explicit --repo-map pointing at the checkout under diff previously
	// served ONE worktree to both sides (silent empty diff); rewrite per side.
	// request.Repo is non-empty only for ref-based (or plugin-policy --repo)
	// diffs; sameLocalPath("", x) is false, so pure path diffs are untouched.
	for i, repoMap := range options.RepoMaps {
		if sameLocalPath(repoMap.Path, request.Repo) {
			options.RepoMaps[i].Path = path
		}
	}
	return BuildRequest{
		Path:                   path,
		Strict:                 request.Strict,
		ProjectDiagnosticsMode: request.ProjectDiagnosticsMode,
		DiscoveryOptions:       cloneDiscoveryOptions(request.DiscoveryOptions),
		AcquisitionOptions:     options,
		RenderCacheOptions:     request.RenderCacheOptions,
		PluginOptions:          request.PluginOptions,
		ExecutionOptions:       request.ExecutionOptions,
		FilterOptions:          cloneFilterOptions(request.FilterOptions),
		ApplicationSetOptions:  cloneApplicationSetOptions(request.ApplicationSetOptions),
		CapabilityOptions:      request.CapabilityOptions,
		CRDScopeOptions:        request.CRDScopeOptions,
		// per-side deep copy: no shared mutable state across concurrent sides
		selfRepo:              request.selfRepo.clone(),
		persistentRenderCache: request.persistentRenderCache,
	}
}

func (request DiffRequest) buildAcquisitionOptions(forbiddenRoots []string) AcquisitionOptions {
	options := cloneAcquisitionOptions(request.AcquisitionOptions)
	for _, root := range forbiddenRoots {
		options.RemoteResourceForbiddenRoots = appendUniqueString(options.RemoteResourceForbiddenRoots, root)
	}
	return options
}

func cloneDiscoveryOptions(input DiscoveryOptions) DiscoveryOptions {
	input.DiscoverKustomizePaths = append([]string(nil), input.DiscoverKustomizePaths...)
	return input
}

func cloneAcquisitionOptions(input AcquisitionOptions) AcquisitionOptions {
	input.RepoMaps = append([]sourcepkg.RepoMap(nil), input.RepoMaps...)
	input.RemoteResourceForbiddenRoots = append([]string(nil), input.RemoteResourceForbiddenRoots...)
	return input
}

func cloneFilterOptions(input FilterOptions) FilterOptions {
	input.SkipKinds = append([]string(nil), input.SkipKinds...)
	return input
}

func cloneApplicationSetOptions(input ApplicationSetOptions) ApplicationSetOptions {
	input.ApplicationSetProviderFixtures = append([]string(nil), input.ApplicationSetProviderFixtures...)
	return input
}

func validateDiffPaths(request DiffRequest) error {
	if request.LeftPath == "" {
		return fmt.Errorf("--path-orig is required")
	}
	if request.RightPath == "" {
		return fmt.Errorf("--path is required")
	}
	if _, err := changedOnlyPathFilter(request); err != nil {
		return err
	}
	return nil
}

func validateDiffCacheRoots(request DiffRequest, forbiddenRoots []string) error {
	if _, err := chart.ResolveCacheDir(request.ChartCacheDir, forbiddenRoots); err != nil {
		return err
	}
	if _, err := remote.ResolveCacheDir(request.RemoteResourceCacheDir, forbiddenRoots); err != nil {
		return err
	}
	if err := validateDiffRenderCacheRoot(request); err != nil {
		return err
	}
	return nil
}

func validateDiffRenderCacheRoot(request DiffRequest) error {
	if !request.RenderCacheEnabled {
		return nil
	}
	dir, err := rendercache.ResolveDir(request.RenderCacheDir, diffForbiddenRoots(request))
	if err != nil {
		return err
	}
	if !request.EngineFingerprint.Known() {
		return nil
	}
	_, err = rendercache.Open(dir, request.RenderCacheMaxBytes)
	return err
}

func diffForbiddenRoots(request DiffRequest) []string {
	forbiddenRoots := append([]string(nil), request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = appendUniqueString(forbiddenRoots, request.LeftPath)
	forbiddenRoots = appendUniqueString(forbiddenRoots, request.RightPath)
	forbiddenRoots = appendUniqueString(forbiddenRoots, request.Repo)
	for _, repoMap := range request.RepoMaps {
		if strings.TrimSpace(repoMap.Path) != "" {
			forbiddenRoots = appendUniqueString(forbiddenRoots, repoMap.Path)
		}
	}
	return forbiddenRoots
}

func selectedApplications(application argoappv1.Application, ok bool) []argoappv1.Application {
	if !ok {
		return []argoappv1.Application{}
	}
	return []argoappv1.Application{application}
}

func selectDiffAppApplications(leftApps, rightApps []argoappv1.Application, name string) (argoappv1.Application, bool, argoappv1.Application, bool, error) {
	leftApp, leftOK, err := SelectOptionalApplicationByName(leftApps, name)
	if err != nil {
		return argoappv1.Application{}, false, argoappv1.Application{}, false, err
	}
	rightApp, rightOK, err := SelectOptionalApplicationByName(rightApps, name)
	if err != nil {
		return argoappv1.Application{}, false, argoappv1.Application{}, false, err
	}
	if !leftOK && !rightOK {
		return argoappv1.Application{}, false, argoappv1.Application{}, false, fmt.Errorf("application %q not found in either tree", name)
	}
	return leftApp, leftOK, rightApp, rightOK, nil
}

func diffBuildResults(leftBuild, rightBuild BuildResult, opts diff.Options) ([]diff.Result, error) {
	leftDocs, err := diffDocuments(leftBuild)
	if err != nil {
		return nil, err
	}
	rightDocs, err := diffDocuments(rightBuild)
	if err != nil {
		return nil, err
	}
	return diff.Run(leftDocs, rightDocs, opts)
}

func hasRenderedDiffInput(leftBuild, rightBuild BuildResult) bool {
	return len(leftBuild.ApplicationManifests) > 0 || len(rightBuild.ApplicationManifests) > 0
}

func diffDocuments(build BuildResult) ([]diff.Document, error) {
	docs := make([]diff.Document, 0, len(build.ApplicationManifests))
	for _, item := range build.ApplicationManifests {
		if item.Manifest.Object == nil {
			continue
		}
		id := manifest.IdentityOf(item.Manifest.Object)
		body, err := marshalDiffObject(item.Manifest.Object.Object)
		if err != nil {
			return nil, err
		}
		docs = append(docs, diff.Document{
			Parent: diff.Parent{
				Namespace:   item.Application.Namespace,
				Name:        item.Application.Name,
				SourceIndex: item.Manifest.SourceIndex,
				SourceName:  item.Manifest.SourceName,
				SourcePath:  item.Manifest.Path,
			},
			Resource: diff.Resource{
				Group:     id.Group,
				Kind:      id.Kind,
				Namespace: id.Namespace,
				Name:      id.Name,
			},
			Body:          body,
			Normalization: normalizationFor(item.Application, id, build.Settings),
		})
	}
	return docs, nil
}
