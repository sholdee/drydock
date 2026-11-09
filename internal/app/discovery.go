package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
)

func (o Orchestrator) discoverRepository(ctx context.Context, root string, request BuildRequest) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, *applicationRenderCache, string, error) {
	ociFetcher := newAppsetOCIArtifactFetcher(ctx, o, root, request)
	discovered, diags, events, renderCache, renderSig, err := o.discoverWithControllerNamespace(ctx, root, request, ociFetcher)
	events = append(events, ociFetcher.close()...)
	return discovered, diags, events, renderCache, renderSig, err
}

// discoverRepositoryWithOCI discovers the repository once, and once more
// when the settings at the end set an argocd-cmd-params-cm key that changes
// ApplicationSet generation (appsetGenerationSettings) to a value other than
// the one the ApplicationSet expansions used. Fleet discovery can find such
// a setting late, in a rendered Argo CD install, after the ApplicationSets
// were expanded and their Applications rendered without it. The second run
// uses the final values from the start, so the result is the one committed
// settings give. It reuses the renders of the first run. Each key changes
// only some ApplicationSets (appsetGenerationSettings.affects), so without
// one of them there is no second run. There is never a third run: when the
// second run ends with yet other values, it keeps its result and warns
// about the cycle.
//
// A first run that fails once its first ApplicationSet expansion has chosen
// the values may fail because of them, for example on an ApplicationSet
// that only an Application Argo CD would never create declares. So its
// partial result decides the same way, and the second run's result or
// error is returned. A first run that fails earlier returns its error. So
// does a first run whose partial result does not hold the settings yet, for
// example when an app-of-apps renders the Argo CD install one level deeper
// than the failure.
func (o Orchestrator) discoverRepositoryWithOCI(ctx context.Context, root string, request BuildRequest, ociFetcher *appsetOCIArtifactFetcher) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, *applicationRenderCache, string, error) {
	var used *appsetGenerationSettings
	discovered, diags, events, renderCache, renderSig, runErr := o.observedDiscoveryRun(ctx, root, request, ociFetcher, &used)
	if used == nil || !hasGenerationSensitiveApplicationSet(discovered) {
		return discovered, diags, events, renderCache, renderSig, runErr
	}
	settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), discovered)
	if err != nil {
		if runErr != nil {
			err = runErr
		}
		return discovered, diags, events, renderCache, renderSig, err
	}
	final := appsetGenerationSettingsFrom(settings)
	if !final.affects(*used, discovered) {
		return discovered, diags, events, renderCache, renderSig, runErr
	}
	request.appsetGeneration = &final
	request.renderCache = renderCache
	restarted, diags, restartEvents, renderCache, renderSig, err := o.observedDiscoveryRun(ctx, root, request, ociFetcher, &used)
	events = append(events, restartEvents...)
	if err != nil {
		return restarted, diags, events, renderCache, renderSig, err
	}
	restartedSettings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), restarted)
	if err != nil {
		return restarted, diags, events, renderCache, renderSig, err
	}
	diags = append(diags, appsetGenerationCycleDiagnostics(final, appsetGenerationSettingsFrom(restartedSettings), settings, restarted)...)
	return restarted, diags, events, renderCache, renderSig, nil
}

// hasGenerationSensitiveApplicationSet reports a discovered ApplicationSet
// that some value of appsetGenerationSettings changes.
func hasGenerationSensitiveApplicationSet(discovered discovery.Result) bool {
	all := appsetGenerationSettings{ProgressiveSyncs: true, NewGitFileGlobbing: true, IncludeHiddenDirectories: true, SubmodulesDisabled: true}
	for _, appSetFile := range discovered.ApplicationSets {
		if all.affectsApplicationSet(appsetGenerationSettings{}, appSetFile) {
			return true
		}
	}
	return false
}

