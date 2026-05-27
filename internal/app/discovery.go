package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (o Orchestrator) discoverRepository(ctx context.Context, root string, request BuildRequest) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, *applicationRenderCache, string, error) {
	mode, err := normalizeDiscoveryMode(request.DiscoveryMode)
	if err != nil {
		return discovery.Result{}, nil, nil, nil, "", err
	}
	maxDepth, err := normalizeMaxDiscoveryDepth(request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
	if err != nil {
		return discovery.Result{}, nil, nil, nil, "", err
	}
	renderCache := request.renderCache
	if renderCache == nil {
		renderCache = newApplicationRenderCache()
	}

	discovered, err := discovery.Scan(root, discovery.Options{})
	if err != nil {
		return discovery.Result{}, nil, nil, renderCache, "", err
	}
	markDiscoveryTier(&discovered, discovery.SourceTierStatic, nil)

	appsetOptions, providerDiags, err := applicationSetOptionsForRequest(request)
	if err != nil {
		return discovered, providerDiags, nil, renderCache, "", diagnosticsError(providerDiags, err)
	}
	var allDiags []diagnostic.Diagnostic
	allDiags = append(allDiags, providerDiags...)
	var allEvents []cacheevent.Event

	discovered, explicitDiags, explicitEvents, err := o.applyExplicitKustomizeDiscovery(ctx, root, request, discovered)
	allDiags = append(allDiags, explicitDiags...)
	allEvents = append(allEvents, explicitEvents...)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}

	var expansionDiags []diagnostic.Diagnostic
	discovered, expansionDiags, err = expandApplicationSetDiscovery(root, request, discovered, appsetOptions)
	allDiags = append(allDiags, expansionDiags...)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}

	settings, _, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}
	settingsSig, err := settingsSignature(settings)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}
	renderSig, err := renderSettingsSignature(settings)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}
	if mode == DiscoveryModeFleet && maxDepth > 0 {
		rendered, _, nextRenderSig, diags, events, err := o.discoverRenderedFleet(ctx, root, request, discovered, appsetOptions, renderCache, maxDepth, settingsSig, renderSig)
		allDiags = append(allDiags, diags...)
		allEvents = append(allEvents, events...)
		if err != nil {
			return rendered, dedupeDiagnostics(allDiags), allEvents, renderCache, nextRenderSig, err
		}
		discovered = rendered
		renderSig = nextRenderSig
	}

	return discovered, dedupeDiagnostics(allDiags), allEvents, renderCache, renderSig, nil
}

func (o Orchestrator) applyExplicitKustomizeDiscovery(ctx context.Context, root string, request BuildRequest, discovered discovery.Result) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	if len(request.DiscoverKustomizePaths) == 0 {
		return discovered, nil, nil, nil
	}
	settings, _, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		return discovered, nil, nil, err
	}
	rendered, diags, events, err := o.discoverRenderedKustomize(ctx, root, settings, request)
	if err != nil {
		return discovered, diags, events, err
	}
	next, mergeDiags := mergeDiscoveryResultsWithDiagnostics(discovered, rendered)
	diags = append(diags, mergeDiags...)
	return next, diags, events, nil
}

func (o Orchestrator) discoverRenderedKustomize(ctx context.Context, root string, settings config.ArgoSettings, request BuildRequest) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	provider, cleanup, err := o.discoveryProvider(root, settings, request, recorder)
	if err != nil {
		return discovery.Result{}, nil, recorder.Events(), err
	}
	defer cleanup()

	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	seenPaths := map[string]struct{}{}
	for _, rawPath := range request.DiscoverKustomizePaths {
		clean, err := cleanDiscoverKustomizePath(root, rawPath)
		if err != nil {
			return out, allDiags, recorder.Events(), err
		}
		displayPath := filepath.ToSlash(clean)
		if _, ok := seenPaths[displayPath]; ok {
			continue
		}
		seenPaths[displayPath] = struct{}{}

		manifests, diags, err := provider.RenderSource(ctx, render.ResolvedSource{
			RepoRoot: root,
			Path:     clean,
		}, render.RenderOptions{})
		allDiags = append(allDiags, diags...)
		if err != nil {
			return out, allDiags, recorder.Events(), fmt.Errorf("discover kustomize %q: %w", displayPath, err)
		}
		next, err := discovery.ScanObjects(displayPath, manifestObjects(manifests))
		if err != nil {
			return out, allDiags, recorder.Events(), fmt.Errorf("discover kustomize %q: %w", displayPath, err)
		}
		markDiscoveryTier(&next, discovery.SourceTierExplicitRendered, []string{displayPath})
		var mergeDiags []diagnostic.Diagnostic
		out, mergeDiags = mergeDiscoveryResultsWithDiagnostics(out, next)
		allDiags = append(allDiags, mergeDiags...)
	}
	return out, allDiags, recorder.Events(), nil
}

