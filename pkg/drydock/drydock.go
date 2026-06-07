// Package drydock exposes the drydock orchestrator as an embeddable Go API for
// rendering Argo CD Applications and calculating local diffs.
package drydock

import (
	"context"
	"time"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/requestopts"
)

// ProjectDiagnosticsMode controls which AppProject-adjacent diagnostics are
// returned and allowed to affect strict render/test/diff outcomes.
type ProjectDiagnosticsMode string

const (
	ProjectDiagnosticsModeActionable ProjectDiagnosticsMode = "actionable"
	ProjectDiagnosticsModeAll        ProjectDiagnosticsMode = "all"
	ProjectDiagnosticsModeOff        ProjectDiagnosticsMode = "off"
)

// Config controls render, list, and diff operations.
//
// Path is the working tree to inspect for render/list operations and the right
// side for diff operations. PathOrig is the left side for diff operations. Use
// keyed struct literals; new fields may be added as drydock gains parity.
type Config struct {
	Path                   string
	PathOrig               string
	Repo                   string
	Ref                    string
	RefOrig                string
	DiscoveryMode          string
	MaxDiscoveryDepth      *int
	DiscoverKustomizePaths []string
	Strict                 bool
	ProjectDiagnosticsMode ProjectDiagnosticsMode
	Offline                bool
	RefreshCharts          bool
	ChartCacheDir          string
	ChartCredentials       ChartCredentials
	RepoMaps               []RepoMap
	// Deprecated: Git, chart, and remote resource acquisition are enabled by
	// default. Set Offline to true to disable network acquisition. Offline is
	// authoritative when both fields are set.
	AllowNetwork                   bool
	GitCacheDir                    string
	RefreshGit                     bool
	GitCredentials                 GitCredentials
	RefreshRemoteResources         bool
	RemoteResourceCacheDir         string
	RemoteResourceForbiddenRoots   []string
	RemoteResourceCredentials      RemoteResourceCredentials
	EnableAVPCompat                bool
	EnablePlugins                  bool
	PluginPolicyPath               string
	PluginPolicyPathExplicit       bool
	PluginPolicyRef                string
	PluginPolicyRepo               string
	DisablePluginPolicy            bool
	PluginRenderer                 PluginRenderer
	PluginTimeout                  time.Duration
	Parallelism                    int
	SkipKinds                      []string
	SkipCRDs                       bool
	SkipSecrets                    bool
	ApplicationSetProviderFixtures []string
	ApplicationSetProviderData     ApplicationSetProviderData
	ChangedOnly                    *bool
	ChangedOnlyIncludes            []string
	ChangedOnlyIgnores             []string
	StrictChangedOnly              bool
	Unified                        int
	StripAttrs                     []string
	ShowIgnoredFields              bool
	GitAcquirer                    GitAcquirer
	ChartAcquirer                  ChartAcquirer
	RemoteResourceAcquirer         RemoteResourceAcquirer
	RecordCacheEvents              bool
}

// Client runs drydock operations with a reusable Config and optional
// injected source acquirers.
type Client struct {
	config       Config
	orchestrator app.Orchestrator
}

// NewClient creates a Client from config.
//
// If GitAcquirer, ChartAcquirer, or RemoteResourceAcquirer are set, the client
// uses those implementations instead of the default local fetchers.
func NewClient(config Config) *Client {
	client := &Client{config: config}
	if config.GitAcquirer != nil {
		client.orchestrator.GitAcquirer = gitAcquirerAdapter{acquirer: config.GitAcquirer}
	}
	if config.ChartAcquirer != nil {
		client.orchestrator.ChartAcquirer = chartAcquirerAdapter{acquirer: config.ChartAcquirer}
	}
	if config.RemoteResourceAcquirer != nil {
		client.orchestrator.RemoteResourceAcquirer = remoteResourceAcquirerAdapter{acquirer: config.RemoteResourceAcquirer}
	}
	if config.PluginRenderer != nil {
		client.orchestrator.PluginRenderer = pluginRendererAdapter{renderer: config.PluginRenderer}
	}
	return client
}

// Render creates manifests for Applications found under config.Path.
//
// When an Application fails to render, the returned RenderResult may still
// contain partial manifests, diagnostics, and per-Application statuses.
func Render(ctx context.Context, config Config) (RenderResult, error) {
	return NewClient(config).Render(ctx)
}

// Render creates manifests for Applications found under the client's Path.
//
// When an Application fails to render, the returned RenderResult may still
// contain partial manifests, diagnostics, and per-Application statuses.
func (client *Client) Render(ctx context.Context) (RenderResult, error) {
	result, err := client.orchestrator.Build(ctx, client.buildRequest())
	return renderResultFromBuild(result), err
}

// ListApplications returns Applications discovered under config.Path.
func ListApplications(ctx context.Context, config Config) (ListApplicationsResult, error) {
	return NewClient(config).ListApplications(ctx)
}

// ListApplications returns Applications discovered under the client's Path.
func (client *Client) ListApplications(ctx context.Context) (ListApplicationsResult, error) {
	result, err := client.orchestrator.ListApplications(ctx, client.buildRequest())
	return ListApplicationsResult{
		Applications: applicationsFromInternal(result.Applications),
		Diagnostics:  diagnosticsFromInternal(result.Diagnostics),
		CacheEvents:  cacheEventsFromInternal(result.CacheEvents),
	}, err
}

// DiffApplications compares rendered Applications between config.PathOrig and
// config.Path.
func DiffApplications(ctx context.Context, config Config) (DiffApplicationsResult, error) {
	return NewClient(config).DiffApplications(ctx)
}

