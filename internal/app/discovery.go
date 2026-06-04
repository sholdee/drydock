package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
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

func (o Orchestrator) discoveryProvider(root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder) (localProvider, func(), error) {
	return newLocalProvider(o, root, settings, request, recorder, "drydock-discovery-cache-snapshots-*")
}