// appsetGenerationSettings are the argocd-cmd-params-cm values that change
// what an ApplicationSet generates, as the Argo CD v3.6.1 components parse
// them. Each discovery run uses one set of values for every expansion.
type appsetGenerationSettings struct {
	// ProgressiveSyncs is applicationsetcontroller.enable.progressive.syncs.
	ProgressiveSyncs bool
	// NewGitFileGlobbing is
	// applicationsetcontroller.enable.new.git.file.globbing.
	NewGitFileGlobbing bool
	// IncludeHiddenDirectories is reposerver.include.hidden.directories.
	IncludeHiddenDirectories bool
	// SubmodulesDisabled is applicationsetcontroller.enable.git.submodule
	// turned off.
	SubmodulesDisabled bool
}

func appsetGenerationSettingsFrom(settings config.ArgoSettings) appsetGenerationSettings {
	return appsetGenerationSettings{
		ProgressiveSyncs:         config.ParseEnableProgressiveSyncs(settings.EnableProgressiveSyncs.Value),
		NewGitFileGlobbing:       config.ParseEnableNewGitFileGlobbing(settings.EnableNewGitFileGlobbing.Value),
		IncludeHiddenDirectories: config.ParseIncludeHiddenDirectories(settings.IncludeHiddenDirectories.Value),
		SubmodulesDisabled:       !config.ParseEnableGitSubmodules(settings.AppSetEnableGitSubmodules.Value),
	}
}

func (s appsetGenerationSettings) apply(options *appset.Options) {
	options.ProgressiveSyncs = s.ProgressiveSyncs
	options.NewGitFileGlobbing = s.NewGitFileGlobbing
	options.IncludeHiddenDirectories = s.IncludeHiddenDirectories
	options.SubmodulesDisabled = s.SubmodulesDisabled
}

// affects reports whether s differs from used in a value that changes a
// discovered ApplicationSet: progressive syncs change only an
// ApplicationSet with a RollingSync strategy, Git file globbing only one
// with a Git files generator, and hidden directories only one with a Git or
// OCI directory generator.
func (s appsetGenerationSettings) affects(used appsetGenerationSettings, discovered discovery.Result) bool {
	if s == used {
		return false
	}
	for _, appSetFile := range discovered.ApplicationSets {
		if s.affectsApplicationSet(used, appSetFile) {
			return true
		}
	}
	return false
}

func (s appsetGenerationSettings) affectsApplicationSet(used appsetGenerationSettings, appSetFile discovery.ApplicationSetFile) bool {
	if s.ProgressiveSyncs != used.ProgressiveSyncs && appset.IsRollingSyncStrategy(appSetFile.ApplicationSet) {
		return true
	}
	if s.NewGitFileGlobbing == used.NewGitFileGlobbing && s.IncludeHiddenDirectories == used.IncludeHiddenDirectories && s.SubmodulesDisabled == used.SubmodulesDisabled {
		return false
	}
	uses := appset.RepositoryGeneratorsOf(appSetFile.ApplicationSet)
	return (s.NewGitFileGlobbing != used.NewGitFileGlobbing && uses.GitFiles) ||
		(s.IncludeHiddenDirectories != used.IncludeHiddenDirectories && uses.Directories) ||
		(s.SubmodulesDisabled != used.SubmodulesDisabled && (uses.GitFiles || uses.Directories))
}

// appsetGenerationCycleDiagnostics warns for each value that a second
// discovery run used and that its own result sets to another value.
// settings are the first run's, which set the value the second run used.
func appsetGenerationCycleDiagnostics(used, final appsetGenerationSettings, settings config.ArgoSettings, discovered discovery.Result) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	if used.ProgressiveSyncs != final.ProgressiveSyncs {
		diags = append(diags, progressiveSyncsCycleDiagnostic(used.ProgressiveSyncs, settings.EnableProgressiveSyncs.Provenance))
	}
	if used.NewGitFileGlobbing != final.NewGitFileGlobbing && (appsetGenerationSettings{NewGitFileGlobbing: final.NewGitFileGlobbing}).affects(appsetGenerationSettings{NewGitFileGlobbing: used.NewGitFileGlobbing}, discovered) {
		diags = append(diags, appsetGenerationCycleDiagnostic("applicationsetcontroller.enable.new.git.file.globbing", "a Git files generator", used.NewGitFileGlobbing, settings.EnableNewGitFileGlobbing.Provenance))
	}
	if used.IncludeHiddenDirectories != final.IncludeHiddenDirectories && (appsetGenerationSettings{IncludeHiddenDirectories: final.IncludeHiddenDirectories}).affects(appsetGenerationSettings{IncludeHiddenDirectories: used.IncludeHiddenDirectories}, discovered) {
		diags = append(diags, appsetGenerationCycleDiagnostic("reposerver.include.hidden.directories", "a Git or OCI directory generator", used.IncludeHiddenDirectories, settings.IncludeHiddenDirectories.Provenance))
	}
	if used.SubmodulesDisabled != final.SubmodulesDisabled && (appsetGenerationSettings{SubmodulesDisabled: final.SubmodulesDisabled}).affects(appsetGenerationSettings{SubmodulesDisabled: used.SubmodulesDisabled}, discovered) {
		diags = append(diags, appsetGenerationCycleDiagnostic("applicationsetcontroller.enable.git.submodule", "a Git generator", !used.SubmodulesDisabled, settings.AppSetEnableGitSubmodules.Provenance))
	}
	return diags
}

