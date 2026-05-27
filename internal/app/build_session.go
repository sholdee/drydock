package app

import (
	"context"
	"os"

	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/luahealth"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/project"
	sourcepkg "github.com/sholdee/drydock/internal/source"
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
	return &buildSession{
		orchestrator:  orchestrator,
		request:       request,
		root:          root,
		parallelism:   parallelism,
		cacheRecorder: cacheevent.NewRecorder(request.RecordCacheEvents),
	}, nil
}

func (session *buildSession) Build(ctx context.Context) (BuildResult, error) {
	result, err := session.orchestrator.prepareBuildResult(ctx, session.request, session.root)
	if err != nil {
		result.CacheEvents = session.cacheRecorder.Events()
		return result, err
	}
	projectDiags := project.ValidateApplications(result.Applications, result.Projects, result.Settings)
	projectDiags = normalizeDiagnostics(projectDiags, session.request.Strict, false)
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

	acquirer := session.orchestrator.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	gitAcquirer := session.orchestrator.GitAcquirer
	if gitAcquirer == nil {
		gitAcquirer = sourcepkg.DefaultGitAcquirer{}
	}
	forbiddenRoots := append([]string(nil), session.request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = append(forbiddenRoots, session.root)
	provider := localProvider{
		repoRoot:                     session.root,
		sourceResolver:               sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: session.request.RepoMaps, Offline: session.request.Offline}),
		chartAcquirer:                acquirer,
		gitAcquirer:                  gitAcquirer,
		remoteResourceAcquirer:       session.orchestrator.RemoteResourceAcquirer,
		pluginRenderer:               session.orchestrator.pluginRenderer(session.request),
		offline:                      session.request.Offline,
		refreshCharts:                session.request.RefreshCharts,
		chartCacheDir:                session.request.ChartCacheDir,
		chartCredentials:             session.request.ChartCredentials,
		ociChartRepositories:         ociChartRepositoriesFromSettings(result.Settings),
		gitCacheDir:                  session.request.GitCacheDir,
		refreshGit:                   session.request.RefreshGit,
		gitCredentials:               session.request.GitCredentials,
		refreshRemoteResources:       session.request.RefreshRemoteResources,
		remoteResourceCacheDir:       session.request.RemoteResourceCacheDir,
		remoteResourceForbiddenRoots: forbiddenRoots,
		remoteResourceCredentials:    session.request.RemoteResourceCredentials,
		remoteResourceGitCredentials: session.request.RemoteResourceGitCredentials,
		pluginTimeout:                session.request.PluginTimeout,
		kustomizeBuildOptions:        settingsBuildOptions(result.Settings),
		configManagementPlugins:      result.Settings.ConfigManagementPlugins,
		cacheEvents:                  session.cacheRecorder,
	}
	snapshotRoot, err := os.MkdirTemp("", "drydock-cache-snapshots-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(snapshotRoot)
	provider.acquisition = acquisition.Session{
		Locks:              processCacheTargetLocks,
		SnapshotRoot:       snapshotRoot,
		SnapshotCacheReads: shouldSnapshotCacheReads(session.request),
		SnapshotCache:      acquisition.NewSnapshotCache(),
	}

	rendered, renderErr := renderApplications(ctx, renderApplicationsRequest{
		applications:    result.Applications,
		provider:        provider,
		strict:          session.request.Strict,
		statusOnly:      session.request.StatusOnly,
		settingsFilter:  settingsFilter,
		resourceFilter:  resourceFilter,
		healthEvaluator: healthEvaluator,
		recordEvents:    session.request.RecordCacheEvents,
		parallelism:     session.parallelism,
		statusCallback:  session.request.StatusCallback,
	})
	result.Manifests = append(result.Manifests, rendered.manifests...)
	result.ApplicationManifests = append(result.ApplicationManifests, rendered.applicationManifests...)
	result.Diagnostics = append(result.Diagnostics, rendered.diagnostics...)
	result.Statuses = append(result.Statuses, rendered.statuses...)
	result.CacheEvents = append(result.CacheEvents, rendered.cacheEvents...)
	if renderErr != nil {
		return result, renderErr
	}
	if err := buildStatusFailure(result.Statuses); err != nil {
		return result, err
	}
	return result, nil
}

func shouldSnapshotCacheReads(request BuildRequest) bool {
	if !request.Offline {
		return true
	}
	return true
}
