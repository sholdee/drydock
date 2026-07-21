// Package drydock exposes the drydock orchestrator as an embeddable Go API for
// rendering Argo CD Applications and calculating local diffs.
package drydock

import (
	"context"
	"time"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/rendercache"
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

// RenderCacheOptions controls the persistent render-output cache, which
// reuses successful Application render outputs across processes.
type RenderCacheOptions struct {
	// Enabled toggles the persistent render cache. Nil defaults to enabled.
	// Persistence additionally requires a provable engine fingerprint; builds
	// without VCS stamping disable themselves automatically.
	Enabled *bool
	// Dir overrides the cache directory. Empty uses
	// <user cache dir>/drydock/render.
	Dir string
	// MaxSizeBytes caps the on-disk cache size before LRU eviction. Zero uses
	// the 512 MiB default.
	MaxSizeBytes int64
	// Refresh ignores existing entries and overwrites them after rendering.
	Refresh bool
}

// Config controls render, list, and diff operations.
//
// Path is the working tree to inspect for render/list operations and the right
// side for diff operations. PathOrig is the left side for diff operations. Use
// keyed struct literals; new fields may be added as drydock gains parity.
type Config struct {
	// Path is the working tree to inspect for render/list operations and the
	// right side for diff operations.
	Path string
	// PathOrig is the left-side working tree for diff operations when comparing
	// two checked-out trees.
	PathOrig string
	// Repo is a local Git repository used with Ref or RefOrig when drydock should
	// materialize comparison worktrees from Git refs.
	Repo string
	// Ref is the right-side Git ref for diff operations.
	Ref string
	// RefOrig is the left-side Git ref for diff operations.
	RefOrig string
	// DiscoveryMode selects the Application discovery strategy. Empty uses the
	// CLI/default discovery behavior.
	DiscoveryMode string
	// MaxDiscoveryDepth limits recursive rendered Application discovery. Nil uses
	// the default; a pointer to zero disables recursive depth.
	MaxDiscoveryDepth *int
	// DiscoverKustomizePaths adds explicit Kustomize entrypoints to rendered
	// bootstrap discovery.
	DiscoverKustomizePaths []string
	// DiscoverIgnores removes repository-relative glob matches from repository
	// discovery before decoding, including explicit path scans.
	DiscoverIgnores []string
	// Strict promotes supported diagnostics that are warnings by default to
	// operation errors.
	Strict bool
	// ProjectDiagnosticsMode controls AppProject-adjacent diagnostics.
	ProjectDiagnosticsMode ProjectDiagnosticsMode
	// Offline disables source-network acquisition and requires local inputs,
	// explicit repo maps, or cache hits.
	Offline bool
	// RefreshCharts refreshes chart cache entries instead of reusing cached
	// charts when network acquisition is allowed.
	RefreshCharts bool
	// ChartCacheDir overrides the Helm chart cache root.
	ChartCacheDir string
	// ChartCredentials supplies credentials for chart repository acquisition.
	ChartCredentials ChartCredentials
	// RepoMaps map declared source repository URLs to local checkout paths.
	RepoMaps []RepoMap
	// Deprecated: Git, chart, and remote resource acquisition are enabled by
	// default. Set Offline to true to disable network acquisition. Offline is
	// authoritative when both fields are set.
	AllowNetwork bool
	// GitCacheDir overrides the Git source cache root.
	GitCacheDir string
	// RefreshGit refreshes Git cache entries instead of reusing cached checkouts
	// when network acquisition is allowed.
	RefreshGit bool
	// GitCredentials supplies credentials for Git source acquisition.
	GitCredentials GitCredentials
	// RefreshRemoteResources refreshes remote Kustomize resource cache entries
	// instead of reusing cached resources when network acquisition is allowed.
	RefreshRemoteResources bool
	// RemoteResourceCacheDir overrides the remote Kustomize resource cache root.
	RemoteResourceCacheDir string
	// RemoteResourceForbiddenRoots rejects remote-resource cache locations under
	// protected roots.
	RemoteResourceForbiddenRoots []string
	// RemoteResourceCredentials supplies credentials for remote Kustomize HTTP
	// resources.
	RemoteResourceCredentials RemoteResourceCredentials
	// OCICacheDir overrides the OCI artifact source cache root.
	OCICacheDir string
	// EnableAVPCompat forces argocd-vault-plugin placeholder redaction for
	// native-rendered sources. Explicit argocd-vault-plugin sources use native
	// compatibility by default.
	EnableAVPCompat bool
	// EnableKSOPSCompat renders KSOPS kustomize generators as deterministic
	// placeholder manifests without decryption.
	EnableKSOPSCompat bool
	// EnablePlugins allows trusted PluginPolicy exec or container engines to run
	// when policy provenance matches.
	EnablePlugins bool
	// PluginPolicyPath points to a drydock PluginPolicy file.
	PluginPolicyPath string
	// PluginPolicyPathExplicit records whether PluginPolicyPath was explicitly
	// configured by the caller.
	PluginPolicyPathExplicit bool
	// PluginPolicyRef is the Git ref used to load repository-local plugin policy.
	PluginPolicyRef string
	// PluginPolicyRepo is the repository root used to load repository-local
	// plugin policy.
	PluginPolicyRepo string
	// DisablePluginPolicy disables repository-local plugin policy loading.
	DisablePluginPolicy bool
	// PluginRenderer injects an in-process plugin renderer for embedded callers.
	PluginRenderer PluginRenderer
	// PluginTimeout limits each plugin render request. Zero uses the default.
	PluginTimeout time.Duration
	// Parallelism limits concurrent Application rendering. Zero uses the default.
	Parallelism int
	// SkipKinds omits rendered resources with matching kind names.
	SkipKinds []string
	// SkipCRDs omits rendered CustomResourceDefinition resources.
	SkipCRDs bool
	// SkipSecrets omits rendered Secret resources.
	SkipSecrets bool
	// ApplicationSetProviderFixtures loads provider-backed ApplicationSet fixture
	// data from files.
	ApplicationSetProviderFixtures []string
	// ApplicationSetProviderData supplies provider-backed ApplicationSet fixture
	// data directly.
	ApplicationSetProviderData ApplicationSetProviderData
	// ChangedOnly controls PR-focused changed-only selection. Nil uses the
	// operation default.
	ChangedOnly *bool
	// ChangedOnlyIncludes adds changed-only include globs.
	ChangedOnlyIncludes []string
	// ChangedOnlyIgnores adds changed-only ignore globs.
	ChangedOnlyIgnores []string
	// StrictChangedOnly turns changed-only selection diagnostics into operation
	// errors.
	StrictChangedOnly bool
	// Unified controls unified diff context lines. Zero uses the default.
	Unified int
	// StripAttrs removes matching manifest attributes before diffing.
	StripAttrs []string
	// ShowIgnoredFields includes Argo CD ignored-field differences in diffs.
	ShowIgnoredFields bool
	// GitAcquirer injects Git source acquisition for deterministic embedding.
	GitAcquirer GitAcquirer
	// ChartAcquirer injects Helm chart acquisition for deterministic embedding.
	ChartAcquirer ChartAcquirer
	// RemoteResourceAcquirer injects remote Kustomize resource acquisition for
	// deterministic embedding.
	RemoteResourceAcquirer RemoteResourceAcquirer
	// RecordCacheEvents includes source acquisition cache events in results.
	RecordCacheEvents bool
	// RenderCache controls the persistent render-output cache.
	RenderCache RenderCacheOptions
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
	renderCacheEnabled := true
	if client.config.RenderCache.Enabled != nil {
		renderCacheEnabled = *client.config.RenderCache.Enabled
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
		DiscoverIgnores:                append([]string(nil), client.config.DiscoverIgnores...),
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
		OCICacheDir:                    client.config.OCICacheDir,
		EnableAVPCompat:                client.config.EnableAVPCompat,
		EnableKSOPSCompat:              client.config.EnableKSOPSCompat,
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
		RenderCacheEnabled:             renderCacheEnabled,
		RenderCacheDir:                 client.config.RenderCache.Dir,
		RenderCacheMaxBytes:            client.config.RenderCache.MaxSizeBytes,
		RefreshRenders:                 client.config.RenderCache.Refresh,
		EngineFingerprint:              rendercache.FingerprintFromBuildInfo(),
	}
}

func projectDiagnosticsModeToInternal(mode ProjectDiagnosticsMode) diagnostic.ProjectDiagnosticsMode {
	return diagnostic.ProjectDiagnosticsMode(mode)
}