// progressiveSyncsCycleDiagnostic reports a progressive syncs value that
// decides its own source: with it, an ApplicationSet with a RollingSync
// strategy adds or removes the Application that sets it. The "has no stable
// value" substring is the stable-code dispatch key
// (settings.progressive-syncs-cycle). It is not strict-exempt: the result
// cannot match Argo CD with either value.
func progressiveSyncsCycleDiagnostic(used bool, provenance config.Provenance) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    fmt.Sprintf("applicationsetcontroller.enable.progressive.syncs has no stable value: discovery with it %t ends with it %t, because an ApplicationSet with a RollingSync strategy decides whether the Application that sets it exists; drydock keeps the result with it %t", used, !used, used),
		Provenance: provenance,
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

// appsetGenerationCycleDiagnostic is progressiveSyncsCycleDiagnostic for
// the other keys of appsetGenerationSettings. The "does not settle"
// substring is the stable-code dispatch key
// (settings.appset-generation-cycle). It is not strict-exempt either.
func appsetGenerationCycleDiagnostic(key, generator string, used bool, provenance config.Provenance) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    fmt.Sprintf("%s does not settle: discovery with it %t ends with it %t, because an ApplicationSet with %s decides whether the Application that sets it exists; drydock keeps the result with it %t", key, used, !used, generator, used),
		Provenance: provenance,
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

// observedDiscoveryRun calls the discoveryRunObserver test seam and runs
// discoverRepositoryRun.
func (o Orchestrator) observedDiscoveryRun(ctx context.Context, root string, request BuildRequest, ociFetcher *appsetOCIArtifactFetcher, used **appsetGenerationSettings) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, *applicationRenderCache, string, error) {
	if o.discoveryRunObserver != nil {
		o.discoveryRunObserver()
	}
	return o.discoverRepositoryRun(ctx, root, request, ociFetcher, used)
}

