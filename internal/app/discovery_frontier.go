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
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
	result discovery.Result
	diags  []diagnostic.Diagnostic
	err    error
}

func renderDiscoveryFrontierParallel(ctx context.Context, root string, request BuildRequest, discovered discovery.Result, inputs map[string][]string, provider localProvider, settingsSig string, renderCache *applicationRenderCache, parallelism int) (discovery.Result, []diagnostic.Diagnostic, error) {
	applications := discovered.Applications
	results, completed, parallelErr := runOrderedParallel(ctx, orderedParallelOptions[indexedDiscoveryRenderResult]{
		total:       len(applications),
		parallelism: parallelism,
		run: func(ctx context.Context, index int) indexedDiscoveryRenderResult {
			result, diags, err := renderDiscoveryApplication(ctx, root, request, provider, settingsSig, renderCache, inputs, discovered, applications[index])
			return indexedDiscoveryRenderResult{result: result, diags: diags, err: err}
		},
		shouldCancel: func(result indexedDiscoveryRenderResult) bool {
			return result.err != nil
		},
	})
	if parallelErr != nil {
		return discovery.Result{}, nil, parallelErr
	}

	var out discovery.Result
	var allDiags []diagnostic.Diagnostic
	for index, result := range results {
		if !completed[index] {
			continue
		}
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
