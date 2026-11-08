package app

import (
	"context"
	"sync"
	"time"

	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

type localProvider struct {
	repoRoot string
	// controllerNamespace is the Argo CD controller namespace that decides an
	// Application's instance name (<namespace>_<name> outside it); empty means
	// the drydock default. It must match the tracking options the render path
	// uses so override files, ARGOCD_APP_NAME, and tracking metadata agree.
	controllerNamespace    string
	sourceResolver         *sourcepkg.Resolver
	chartAcquirer          chart.Acquirer
	gitAcquirer            sourcepkg.GitAcquirer
	remoteResourceAcquirer remote.Acquirer
	pluginRenderer         render.PluginRenderer
	offline                bool
	refreshCharts          bool
	chartCacheMaxAge       time.Duration
	chartCacheDir          string
	chartForbiddenRoots    []string
	chartCredentials       chart.ChartCredentials
	ociChartRepositories   map[string]bool
	gitCacheDir            string
	// gitSubmodules is argocd-cmd-params-cm reposerver.enable.git.submodule:
	// a fetched Git source holds the content of its submodules.
	gitSubmodules                bool
	refreshGit                   bool
	gitCredentials               sourcepkg.GitCredentials
	refreshRemoteResources       bool
	remoteResourceCacheDir       string
	remoteResourceForbiddenRoots []string
	remoteResourceCredentials    remote.Credentials
	remoteResourceGitCredentials remote.GitCredentials
	ociArtifactAcquirer          ociartifact.Acquirer
	ociCacheDir                  string
	ociForbiddenRoots            []string
	ociCredentials               ociartifact.Credentials
	helmValueFileSchemes         []string
	helmValueFileSchemesSet      bool
	helmChartLoadCache           *render.HelmChartLoadCache
	pluginTimeout                time.Duration
	pluginCacheDir               string
	pluginPolicy                 pluginpolicy.Policy
	pluginPolicyFingerprint      string
	pluginPolicyExecTrusted      bool
	pluginExecRunner             pluginexec.Runner
	pluginContainerRunner        plugincontainer.Runner
	kustomizeBuildOptions        []string
	// kustomizeCaller says whether kustomizeBuildOptions come from a found
	// argocd-cm (see render.KustomizeBuildCaller).
	kustomizeCaller render.KustomizeBuildCaller
	// kustomizeSettings gives the kustomize versions that argocd-cm
	// registers.
	kustomizeSettings config.ArgoSettings
	// maxCombinedDirectoryManifestsSize is the repo-server limit on the
	// combined size of the files of one directory source, in bytes. Zero
	// means no limit.
	maxCombinedDirectoryManifestsSize int64
	// allowOutOfBoundsSymlinks is argocd-cmd-params-cm
	// reposerver.allow.oob.symlinks. When false, a source fails while the
	// tree it renders from holds an out-of-bounds symlink.
	allowOutOfBoundsSymlinks bool
	// sourceTypes is argocd-cm helm.enable, kustomize.enable, and
	// jsonnet.enable. A source whose tool is off renders as a directory.
	sourceTypes sourceTypeGates
	// helmNamespace is the namespace that helm uses in the repo-server when
	// no --namespace is given (config.ArgoSettings.HelmNamespace).
	helmNamespace string
	// symlinkBounds keeps the out-of-bounds symlink check of each tree for
	// the run. nil checks every time.
	symlinkBounds *symlinkBoundsMemo
	// userRoots holds the trees a user named (userRootSet). Only their
	// out-of-bounds check leaves Git-ignored, untracked paths out.
	userRoots               map[string]bool
	configManagementPlugins map[string]config.ConfigManagementPlugin
	cacheEvents             *cacheevent.Recorder
	acquisitions            *cacheevent.AcquisitionCollector
	// inputVerifier accumulates the digest path sets behind one application's
	// persistent key so store() can re-check them after the render. nil for
	// renders without the persistent tier.
	inputVerifier *renderInputVerifier
	rootIdentity  SourceIdentity
	rootInputMode rootInputMode
	// rootAppRevision is the commit SHA of the tree at repoRoot: the
	// snapshot revision of a diff side that renders a Git ref, else the
	// HEAD commit when repoRoot is the top of a Git repository, else "".
	// It is the ARGOCD_APP_REVISION of every source that renders from
	// repoRoot (see sourceAppRevision).
	rootAppRevision string
	// rootGitRepo is the Git repository that holds rootAppRevision: the
	// repository a diff side snapshot came from, else repoRoot. Its refs
	// give the ls-remote form of the revision for multi-source
	// Applications (sourceAppRevision).
	rootGitRepo string
	// mappedAppRevisions memoizes the HEAD commit of each --repo-map target
	// directory for the run, keyed by absolute path. nil computes every time.
	mappedAppRevisions *sync.Map
	// hydratorSnapshots memoizes the snapshot of a branch of a --repo-map
	// Git repository for a source hydrator sync source, keyed by the
	// absolute path and the branch (hydratorSnapshot).
	hydratorSnapshots *sync.Map
	// submodules describes the submodules of the tree at repoRoot, for the
	// warnings of submoduleRenderDiagnostics.
	submodules *rootSubmodules
	// rootDirtyPaths is the sorted complete dirty-path enumeration for the
	// repo root when rootInputMode is dirty; empty otherwise. Per-path-set
	// keying uses it to keep committed keys for untouched applications. An
	// empty list in dirty mode disables the committed shortcut (fail-safe
	// for hand-built providers). Aliases the run-scoped handle memo — treat
	// as read-only; never sort, append to, or mutate it in place.
	rootDirtyPaths []string
	// selfRepoURLKeys holds the canonical remote URLs of the local repository
	// under analysis — populated by diff entry points and, on build/list
	// surfaces, by ensureBuildSelfRepoRefs; nil for non-git roots or roots
	// without remotes. selfRepoRevisions holds the symbolic revision names
	// that self-map beyond ""/HEAD (diffed ref names plus symref-derived
	// default-branch names). selfRepoNearMissOnce dedupes fork near-miss
	// warnings; nil when keys are empty. All three are write-once in
	// newLocalProvider (single-threaded, before any render goroutine starts)
	// and read-only thereafter — no locking needed.
	selfRepoURLKeys      map[string]struct{}
	selfRepoRevisions    map[string]struct{}
	selfRepoNearMissOnce *sync.Map
	renderObserver       func(render.ResolvedSource)
	pluginExecutions     *[]PluginExecution
	acquisition          acquisition.Session
}

var processCacheTargetLocks = acquisition.NewTargetLocks()

func (p localProvider) RenderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	// Every chart acquisition of this source, at any depth (chart sources,
	// Helm dependencies, Kustomize helmCharts), records itself here; the
	// stale-chart warnings are read back from those records. A provider
	// without a collector gets one for this call only.
	if p.acquisitions == nil {
		p.acquisitions = cacheevent.NewAcquisitionCollector()
	}
	recorded := p.acquisitions.Len()
	manifests, diags, err := p.renderSource(ctx, source, opts)
	diags = append(diags, staleChartDiagnostics(p.acquisitions.Records()[recorded:])...)
	return manifests, diags, err
}