// discoverRepositoryRun is one discovery run. used receives the
// appsetGenerationSettings that its ApplicationSet expansions use, as soon
// as expandFirstApplicationSets has chosen them. It stays nil when the run
// fails before that choice.
func (o Orchestrator) discoverRepositoryRun(ctx context.Context, root string, request BuildRequest, ociFetcher *appsetOCIArtifactFetcher, used **appsetGenerationSettings) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, *applicationRenderCache, string, error) {
	mode, maxDepth, err := normalizeDiscoveryOptions(request.DiscoveryOptions)
	if err != nil {
		return discovery.Result{}, nil, nil, nil, "", err
	}
	renderCache := request.renderCache
	if renderCache == nil {
		renderCache = newApplicationRenderCache()
	}
	request.discoveryPathMemo = &sync.Map{}
	request.appsetGenerationMemo = &sync.Map{}
	request.settingsInstallMemo = &sync.Map{}
	if request.committedRenderMemo == nil {
		request.committedRenderMemo = &sync.Map{}
	}

	discovered, externalDiags, err := scanRepositoryWithSubmodules(ctx, root, request)
	if err != nil {
		return discovered, externalDiags, nil, renderCache, "", err
	}

	appsetOptions, providerDiags, err := applicationSetOptionsForRun(ctx, root, request, ociFetcher)
	if err != nil {
		return discovered, providerDiags, nil, renderCache, "", diagnosticsError(providerDiags, err)
	}
	var allDiags []diagnostic.Diagnostic
	allDiags = append(allDiags, externalDiags...)
	allDiags = append(allDiags, providerDiags...)
	var allEvents []cacheevent.Event

	discovered, explicitDiags, explicitEvents, err := o.applyExplicitKustomizeDiscovery(ctx, root, request, discovered)
	allDiags = append(allDiags, explicitDiags...)
	allEvents = append(allEvents, explicitEvents...)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}

	var expansionDiags []diagnostic.Diagnostic
	discovered, expansionDiags, err = expandFirstApplicationSets(root, request, discovered, &appsetOptions, used)
	allDiags = append(allDiags, expansionDiags...)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}

	discovered, bootstrapDiags, bootstrapEvents, err := o.applyPolicyBootstrapDiscovery(ctx, root, request, discovered, appsetOptions, renderCache, mode)
	allDiags = append(allDiags, bootstrapDiags...)
	allEvents = append(allEvents, bootstrapEvents...)
	if err != nil {
		return discovered, allDiags, allEvents, renderCache, "", err
	}

	settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), discovered)
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

	discovered, gateDiags, err := applyFinalGates(root, request, discovered)
	return discovered, dedupeDiagnostics(append(allDiags, gateDiags...)), allEvents, renderCache, renderSig, err
}

// applicationSetOptionsForRun returns the ApplicationSet options of one
// discovery run: the provider data of the request, the OCI fetcher, and,
// for a working tree rather than a snapshot of a Git ref, the filter that
// hides from the Git generator what Git ignores and does not track.
func applicationSetOptionsForRun(ctx context.Context, root string, request BuildRequest, ociFetcher *appsetOCIArtifactFetcher) (appset.Options, []diagnostic.Diagnostic, error) {
	appsetOptions, diags, err := applicationSetOptionsForRequest(request)
	if err != nil {
		return appsetOptions, diags, err
	}
	appsetOptions.OCI = ociFetcher
	if request.rootRevision == "" {
		appsetOptions.Worktree = newLazyWorktreeFilter(ctx, root)
	} else {
		appsetOptions.Worktree = &snapshotGitlinks{root: root, links: request.rootSnapshot.gitlinks, listed: request.rootSnapshot.gitlinks != nil}
	}
	return appsetOptions, diags, nil
}

// applyFinalGates runs the checks that need the whole discovery result,
// with the settings at the end of discovery. The namespace gate prunes what
// Argo CD never reconciles. Fleet discovery can find argocd-cmd-params-cm
// late, after it expanded an ApplicationSet or rendered an Application that
// Argo CD never reconciles, so the gate runs once more on the whole result.
// Then the AppProject lookup of the Git generator
// (warnGitGeneratorProjects) warns about an AppProject that the repository
// does not declare.
func applyFinalGates(root string, request BuildRequest, discovered discovery.Result) (discovery.Result, []diagnostic.Diagnostic, error) {
	settings, landing, _, err := loadSettingsWithLanding(root, request.namespaceScope(), discovered)
	if err != nil {
		return discovered, nil, err
	}
	discovered, diags := pruneNotEnabledNamespaces(discovered, namespaceGateFromSettings(settings))
	projectDiags := warnGitGeneratorProjects(discovered, settings, landing)
	return discovered, append(diags, request.normalizeDiagnostics(projectDiags, false)...), nil
}

// expandFirstApplicationSets sets the appsetGenerationSettings that every
// ApplicationSet expansion of one discovery run uses, and runs the first
// expansion. The values are the ones a second run carries, or else the ones
// in the settings found before the first expansion. used receives them once
// they are chosen.
func expandFirstApplicationSets(root string, request BuildRequest, discovered discovery.Result, appsetOptions *appset.Options, used **appsetGenerationSettings) (discovery.Result, []diagnostic.Diagnostic, error) {
	generation := appsetGenerationSettings{}
	if request.appsetGeneration != nil {
		generation = *request.appsetGeneration
	} else {
		settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), discovered)
		if err != nil {
			return discovered, nil, err
		}
		generation = appsetGenerationSettingsFrom(settings)
	}
	generation.apply(appsetOptions)
	*used = &generation
	return expandApplicationSetDiscovery(root, request, discovered, *appsetOptions)
}