func (o Orchestrator) discoverRenderedFleet(ctx context.Context, root string, request BuildRequest, start discovery.Result, appsetOptions appset.Options, renderCache *applicationRenderCache, maxDepth int, initialSettingsSignature string, initialRenderSettingsSignature string) (discovery.Result, string, string, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	current := start
	settingsSig := initialSettingsSignature
	renderSig := initialRenderSettingsSignature
	var allDiags []diagnostic.Diagnostic
	var allEvents []cacheevent.Event

	for depth := 1; depth <= maxDepth; depth++ {
		settings, _, err := loadSettingsFromDiscovery(root, current)
		if err != nil {
			return current, settingsSig, renderSig, allDiags, allEvents, err
		}
		settingsSig, err = settingsSignature(settings)
		if err != nil {
			return current, settingsSig, renderSig, allDiags, allEvents, err
		}
		renderSig, err = renderSettingsSignature(settings)
		if err != nil {
			return current, settingsSig, renderSig, allDiags, allEvents, err
		}
		before, err := discoveryFingerprint(current, settingsSig)
		if err != nil {
			return current, settingsSig, renderSig, allDiags, allEvents, err
		}

		rendered, diags, events, err := o.renderDiscoveryFrontier(ctx, root, request, current, settings, renderSig, renderCache)
		allDiags = append(allDiags, diags...)
		allEvents = append(allEvents, events...)
		if err != nil {
			return current, settingsSig, renderSig, allDiags, allEvents, err
		}

		next, mergeDiags := mergeDiscoveryResultsWithDiagnostics(current, rendered)
		allDiags = append(allDiags, mergeDiags...)
		var expansionDiags []diagnostic.Diagnostic
		next, expansionDiags, err = expandApplicationSetDiscovery(root, request, next, appsetOptions)
		allDiags = append(allDiags, expansionDiags...)
		if err != nil {
			return next, settingsSig, renderSig, allDiags, allEvents, err
		}

		nextSettings, _, err := loadSettingsFromDiscovery(root, next)
		if err != nil {
			return next, settingsSig, renderSig, allDiags, allEvents, err
		}
		nextSig, err := settingsSignature(nextSettings)
		if err != nil {
			return next, settingsSig, renderSig, allDiags, allEvents, err
		}
		nextRenderSig, err := renderSettingsSignature(nextSettings)
		if err != nil {
			return next, nextSig, renderSig, allDiags, allEvents, err
		}
		after, err := discoveryFingerprint(next, nextSig)
		if err != nil {
			return next, nextSig, nextRenderSig, allDiags, allEvents, err
		}
		if after == before {
			return next, nextSig, nextRenderSig, allDiags, allEvents, nil
		}
		if depth == maxDepth {
			diag := discoveryDepthExceededDiagnostic(maxDepth)
			allDiags = append(allDiags, diag)
			return next, nextSig, nextRenderSig, allDiags, allEvents, errors.New(diag.Message)
		}
		current = next
		settingsSig = nextSig
		renderSig = nextRenderSig
	}
	return current, settingsSig, renderSig, allDiags, allEvents, nil
}

func (o Orchestrator) renderDiscoveryFrontier(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, settings config.ArgoSettings, settingsSig string, renderCache *applicationRenderCache) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	provider, cleanup, err := o.discoveryProvider(root, settings, request, recorder)
	if err != nil {
		return discovery.Result{}, nil, recorder.Events(), err
	}
	defer cleanup()

	inputs := applicationInputsByKey(discovered)
	parallelism, err := normalizeParallelism(request.Parallelism)
	if err != nil {
		return discovery.Result{}, nil, recorder.Events(), err
	}
	if parallelism > 1 && len(discovered.Applications) > 1 {
		out, diags, err := renderDiscoveryFrontierParallel(ctx, root, request, discovered, inputs, provider, settingsSig, renderCache, parallelism)
		return out, diags, recorder.Events(), err
	}

	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, appFile := range discovered.Applications {
		next, scanDiags, err := renderDiscoveryApplication(ctx, root, request, provider, settingsSig, renderCache, inputs, discovered, appFile)
		allDiags = append(allDiags, scanDiags...)
		if err != nil {
			return out, allDiags, recorder.Events(), err
		}
		var mergeDiags []diagnostic.Diagnostic
		out, mergeDiags = mergeDiscoveryResultsWithDiagnostics(out, next)
		allDiags = append(allDiags, mergeDiags...)
	}
	return out, allDiags, recorder.Events(), nil
}

