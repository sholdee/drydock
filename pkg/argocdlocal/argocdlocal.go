// Package argocdlocal exposes the argocd-local orchestrator as an embeddable Go
// API for rendering Argo CD Applications and calculating local diffs.
package argocdlocal

import (
	"context"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/app"
	"github.com/home-operations/argocd-local/internal/chart"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/diff"
	"github.com/home-operations/argocd-local/internal/remote"
	sourcepkg "github.com/home-operations/argocd-local/internal/source"
)

// Config controls render, list, and diff operations.
//
// Path is the working tree to inspect for render/list operations and the right
// side for diff operations. PathOrig is the left side for diff operations. Use
// keyed struct literals; new fields may be added as argocd-local gains parity.
type Config struct {
	Path                         string
	PathOrig                     string
	Strict                       bool
	Offline                      bool
	RefreshCharts                bool
	ChartCacheDir                string
	ChartCredentials             ChartCredentials
	RepoMaps                     []RepoMap
	AllowNetwork                 bool
	GitCacheDir                  string
	RefreshGit                   bool
	GitCredentials               GitCredentials
	RefreshRemoteResources       bool
	RemoteResourceCacheDir       string
	RemoteResourceForbiddenRoots []string
	RemoteResourceCredentials    RemoteResourceCredentials
	SkipKinds                    []string
	SkipCRDs                     bool
	SkipSecrets                  bool
	ChangedOnly                  *bool
	StrictChangedOnly            bool
	Unified                      int
	StripAttrs                   []string
	GitAcquirer                  GitAcquirer
	ChartAcquirer                ChartAcquirer
	RemoteResourceAcquirer       RemoteResourceAcquirer
}

// Client runs argocd-local operations with a reusable Config and optional
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
	}, err
}

func (client *Client) buildRequest() app.BuildRequest {
	return app.BuildRequest{
		Path:                         client.config.Path,
		Strict:                       client.config.Strict,
		Offline:                      client.config.Offline,
		RefreshCharts:                client.config.RefreshCharts,
		ChartCacheDir:                client.config.ChartCacheDir,
		ChartCredentials:             chartCredentialsToInternal(client.config.ChartCredentials),
		RepoMaps:                     repoMapsToInternal(client.config.RepoMaps),
		AllowNetwork:                 client.config.AllowNetwork,
		GitCacheDir:                  client.config.GitCacheDir,
		RefreshGit:                   client.config.RefreshGit,
		GitCredentials:               gitCredentialsToInternal(client.config.GitCredentials),
		RefreshRemoteResources:       client.config.RefreshRemoteResources,
		RemoteResourceCacheDir:       client.config.RemoteResourceCacheDir,
		RemoteResourceForbiddenRoots: append([]string(nil), client.config.RemoteResourceForbiddenRoots...),
		RemoteResourceCredentials:    remoteResourceCredentialsToInternal(client.config.RemoteResourceCredentials),
		RemoteResourceGitCredentials: gitCredentialsToRemoteInternal(client.config.GitCredentials),
		SkipKinds:                    append([]string(nil), client.config.SkipKinds...),
		SkipCRDs:                     client.config.SkipCRDs,
		SkipSecrets:                  client.config.SkipSecrets,
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
		LeftPath:                     client.config.PathOrig,
		RightPath:                    client.config.Path,
		ChangedOnly:                  changedOnly,
		StrictChangedOnly:            client.config.StrictChangedOnly,
		Strict:                       client.config.Strict,
		Unified:                      unified,
		StripAttrs:                   append([]string(nil), client.config.StripAttrs...),
		Offline:                      client.config.Offline,
		RefreshCharts:                client.config.RefreshCharts,
		ChartCacheDir:                client.config.ChartCacheDir,
		ChartCredentials:             chartCredentialsToInternal(client.config.ChartCredentials),
		RepoMaps:                     repoMapsToInternal(client.config.RepoMaps),
		AllowNetwork:                 client.config.AllowNetwork,
		GitCacheDir:                  client.config.GitCacheDir,
		RefreshGit:                   client.config.RefreshGit,
		GitCredentials:               gitCredentialsToInternal(client.config.GitCredentials),
		RefreshRemoteResources:       client.config.RefreshRemoteResources,
		RemoteResourceCacheDir:       client.config.RemoteResourceCacheDir,
		RemoteResourceCredentials:    remoteResourceCredentialsToInternal(client.config.RemoteResourceCredentials),
		RemoteResourceGitCredentials: gitCredentialsToRemoteInternal(client.config.GitCredentials),
		SkipKinds:                    append([]string(nil), client.config.SkipKinds...),
		SkipCRDs:                     client.config.SkipCRDs,
		SkipSecrets:                  client.config.SkipSecrets,
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

// RenderResult is returned by render operations.
type RenderResult struct {
	Applications []Application
	Manifests    []Manifest
	Diagnostics  []Diagnostic
	Statuses     []ApplicationStatus
}

// ListApplicationsResult is returned by list operations.
type ListApplicationsResult struct {
	Applications []Application
	Diagnostics  []Diagnostic
}

// DiffApplicationsResult is returned by Application diff operations.
type DiffApplicationsResult struct {
	Results     []DiffResult
	Diagnostics []Diagnostic
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
	Path     string
	Revision string
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
	out := make([]Diagnostic, 0, len(diagnostics))
	for _, item := range diagnostics {
		out = append(out, Diagnostic{
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

func remoteResourceKindToInternal(kind RemoteResourceKind) remote.RequestKind {
	switch kind {
	case "", RemoteResourceHTTPFile:
		return remote.RequestHTTPFile
	case RemoteResourceGitRepo:
		return remote.RequestGitRepo
	default:
		return remote.RequestKind(kind)
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
		Path:     result.Path,
		Revision: result.Revision,
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
