package app

import (
	"context"
	"fmt"
	"path/filepath"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (o Orchestrator) renderDiscoveryFrontier(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, settings config.ArgoSettings, settingsSig string, renderCache *applicationRenderCache) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	provider, cleanup, err := o.discoveryProvider(ctx, root, settings, request, recorder)
	if err != nil {
		return discovery.Result{}, nil, recorder.Events(), err
	}
	defer cleanup()

	inputs := applicationInputsByKey(discovered)
	renderSettings := discoveryRenderSettings{signature: settingsSig, tracking: trackingOptionsFromSettings(settings), settings: settings}
	parallelism, err := normalizeParallelism(request.Parallelism)
	if err != nil {
		return discovery.Result{}, nil, recorder.Events(), err
	}
	if parallelism > 1 && len(discovered.Applications) > 1 {
		return renderDiscoveryFrontierParallel(ctx, root, request, discovered, inputs, provider, renderSettings, renderCache, parallelism)
	}

	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	for _, appFile := range discovered.Applications {
		next, scanDiags, err := renderDiscoveryApplication(ctx, root, request, provider, renderSettings, renderCache, inputs, discovered, appFile)
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

// discoveryRenderSettings is what a frontier render reads from the Argo CD
// settings: the render settings signature and the tracking options. The
// final render uses the same tracking options, so it reuses a frontier
// render of the same Application.
type discoveryRenderSettings struct {
	signature string
	tracking  TrackingOptions
	// settings resolves Application destinations
	// (settingsInstallInCluster).
	settings config.ArgoSettings
}

type indexedDiscoveryRenderResult struct {
	result discovery.Result
	diags  []diagnostic.Diagnostic
	events []cacheevent.Event
	err    error
}

// renderDiscoveryFrontierParallel merges the frontier in frontier order, as
// the sequential frontier does: the first Application to fail decides the
// error, and only it and the Applications before it contribute diagnostics
// and cache events. A failure therefore waits for the slower Applications
// before it to finish, as the sequential frontier would have rendered them
// first; only the Applications after it are cut short.
func renderDiscoveryFrontierParallel(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, inputs map[string]applicationInputPaths, provider localProvider, renderSettings discoveryRenderSettings, renderCache *applicationRenderCache, parallelism int) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, error) {
	applications := discovered.Applications
	results, completed, parallelErr := runOrderedParallel(ctx, orderedParallelOptions[indexedDiscoveryRenderResult]{
		total:       len(applications),
		parallelism: parallelism,
		run: func(ctx context.Context, index int) indexedDiscoveryRenderResult {
			return renderParallelDiscoveryApplication(ctx, root, request, provider, renderSettings, renderCache, inputs, discovered, applications[index])
		},
		shouldStop: func(result indexedDiscoveryRenderResult) bool {
			return result.err != nil
		},
	})
	if parallelErr != nil {
		var events []cacheevent.Event
		for index, result := range results {
			if completed[index] {
				events = append(events, result.events...)
			}
		}
		return discovery.Result{}, nil, events, parallelErr
	}

	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	var allEvents []cacheevent.Event
	for index, result := range results {
		if !completed[index] {
			// Only Applications after a failed one are skipped, and the loop
			// returns at that failure first; an earlier gap would silently
			// drop an Application from discovery, so refuse it.
			return out, allDiags, allEvents, fmt.Errorf("render frontier Application %s: never rendered", applications[index].Path)
		}
		allDiags = append(allDiags, result.diags...)
		allEvents = append(allEvents, result.events...)
		if result.err != nil {
			return out, allDiags, allEvents, result.err
		}
		var mergeDiags []diagnostic.Diagnostic
		out, mergeDiags = mergeDiscoveryResultsWithDiagnostics(out, result.result)
		allDiags = append(allDiags, mergeDiags...)
	}
	return out, allDiags, allEvents, nil
}

// renderParallelDiscoveryApplication renders one frontier Application with
// its own cache event recorder, as the final render does, so the frontier
// can report events in frontier order.
func renderParallelDiscoveryApplication(ctx context.Context, root string, request BuildRequest, provider localProvider, renderSettings discoveryRenderSettings, renderCache *applicationRenderCache, inputs map[string]applicationInputPaths, discovered discovery.Result, appFile discovery.ApplicationFile) indexedDiscoveryRenderResult {
	recorder := cacheevent.NewRecorder(request.RecordCacheEvents)
	provider.cacheEvents = recorder
	result, diags, err := renderDiscoveryApplication(ctx, root, request, provider, renderSettings, renderCache, inputs, discovered, appFile)
	if ctxErr := ctx.Err(); ctxErr != nil {
		// A cancelled render reads as one that rendered nothing; report the
		// cancellation instead of a frontier missing this Application.
		return indexedDiscoveryRenderResult{events: recorder.Events(), err: ctxErr}
	}
	return indexedDiscoveryRenderResult{result: result, diags: diags, events: recorder.Events(), err: err}
}

func renderDiscoveryApplication(ctx context.Context, root string, request BuildRequest, provider localProvider, renderSettings discoveryRenderSettings, renderCache *applicationRenderCache, inputs map[string]applicationInputPaths, discovered discovery.Result, appFile discovery.ApplicationFile) (discovery.Result, []diagnostic.Diagnostic, error) {
	// Argo CD never reconciles an Application outside the enabled
	// namespaces, so nothing it renders exists (pruneNotEnabledNamespaces).
	if !namespaceGateFromSettings(renderSettings.settings).applicationAllowed(appFile.Application.Namespace) {
		return discovery.Result{}, nil, nil
	}
	if !applicationMayRenderDiscoveryObjects(root, request, discovered, appFile.Application) {
		return discovery.Result{}, nil, nil
	}
	return renderAndScanDiscoveryApplication(ctx, request, provider, renderSettings, renderCache, inputs, appFile, false)
}

// renderAndScanDiscoveryApplication renders one Application and scans its
// output for discovery objects. With settingsOnly, it keeps only the
// argocd-cm, argocd-cmd-params-cm, and argocd-cmp-cm ConfigMaps
// (settleSettingsInstalls). Each rendered settings ConfigMap records the
// repository paths the render read (markSettingsRenderInputs). When the
// render fails and the Application's Helm values set Argo CD settings,
// drydock reads the values files instead, and it reports that.
func renderAndScanDiscoveryApplication(ctx context.Context, request BuildRequest, provider localProvider, renderSettings discoveryRenderSettings, renderCache *applicationRenderCache, inputs map[string]applicationInputPaths, appFile discovery.ApplicationFile, settingsOnly bool) (discovery.Result, []diagnostic.Diagnostic, error) {
	application := appFile.Application
	entry := inputs[applicationDiscoveryKey(application)]
	parentInputs := entry.Paths
	rendered, err := renderApplicationCached(renderContext{
		context:                    ctx,
		provider:                   provider,
		cache:                      renderCache,
		settingsSignature:          renderSettings.signature,
		trackingOptions:            renderSettings.tracking,
		request:                    request,
		applicationInputPaths:      parentInputs,
		applicationInputsKnown:     inputs != nil,
		applicationInputsDuplicate: entry.Duplicate,
	}, application)
	inCluster := settingsInstallInCluster(application, renderSettings.settings)
	if err != nil {
		if ctx.Err() != nil || !inCluster {
			return skippedRenderedDiscovery()
		}
		if install := applicationSettingsInstall(ctx, request, provider, application); install.installsArgoCD() {
			// The values files stay settings candidates, so drydock falls
			// back to reading them; say so instead of falling back silently.
			return discovery.Result{}, []diagnostic.Diagnostic{settingsInstallRenderFailedDiagnostic(appFile, install, err)}, nil
		}
		return skippedRenderedDiscovery()
	}
	manifests := rendered.Manifests
	if settingsOnly {
		manifests = settingsConfigMapManifests(manifests)
	}
	result, diags, err := scanRenderedApplicationObjects(application, parentInputs, manifests)
	if err != nil {
		return result, diags, err
	}
	argoNamespace := provider.controllerNamespace
	if argoNamespace == "" {
		argoNamespace = defaultArgoCDControllerNamespace
	}
	destinationNamespace := application.Spec.Destination.Namespace
	result = placeRepoServerCandidates(result, destinationNamespace, inCluster)
	if !hasSettingsConfigMapCandidate(result) || !inCluster {
		return result, diags, nil
	}
	install := applicationSettingsInstall(ctx, request, provider, application)
	if !settingsOnly && !install.installsArgoCD() {
		// Not an Argo CD install: its settings ConfigMaps stay candidates
		// as for any rendered object.
		return result, diags, nil
	}
	// Only the settings ConfigMaps in the Argo CD namespace are settings of
	// this Argo CD, also when the frontier renders the install for discovery.
	result = settingsConfigMapsInNamespace(result, destinationNamespace, argoNamespace)
	if install.installsArgoCD() {
		markSettingsRenderInputs(&result, install, destinationNamespace, argoNamespace)
	}
	return result, diags, nil
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
		markRenderedOrigin(&next, applicationKey(parent), "")
		for i := range next.SettingsCandidates {
			next.SettingsCandidates[i].Origin = applicationKey(parent)
		}
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
		// OCI artifact content comes from a registry exactly like chart-only
		// sources: it never renders local discovery objects, and `path: .`
		// must not pull the local repository into the discovery frontier. A
		// repo-mapped oci:// URL is the documented escape hatch, though: the
		// mapped local tree stands in for the artifact content, so it keeps
		// frontier eligibility through localDiscoverySourceRoot below (which
		// returns the mapped root for an empty path). The oci:// rule is
		// evaluated first and whatever chart says, because Argo CD ignores
		// chart on oci:// sources; the chart-only skip below is for the
		// schemeless-OCI and HTTP(S) Helm chart flow.
		if ociartifact.IsOCIURL(sourcePlan.Source.RepoURL) {
			if _, mapped := mappedRepositoryPath(request, sourcePlan.Source.RepoURL); !mapped {
				continue
			}
		} else if sourcePlan.Source.Chart != "" && sourcePlan.Source.Path == "" {
			continue
		}
		sourceRoot, ok := localDiscoverySourceRoot(root, request, sourcePlan.Source)
		if !ok {
			continue
		}
		matches, err := pathMayContainDiscoveryObjectsCached(request.discoveryPathMemo, sourceRoot)
		if err != nil || matches {
			if localSourceAlreadyDiscovered(root, sourceRoot, discovered) && !sourceRootHasLocalChart(sourceRoot) {
				continue
			}
			return true
		}
	}
	return false
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

func applicationInputsByKey(discovered discovery.Result) map[string]applicationInputPaths {
	out := make(map[string]applicationInputPaths, len(discovered.Applications))
	for _, appFile := range discovered.Applications {
		key := applicationDiscoveryKey(appFile.Application)
		if existing, ok := out[key]; ok {
			// Two discovered Applications share namespace/name; neither input
			// set is authoritative for the render, so the duplicate marker
			// makes the persistent key collection reject the app
			// (duplicate-application reason) instead of digesting the wrong
			// file.
			existing.Paths = nil
			existing.Duplicate = true
			out[key] = existing
			continue
		}
		inputs := discoveredApplicationInputPaths(appFile)
		inputs = applicationSelectionPaths(ApplicationSelectionInput{Application: appFile.Application, Paths: inputs})
		out[key] = applicationInputPaths{Paths: uniqueStrings(inputs)}
	}
	return out
}