type indexedDiscoveryRenderResult struct {
	index  int
	result discovery.Result
	diags  []diagnostic.Diagnostic
	err    error
}

func renderDiscoveryFrontierParallel(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, inputs map[string][]string, provider localProvider, settingsSig string, renderCache *applicationRenderCache, parallelism int) (discovery.Result, []diagnostic.Diagnostic, error) {
	applications := discovered.Applications
	workerCount := parallelism
	if workerCount > len(applications) {
		workerCount = len(applications)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan int)
	resultsCh := make(chan indexedDiscoveryRenderResult, len(applications))
	results := make([]indexedDiscoveryRenderResult, len(applications))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				result, diags, err := renderDiscoveryApplication(ctx, root, request, provider, settingsSig, renderCache, inputs, discovered, applications[index])
				resultsCh <- indexedDiscoveryRenderResult{index: index, result: result, diags: diags, err: err}
			}
		}()
	}

	scheduleErrCh := make(chan error, 1)
	go func() {
		defer close(jobs)
		for index := range applications {
			select {
			case <-ctx.Done():
				scheduleErrCh <- ctx.Err()
				return
			case jobs <- index:
			}
		}
		scheduleErrCh <- nil
	}()

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	for indexed := range resultsCh {
		results[indexed.index] = indexed
		if indexed.err != nil {
			cancel()
		}
	}
	if scheduleErr := <-scheduleErrCh; scheduleErr != nil {
		return discovery.Result{}, nil, scheduleErr
	}

	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, result := range results {
		allDiags = append(allDiags, result.diags...)
		if result.err != nil {
			return out, allDiags, result.err
		}
		var mergeDiags []diagnostic.Diagnostic
		out, mergeDiags = mergeDiscoveryResultsWithDiagnostics(out, result.result)
		allDiags = append(allDiags, mergeDiags...)
	}
	return out, allDiags, nil
}

func renderDiscoveryApplication(ctx context.Context, root string, request BuildRequest, provider localProvider, settingsSig string, renderCache *applicationRenderCache, inputs map[string][]string, discovered discovery.Result, appFile discovery.ApplicationFile) (discovery.Result, []diagnostic.Diagnostic, error) {
	application := appFile.Application
	if !applicationMayRenderDiscoveryObjects(root, request, discovered, application) {
		return discovery.Result{}, nil, nil
	}
	rendered, err := renderApplicationCached(renderContext{
		context:           ctx,
		provider:          provider,
		cache:             renderCache,
		settingsSignature: settingsSig,
		request:           request,
	}, application)
	if err != nil {
		return skippedRenderedDiscovery()
	}
	parentInputs := inputs[applicationDiscoveryKey(application)]
	return scanRenderedApplicationObjects(application, parentInputs, rendered.Manifests)
}

func skippedRenderedDiscovery() (discovery.Result, []diagnostic.Diagnostic, error) {
	return discovery.Result{}, nil, nil
}

func scanRenderedApplicationObjects(parent argoappv1.Application, parentInputs []string, manifests []render.Manifest) (discovery.Result, []diagnostic.Diagnostic, error) {
	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, renderedManifest := range manifests {
		if renderedManifest.Object == nil {
			continue
		}
		displayPath := renderedObjectDiscoveryPath(parent, renderedManifest)
		next, err := discovery.ScanObjects(displayPath, []*unstructured.Unstructured{renderedManifest.Object.DeepCopy()})
		if err != nil {
			return out, allDiags, fmt.Errorf("discover rendered Application %s output %q: %w", applicationDisplayName(parent), displayPath, err)
		}
		markDiscoveryTier(&next, discovery.SourceTierRenderedFleet, renderedInputPaths(parentInputs, renderedManifest))
		var mergeDiags []diagnostic.Diagnostic
		out, mergeDiags = mergeDiscoveryResultsWithDiagnostics(out, next)
		allDiags = append(allDiags, mergeDiags...)
	}
	return out, allDiags, nil
}