// scanRepositoryDiscovery scans root for committed objects and adds the
// operator-supplied --discover-manifest objects.
func scanRepositoryDiscovery(root string, request BuildRequest) (discovery.Result, []diagnostic.Diagnostic, error) {
	discovered, err := discovery.Scan(root, discovery.Options{IgnoreGlobs: request.DiscoverIgnoreGlobs})
	if err != nil {
		return discovery.Result{}, nil, err
	}
	markDiscoveryTier(&discovered, discovery.SourceTierStatic, nil)
	return mergeDiscoverManifests(request, discovered)
}

func (o Orchestrator) applyExplicitKustomizeDiscovery(ctx context.Context, root string, request BuildRequest, discovered discovery.Result) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	if len(request.DiscoverKustomizePaths) == 0 {
		return discovered, nil, nil, nil
	}
	settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), discovered)
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
	// A --discover-kustomize path is not an Argo CD source, so
	// kustomize.buildOptions does not decide its flags. It is usually the
	// install that a bootstrap builds with --enable-helm.
	provider, cleanup, err := o.discoveryProvider(ctx, root, settings.WithKustomizeHelmAssumed(), request, recorder)
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
		markRenderedOrigin(&next, "", displayPath)
		if hasSettingsConfigMapCandidate(next) {
			// Best-effort: without the graph paths, committed settings
			// files stay candidates as before.
			if values, err := render.KustomizeHelmValues(ctx, root, displayPath, nil); err == nil {
				markSettingsRenderInputs(&next, settingsInstall{valuesFilesRead: values.ValuesFiles, files: values.Files})
			}
		}
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
		settled, diags, events, err := o.settleSettingsInstalls(ctx, root, request, current, start.SettingsCandidates, renderCache)
		allDiags = append(allDiags, diags...)
		allEvents = append(allEvents, events...)
		if err != nil {
			return current, settingsSig, renderSig, allDiags, allEvents, err
		}
		current = settled
		settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), current)
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

		nextSettings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), next)
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

// maxSettingsInstallRounds bounds settleSettingsInstalls. One round settles
// the usual case: the settings read from the values files and the rendered
// settings render the installs the same way. Three rounds find a cycle of
// two states.
const maxSettingsInstallRounds = 3

