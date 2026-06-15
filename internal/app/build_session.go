package app

import (
	"context"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/luahealth"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/project"
)

type buildSession struct {
	orchestrator  Orchestrator
	request       BuildRequest
	root          string
	parallelism   int
	cacheRecorder *cacheevent.Recorder
}

func newBuildSession(orchestrator Orchestrator, request BuildRequest) (*buildSession, error) {
	root := request.Path
	if root == "" {
		root = "."
	}
	parallelism, err := normalizeParallelism(request.Parallelism)
	if err != nil {
		return nil, err
	}
	if _, err := normalizeDiscoveryMode(request.DiscoveryMode); err != nil {
		return nil, err
	}
	if _, err := normalizeMaxDiscoveryDepth(request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet); err != nil {
		return nil, err
	}
	if err := request.ProjectDiagnosticsMode.Validate(); err != nil {
		return nil, err
	}
	return &buildSession{
		orchestrator:  orchestrator,
		request:       request,
		root:          root,
		parallelism:   parallelism,
		cacheRecorder: cacheevent.NewRecorder(request.RecordCacheEvents),
	}, nil
}

func (session *buildSession) Build(ctx context.Context) (BuildResult, error) {
	loadedRequest, policyDiags, cleanup, err := ensureBuildPluginPolicy(ctx, session.request, session.root)
	defer cleanup()
	session.request = loadedRequest
	policyDiags = session.request.filterProjectDiagnostics(policyDiags)
	if err != nil {
		return BuildResult{Diagnostics: policyDiags, CacheEvents: session.cacheRecorder.Events()}, err
	}
	result, err := session.orchestrator.prepareBuildResult(ctx, session.request, session.root)
	result.Diagnostics = append(policyDiags, result.Diagnostics...)
	if err != nil {
		result.CacheEvents = session.cacheRecorder.Events()
		return result, err
	}
	projectDiags := project.ValidateApplications(result.Applications, result.Projects, result.Settings)
	projectDiags = session.request.normalizeDiagnostics(projectDiags, false)
	result.Diagnostics = append(result.Diagnostics, projectDiags...)
	if err := diagnosticFailure(projectDiags, session.request.Strict); err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		result.CacheEvents = session.cacheRecorder.Events()
		return result, err
	}
	var healthEvaluator *luahealth.Evaluator
	if session.request.ValidateLuaHealth {
		evaluator := luahealth.New(result.Settings)
		healthEvaluator = &evaluator
	}
	if err := validateBuildNetworkOptions(session.request); err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		result.CacheEvents = session.cacheRecorder.Events()
		return result, err
	}
	if len(result.Applications) == 0 {
		result.CacheEvents = session.cacheRecorder.Events()
		return result, nil
	}

	resourceFilter := session.request.resourceFilter()
	settingsFilter := manifest.SettingsResourceFilter{
		Exclusions: result.Settings.ResourceExclusions,
		Inclusions: result.Settings.ResourceInclusions,
	}

	provider, cleanup, err := newLocalProvider(ctx, session.orchestrator, session.root, result.Settings, session.request, session.cacheRecorder, "drydock-cache-snapshots-*")
	if err != nil {
		return result, err
	}
	defer cleanup()

	rendered, renderErr := renderApplications(ctx, renderApplicationsRequest{
		applications:      result.Applications,
		applicationInputs: applicationInputPathsByKey(result.ApplicationInputs),
		provider:          provider,
		renderCache:       result.renderCache,
		settingsSignature: result.renderSettingsSignature,
		trackingOptions:   trackingOptionsFromSettings(result.Settings),
		request:           session.request,
		projects:          result.Projects,
		settings:          result.Settings,
		strict:            session.request.Strict,
		statusOnly:        session.request.StatusOnly,
		settingsFilter:    settingsFilter,
		resourceFilter:    resourceFilter,
		healthEvaluator:   healthEvaluator,
		recordEvents:      session.request.RecordCacheEvents,
		parallelism:       session.parallelism,
		statusCallback:    session.request.StatusCallback,
	})
	result.Manifests = append(result.Manifests, rendered.manifests...)
	result.ApplicationManifests = append(result.ApplicationManifests, rendered.applicationManifests...)
	result.Diagnostics = append(result.Diagnostics, rendered.diagnostics...)
	result.Statuses = append(result.Statuses, rendered.statuses...)
	result.CacheEvents = append(result.CacheEvents, rendered.cacheEvents...)
	result.PluginExecutions = append(result.PluginExecutions, rendered.pluginExecutions...)
	if !session.request.Disabled {
		registry := manifest.BuildCRDScopeRegistry(manifestObjectPtrs(result.Manifests))
		result.Diagnostics = append(result.Diagnostics, normalizeCRDScope(result.Manifests, registry)...)
	}
	result.Diagnostics = session.request.filterProjectDiagnostics(dedupeDiagnostics(result.Diagnostics))
	if renderErr != nil {
		return result, renderErr
	}
	if err := buildStatusFailure(result.Statuses); err != nil {
		return result, err
	}
	return result, nil
}