func applicationMayRenderDiscoveryObjects(root string, request BuildRequest, discovered discovery.Result, application argoappv1.Application) bool {
	plan, err := Plan(application)
	if err != nil {
		return true
	}
	for _, sourcePlan := range plan.Sources {
		if sourcePlan.RefOnly {
			continue
		}
		if sourcePlan.Source.Chart != "" && sourcePlan.Source.Path == "" {
			continue
		}
		sourceRoot, ok := localDiscoverySourceRoot(root, request, sourcePlan.Source)
		if !ok {
			continue
		}
		matches, err := pathMayContainDiscoveryObjects(sourceRoot)
		if err != nil || matches {
			if localSourceAlreadyDiscovered(root, sourceRoot, discovered) && !sourceRootHasLocalChart(sourceRoot) {
				continue
			}
			return true
		}
	}
	return false
}

func localDiscoverySourceRoot(root string, request BuildRequest, source argoappv1.ApplicationSource) (string, bool) {
	repoRoot := root
	if mapped, ok := mappedRepositoryPath(request, source.RepoURL); ok {
		repoRoot = mapped
	}
	sourcePath := source.Path
	if strings.TrimSpace(sourcePath) == "" {
		return repoRoot, true
	}
	clean, err := cleanLocalSourcePath(sourcePath)
	if err != nil {
		return "", false
	}
	path := filepath.Join(repoRoot, clean)
	exists, err := localPathExists(path)
	if err != nil || !exists {
		return "", false
	}
	return path, true
}

func mappedRepositoryPath(request BuildRequest, repoURL string) (string, bool) {
	normalized := sourcepkg.NormalizeURL(repoURL)
	for _, repoMap := range request.RepoMaps {
		if sourcepkg.NormalizeURL(repoMap.URL) == normalized {
			return repoMap.Path, true
		}
	}
	return "", false
}

func pathMayContainDiscoveryObjects(root string) (bool, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	if !info.IsDir() {
		return fileMayContainDiscoveryObjects(root)
	}
	found := false
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found {
			return filepath.SkipAll
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if path != root && shouldSkipDiscoveryCandidateDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		matches, err := fileMayContainDiscoveryObjects(path)
		if err != nil {
			return err
		}
		found = matches
		return nil
	})
	return found, err
}

func fileMayContainDiscoveryObjects(path string) (bool, error) {
	if !isDiscoveryCandidateYAML(path) {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return textMayContainDiscoveryObjects(string(data)), nil
}

func textMayContainDiscoveryObjects(text string) bool {
	return strings.Contains(text, "kind: Application") ||
		strings.Contains(text, "kind: ApplicationSet") ||
		strings.Contains(text, "kind: AppProject") ||
		strings.Contains(text, "argocd-cm") ||
		strings.Contains(text, "argocd-cmp-cm") ||
		strings.Contains(text, "argocd.argoproj.io/secret-type")
}

func localSourceAlreadyDiscovered(root, sourceRoot string, discovered discovery.Result) bool {
	rel, err := filepath.Rel(root, sourceRoot)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." {
		rel = ""
	}
	return discoveryHasPathUnder(discovered, rel)
}