// settleSettingsInstalls renders the Applications that drydock renders only
// for their Argo CD settings (renderSettingsInstalls) before the rest of
// the frontier, until the settings they give no longer change how they
// render (settingsInstallRenderSignature). The frontier then renders every
// other Application once, with those settings, instead of once with the
// settings of the values files and again with the rendered ones. When the
// rounds run out, the frontier loop keeps converging as before.
//
// Each round starts again from the discovery result without the settings
// of the settings-only renders of earlier rounds and earlier frontier
// iterations (withoutSettingsOnlyRenders), and adds the renders of the
// installs that count with the settings of this round. So an install that
// counted in an earlier round, but does not count with the settings that
// settle, leaves no settings behind. An example is an install whose
// destination a cluster Secret, which a later frontier iteration finds,
// moves to another cluster.
//
// The installs can also fail to settle: a round comes back to the settings
// of an earlier round, because an install counts only without the settings
// that it renders, for example an argocd-cm that turns the in-cluster
// destination off. Then drydock keeps the last settings-only render of
// every install that counted in a round, as earlier releases did, and
// warns once for each install that counted in some rounds and not in others
// (settingsInstallsUnsettledDiagnostic). start holds the settings
// candidates that discovery found before the frontier. They come back when
// no render reads them anymore.
func (o Orchestrator) settleSettingsInstalls(ctx context.Context, root string, request BuildRequest, current discovery.Result, start []discovery.SettingsCandidate, renderCache *applicationRenderCache) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	var allDiags []diagnostic.Diagnostic
	var allEvents []cacheevent.Event
	previous := ""
	// Only the namespace of the last round counts as used
	// (discoverWithControllerNamespace): a later round renders the installs
	// again with the namespace that an earlier round found.
	used := ""
	defer func() {
		if used != "" {
			request.renderNamespaces.record(used)
		}
	}()
	rounds := settingsInstallRounds{seen: map[string]bool{}, renders: map[string]settingsInstallRender{}}
	for round := range maxSettingsInstallRounds {
		settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), current)
		if err != nil {
			return current, allDiags, allEvents, err
		}
		installSig, err := settingsInstallRenderSignature(settings)
		if err != nil {
			return current, allDiags, allEvents, err
		}
		// The controller namespace decides the instance name of an
		// install, so a new one renders the installs again. It is part of
		// the tracking options of the render key, not of installSig.
		settled := installSig + "\x00" + settings.ControllerNamespace("")
		if round > 0 && settled == previous {
			break
		}
		if rounds.seen[settled] {
			var mergeDiags []diagnostic.Diagnostic
			current, mergeDiags = mergeDiscoveryResultsWithDiagnostics(withoutSettingsOnlyRenders(current, start), rounds.union())
			allDiags = append(allDiags, mergeDiags...)
			allDiags = append(allDiags, rounds.unsettledDiagnostics()...)
			break
		}
		rounds.seen[settled] = true
		previous = settled
		installs := o.settingsInstallApplications(ctx, root, request, current, settings)
		base := withoutSettingsOnlyRenders(current, start)
		if len(installs) == 0 {
			rounds.record(nil)
			if !hasSettingsOnlyRender(current) {
				return current, allDiags, allEvents, nil
			}
			// The settings of an earlier settings-only render are gone, so
			// the next round checks the installs again without them.
			current = base
			continue
		}
		rendered, diags, events, err := o.renderSettingsInstalls(ctx, root, request, current, settings, installSig, installs, renderCache)
		used = settings.ControllerNamespace("")
		allDiags = append(allDiags, diags...)
		allEvents = append(allEvents, events...)
		if err != nil {
			return current, allDiags, allEvents, err
		}
		rounds.record(rendered)
		current = base
		for _, item := range rendered {
			var mergeDiags []diagnostic.Diagnostic
			current, mergeDiags = mergeDiscoveryResultsWithDiagnostics(current, item.result)
			allDiags = append(allDiags, mergeDiags...)
		}
	}
	return current, allDiags, allEvents, nil
}

// settingsInstallRender is the settings-only render of one install.
type settingsInstallRender struct {
	appFile discovery.ApplicationFile
	result  discovery.Result
}

// settingsInstallRounds records the installs of each settle round.
type settingsInstallRounds struct {
	// seen holds the settled signature of each round.
	seen map[string]bool
	// order lists the key of each install that counted in a round, in the
	// order the rounds found them, and renders holds its last render.
	order   []string
	renders map[string]settingsInstallRender
	// counted holds, for each round, the keys of the installs that
	// counted in it.
	counted []map[string]bool
}

func (r *settingsInstallRounds) record(rendered []settingsInstallRender) {
	keys := map[string]bool{}
	for _, item := range rendered {
		key := applicationKey(item.appFile.Application)
		if _, ok := r.renders[key]; !ok {
			r.order = append(r.order, key)
		}
		r.renders[key] = item
		keys[key] = true
	}
	r.counted = append(r.counted, keys)
}

// union merges the last render of every install that counted in a round.
func (r *settingsInstallRounds) union() discovery.Result {
	var out discovery.Result
	for _, key := range r.order {
		out, _ = mergeDiscoveryResultsWithDiagnostics(out, r.renders[key].result)
	}
	return out
}

// unsettledDiagnostics warns once for each install that counted in some
// rounds and not in others.
func (r *settingsInstallRounds) unsettledDiagnostics() []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for _, key := range r.order {
		for _, counted := range r.counted {
			if !counted[key] {
				diags = append(diags, settingsInstallsUnsettledDiagnostic(r.renders[key].appFile))
				break
			}
		}
	}
	return diags
}

