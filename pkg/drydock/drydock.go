// Package drydock exposes the drydock orchestrator as an embeddable Go API for
// rendering Argo CD Applications and calculating local diffs.
package drydock

import (
	"context"
	"fmt"
	"strings"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
	"github.com/sholdee/drydock/internal/remote"
	renderpkg "github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Config controls render, list, and diff operations.
//
// Path is the working tree to inspect for render/list operations and the right
// side for diff operations. PathOrig is the left side for diff operations. Use
// keyed struct literals; new fields may be added as drydock gains parity.
type Config struct {
	Path                           string
	PathOrig                       string
	Strict                         bool
	Offline                        bool
	RefreshCharts                  bool
	ChartCacheDir                  string
	ChartCredentials               ChartCredentials
	RepoMaps                       []RepoMap
	AllowNetwork                   bool
	GitCacheDir                    string
	RefreshGit                     bool
	GitCredentials                 GitCredentials
	RefreshRemoteResources         bool
	RemoteResourceCacheDir         string
	RemoteResourceForbiddenRoots   []string
	RemoteResourceCredentials      RemoteResourceCredentials
	PluginRenderer                 PluginRenderer
	PluginTimeout                  time.Duration
	SkipKinds                      []string
	SkipCRDs                       bool
	SkipSecrets                    bool
	ApplicationSetProviderFixtures []string
	ApplicationSetProviderData     ApplicationSetProviderData
	ChangedOnly                    *bool
	StrictChangedOnly              bool
	Unified                        int
	StripAttrs                     []string
	GitAcquirer                    GitAcquirer
	ChartAcquirer                  ChartAcquirer
	RemoteResourceAcquirer         RemoteResourceAcquirer
	RecordCacheEvents              bool
}

// ApplicationSetProviderData supplies explicit offline data for provider-backed
// ApplicationSet generators.
type ApplicationSetProviderData struct {
	Clusters         []ApplicationSetProviderCluster
	ClusterDecisions []ApplicationSetProviderClusterDecision
	SCMRepositories  []ApplicationSetProviderSCMRepository
	PullRequests     []ApplicationSetProviderPullRequest
	Plugins          []ApplicationSetProviderPlugin
}

// ApplicationSetProviderCluster mirrors one cluster fixture entry.
type ApplicationSetProviderCluster struct {
	Name        string
	Server      string
	Project     string
	Labels      map[string]string
	Annotations map[string]string
	Values      map[string]string
}

// ApplicationSetProviderClusterDecision mirrors one cluster decision fixture entry.
type ApplicationSetProviderClusterDecision struct {
	ConfigMapRef  string
	ResourceName  string
	Labels        map[string]string
	MatchKey      string
	StatusListKey string
	Decisions     []map[string]any
	Values        map[string]string
}

// ApplicationSetProviderSCMRepository mirrors one SCM repository fixture entry.
type ApplicationSetProviderSCMRepository struct {
	Provider     string
	Organization string
	Project      string
	Region       string
	Repository   string
	RepositoryID string
	Branch       string
	SHA          string
	URL          string
	Tags         map[string]string
	Labels       []string
	Paths        []string
	Values       map[string]string
}

// ApplicationSetProviderPullRequest mirrors one pull request fixture entry.
type ApplicationSetProviderPullRequest struct {
	Provider     string
	Organization string
	Project      string
	Repository   string
	Number       int
	Title        string
	Branch       string
	TargetBranch string
	HeadSHA      string
	Author       string
	State        string
	Labels       []string
	Values       map[string]string
}

// ApplicationSetProviderPlugin mirrors one plugin fixture entry.
type ApplicationSetProviderPlugin struct {
	ConfigMapRef string
	Outputs      []map[string]any
	Values       map[string]string
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

// DiffImages compares container images between config.PathOrig and config.Path.
func DiffImages(ctx context.Context, config Config) (ImageDiffResult, error) {
	return NewClient(config).DiffImages(ctx)
}

// DiffImages compares container images between the client's PathOrig and Path.
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
	return app.BuildRequest{
		Path:                           client.config.Path,
		Strict:                         client.config.Strict,
		Offline:                        client.config.Offline,
		RefreshCharts:                  client.config.RefreshCharts,
		ChartCacheDir:                  client.config.ChartCacheDir,
		ChartCredentials:               chartCredentialsToInternal(client.config.ChartCredentials),
		RepoMaps:                       repoMapsToInternal(client.config.RepoMaps),
		AllowNetwork:                   client.config.AllowNetwork,
		GitCacheDir:                    client.config.GitCacheDir,
		RefreshGit:                     client.config.RefreshGit,
		GitCredentials:                 gitCredentialsToInternal(client.config.GitCredentials),
		RefreshRemoteResources:         client.config.RefreshRemoteResources,
		RemoteResourceCacheDir:         client.config.RemoteResourceCacheDir,
		RemoteResourceForbiddenRoots:   append([]string(nil), client.config.RemoteResourceForbiddenRoots...),
		RemoteResourceCredentials:      remoteResourceCredentialsToInternal(client.config.RemoteResourceCredentials),
		RemoteResourceGitCredentials:   gitCredentialsToRemoteInternal(client.config.GitCredentials),
		PluginTimeout:                  client.config.PluginTimeout,
		SkipKinds:                      append([]string(nil), client.config.SkipKinds...),
		SkipCRDs:                       client.config.SkipCRDs,
		SkipSecrets:                    client.config.SkipSecrets,
		ApplicationSetProviderFixtures: append([]string(nil), client.config.ApplicationSetProviderFixtures...),
		ApplicationSetProviderData:     applicationSetProviderDataToInternal(client.config.ApplicationSetProviderData),
		RecordCacheEvents:              client.config.RecordCacheEvents,
	}
}

func (client *Client) diffRequest() app.DiffRequest {
	unified := client.config.Unified
	if unified == 0 {
		unified = 3
	}
	changedOnly := true
	if client.config.ChangedOnly != nil {
		changedOnly = *client.config.ChangedOnly
	}
	return app.DiffRequest{
		LeftPath:                       client.config.PathOrig,
		RightPath:                      client.config.Path,
		ChangedOnly:                    changedOnly,
		StrictChangedOnly:              client.config.StrictChangedOnly,
		Strict:                         client.config.Strict,
		Unified:                        unified,
		StripAttrs:                     append([]string(nil), client.config.StripAttrs...),
		Offline:                        client.config.Offline,
		RefreshCharts:                  client.config.RefreshCharts,
		ChartCacheDir:                  client.config.ChartCacheDir,
		ChartCredentials:               chartCredentialsToInternal(client.config.ChartCredentials),
		RepoMaps:                       repoMapsToInternal(client.config.RepoMaps),
		AllowNetwork:                   client.config.AllowNetwork,
		GitCacheDir:                    client.config.GitCacheDir,
		RefreshGit:                     client.config.RefreshGit,
		GitCredentials:                 gitCredentialsToInternal(client.config.GitCredentials),
		RefreshRemoteResources:         client.config.RefreshRemoteResources,
		RemoteResourceCacheDir:         client.config.RemoteResourceCacheDir,
		RemoteResourceCredentials:      remoteResourceCredentialsToInternal(client.config.RemoteResourceCredentials),
		RemoteResourceGitCredentials:   gitCredentialsToRemoteInternal(client.config.GitCredentials),
		PluginTimeout:                  client.config.PluginTimeout,
		SkipKinds:                      append([]string(nil), client.config.SkipKinds...),
		SkipCRDs:                       client.config.SkipCRDs,
		SkipSecrets:                    client.config.SkipSecrets,
		ApplicationSetProviderFixtures: append([]string(nil), client.config.ApplicationSetProviderFixtures...),
		ApplicationSetProviderData:     applicationSetProviderDataToInternal(client.config.ApplicationSetProviderData),
		RecordCacheEvents:              client.config.RecordCacheEvents,
	}
}

// RepoMap maps a source repository URL to a local checkout path.
type RepoMap struct {
	URL  string
	Path string
}

// Application identifies an Argo CD Application.
type Application struct {
	Namespace string
	Name      string
	Project   string
}

// PluginRenderer renders Argo CD config management plugin sources for embedded
// callers. The default CLI and public API paths do not execute plugin commands.
type PluginRenderer interface {
	RenderPlugin(ctx context.Context, request PluginRequest) (PluginResult, error)
}

// PluginRegistry dispatches plugin render requests by explicit plugin name.
// It never discovers plugins or executes plugin commands itself.
type PluginRegistry struct {
	renderers map[string]PluginRenderer
}

// NewPluginRegistry creates a named in-process plugin renderer registry.
//
// Plugin names are trimmed. A source with an empty plugin name only matches an
// explicitly registered empty-name renderer.
func NewPluginRegistry(renderers map[string]PluginRenderer) *PluginRegistry {
	registry := &PluginRegistry{renderers: make(map[string]PluginRenderer, len(renderers))}
	for name, renderer := range renderers {
		registry.renderers[strings.TrimSpace(name)] = renderer
	}
	return registry
}

// RenderPlugin renders a plugin source with the registered renderer for the
// requested plugin name.
func (registry *PluginRegistry) RenderPlugin(ctx context.Context, request PluginRequest) (PluginResult, error) {
	name := strings.TrimSpace(request.Plugin.Name)
	renderer, ok := registry.renderer(name)
	if !ok {
		message := fmt.Sprintf("config management plugin %s is not registered in plugin registry", pluginDisplayName(name))
		return PluginResult{Diagnostics: []Diagnostic{{
			Code:     diagnostic.CodePluginUnsupported,
			Severity: "error",
			Category: "plugin",
			Message:  message,
		}}}, fmt.Errorf("%s", message)
	}
	return renderer.RenderPlugin(ctx, request)
}

func (registry *PluginRegistry) renderer(name string) (PluginRenderer, bool) {
	if registry == nil {
		return nil, false
	}
	renderer, ok := registry.renderers[name]
	return renderer, ok && renderer != nil
}

func pluginDisplayName(name string) string {
	if name == "" {
		return "<unnamed>"
	}
	return name
}

// PluginRequest is passed to an injected PluginRenderer.
type PluginRequest struct {
	Application          Application
	DestinationNamespace string
	Source               PluginSource
	Plugin               PluginConfig
}

// PluginSource describes the resolved source for a plugin render.
type PluginSource struct {
	RepoRoot       string
	Path           string
	RepoURL        string
	TargetRevision string
}

// PluginConfig is the explicit plugin configuration from an Application source.
type PluginConfig struct {
	Name       string
	Env        []PluginEnvEntry
	Parameters []PluginParameter
}

// PluginEnvEntry is one explicit plugin environment entry.
type PluginEnvEntry struct {
	Name  string
	Value string
}

// PluginParameter is one plugin parameter. String, Map, and Array preserve
// Argo CD's distinct optional value semantics.
type PluginParameter struct {
	Name   string
	String *string
	Map    *PluginMapParameter
	Array  *PluginArrayParameter
}

// PluginMapParameter wraps a map parameter so present-empty maps are distinct
// from absent map parameters.
type PluginMapParameter struct {
	Values map[string]string
}

// PluginArrayParameter wraps an array parameter so present-empty arrays are
// distinct from absent array parameters.
type PluginArrayParameter struct {
	Values []string
}

// PluginResult is returned by an injected PluginRenderer.
type PluginResult struct {
	Manifests   []PluginManifest
	Diagnostics []Diagnostic
}

// PluginManifest is one rendered plugin object with optional source path.
type PluginManifest struct {
	Path   string
	Object map[string]any
}

// Manifest is one rendered Kubernetes object with source provenance.
type Manifest struct {
	Application Application
	SourceIndex int
	SourceName  string
	SourcePath  string
	Object      map[string]any
}

// Diagnostic describes a warning, error, or informational finding.
type Diagnostic struct {
	Code       string
	Severity   string
	Category   string
	Message    string
	Provenance Provenance
}

// Provenance identifies where a Diagnostic originated.
type Provenance struct {
	Path    string
	Pointer string
}

// ApplicationStatus reports whether rendering an Application passed, failed, or
// was skipped.
type ApplicationStatus struct {
	Application Application
	Status      string
	Message     string
}

// CacheEvent describes an optional source acquisition cache observation.
type CacheEvent struct {
	Source   string
	Action   string
	Target   string
	Revision string
	CacheHit bool
	Offline  bool
	Refresh  bool
	Error    string
}

// RenderResult is returned by render operations.
type RenderResult struct {
	Applications []Application
	Manifests    []Manifest
	Diagnostics  []Diagnostic
	Statuses     []ApplicationStatus
	CacheEvents  []CacheEvent
}

// ListApplicationsResult is returned by list operations.
type ListApplicationsResult struct {
	Applications []Application
	Diagnostics  []Diagnostic
	CacheEvents  []CacheEvent
}

// DiffApplicationsResult is returned by Application diff operations.
type DiffApplicationsResult struct {
	Results     []DiffResult
	Diagnostics []Diagnostic
	CacheEvents []CacheEvent
}

// DiffResult describes one resource-level Application diff.
type DiffResult struct {
	Parent   DiffParent
	Resource Resource
	Change   string
	Diff     string
}

// DiffParent identifies the Application source that produced a diff.
type DiffParent struct {
	Namespace   string
	Name        string
	SourceIndex int
	SourceName  string
	SourcePath  string
}

// Resource identifies a Kubernetes resource.
type Resource struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// ImageDiffResult is returned by image diff operations.
type ImageDiffResult struct {
	Added       []string
	Removed     []string
	Unchanged   []string
	Diagnostics []Diagnostic
	CacheEvents []CacheEvent
}

// GitCredentials supplies credentials for Git source acquisition.
type GitCredentials struct {
	Username          string
	Password          string
	BearerToken       string
	SSHPrivateKeyPath string
	SSHPrivateKey     string
	SSHPassphrase     string
	SSHKnownHostsPath string
}

// GitRequest identifies a Git source to acquire.
type GitRequest struct {
	URL      string
	Revision string
}

// GitOptions controls Git source acquisition.
type GitOptions struct {
	AllowNetwork bool
	CacheDir     string
	Refresh      bool
	Credentials  GitCredentials
}

// GitResult describes an acquired Git source.
type GitResult struct {
	Path      string
	Revision  string
	FromCache bool
	Network   bool
}

// GitAcquirer acquires Git sources for Application rendering.
type GitAcquirer interface {
	Acquire(ctx context.Context, request GitRequest, opts GitOptions) (GitResult, error)
}

// RepositoryKind identifies a Helm repository transport.
type RepositoryKind string

const (
	// RepositoryHTTP identifies an HTTP(S) Helm chart repository.
	RepositoryHTTP RepositoryKind = "http"
	// RepositoryOCI identifies an OCI Helm chart repository.
	RepositoryOCI RepositoryKind = "oci"
)

// ChartCredentials supplies credentials for Helm chart acquisition.
type ChartCredentials struct {
	Username       string
	Password       string
	BearerToken    string
	RegistryConfig string
}

// ChartRequest identifies a Helm chart to acquire.
type ChartRequest struct {
	Repository string
	Name       string
	Version    string
	Kind       RepositoryKind
}

// ChartOptions controls Helm chart acquisition.
type ChartOptions struct {
	CacheDir    string
	Offline     bool
	Refresh     bool
	Credentials ChartCredentials
}

// ChartResult describes an acquired Helm chart.
type ChartResult struct {
	ChartDir   string
	Repository string
	Name       string
	Version    string
	Kind       RepositoryKind
	FromCache  bool
}

// ChartAcquirer acquires Helm charts for Application rendering.
type ChartAcquirer interface {
	Acquire(ctx context.Context, request ChartRequest, opts ChartOptions) (ChartResult, error)
}

// RemoteResourceKind classifies a remote Kustomize resource acquisition.
type RemoteResourceKind string

const (
	RemoteResourceHTTPFile RemoteResourceKind = "http-file"
	RemoteResourceGitRepo  RemoteResourceKind = "git-repo"
)

// RemoteResourceRequest identifies a remote Kustomize resource to acquire. URL
// is the original Kustomize ref; RepoURL and Revision are structured metadata
// for Git refs.
type RemoteResourceRequest struct {
	URL      string
	Kind     RemoteResourceKind
	RepoURL  string
	Revision string
}

// RemoteResourceCredentials supplies credentials for remote Kustomize HTTP resources.
type RemoteResourceCredentials struct {
	Username    string
	Password    string
	BearerToken string
}

// RemoteResourceOptions controls remote Kustomize resource acquisition.
type RemoteResourceOptions struct {
	CacheDir       string
	Offline        bool
	Refresh        bool
	ForbiddenRoots []string
	Credentials    RemoteResourceCredentials
	GitCredentials GitCredentials
}

// RemoteResourceResult describes an acquired remote Kustomize resource.
type RemoteResourceResult struct {
	Path      string
	URL       string
	Revision  string
	FromCache bool
}

// RemoteResourceAcquirer acquires remote Kustomize resources for Application
// rendering.
type RemoteResourceAcquirer interface {
	Acquire(ctx context.Context, request RemoteResourceRequest, opts RemoteResourceOptions) (RemoteResourceResult, error)
}

func renderResultFromBuild(result app.BuildResult) RenderResult {
	return RenderResult{
		Applications: applicationsFromInternal(result.Applications),
		Manifests:    manifestsFromInternal(result.ApplicationManifests),
		Diagnostics:  diagnosticsFromInternal(result.Diagnostics),
		Statuses:     statusesFromInternal(result.Statuses),
		CacheEvents:  cacheEventsFromInternal(result.CacheEvents),
	}
}

func applicationsFromInternal(applications []argoappv1.Application) []Application {
	out := make([]Application, 0, len(applications))
	for _, application := range applications {
		out = append(out, Application{
			Namespace: application.Namespace,
			Name:      application.Name,
			Project:   application.Spec.Project,
		})
	}
	return out
}

func manifestsFromInternal(manifests []app.ApplicationManifest) []Manifest {
	out := make([]Manifest, 0, len(manifests))
	for _, item := range manifests {
		out = append(out, Manifest{
			Application: Application{
				Namespace: item.Application.Namespace,
				Name:      item.Application.Name,
				Project:   item.Application.Spec.Project,
			},
			SourceIndex: item.Manifest.SourceIndex,
			SourceName:  item.Manifest.SourceName,
			SourcePath:  item.Manifest.Path,
			Object:      cloneMap(item.Manifest.Object.Object),
		})
	}
	return out
}

func diagnosticsFromInternal(diagnostics []diagnostic.Diagnostic) []Diagnostic {
	normalized := diagnostic.WithStableCodes(diagnostics)
	out := make([]Diagnostic, 0, len(normalized))
	for _, item := range normalized {
		out = append(out, Diagnostic{
			Code:     item.Code,
			Severity: string(item.Severity),
			Category: item.Category,
			Message:  item.Message,
			Provenance: Provenance{
				Path:    item.Provenance.Path,
				Pointer: item.Provenance.Pointer,
			},
		})
	}
	return out
}

func diagnosticsToInternal(diagnostics []Diagnostic) []diagnostic.Diagnostic {
	out := make([]diagnostic.Diagnostic, 0, len(diagnostics))
	for _, item := range diagnostics {
		out = append(out, diagnostic.Diagnostic{
			Code:     item.Code,
			Severity: diagnostic.Severity(item.Severity),
			Category: item.Category,
			Message:  item.Message,
			Provenance: diagnostic.Provenance{
				Path:    item.Provenance.Path,
				Pointer: item.Provenance.Pointer,
			},
		})
	}
	return out
}

func statusesFromInternal(statuses []app.ApplicationStatus) []ApplicationStatus {
	out := make([]ApplicationStatus, 0, len(statuses))
	for _, item := range statuses {
		out = append(out, ApplicationStatus{
			Application: Application{
				Namespace: item.Namespace,
				Name:      item.Name,
			},
			Status:  item.Status,
			Message: item.Message,
		})
	}
	return out
}

func cacheEventsFromInternal(events []cacheevent.Event) []CacheEvent {
	out := make([]CacheEvent, 0, len(events))
	for _, event := range events {
		out = append(out, CacheEvent{
			Source:   string(event.Source),
			Action:   string(event.Action),
			Target:   event.Target,
			Revision: event.Revision,
			CacheHit: event.CacheHit,
			Offline:  event.Offline,
			Refresh:  event.Refresh,
			Error:    event.Error,
		})
	}
	return out
}

func diffResultsFromInternal(results []diff.Result) []DiffResult {
	out := make([]DiffResult, 0, len(results))
	for _, item := range results {
		out = append(out, DiffResult{
			Parent: DiffParent{
				Namespace:   item.Parent.Namespace,
				Name:        item.Parent.Name,
				SourceIndex: item.Parent.SourceIndex,
				SourceName:  item.Parent.SourceName,
				SourcePath:  item.Parent.SourcePath,
			},
			Resource: Resource{
				Group:     item.Resource.Group,
				Kind:      item.Resource.Kind,
				Namespace: item.Resource.Namespace,
				Name:      item.Resource.Name,
			},
			Change: string(item.Change),
			Diff:   item.Diff,
		})
	}
	return out
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return typed
	}
}

