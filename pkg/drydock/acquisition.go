package drydock

import (
	"context"

	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// RepoMap maps a source repository URL to a local checkout path.
type RepoMap struct {
	URL  string
	Path string
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