func discoveryHasPathUnder(discovered discovery.Result, root string) bool {
	for _, item := range discovered.Applications {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	for _, item := range discovered.ApplicationSets {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	for _, item := range discovered.Projects {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	for _, item := range discovered.SettingsCandidates {
		if pathUnderRoot(item.Path, root) {
			return true
		}
	}
	return false
}

func pathUnderRoot(pathValue, root string) bool {
	pathValue = filepath.ToSlash(filepath.Clean(pathValue))
	root = filepath.ToSlash(filepath.Clean(root))
	if root == "." || root == "" {
		return pathValue != "." && pathValue != ""
	}
	return pathValue == root || strings.HasPrefix(pathValue, root+"/")
}

func sourceRootHasLocalChart(root string) bool {
	exists, err := localPathExists(filepath.Join(root, "Chart.yaml"))
	return err == nil && exists
}

func shouldSkipDiscoveryCandidateDir(name string) bool {
	return name == ".git" || name == ".out" || strings.HasPrefix(name, ".cache")
}

func isDiscoveryCandidateYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func renderedObjectDiscoveryPath(parent argoappv1.Application, renderedManifest render.Manifest) string {
	base := filepath.ToSlash(filepath.Join("rendered", applicationDisplayName(parent)))
	if renderedManifest.Path == "" {
		return base
	}
	return filepath.ToSlash(filepath.Join(base, renderedManifest.Path))
}

func renderedInputPaths(parentInputs []string, renderedManifest render.Manifest) []string {
	inputs := append([]string(nil), parentInputs...)
	if renderedManifest.Path != "" {
		inputs = append(inputs, filepath.ToSlash(renderedManifest.Path))
	}
	return uniqueStrings(inputs)
}

func applicationInputsByKey(discovered discovery.Result) map[string][]string {
	out := make(map[string][]string, len(discovered.Applications))
	for _, appFile := range discovered.Applications {
		inputs := discoveredApplicationInputPaths(appFile)
		inputs = applicationSelectionPaths(ApplicationSelectionInput{Application: appFile.Application, Paths: inputs})
		out[applicationDiscoveryKey(appFile.Application)] = uniqueStrings(inputs)
	}
	return out
}

func expandApplicationSetDiscovery(root string, request BuildRequest, discovered discovery.Result, appsetOptions appset.Options) (discovery.Result, []diagnostic.Diagnostic, error) {
	var generated discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, appSetFile := range discovered.ApplicationSets {
		apps, diags, err := appset.GenerateWithOptions(root, appSetFile.Path, appSetFile.ApplicationSet, appsetOptions)
		if err != nil {
			if errors.Is(err, appset.ErrUnsupportedGenerator) && len(diags) > 0 {
				allDiags = append(allDiags, normalizeDiagnostics(diags, request.Strict, true)...)
				continue
			}
			allDiags = append(allDiags, diags...)
			return discovered, allDiags, diagnosticsError(diags, err)
		}
		allDiags = append(allDiags, normalizeDiagnostics(diags, request.Strict, false)...)
		if len(apps) == 0 {
			if len(diags) != 0 {
				continue
			}
			diags := normalizeDiagnostics([]diagnostic.Diagnostic{emptyApplicationSetDiagnostic(appSetFile)}, request.Strict, false)
			allDiags = append(allDiags, diags...)
			if err := diagnosticFailure(diags, request.Strict); err != nil {
				return discovered, allDiags, err
			}
			continue
		}
		for _, app := range apps {
			generated.Applications = append(generated.Applications, discovery.ApplicationFile{
				Path:          appSetFile.Path,
				DocumentIndex: appSetFile.DocumentIndex,
				Application:   app.Application,
				Tier:          appSetFile.Tier,
				InputPaths:    generatedApplicationInputPaths(appSetFile, app),
			})
		}
	}
	merged, mergeDiags := mergeDiscoveryResultsWithDiagnostics(discovered, generated)
	allDiags = append(allDiags, mergeDiags...)
	return merged, allDiags, nil
}

func (o Orchestrator) discoveryProvider(root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder) (localProvider, func(), error) {
	acquirer := o.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	gitAcquirer := o.GitAcquirer
	if gitAcquirer == nil {
		gitAcquirer = sourcepkg.DefaultGitAcquirer{}
	}
	forbiddenRoots := append([]string(nil), request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = append(forbiddenRoots, root)
	provider := localProvider{
		repoRoot:                     root,
		sourceResolver:               sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, Offline: request.Offline}),
		chartAcquirer:                acquirer,
		gitAcquirer:                  gitAcquirer,
		remoteResourceAcquirer:       o.RemoteResourceAcquirer,
		pluginRenderer:               o.pluginRenderer(request),
		offline:                      request.Offline,
		refreshCharts:                request.RefreshCharts,
		chartCacheDir:                request.ChartCacheDir,
		chartCredentials:             request.ChartCredentials,
		ociChartRepositories:         ociChartRepositoriesFromSettings(settings),
		gitCacheDir:                  request.GitCacheDir,
		refreshGit:                   request.RefreshGit,
		gitCredentials:               request.GitCredentials,
		refreshRemoteResources:       request.RefreshRemoteResources,
		remoteResourceCacheDir:       request.RemoteResourceCacheDir,
		remoteResourceForbiddenRoots: forbiddenRoots,
		remoteResourceCredentials:    request.RemoteResourceCredentials,
		remoteResourceGitCredentials: request.RemoteResourceGitCredentials,
		pluginTimeout:                request.PluginTimeout,
		kustomizeBuildOptions:        settingsBuildOptions(settings),
		configManagementPlugins:      settings.ConfigManagementPlugins,
		cacheEvents:                  recorder,
	}
	snapshotRoot, err := os.MkdirTemp("", "drydock-discovery-cache-snapshots-*")
	if err != nil {
		return provider, func() {}, err
	}
	provider.acquisition = acquisition.Session{
		Locks:              processCacheTargetLocks,
		SnapshotRoot:       snapshotRoot,
		SnapshotCacheReads: shouldSnapshotCacheReads(request),
		SnapshotCache:      acquisition.NewSnapshotCache(),
	}
	return provider, func() { _ = os.RemoveAll(snapshotRoot) }, nil
}

func cleanDiscoverKustomizePath(root, rawPath string) (string, error) {
	clean, err := cleanLocalSourcePath(rawPath)
	if err != nil {
		return "", fmt.Errorf("discover-kustomize path %q: %w", rawPath, err)
	}
	if err := rejectLocalSymlinkComponents(root, clean); err != nil {
		return "", fmt.Errorf("discover-kustomize path %q: %w", rawPath, err)
	}
	if !hasKustomizationFile(filepath.Join(root, clean)) {
		return "", fmt.Errorf("discover-kustomize path %q does not contain a kustomization file", rawPath)
	}
	return clean, nil
}

func hasKustomizationFile(root string) bool {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		exists, err := localPathExists(filepath.Join(root, name))
		if err == nil && exists {
			return true
		}
	}
	return false
}

func manifestObjects(manifests []render.Manifest) []*unstructured.Unstructured {
	objects := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, manifest := range manifests {
		if manifest.Object == nil {
			continue
		}
		objects = append(objects, manifest.Object.DeepCopy())
	}
	return objects
}

func markDiscoveryTier(result *discovery.Result, tier discovery.SourceTier, inputPaths []string) {
	for i := range result.Applications {
		result.Applications[i].Tier = tier
		result.Applications[i].InputPaths = fallbackInputPaths(result.Applications[i].InputPaths, inputPaths, result.Applications[i].Path)
	}
	for i := range result.ApplicationSets {
		result.ApplicationSets[i].Tier = tier
		result.ApplicationSets[i].InputPaths = fallbackInputPaths(result.ApplicationSets[i].InputPaths, inputPaths, result.ApplicationSets[i].Path)
	}
	for i := range result.Projects {
		result.Projects[i].Tier = tier
	}
	for i := range result.SettingsCandidates {
		result.SettingsCandidates[i].Tier = tier
	}
}

func fallbackInputPaths(existing, fallback []string, path string) []string {
	if len(fallback) != 0 {
		return uniqueStrings(fallback)
	}
	if len(existing) != 0 {
		return uniqueStrings(existing)
	}
	if path == "" {
		return nil
	}
	return []string{filepath.ToSlash(path)}
}

func mergeDiscoveryResultsWithDiagnostics(base, overlay discovery.Result) (discovery.Result, []diagnostic.Diagnostic) {
	out := base
	diags := make([]diagnostic.Diagnostic, 0, len(overlay.Applications)+len(overlay.ApplicationSets)+len(overlay.Projects)+len(overlay.SettingsCandidates))
	var next []diagnostic.Diagnostic
	out.Applications, next = mergeApplications(out.Applications, overlay.Applications)
	diags = append(diags, next...)
	out.ApplicationSets, next = mergeApplicationSets(out.ApplicationSets, overlay.ApplicationSets)
	diags = append(diags, next...)
	out.Projects, next = mergeProjects(out.Projects, overlay.Projects)
	diags = append(diags, next...)
	out.SettingsCandidates, next = mergeSettingsCandidates(out.SettingsCandidates, overlay.SettingsCandidates)
	diags = append(diags, next...)
	return out, diags
}

func mergeApplications(base, overlay []discovery.ApplicationFile) ([]discovery.ApplicationFile, []diagnostic.Diagnostic) {
	out := append([]discovery.ApplicationFile(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, applicationDiscoveryKey(item.Application), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := applicationDiscoveryKey(item.Application)
		if index, ok := indexes[key]; ok {
			if sameApplicationDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict("Application", key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict("Application", existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

func mergeApplicationSets(base, overlay []discovery.ApplicationSetFile) ([]discovery.ApplicationSetFile, []diagnostic.Diagnostic) {
	out := append([]discovery.ApplicationSetFile(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, applicationSetDiscoveryKey(item.ApplicationSet), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := applicationSetDiscoveryKey(item.ApplicationSet)
		if index, ok := indexes[key]; ok {
			if sameApplicationSetDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict("ApplicationSet", key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict("ApplicationSet", existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

func mergeProjects(base, overlay []discovery.ProjectFile) ([]discovery.ProjectFile, []diagnostic.Diagnostic) {
	out := append([]discovery.ProjectFile(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, projectDiscoveryKey(item.Project), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := projectDiscoveryKey(item.Project)
		if index, ok := indexes[key]; ok {
			if sameProjectDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict("AppProject", key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict("AppProject", existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

func mergeSettingsCandidates(base, overlay []discovery.SettingsCandidate) ([]discovery.SettingsCandidate, []diagnostic.Diagnostic) {
	out := append([]discovery.SettingsCandidate(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, settingsDiscoveryKey(item), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := settingsDiscoveryKey(item)
		if index, ok := indexes[key]; ok {
			if sameSettingsDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict(settingsObjectKind(item.Kind), key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict(settingsObjectKind(item.Kind), existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

type discoveryLooseIndex struct {
	key       string
	index     int
	ambiguous bool
}

func addDiscoveryIndex(indexes map[string]int, looseIndexes map[string]discoveryLooseIndex, key string, index int) {
	indexes[key] = index
	looseKey, ok := looseDiscoveryKey(key)
	if !ok {
		return
	}
	if existing, ok := looseIndexes[looseKey]; ok && existing.key != key {
		looseIndexes[looseKey] = discoveryLooseIndex{ambiguous: true}
		return
	}
	looseIndexes[looseKey] = discoveryLooseIndex{key: key, index: index}
}

func removeDiscoveryLooseIndex(looseIndexes map[string]discoveryLooseIndex, key string) {
	looseKey, ok := looseDiscoveryKey(key)
	if !ok {
		return
	}
	existing, ok := looseIndexes[looseKey]
	if ok && existing.key == key && !existing.ambiguous {
		delete(looseIndexes, looseKey)
	}
}

func namespaceDefaultedConflict(indexes map[string]int, looseIndexes map[string]discoveryLooseIndex, key string) (int, string, bool) {
	if _, ok := indexes[key]; ok {
		return 0, "", false
	}
	looseKey, ok := looseDiscoveryKey(key)
	if !ok {
		return 0, "", false
	}
	existing, ok := looseIndexes[looseKey]
	if !ok || existing.ambiguous {
		return 0, "", false
	}
	existingNamespace, ok := discoveryKeyNamespace(existing.key)
	if !ok {
		return 0, "", false
	}
	incomingNamespace, ok := discoveryKeyNamespace(key)
	if !ok || existingNamespace == incomingNamespace {
		return 0, "", false
	}
	if existingNamespace == "" || incomingNamespace == "" {
		return existing.index, existing.key, true
	}
	return 0, "", false
}

func looseDiscoveryKey(key string) (string, bool) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", false
	}
	return parts[0] + "\x00" + parts[1] + "\x00" + parts[3], true
}

func discoveryKeyNamespace(key string) (string, bool) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 4 {
		return "", false
	}
	return parts[2], true
}

func resolveDiscoveryConflict(kind, key string, existingTier discovery.SourceTier, existingPath string, existingDocument int, incomingTier discovery.SourceTier, incomingPath string, incomingDocument int) (bool, *diagnostic.Diagnostic) {
	if existingTier == incomingTier && existingPath == incomingPath && existingDocument == incomingDocument {
		return true, nil
	}
	replace := discoveryTierPriority(incomingTier) < discoveryTierPriority(existingTier)
	winnerPath := existingPath
	ignoredPath := incomingPath
	if replace {
		winnerPath = incomingPath
		ignoredPath = existingPath
	}
	message := fmt.Sprintf("duplicate %s %s from %s ignored; %s takes precedence", kind, displayDiscoveryKey(key), ignoredPath, winnerPath)
	diag := diagnostic.Diagnostic{
		Severity: diagnostic.SeverityWarning,
		Category: "discovery",
		Message:  message,
		Provenance: diagnostic.Provenance{
			Path: ignoredPath,
		},
	}
	diag.Code = diagnostic.StableCode(diag)
	return replace, &diag
}

func discoveryTierPriority(tier discovery.SourceTier) int {
	switch tier {
	case discovery.SourceTierExplicitRendered:
		return 0
	case discovery.SourceTierStatic:
		return 1
	case discovery.SourceTierRenderedFleet:
		return 2
	default:
		return 1
	}
}

func resolveNamespaceDefaultedDiscoveryConflict(kind, existingKey, incomingKey string, existingTier discovery.SourceTier, existingPath string, existingDocument int, incomingTier discovery.SourceTier, incomingPath string, incomingDocument int) (bool, *diagnostic.Diagnostic) {
	existingNamespace, existingOK := discoveryKeyNamespace(existingKey)
	incomingNamespace, incomingOK := discoveryKeyNamespace(incomingKey)
	if !existingOK || !incomingOK || existingNamespace == incomingNamespace || (existingNamespace != "" && incomingNamespace != "") {
		return resolveDiscoveryConflict(kind, incomingKey, existingTier, existingPath, existingDocument, incomingTier, incomingPath, incomingDocument)
	}
	replace := incomingNamespace != ""
	return replace, nil
}

func sameApplicationDiscoveryObject(left, right discovery.ApplicationFile) bool {
	return reflect.DeepEqual(left.Application, right.Application)
}

func sameApplicationSetDiscoveryObject(left, right discovery.ApplicationSetFile) bool {
	return reflect.DeepEqual(left.ApplicationSet, right.ApplicationSet)
}

func sameProjectDiscoveryObject(left, right discovery.ProjectFile) bool {
	return reflect.DeepEqual(left.Project, right.Project)
}

func sameSettingsDiscoveryObject(left, right discovery.SettingsCandidate) bool {
	if left.Kind != right.Kind {
		return false
	}
	if left.Object != nil || right.Object != nil {
		if left.Object == nil || right.Object == nil {
			return false
		}
		return reflect.DeepEqual(left.Object.Object, right.Object.Object)
	}
	return left.APIVersion == right.APIVersion &&
		left.Namespace == right.Namespace &&
		left.Name == right.Name
}

func displayDiscoveryKey(key string) string {
	parts := strings.Split(key, "\x00")
	if len(parts) < 4 {
		return key
	}
	if parts[2] == "" {
		return parts[3]
	}
	return parts[2] + "/" + parts[3]
}

func discoveryDepthExceededDiagnostic(maxDepth int) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity: diagnostic.SeverityError,
		Category: "discovery",
		Message:  fmt.Sprintf("maximum discovery depth %d reached before rendered Application discovery converged", maxDepth),
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

func dedupeDiagnostics(input []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if len(input) == 0 {
		return nil
	}
	out := make([]diagnostic.Diagnostic, 0, len(input))
	seen := map[string]struct{}{}
	for _, diag := range input {
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", diag.Code, diag.Severity, diag.Category, diag.Message, diag.Provenance.Path, diag.Provenance.Pointer)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, diag)
	}
	return out
}

func uniqueStrings(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	out := make([]string, 0, len(input))
	seen := map[string]struct{}{}
	for _, value := range input {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func applicationDiscoveryKey(app argoappv1.Application) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "Application", app.Namespace, app.Name)
}

func applicationSetDiscoveryKey(appSet argoappv1.ApplicationSet) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "ApplicationSet", appSet.Namespace, appSet.Name)
}

func projectDiscoveryKey(project argoappv1.AppProject) string {
	return objectDiscoveryKey("argoproj.io/v1alpha1", "AppProject", project.Namespace, project.Name)
}

func settingsDiscoveryKey(candidate discovery.SettingsCandidate) string {
	if candidate.Object != nil && candidate.Object.GetKind() != "" && candidate.Object.GetName() != "" {
		return objectDiscoveryKey(candidate.Object.GetAPIVersion(), candidate.Object.GetKind(), candidate.Object.GetNamespace(), candidate.Object.GetName())
	}
	if candidate.APIVersion != "" && candidate.Name != "" {
		return objectDiscoveryKey(candidate.APIVersion, settingsObjectKind(candidate.Kind), candidate.Namespace, candidate.Name)
	}
	return fmt.Sprintf("settings\x00%s\x00%s\x00%d", candidate.Kind, filepath.ToSlash(candidate.Path), candidate.DocumentIndex)
}

func settingsObjectKind(kind string) string {
	switch kind {
	case "argocd-cm", "argocd-cmp-cm":
		return "ConfigMap"
	case "repository-secret":
		return "Secret"
	default:
		return kind
	}
}

func objectDiscoveryKey(apiVersion, kind, namespace, name string) string {
	return apiVersion + "\x00" + kind + "\x00" + namespace + "\x00" + name
}

func emptyApplicationSetDiagnostic(appSetFile discovery.ApplicationSetFile) diagnostic.Diagnostic {
	name := appSetFile.ApplicationSet.Name
	if appSetFile.ApplicationSet.Namespace != "" {
		name = appSetFile.ApplicationSet.Namespace + "/" + name
	}
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet %s generated zero Applications", name),
		Provenance: diagnostic.Provenance{Path: appSetFile.Path, Pointer: "spec.generators"},
	}
}