// DiffApplications compares rendered Applications between the client's PathOrig
// and Path.
func (client *Client) DiffApplications(ctx context.Context) (DiffApplicationsResult, error) {
	result, err := client.orchestrator.DiffApps(ctx, client.diffRequest())
	return DiffApplicationsResult{
		Results:     diffResultsFromInternal(result.Results),
		Diagnostics: diagnosticsFromInternal(result.Diagnostics),
		CacheEvents: cacheEventsFromInternal(result.CacheEvents),
	}, err
}

// DiffImages compares image references between config.PathOrig and config.Path.
func DiffImages(ctx context.Context, config Config) (ImageDiffResult, error) {
	return NewClient(config).DiffImages(ctx)
}

// DiffImages compares image references between the client's PathOrig and Path.
func (client *Client) DiffImages(ctx context.Context) (ImageDiffResult, error) {
	result, err := client.orchestrator.DiffImages(ctx, client.diffRequest())
	return ImageDiffResult{
		Added:       append([]string(nil), result.Added...),
		Removed:     append([]string(nil), result.Removed...),
		Unchanged:   append([]string(nil), result.Unchanged...),
		Diagnostics: diagnosticsFromInternal(result.Diagnostics),
		CacheEvents: cacheEventsFromInternal(result.CacheEvents),
	}, err
}

func (client *Client) buildRequest() app.BuildRequest {
	return client.requestOptions().Build()
}

func (client *Client) diffRequest() app.DiffRequest {
	return client.requestOptions().Diff()
}

func (client *Client) requestOptions() requestopts.Options {
	unified := client.config.Unified
	if unified == 0 {
		unified = 3
	}
	maxDiscoveryDepth := app.DefaultMaxDiscoveryDepth
	maxDiscoveryDepthSet := false
	if client.config.MaxDiscoveryDepth != nil {
		maxDiscoveryDepth = *client.config.MaxDiscoveryDepth
		maxDiscoveryDepthSet = true
	}
	return requestopts.Options{
		Path:                           client.config.Path,
		LeftPath:                       client.config.PathOrig,
		RightPath:                      client.config.Path,
		Repo:                           client.config.Repo,
		Ref:                            client.config.Ref,
		RefOrig:                        client.config.RefOrig,
		DiscoveryMode:                  client.config.DiscoveryMode,
		MaxDiscoveryDepth:              maxDiscoveryDepth,
		MaxDiscoveryDepthSet:           maxDiscoveryDepthSet,
		DiscoverKustomizePaths:         append([]string(nil), client.config.DiscoverKustomizePaths...),
		ChangedOnly:                    client.config.ChangedOnly,
		ChangedOnlyIncludes:            append([]string(nil), client.config.ChangedOnlyIncludes...),
		ChangedOnlyIgnores:             append([]string(nil), client.config.ChangedOnlyIgnores...),
		StrictChangedOnly:              client.config.StrictChangedOnly,
		Strict:                         client.config.Strict,
		ProjectDiagnosticsMode:         projectDiagnosticsModeToInternal(client.config.ProjectDiagnosticsMode),
		Unified:                        unified,
		StripAttrs:                     append([]string(nil), client.config.StripAttrs...),
		ShowIgnoredFields:              client.config.ShowIgnoredFields,
		Offline:                        client.config.Offline,
		RefreshCharts:                  client.config.RefreshCharts,
		ChartCacheDir:                  client.config.ChartCacheDir,
		ChartCredentials:               chartCredentialsToInternal(client.config.ChartCredentials),
		RepoMaps:                       repoMapsToInternal(client.config.RepoMaps),
		GitCacheDir:                    client.config.GitCacheDir,
		RefreshGit:                     client.config.RefreshGit,
		GitCredentials:                 gitCredentialsToInternal(client.config.GitCredentials),
		RefreshRemoteResources:         client.config.RefreshRemoteResources,
		RemoteResourceCacheDir:         client.config.RemoteResourceCacheDir,
		RemoteResourceForbiddenRoots:   append([]string(nil), client.config.RemoteResourceForbiddenRoots...),
		RemoteResourceCredentials:      remoteResourceCredentialsToInternal(client.config.RemoteResourceCredentials),
		RemoteResourceGitCredentials:   gitCredentialsToRemoteInternal(client.config.GitCredentials),
		EnableAVPCompat:                client.config.EnableAVPCompat,
		EnablePlugins:                  client.config.EnablePlugins,
		PluginPolicyPath:               client.config.PluginPolicyPath,
		PluginPolicyPathExplicit:       client.config.PluginPolicyPathExplicit,
		PluginPolicyRef:                client.config.PluginPolicyRef,
		PluginPolicyRepo:               client.config.PluginPolicyRepo,
		DisablePluginPolicy:            client.config.DisablePluginPolicy,
		PluginTimeout:                  client.config.PluginTimeout,
		Parallelism:                    client.config.Parallelism,
		SkipKinds:                      append([]string(nil), client.config.SkipKinds...),
		SkipCRDs:                       client.config.SkipCRDs,
		SkipSecrets:                    client.config.SkipSecrets,
		ApplicationSetProviderFixtures: append([]string(nil), client.config.ApplicationSetProviderFixtures...),
		ApplicationSetProviderData:     applicationSetProviderDataToInternal(client.config.ApplicationSetProviderData),
		RecordCacheEvents:              client.config.RecordCacheEvents,
	}
}

func projectDiagnosticsModeToInternal(mode ProjectDiagnosticsMode) diagnostic.ProjectDiagnosticsMode {
	return diagnostic.ProjectDiagnosticsMode(mode)
}
