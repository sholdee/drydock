package app

import (
	"context"
	"time"

	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

type localProvider struct {
	repoRoot                     string
	sourceResolver               *sourcepkg.Resolver
	chartAcquirer                chart.Acquirer
	gitAcquirer                  sourcepkg.GitAcquirer
	remoteResourceAcquirer       remote.Acquirer
	pluginRenderer               render.PluginRenderer
	offline                      bool
	refreshCharts                bool
	chartCacheDir                string
	chartForbiddenRoots          []string
	chartCredentials             chart.ChartCredentials
	ociChartRepositories         map[string]bool
	gitCacheDir                  string
	refreshGit                   bool
	gitCredentials               sourcepkg.GitCredentials
	refreshRemoteResources       bool
	remoteResourceCacheDir       string
	remoteResourceForbiddenRoots []string
	remoteResourceCredentials    remote.Credentials
	remoteResourceGitCredentials remote.GitCredentials
	helmValueFileSchemes         []string
	helmValueFileSchemesSet      bool
	pluginTimeout                time.Duration
	pluginPolicy                 pluginpolicy.Policy
	pluginPolicyFingerprint      string
	pluginPolicyExecTrusted      bool
	kustomizeBuildOptions        []string
	configManagementPlugins      map[string]config.ConfigManagementPlugin
	cacheEvents                  *cacheevent.Recorder
	pluginExecutions             *[]PluginExecution
	acquisition                  acquisition.Session
}

var processCacheTargetLocks = acquisition.NewTargetLocks()

func (p localProvider) RenderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	sourceRoot := source.RepoRoot
	var err error
	if sourceRoot == "" {
		sourceRoot, err = p.resolveSourceRoot(ctx, source)
		if err != nil {
			return nil, nil, err
		}
	}
	source.RepoRoot = sourceRoot
	opts.ChartAcquirer = p.acquisition.ChartAcquirer(p.chartAcquirer)
	opts.ChartCacheDir = p.chartCacheDir
	opts.OfflineCharts = p.offline
	opts.RefreshCharts = p.refreshCharts
	opts.ChartForbiddenRoots = append([]string(nil), p.chartForbiddenRoots...)
	opts.ChartForbiddenRoots = appendUniqueString(opts.ChartForbiddenRoots, sourceRoot)
	opts.ChartCredentials = p.chartCredentials
	opts.OCIChartRepositories = p.ociChartRepositories
	opts.RemoteResourceAcquirer = p.acquisition.RemoteAcquirer(p.remoteResourceAcquirer)
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
	anchoredRefRoots, err := anchorLocalRefRoots(sourceRoot, opts.RefRoots)
	if err != nil {
		return nil, nil, err
	}
	refRoots, err := p.resolveRefRoots(ctx, opts.RefSources)
	if err != nil {
		return nil, nil, err
	}
	opts.RefRoots = mergeRefRoots(anchoredRefRoots, refRoots)
	if opts.Plugin != nil {
		return p.renderPluginSource(ctx, source, opts)
	}
	opts.BuildOptions = append(opts.BuildOptions, p.kustomizeBuildOptions...)
	if source.Path != "" {
		renderer, err := selectLocalRenderer(source)
		if err != nil {
			return nil, nil, err
		}
		guardrailDiags := p.cmpAutoDiscoveryDeferredDiagnostics(source, opts)
		manifests, diags, err := renderer.Render(ctx, source, opts)
		diags = append(guardrailDiags, diags...)
		return manifests, diags, err
	}
	if source.Chart != "" {
		return p.renderChartOnlySource(ctx, source, opts)
	}
	return nil, nil, nil
}