// settingsInstallsUnsettledDiagnostic reports an install that counts as an
// Argo CD install on this cluster only without the settings that some
// installs render, so the settings do not settle.
func settingsInstallsUnsettledDiagnostic(appFile discovery.ApplicationFile) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:       diagnostic.CodeSettingsInstallsUnsettled,
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    fmt.Sprintf("the Argo CD settings do not settle: Application %s installs Argo CD on this cluster with some of the settings that discovery finds and not with others, for example because a setting that it renders moves or turns off its destination; drydock keeps the settings that it renders", applicationDisplayName(appFile.Application)),
		Provenance: diagnostic.Provenance{Path: filepath.ToSlash(appFile.Path)},
	}
}

// hasSettingsOnlyRender reports a settings candidate of a settings-only
// render in result.
func hasSettingsOnlyRender(result discovery.Result) bool {
	for _, candidate := range result.SettingsCandidates {
		if candidate.SettingsOnly {
			return true
		}
	}
	return false
}

// withoutSettingsOnlyRenders drops the settings candidates of the
// settings-only renders from result. A candidate of start that a merge
// dropped because such a render read it (mergeSettingsCandidates) comes
// back in its place when no render that stays reads it. The other
// candidates keep their order.
func withoutSettingsOnlyRenders(result discovery.Result, start []discovery.SettingsCandidate) discovery.Result {
	kept := make([]discovery.SettingsCandidate, 0, len(result.SettingsCandidates))
	present := make(map[string]struct{}, len(result.SettingsCandidates))
	for _, candidate := range result.SettingsCandidates {
		if candidate.SettingsOnly {
			continue
		}
		kept = append(kept, candidate)
		present[settingsCandidateLoadKey(candidate)] = struct{}{}
	}
	if len(kept) == len(result.SettingsCandidates) {
		return result
	}
	inputs := renderedSettingsInputs(kept)
	out := make([]discovery.SettingsCandidate, 0, len(kept)+len(start))
	fromStart := make(map[string]struct{}, len(start))
	byKey := make(map[string]discovery.SettingsCandidate, len(kept))
	for _, candidate := range kept {
		byKey[settingsCandidateLoadKey(candidate)] = candidate
	}
	for _, candidate := range start {
		key := settingsCandidateLoadKey(candidate)
		fromStart[key] = struct{}{}
		if current, ok := byKey[key]; ok {
			out = append(out, current)
			continue
		}
		if _, ok := present[key]; !ok && !candidate.SettingsOnly && !supersededBySettingsRender(candidate, inputs) {
			out = append(out, candidate)
		}
	}
	for _, candidate := range kept {
		if _, ok := fromStart[settingsCandidateLoadKey(candidate)]; !ok {
			out = append(out, candidate)
		}
	}
	result.SettingsCandidates = out
	return result
}

// settingsInstallRenderSignature is renderSettingsSignature without the
// tracking settings and the resource customizations. Tracking changes only
// the metadata of rendered objects, and a customization changes only health
// and diffs, so neither changes the data of a rendered settings ConfigMap.
// A change of these alone does not need another render of the installs.
func settingsInstallRenderSignature(settings config.ArgoSettings) (string, error) {
	settings.TrackingMethod = config.Value[string]{}
	settings.InstanceLabelKey = config.Value[string]{}
	settings.InstallationID = config.Value[string]{}
	settings.ResourceCustomizations = nil
	return renderSettingsSignature(settings)
}

// settingsInstallApplications returns the discovered Applications whose
// Helm values set Argo CD settings or that render the argo-cd chart, on this
// cluster, that the frontier does not render for discovery
// (applicationMayRenderDiscoveryObjects).
func (o Orchestrator) settingsInstallApplications(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, settings config.ArgoSettings) []discovery.ApplicationFile {
	probe := settingsInstallProbe(root, request, settings.ControllerNamespace(""))
	explicit := explicitDiscoverKustomizePaths(request)
	var installs []discovery.ApplicationFile
	for _, appFile := range discovered.Applications {
		if applicationMayRenderDiscoveryObjects(root, request, discovered, appFile.Application) {
			continue
		}
		if applicationRendersExplicitKustomizePath(probe, appFile.Application, explicit) {
			// --discover-kustomize already rendered this path, and that
			// render is the install: it holds the settings ConfigMaps and
			// supersedes the files the render read. A second render would
			// only give duplicates with other tracking metadata.
			continue
		}
		if settingsInstallInCluster(appFile.Application, settings) && applicationSettingsInstall(ctx, request, probe, appFile.Application).installsArgoCD() {
			installs = append(installs, appFile)
		}
	}
	return installs
}