func (p localProvider) renderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	// Computed before any acquisition so the --repo-map remediation hint still
	// reaches the user when the fork-shaped URL's own acquisition fails
	// (offline cache miss, unreachable fork) — the moment it is most useful.
	nearMissDiags := p.selfRepoNearMissDiagnostics(source, opts.RefSources)
	// Same timing for the oci-chart-ignored advisory: it names the artifact
	// drydock is about to pull, so it must accompany a failing pull. Argo CD
	// dispatches every oci:// repoURL as an OCI artifact source before any
	// Helm handling and renders apppathutil.Path(ociPath, source.Path) — the
	// extraction root when path is empty — without reading chart
	// (repository.go:358-364,424-427 at v3.5.4). Normalizing here puts every
	// later source.Chart reader (the chart dispatch below, renderPluginSource
	// and the plugin guards) behind that rule.
	if diag, ok := ociChartIgnoredDiagnostic(source); ok {
		nearMissDiags = append(nearMissDiags, diag)
	}
	source = normalizeOCISource(source)
	if diag, ok := dotGitSourceDiagnostic(source); ok {
		return nil, append(nearMissDiags, diag), nil
	}
	sourceRoot := source.RepoRoot
	var err error
	if sourceRoot == "" {
		sourceRoot, err = p.resolveSourceRoot(ctx, source)
		if err != nil {
			return nil, nearMissDiags, err
		}
	}
	source.RepoRoot = sourceRoot
	nearMissDiags = append(nearMissDiags, p.submoduleRenderDiagnostics(ctx, source)...)
	opts.ChartAcquirer = p.acquisition.ChartAcquirer(p.chartAcquirer)
	opts.ChartCacheDir = p.chartCacheDir
	opts.OfflineCharts = p.offline
	opts.RefreshCharts = p.refreshCharts
	opts.ChartCacheMaxAge = p.chartCacheMaxAge
	opts.ChartForbiddenRoots = append([]string(nil), p.chartForbiddenRoots...)
	opts.ChartForbiddenRoots = appendUniqueString(opts.ChartForbiddenRoots, sourceRoot)
	opts.ChartCredentials = p.chartCredentials
	opts.HelmChartLoadCache = p.helmChartLoadCache
	opts.OCIChartRepositories = p.ociChartRepositories
	opts.RemoteResourceAcquirer = p.selfMapRemote(p.acquisition.RemoteAcquirer(p.remoteResourceAcquirer))
	opts.RemoteResourceCacheDir = p.remoteResourceCacheDir
	opts.OfflineRemoteResources = p.offline
	opts.RefreshRemoteResources = p.refreshRemoteResources
	opts.RemoteResourceForbiddenRoots = append([]string(nil), p.remoteResourceForbiddenRoots...)
	opts.RemoteResourceForbiddenRoots = appendUniqueString(opts.RemoteResourceForbiddenRoots, sourceRoot)
	opts.RemoteResourceCredentials = p.remoteResourceCredentials
	opts.RemoteResourceGitCredentials = p.remoteResourceGitCredentials
	if !opts.HelmValueFileSchemesSet {
		opts.HelmValueFileSchemes = append([]string(nil), p.helmValueFileSchemes...)
		opts.HelmValueFileSchemesSet = p.helmValueFileSchemesSet
	}
	opts.CacheEventRecorder = p.cacheEvents
	opts.DirectoryMaxCombinedManifestsSize = p.maxCombinedDirectoryManifestsSize
	opts.DirectoryJsonnetDisabled = p.sourceTypes.jsonnetDisabled
	opts.HelmDefaultNamespace = p.helmNamespace
	opts.AcquisitionCollector = p.acquisitions
	anchoredRefRoots, err := anchorLocalRefRoots(sourceRoot, opts.RefRoots)
	if err != nil {
		return nil, nearMissDiags, err
	}
	refRoots, err := p.resolveRefRoots(ctx, opts.RefSources)
	if err != nil {
		return nil, nearMissDiags, err
	}
	opts.RefRoots = mergeRefRoots(anchoredRefRoots, refRoots)
	if err := p.checkSourceTreesOutOfBoundsSymlinks(ctx, source); err != nil {
		return nil, nearMissDiags, err
	}
	if opts.Plugin != nil {
		manifests, diags, err := p.renderPluginSource(ctx, source, opts)
		return manifests, append(nearMissDiags, diags...), err
	}
	opts.BuildOptions = append(opts.BuildOptions, p.kustomizeBuildOptions...)
	opts.KustomizeCaller = p.kustomizeCaller
	if err := kustomizeVersionError(p.kustomizeSettings, opts.Kustomize); err != nil {
		opts.KustomizeSpecError = err
	}
	if source.Path != "" {
		renderer, err := selectLocalRenderer(source, p.sourceTypes)
		if err != nil {
			return nil, nearMissDiags, err
		}
		guardrailDiags := p.cmpAutoDiscoveryDeferredDiagnostics(source, opts)
		if p.renderObserver != nil {
			p.renderObserver(source)
		}
		manifests, diags, err := renderer.Render(ctx, source, opts)
		diags = append(guardrailDiags, diags...)
		return manifests, append(nearMissDiags, diags...), err
	}
	if source.Chart != "" {
		manifests, diags, err := p.renderChartOnlySource(ctx, source, opts, &p.sourceTypes)
		return manifests, append(nearMissDiags, diags...), err
	}
	return nil, nearMissDiags, nil
}
