package app

import (
	"context"
	"os"
	"strings"

	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func newLocalProvider(ctx context.Context, orchestrator Orchestrator, root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder, snapshotPrefix string) (localProvider, func(), error) {
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
		helmChartLoadCache:           render.NewHelmChartLoadCache(),
		pluginTimeout:                request.PluginTimeout,
		pluginCacheDir:               request.PluginCacheDir,
		pluginPolicy:                 request.pluginPolicy,
		pluginPolicyFingerprint:      request.pluginPolicyFingerprint,
		pluginPolicyExecTrusted:      request.pluginPolicyExecTrusted,
		pluginExecRunner:             pluginExecRunner,
		pluginContainerRunner:        pluginContainerRunner,
		kustomizeBuildOptions:        settingsBuildOptions(settings),
		configManagementPlugins:      settings.ConfigManagementPlugins,
		cacheEvents:                  recorder,
	}
	provider.rootIdentity, provider.rootInputMode, provider.rootDirtyPaths = rootIdentityForRequest(ctx, root, request)
	provider.renderObserver = orchestrator.renderObserver
	if request.snapshotSession != nil {
		provider.acquisition = acquisition.Session{
			Locks:                     processCacheTargetLocks,
			SnapshotRoot:              request.snapshotSession.Root,
			SnapshotCacheReads:        true,
			SnapshotCache:             request.snapshotSession.Cache,
			PreserveGitDirInSnapshots: request.EnablePlugins,
		}
		return provider, func() {}, nil
	}
	snapshotRoot, err := os.MkdirTemp("", snapshotPrefix)
	if err != nil {
		return provider, func() {}, err
	}
	provider.acquisition = acquisition.Session{
		Locks:                     processCacheTargetLocks,
		SnapshotRoot:              snapshotRoot,
		SnapshotCacheReads:        true,
		SnapshotCache:             acquisition.NewSnapshotCache(),
		PreserveGitDirInSnapshots: request.EnablePlugins,
	}
	return provider, func() { _ = os.RemoveAll(snapshotRoot) }, nil
}

// rootIdentityForRequest computes the provider root's SourceIdentity, input
// mode, and (for dirty roots) the complete dirty-path enumeration. A
// gitref-snapshot root keys on its resolved snapshot revision; a clean
// worktree root uses HEAD before local input digesting; a dirty worktree
// root uses filesystem input digests for touched path sets and committed
// digests for untouched ones; unknown roots (non-git directories) remain
// ineligible.
func rootIdentityForRequest(ctx context.Context, root string, request BuildRequest) (SourceIdentity, rootInputMode, []string) {
	if revision := strings.TrimSpace(request.rootRevision); revision != "" {
		return SourceIdentity{Kind: sourceIdentityKindRoot, Revision: revision}, rootInputModeSnapshot, nil
	}
	handle := request.persistentRenderCache
	if !handle.active() {
		return SourceIdentity{Kind: sourceIdentityKindRoot}, rootInputModeUnknown, nil
	}
	changeSet := handle.worktreeChangeSet(ctx, root)
	switch changeSet.State {
	case gitref.WorktreeStateClean:
		return SourceIdentity{Kind: sourceIdentityKindRoot, Revision: changeSet.Revision}, rootInputModeClean, nil
	case gitref.WorktreeStateDirty:
		return SourceIdentity{Kind: sourceIdentityKindRoot, Revision: changeSet.Revision}, rootInputModeDirty, changeSet.DirtyPaths
	case gitref.WorktreeStateUnknown:
		return SourceIdentity{Kind: sourceIdentityKindRoot}, rootInputModeUnknown, nil
	default:
		return SourceIdentity{Kind: sourceIdentityKindRoot}, rootInputModeUnknown, nil
	}
}