// renderSettingsInstalls renders installs (settingsInstallApplications) for
// their Argo CD settings only, and returns the render of each.
//
// The render is keyed by settingsInstallRenderSignature and default tracking
// in the controller namespace, not by the full render settings: it only
// reads the data of the settings ConfigMaps, which these leave out, so a
// later round or frontier iteration with the same signature reuses it. The
// final render renders the Application again with the full settings.
func (o Orchestrator) renderSettingsInstalls(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, settings config.ArgoSettings, installSig string, installs []discovery.ApplicationFile, renderCache *applicationRenderCache) ([]settingsInstallRender, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	// The render reads the settings that the install creates, so the
	// settings found so far must not stop it (WithKustomizeHelmAssumed).
	// Its cache key starts with "settings-install", apart from the keys of
	// the final render, which uses the found settings as Argo CD does.
	provider, cleanup, err := o.discoveryProvider(ctx, root, settings.WithKustomizeHelmAssumed(), request, recorder)
	if err != nil {
		return nil, nil, recorder.Events(), err
	}
	defer cleanup()

	inputs := applicationInputsByKey(discovered)
	renderSettings := discoveryRenderSettings{
		signature: "settings-install\x00" + installSig,
		// Only the controller namespace of the tracking options counts:
		// the instance name reaches the build environment and the
		// override file name, as in the final render.
		tracking:     TrackingOptions{ControllerNamespace: settings.ControllerNamespace("")},
		settings:     settings,
		applications: discovered.Applications,
	}
	out := make([]settingsInstallRender, 0, len(installs))
	var allDiags []diagnostic.Diagnostic
	for _, appFile := range installs {
		next, diags, err := renderAndScanDiscoveryApplication(ctx, request, provider, renderSettings, renderCache, inputs, appFile, true)
		allDiags = append(allDiags, diags...)
		if err != nil {
			return out, allDiags, recorder.Events(), err
		}
		for i := range next.SettingsCandidates {
			next.SettingsCandidates[i].SettingsOnly = true
		}
		out = append(out, settingsInstallRender{appFile: appFile, result: next})
	}
	return out, allDiags, recorder.Events(), nil
}

func (o Orchestrator) discoveryProvider(ctx context.Context, root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder) (localProvider, func(), error) {
	return newLocalProvider(ctx, o, root, settings, request, recorder, "drydock-discovery-cache-snapshots-*")
}

// scanRepositoryWithSubmodules scans the repository at root, and then
// checks out the submodules of a snapshot root before its ApplicationSets
// read it (fillSnapshotSubmodulesForDiscovery), with a warning for the
// submodules it could not check out.
func scanRepositoryWithSubmodules(ctx context.Context, root string, request BuildRequest) (discovery.Result, []diagnostic.Diagnostic, error) {
	discovered, diags, err := scanRepositoryDiscovery(root, request)
	if err != nil {
		return discovered, diags, err
	}
	if request.rootSnapshot.options == nil {
		return discovered, diags, nil
	}
	fill, err := fillSnapshotSubmodulesForDiscovery(ctx, root, request, discovered)
	if diag, ok := fill.missingDiagnostic(); ok {
		diags = append(diags, diag)
	}
	return discovered, diags, err
}

// fillSnapshotSubmodulesForDiscovery checks out the submodules of a
// snapshot root before its ApplicationSets read it, with the settings that
// discovery found (fillSnapshotSubmodules). It fails only when it cannot
// read the settings: a configuration that git refuses, or a submodule
// that drydock cannot find, fails the sources that render from it.
func fillSnapshotSubmodulesForDiscovery(ctx context.Context, root string, request BuildRequest, discovered discovery.Result) (*snapshotFill, error) {
	settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), discovered)
	if err != nil {
		return nil, err
	}
	return fillSnapshotSubmodules(ctx, root, request, settings), nil
}
