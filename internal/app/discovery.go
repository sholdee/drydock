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
	discovered, diags, events, renderCache, renderSig, err := o.discoverRepositoryWithOCI(ctx, root, request, ociFetcher)
	events = append(events, ociFetcher.close()...)
	return discovered, diags, events, renderCache, renderSig, err
}

func (o Orchestrator) discoverRepositoryWithOCI(ctx context.Context, root string, request BuildRequest, ociFetcher *appsetOCIArtifactFetcher) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, *applicationRenderCache, string, error) {
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
	request.discoveryPathMemo = &sync.Map{}
	request.appsetGenerationMemo = &sync.Map{}
	request.settingsInstallMemo = &sync.Map{}

	discovered, externalDiags, err := scanRepositoryDiscovery(root, request)
	if err != nil {
		return discovered, externalDiags, nil, renderCache, "", err
	}

	appsetOptions, providerDiags, err := applicationSetOptionsForRequest(request)
	if err != nil {
		return discovered, providerDiags, nil, renderCache, "", diagnosticsError(providerDiags, err)
	}
	appsetOptions.OCI = ociFetcher
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
	discovered, expansionDiags, err = expandApplicationSetDiscovery(root, request, discovered, appsetOptions)
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
	provider, cleanup, err := o.discoveryProvider(ctx, root, settings, request, recorder)
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
			if values, err := render.KustomizeHelmValues(ctx, root, displayPath, nil, nil); err == nil {
				markSettingsRenderInputs(&next, settingsInstall{valuesFilesRead: values.ValuesFiles, files: values.Files}, "", defaultArgoCDControllerNamespace)
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
		settled, diags, events, err := o.settleSettingsInstalls(ctx, root, request, current, renderCache)
		allDiags = append(allDiags, diags...)
		allEvents = append(allEvents, events...)
		if err != nil {
			return current, settingsSig, renderSig, allDiags, allEvents, err
		}
		current = settled
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

// maxSettingsInstallRounds bounds settleSettingsInstalls. One round settles
// the usual case: the settings read from the values files and the rendered
// settings render the installs the same way.
const maxSettingsInstallRounds = 3

// settleSettingsInstalls renders the Applications that drydock renders only
// for their Argo CD settings (renderSettingsInstalls) before the rest of
// the frontier, until the settings they give no longer change how they
// render (settingsInstallRenderSignature). The frontier then renders every
// other Application once, with those settings, instead of once with the
// settings of the values files and again with the rendered ones. When the
// rounds run out, the frontier loop keeps converging as before.
func (o Orchestrator) settleSettingsInstalls(ctx context.Context, root string, request BuildRequest, current discovery.Result, renderCache *applicationRenderCache) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	var allDiags []diagnostic.Diagnostic
	var allEvents []cacheevent.Event
	previous := ""
	for round := range maxSettingsInstallRounds {
		settings, _, err := loadSettingsFromDiscovery(root, current)
		if err != nil {
			return current, allDiags, allEvents, err
		}
		installSig, err := settingsInstallRenderSignature(settings)
		if err != nil {
			return current, allDiags, allEvents, err
		}
		if round > 0 && installSig == previous {
			break
		}
		previous = installSig
		rendered, rendersInstalls, diags, events, err := o.renderSettingsInstalls(ctx, root, request, current, settings, installSig, renderCache)
		allDiags = append(allDiags, diags...)
		allEvents = append(allEvents, events...)
		if err != nil || !rendersInstalls {
			return current, allDiags, allEvents, err
		}
		var mergeDiags []diagnostic.Diagnostic
		current, mergeDiags = mergeDiscoveryResultsWithDiagnostics(current, rendered)
		allDiags = append(allDiags, mergeDiags...)
	}
	return current, allDiags, allEvents, nil
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

// renderSettingsInstalls renders, for their Argo CD settings only, the
// discovered Applications whose Helm values set Argo CD settings and that the
// frontier does not render for discovery (applicationMayRenderDiscoveryObjects).
// rendersInstalls is false when there is none.
//
// The render is keyed by settingsInstallRenderSignature and default tracking,
// not by the full render settings: it only reads the data of the settings
// ConfigMaps, which these leave out, so a later round or frontier iteration
// with the same signature reuses it. The final render renders the
// Application again with the full settings.
func (o Orchestrator) renderSettingsInstalls(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, settings config.ArgoSettings, installSig string, renderCache *applicationRenderCache) (discovery.Result, bool, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	probe := settingsInstallProbe(root, request)
	var installs []discovery.ApplicationFile
	for _, appFile := range discovered.Applications {
		if applicationMayRenderDiscoveryObjects(root, request, discovered, appFile.Application) {
			continue
		}
		if settingsInstallInCluster(appFile.Application, settings) && applicationSettingsInstall(ctx, request, probe, appFile.Application).installsArgoCD() {
			installs = append(installs, appFile)
		}
	}
	if len(installs) == 0 {
		return discovery.Result{}, false, nil, nil, nil
	}

	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	provider, cleanup, err := o.discoveryProvider(ctx, root, settings, request, recorder)
	if err != nil {
		return discovery.Result{}, true, nil, recorder.Events(), err
	}
	defer cleanup()

	inputs := applicationInputsByKey(discovered)
	renderSettings := discoveryRenderSettings{signature: "settings-install\x00" + installSig, settings: settings}
	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, appFile := range installs {
		next, diags, err := renderAndScanDiscoveryApplication(ctx, request, provider, renderSettings, renderCache, inputs, appFile, true)
		allDiags = append(allDiags, diags...)
		if err != nil {
			return out, true, allDiags, recorder.Events(), err
		}
		var mergeDiags []diagnostic.Diagnostic
		out, mergeDiags = mergeDiscoveryResultsWithDiagnostics(out, next)
		allDiags = append(allDiags, mergeDiags...)
	}
	return out, true, allDiags, recorder.Events(), nil
}

func (o Orchestrator) discoveryProvider(ctx context.Context, root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder) (localProvider, func(), error) {
	return newLocalProvider(ctx, o, root, settings, request, recorder, "drydock-discovery-cache-snapshots-*")
}