func applicationSetProviderDataToInternal(data ApplicationSetProviderData) appset.ProviderData {
	out := appset.ProviderData{
		Clusters:         make([]appset.ClusterInput, 0, len(data.Clusters)),
		ClusterDecisions: make([]appset.ClusterDecisionInput, 0, len(data.ClusterDecisions)),
		SCMRepositories:  make([]appset.SCMRepositoryInput, 0, len(data.SCMRepositories)),
		PullRequests:     make([]appset.PullRequestInput, 0, len(data.PullRequests)),
		Plugins:          make([]appset.PluginInput, 0, len(data.Plugins)),
	}
	for _, item := range data.Clusters {
		out.Clusters = append(out.Clusters, appset.ClusterInput{
			Name:        item.Name,
			Server:      item.Server,
			Project:     item.Project,
			Labels:      cloneStringMap(item.Labels),
			Annotations: cloneStringMap(item.Annotations),
			Values:      cloneStringMap(item.Values),
		})
	}
	for _, item := range data.ClusterDecisions {
		out.ClusterDecisions = append(out.ClusterDecisions, appset.ClusterDecisionInput{
			ConfigMapRef:  item.ConfigMapRef,
			ResourceName:  item.ResourceName,
			Labels:        cloneStringMap(item.Labels),
			MatchKey:      item.MatchKey,
			StatusListKey: item.StatusListKey,
			Decisions:     cloneAnyMaps(item.Decisions),
			Values:        cloneStringMap(item.Values),
		})
	}
	for _, item := range data.SCMRepositories {
		out.SCMRepositories = append(out.SCMRepositories, appset.SCMRepositoryInput{
			Provider:     item.Provider,
			Organization: item.Organization,
			Project:      item.Project,
			Region:       item.Region,
			Repository:   item.Repository,
			RepositoryID: item.RepositoryID,
			Branch:       item.Branch,
			SHA:          item.SHA,
			URL:          item.URL,
			Tags:         cloneStringMap(item.Tags),
			Labels:       append([]string(nil), item.Labels...),
			Paths:        append([]string(nil), item.Paths...),
			Values:       cloneStringMap(item.Values),
		})
	}
	for _, item := range data.PullRequests {
		out.PullRequests = append(out.PullRequests, appset.PullRequestInput{
			Provider:     item.Provider,
			Organization: item.Organization,
			Project:      item.Project,
			Repository:   item.Repository,
			Number:       item.Number,
			Title:        item.Title,
			Branch:       item.Branch,
			TargetBranch: item.TargetBranch,
			HeadSHA:      item.HeadSHA,
			Author:       item.Author,
			State:        item.State,
			Labels:       append([]string(nil), item.Labels...),
			Values:       cloneStringMap(item.Values),
		})
	}
	for _, item := range data.Plugins {
		out.Plugins = append(out.Plugins, appset.PluginInput{
			ConfigMapRef: item.ConfigMapRef,
			Outputs:      pluginOutputsToInternal(item.Outputs),
			Values:       cloneStringMap(item.Values),
		})
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneAnyMaps(input []map[string]any) []map[string]any {
	if input == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		out = append(out, cloneMap(item))
	}
	return out
}

func pluginOutputsToInternal(input []map[string]any) []any {
	if input == nil {
		return nil
	}
	out := make([]any, 0, len(input))
	for _, item := range input {
		out = append(out, cloneMap(item))
	}
	return out
}

func repoMapsToInternal(repoMaps []RepoMap) []sourcepkg.RepoMap {
	out := make([]sourcepkg.RepoMap, 0, len(repoMaps))
	for _, repoMap := range repoMaps {
		out = append(out, sourcepkg.RepoMap{URL: repoMap.URL, Path: repoMap.Path})
	}
	return out
}

func gitCredentialsToInternal(credentials GitCredentials) sourcepkg.GitCredentials {
	return sourcepkg.GitCredentials{
		Username:          credentials.Username,
		Password:          credentials.Password,
		BearerToken:       credentials.BearerToken,
		SSHPrivateKeyPath: credentials.SSHPrivateKeyPath,
		SSHPrivateKey:     credentials.SSHPrivateKey,
		SSHPassphrase:     credentials.SSHPassphrase,
		SSHKnownHostsPath: credentials.SSHKnownHostsPath,
	}
}

func gitCredentialsToRemoteInternal(credentials GitCredentials) remote.GitCredentials {
	return remote.GitCredentials{
		Username:          credentials.Username,
		Password:          credentials.Password,
		BearerToken:       credentials.BearerToken,
		SSHPrivateKeyPath: credentials.SSHPrivateKeyPath,
		SSHPrivateKey:     credentials.SSHPrivateKey,
		SSHPassphrase:     credentials.SSHPassphrase,
		SSHKnownHostsPath: credentials.SSHKnownHostsPath,
	}
}

func remoteGitCredentialsFromInternal(credentials remote.GitCredentials) GitCredentials {
	return GitCredentials{
		Username:          credentials.Username,
		Password:          credentials.Password,
		BearerToken:       credentials.BearerToken,
		SSHPrivateKeyPath: credentials.SSHPrivateKeyPath,
		SSHPrivateKey:     credentials.SSHPrivateKey,
		SSHPassphrase:     credentials.SSHPassphrase,
		SSHKnownHostsPath: credentials.SSHKnownHostsPath,
	}
}

func remoteResourceCredentialsToInternal(credentials RemoteResourceCredentials) remote.Credentials {
	return remote.Credentials{
		Username:    credentials.Username,
		Password:    credentials.Password,
		BearerToken: credentials.BearerToken,
	}
}

func remoteResourceCredentialsFromInternal(credentials remote.Credentials) RemoteResourceCredentials {
	return RemoteResourceCredentials{
		Username:    credentials.Username,
		Password:    credentials.Password,
		BearerToken: credentials.BearerToken,
	}
}

func pluginRequestFromInternal(request renderpkg.PluginRequest) PluginRequest {
	return PluginRequest{
		Application: Application{
			Namespace: request.AppNamespace,
			Name:      request.AppName,
			Project:   request.Project,
		},
		DestinationNamespace: request.Namespace,
		Source: PluginSource{
			RepoRoot:       request.Source.RepoRoot,
			Path:           request.Source.Path,
			RepoURL:        request.Source.RepoURL,
			TargetRevision: request.Source.TargetRevision,
		},
		Plugin: pluginConfigFromInternal(request.Plugin),
	}
}

func pluginConfigFromInternal(config renderpkg.PluginConfig) PluginConfig {
	return PluginConfig{
		Name:       config.Name,
		Env:        pluginEnvFromInternal(config.Env),
		Parameters: pluginParametersFromInternal(config.Parameters),
	}
}

func pluginEnvFromInternal(env argoappv1.Env) []PluginEnvEntry {
	out := make([]PluginEnvEntry, 0, len(env))
	for _, item := range env {
		out = append(out, PluginEnvEntry{Name: item.Name, Value: item.Value})
	}
	return out
}

func pluginParametersFromInternal(params argoappv1.ApplicationSourcePluginParameters) []PluginParameter {
	out := make([]PluginParameter, 0, len(params))
	for _, item := range params {
		param := PluginParameter{Name: item.Name}
		if item.String_ != nil {
			value := *item.String_
			param.String = &value
		}
		if item.OptionalMap != nil {
			param.Map = &PluginMapParameter{Values: cloneStringMapPresent(item.Map)}
		}
		if item.OptionalArray != nil {
			param.Array = &PluginArrayParameter{Values: append([]string{}, item.Array...)}
		}
		out = append(out, param)
	}
	return out
}

func pluginManifestsToInternal(manifests []PluginManifest) []renderpkg.Manifest {
	out := make([]renderpkg.Manifest, 0, len(manifests))
	for _, item := range manifests {
		out = append(out, renderpkg.Manifest{
			Path:   item.Path,
			Object: &unstructured.Unstructured{Object: cloneMap(item.Object)},
		})
	}
	return out
}

func cloneStringMapPresent(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func gitCredentialsFromInternal(credentials sourcepkg.GitCredentials) GitCredentials {
	return GitCredentials{
		Username:          credentials.Username,
		Password:          credentials.Password,
		BearerToken:       credentials.BearerToken,
		SSHPrivateKeyPath: credentials.SSHPrivateKeyPath,
		SSHPrivateKey:     credentials.SSHPrivateKey,
		SSHPassphrase:     credentials.SSHPassphrase,
		SSHKnownHostsPath: credentials.SSHKnownHostsPath,
	}
}

func remoteResourceKindFromInternal(kind remote.RequestKind) RemoteResourceKind {
	switch kind {
	case "", remote.RequestHTTPFile:
		return RemoteResourceHTTPFile
	case remote.RequestGitRepo:
		return RemoteResourceGitRepo
	default:
		return RemoteResourceKind(kind)
	}
}

func chartCredentialsToInternal(credentials ChartCredentials) chart.ChartCredentials {
	return chart.ChartCredentials{
		Username:       credentials.Username,
		Password:       credentials.Password,
		BearerToken:    credentials.BearerToken,
		RegistryConfig: credentials.RegistryConfig,
	}
}

func chartCredentialsFromInternal(credentials chart.ChartCredentials) ChartCredentials {
	return ChartCredentials{
		Username:       credentials.Username,
		Password:       credentials.Password,
		BearerToken:    credentials.BearerToken,
		RegistryConfig: credentials.RegistryConfig,
	}
}

func chartKindToInternal(kind RepositoryKind) chart.RepositoryKind {
	return chart.RepositoryKind(kind)
}

func chartKindFromInternal(kind chart.RepositoryKind) RepositoryKind {
	return RepositoryKind(kind)
}

type gitAcquirerAdapter struct {
	acquirer GitAcquirer
}

func (adapter gitAcquirerAdapter) Acquire(ctx context.Context, request sourcepkg.GitRequest, opts sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	result, err := adapter.acquirer.Acquire(ctx, GitRequest{
		URL:      request.URL,
		Revision: request.Revision,
	}, GitOptions{
		AllowNetwork: opts.AllowNetwork,
		CacheDir:     opts.CacheDir,
		Refresh:      opts.Refresh,
		Credentials:  gitCredentialsFromInternal(opts.Credentials),
	})
	return sourcepkg.GitResult{
		Path:      result.Path,
		Revision:  result.Revision,
		FromCache: result.FromCache,
		Network:   result.Network,
	}, err
}

type chartAcquirerAdapter struct {
	acquirer ChartAcquirer
}

func (adapter chartAcquirerAdapter) Acquire(ctx context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	result, err := adapter.acquirer.Acquire(ctx, ChartRequest{
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       chartKindFromInternal(request.Kind),
	}, ChartOptions{
		CacheDir:    opts.CacheDir,
		Offline:     opts.Offline,
		Refresh:     opts.Refresh,
		Credentials: chartCredentialsFromInternal(opts.Credentials),
	})
	return chart.Result{
		ChartDir:   result.ChartDir,
		Repository: result.Repository,
		Name:       result.Name,
		Version:    result.Version,
		Kind:       chartKindToInternal(result.Kind),
		FromCache:  result.FromCache,
	}, err
}

type remoteResourceAcquirerAdapter struct {
	acquirer RemoteResourceAcquirer
}

func (adapter remoteResourceAcquirerAdapter) Acquire(ctx context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	result, err := adapter.acquirer.Acquire(ctx, RemoteResourceRequest{
		URL:      request.URL,
		Kind:     remoteResourceKindFromInternal(request.Kind),
		RepoURL:  request.RepoURL,
		Revision: request.Revision,
	}, RemoteResourceOptions{
		CacheDir:       opts.CacheDir,
		Offline:        opts.Offline,
		Refresh:        opts.Refresh,
		ForbiddenRoots: append([]string(nil), opts.ForbiddenRoots...),
		Credentials:    remoteResourceCredentialsFromInternal(opts.Credentials),
		GitCredentials: remoteGitCredentialsFromInternal(opts.GitCredentials),
	})
	return remote.Result{
		Path:      result.Path,
		URL:       result.URL,
		Revision:  result.Revision,
		FromCache: result.FromCache,
	}, err
}

type pluginRendererAdapter struct {
	renderer PluginRenderer
}

func (adapter pluginRendererAdapter) RenderPlugin(ctx context.Context, request renderpkg.PluginRequest) ([]renderpkg.Manifest, []diagnostic.Diagnostic, error) {
	result, err := adapter.renderer.RenderPlugin(ctx, pluginRequestFromInternal(request))
	return pluginManifestsToInternal(result.Manifests), diagnosticsToInternal(result.Diagnostics), err
}
