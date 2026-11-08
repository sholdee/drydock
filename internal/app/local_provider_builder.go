package app

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func newLocalProvider(ctx context.Context, orchestrator Orchestrator, root string, settings config.ArgoSettings, request BuildRequest, recorder *cacheevent.Recorder, snapshotPrefix string) (localProvider, func(), error) {
	acquirer := orchestrator.ChartAcquirer
	if acquirer == nil {
		// OCI Helm chart pulls honour the --oci-* TLS flags. The client goes on
		// OCIPuller ONLY: DefaultAcquirer.Client is shared with the HTTP(S) Helm
		// repository path (index.yaml, chart archives, Helm dependency
		// resolution, Kustomize helmCharts), so setting it would apply the OCI
		// CA pool and --oci-insecure-skip-verify to every https:// Helm
		// repository in the run. A caller-supplied ChartAcquirer is left alone.
		client, configured, err := request.OCICredentials.HTTPClient()
		if err != nil {
			return localProvider{}, func() {}, err
		}
		if configured {
			acquirer = chart.DefaultAcquirer{OCIPuller: chart.HelmOCIPuller{Client: client}}
		} else {
			acquirer = chart.DefaultAcquirer{}
		}
	}
	gitAcquirer := orchestrator.GitAcquirer
	if gitAcquirer == nil {
		gitAcquirer = sourcepkg.DefaultGitAcquirer{}
	}
	ociAcquirer := orchestrator.OCIArtifactAcquirer
	if ociAcquirer == nil {
		ociAcquirer = ociartifact.DefaultAcquirer{}
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
	maxDirectorySize, err := maxCombinedDirectoryManifestsSize(settings)
	if err != nil {
		return localProvider{}, func() {}, err
	}
	provider := localProvider{
		maxCombinedDirectoryManifestsSize: maxDirectorySize,
		allowOutOfBoundsSymlinks:          allowOutOfBoundsSymlinks(settings),
		sourceTypes:                       sourceTypeGatesFromSettings(settings),
		helmNamespace:                     settings.HelmNamespace(),
		symlinkBounds:                     request.symlinkBounds,
		repoRoot:                          root,
		controllerNamespace:               trackingOptionsFromSettings(settings).ControllerNamespace,
		sourceResolver:                    sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, Offline: request.Offline}),
		chartAcquirer:                     acquirer,
		gitAcquirer:                       gitAcquirer,
		remoteResourceAcquirer:            orchestrator.RemoteResourceAcquirer,
		pluginRenderer:                    orchestrator.pluginRenderer(request),
		offline:                           request.Offline,
		refreshCharts:                     request.RefreshCharts,
		chartCacheMaxAge:                  request.ChartCacheMaxAge,
		chartCacheDir:                     request.ChartCacheDir,
		chartForbiddenRoots:               forbiddenRoots,
		chartCredentials:                  request.ChartCredentials,
		ociChartRepositories:              ociChartRepositoriesFromSettings(settings),
		gitCacheDir:                       request.GitCacheDir,
		gitSubmodules:                     config.ParseEnableGitSubmodules(settings.EnableGitSubmodules.Value),
		refreshGit:                        request.RefreshGit,
		gitCredentials:                    request.GitCredentials,
		refreshRemoteResources:            request.RefreshRemoteResources,
		remoteResourceCacheDir:            request.RemoteResourceCacheDir,
		remoteResourceForbiddenRoots:      forbiddenRoots,
		remoteResourceCredentials:         request.RemoteResourceCredentials,
		remoteResourceGitCredentials:      request.RemoteResourceGitCredentials,
		ociArtifactAcquirer:               ociAcquirer,
		ociCacheDir:                       request.OCICacheDir,
		ociForbiddenRoots:                 forbiddenRoots,
		ociCredentials:                    request.OCICredentials,
		helmValueFileSchemes:              settingsHelmValueFileSchemes(settings),
		helmValueFileSchemesSet:           settings.HelmValuesFileSchemesSet,
		helmChartLoadCache:                render.NewHelmChartLoadCache(),
		pluginTimeout:                     request.PluginTimeout,
		pluginCacheDir:                    request.PluginCacheDir,
		pluginPolicy:                      request.pluginPolicy,
		pluginPolicyFingerprint:           request.pluginPolicyFingerprint,
		pluginPolicyExecTrusted:           request.pluginPolicyExecTrusted,
		pluginExecRunner:                  pluginExecRunner,
		pluginContainerRunner:             pluginContainerRunner,
		kustomizeBuildOptions:             settingsBuildOptions(settings),
		kustomizeCaller:                   settingsKustomizeCaller(settings),
		kustomizeSettings:                 settings,
		configManagementPlugins:           settings.ConfigManagementPlugins,
		cacheEvents:                       recorder,
	}
	if provider.symlinkBounds == nil {
		provider.symlinkBounds = newSymlinkBoundsMemo()
	}
	provider.userRoots = userRootSet(root, request.rootRevision, request.RepoMaps)
	provider.rootIdentity, provider.rootInputMode, provider.rootDirtyPaths = rootIdentityForRequest(ctx, root, request)
	provider.rootAppRevision = rootAppRevisionForRequest(root, request, provider.rootIdentity)
	provider.rootGitRepo = root
	if strings.TrimSpace(request.rootRevision) != "" {
		provider.rootGitRepo = request.rootRepo
	}
	provider.mappedAppRevisions = &sync.Map{}
	provider.hydratorSnapshots = &sync.Map{}
	provider.submodules = newRootSubmodules(root, request)
	provider.lfsRepositories = lfsRepositoryKeys(settings)
	provider.lfsFetched = &sync.Map{}
	provider.rootLFS = prepareRootLFS(ctx, root, request, &provider)
	if matcher := newSelfRepoMatcher(request.selfRepo); len(matcher.urlKeys) > 0 {
		provider.selfRepoURLKeys = matcher.urlKeys
		provider.selfRepoRevisions = matcher.revisions
		provider.selfRepoNearMissOnce = &sync.Map{}
	}
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

// rootAppRevisionForRequest returns the commit SHA that the tree at root
// holds: the resolved snapshot revision of a diff side that renders a Git
// ref, else the HEAD commit when root is the top of a Git repository, else
// "". A dirty worktree still reports its HEAD commit. The persistent render
// cache already resolved HEAD when it is on, so that value is reused.
func rootAppRevisionForRequest(root string, request BuildRequest, rootIdentity SourceIdentity) string {
	if revision := strings.TrimSpace(request.rootRevision); revision != "" {
		return revision
	}
	if revision := strings.TrimSpace(rootIdentity.Revision); revision != "" {
		return revision
	}
	return gitref.HeadRevision(root)
}
