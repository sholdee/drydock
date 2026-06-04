package app

import (
	"os"

	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginexec"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func newLocalProvider(orchestrator Orchestrator, root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder, snapshotPrefix string) (localProvider, func(), error) {
	acquirer := orchestrator.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	gitAcquirer := orchestrator.GitAcquirer
	if gitAcquirer == nil {
		gitAcquirer = sourcepkg.DefaultGitAcquirer{}
	}
	pluginExecRunner := orchestrator.PluginExecRunner
	if pluginExecRunner == nil {
		pluginExecRunner = pluginexec.DefaultRunner{}
	}
	pluginContainerRunner := orchestrator.PluginContainerRunner
	if pluginContainerRunner == nil {
		pluginContainerRunner = plugincontainer.DefaultRunner{}
	}
	forbiddenRoots := requestForbiddenRoots(root, request.AcquisitionOptions)
	provider := localProvider{
		repoRoot:                     root,
		sourceResolver:               sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, Offline: request.Offline}),
		chartAcquirer:                acquirer,
		gitAcquirer:                  gitAcquirer,
		remoteResourceAcquirer:       orchestrator.RemoteResourceAcquirer,
		pluginRenderer:               orchestrator.pluginRenderer(request),
		offline:                      request.Offline,
		refreshCharts:                request.RefreshCharts,
		chartCacheDir:                request.ChartCacheDir,
		chartForbiddenRoots:          forbiddenRoots,
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
		helmValueFileSchemes:         settingsHelmValueFileSchemes(settings),
		helmValueFileSchemesSet:      settings.HelmValuesFileSchemesSet,
		pluginTimeout:                request.PluginTimeout,
		pluginPolicy:                 request.pluginPolicy,
		pluginPolicyFingerprint:      request.pluginPolicyFingerprint,
		pluginPolicyExecTrusted:      request.pluginPolicyExecTrusted,
		pluginExecRunner:             pluginExecRunner,
		pluginContainerRunner:        pluginContainerRunner,
		kustomizeBuildOptions:        settingsBuildOptions(settings),
		configManagementPlugins:      settings.ConfigManagementPlugins,
		cacheEvents:                  recorder,
	}
	snapshotRoot, err := os.MkdirTemp("", snapshotPrefix)
	if err != nil {
		return provider, func() {}, err
	}
	provider.acquisition = acquisition.Session{
		Locks:              processCacheTargetLocks,
		SnapshotRoot:       snapshotRoot,
		SnapshotCacheReads: true,
		SnapshotCache:      acquisition.NewSnapshotCache(),
	}
	return provider, func() { _ = os.RemoveAll(snapshotRoot) }, nil
}
