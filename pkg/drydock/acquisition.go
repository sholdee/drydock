package drydock

import (
	"context"
	"time"

	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// RepoMap maps a source repository URL to a local checkout path.
type RepoMap struct {
	URL  string
	Path string
}

// GitCredentials supplies credentials for Git source acquisition. When no SSH
// private key is set, SSH sources fall back to the ssh-agent, the ~/.ssh/config
// IdentityFile entries for the host, and the default ~/.ssh/id_* keys, in that
// order. Host keys are always verified against known_hosts: the explicit file
// when set, otherwise $SSH_KNOWN_HOSTS, ~/.ssh/known_hosts, and
// /etc/ssh/ssh_known_hosts.
//
// Hosts lists the hosts that receive Username, Password, and BearerToken,
// like the --git-auth-host flag: host[:port] or https://host[:port] for
// HTTPS, and http://host[:port] to allow plain HTTP. The default Git
// acquirers send them only to a Git URL whose scheme, host, and port match
// an entry: Application sources, remote Kustomize Git bases, submodules,
// and the batch requests to a Git LFS server. An object download from a Git
// LFS server never gets them. drydock removes them on a redirect that leaves
// that scheme, host, and port. A run with Username, Password, or BearerToken and no
// Hosts entry fails before it fetches anything. The SSH fields ignore
// Hosts. A custom GitAcquirer or RemoteResourceAcquirer receives Hosts and
// must apply the same rule itself.
type GitCredentials struct {
	Username          string
	Password          string
	BearerToken       string
	SSHPrivateKeyPath string
	SSHPrivateKey     string
	SSHPassphrase     string
	SSHKnownHostsPath string
	Hosts             []string
}

// GitRequest identifies a Git source to acquire.
type GitRequest struct {
	URL      string
	Revision string
}

// GitOptions controls Git source acquisition.
type GitOptions struct {
	// AllowNetwork is set by drydock for custom acquirers. Users should set
	// Config.Offline to disable network acquisition.
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
	CacheDir string
	Offline  bool
	Refresh  bool
	// MaxAge is Config.ChartCacheMaxAge. When positive, an acquirer that
	// caches charts should refetch a cached chart fetched longer ago than it
	// (or of unknown age), or, when Offline is set, reuse it and report
	// ChartResult.Stale. Zero never expires cached charts.
	MaxAge      time.Duration
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
	// CacheKey and FetchedAt identify the drydock chart cache entry under
	// ChartOptions.CacheDir the chart was served from and when that entry was
	// fetched. Under a positive ChartOptions.MaxAge, a persisted render
	// output is served only while every chart it read still matches such an
	// entry; leave them empty when the chart does not come from the drydock
	// chart cache, and such renders always re-run (reason chart-unverified).
	CacheKey  string
	FetchedAt time.Time
	// Stale reports a cached chart older than ChartOptions.MaxAge that was
	// reused because ChartOptions.Offline forbids refetching it. Each render
	// that reads it gets a source.chart-cache-stale warning and is never
	// persisted.
	Stale bool
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

// RemoteResourceCredentials supplies credentials for remote HTTP(S) files:
// remote Kustomize resources, Helm value files, and Helm file parameters.
// Hosts lists the hosts that receive them, like the --remote-auth-host
// flag: host[:port] or https://host[:port] for HTTPS, and
// http://host[:port] to allow plain HTTP. The default remote acquirer sends
// the credentials only to a request whose scheme, host, and port match an
// entry, and removes them on a redirect that leaves that scheme, host, and
// port. A run with a credential and no Hosts entry fails before it fetches
// anything. A custom RemoteResourceAcquirer receives Hosts and must apply
// the same rule itself.
type RemoteResourceCredentials struct {
	Username    string
	Password    string
	BearerToken string
	Hosts       []string
}

// OCICredentials supplies auth and TLS material for first-class OCI artifact
// sources; the TLS fields additionally configure OCI Helm chart pulls. All
// file fields are paths. Auth comes from two sources: RegistryConfig is a
// Docker/Helm registry config file (`helm registry login` format) consulted
// per registry host, so one run can authenticate to several registries; the
// Username/Password pair is the global override, presented to every OCI
// artifact registry and short-circuiting the file whenever either half is
// set. RegistryConfig is validated at the first oci:// acquisition of a run,
// not at request construction, so a run without OCI artifact sources never
// opens it. Hosts without an entry stay anonymous; identity-token entries
// and credential helpers that cannot run are errors; an auth-less file never
// reaches the OS keychain (unlike helm's chart loader). The CLI feeds one
// --registry-config path to both this field and
// ChartCredentials.RegistryConfig; embedders set each. The TLS fields are one
// global set: CAFile REPLACES the system CA pool for artifact registries but
// is ADDED to it for Helm chart registries, so a run pinning a private
// artifact registry keeps pulling public charts. Setting any TLS field (or
// InsecureSkipVerify) also disables the loopback plain-HTTP default for
// artifact sources. The chart-side TLS wiring applies only when
// Config.ChartAcquirer is nil; a supplied ChartAcquirer is responsible for
// its own transport.
type OCICredentials struct {
	Username           string
	Password           string
	RegistryConfig     string
	CAFile             string
	ClientCertFile     string
	ClientKeyFile      string
	InsecureSkipVerify bool
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
		Hosts:             append([]string(nil), credentials.Hosts...),
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
		Hosts:             append([]string(nil), credentials.Hosts...),
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
		Hosts:             append([]string(nil), credentials.Hosts...),
	}
}

func remoteResourceCredentialsToInternal(credentials RemoteResourceCredentials) remote.Credentials {
	return remote.Credentials{
		Username:    credentials.Username,
		Password:    credentials.Password,
		BearerToken: credentials.BearerToken,
		Hosts:       append([]string(nil), credentials.Hosts...),
	}
}

func remoteResourceCredentialsFromInternal(credentials remote.Credentials) RemoteResourceCredentials {
	return RemoteResourceCredentials{
		Username:    credentials.Username,
		Password:    credentials.Password,
		BearerToken: credentials.BearerToken,
		Hosts:       append([]string(nil), credentials.Hosts...),
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
		Hosts:             append([]string(nil), credentials.Hosts...),
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

func ociCredentialsToInternal(credentials OCICredentials) ociartifact.Credentials {
	return ociartifact.Credentials{
		Username:           credentials.Username,
		Password:           credentials.Password,
		RegistryConfig:     credentials.RegistryConfig,
		CAFile:             credentials.CAFile,
		ClientCertFile:     credentials.ClientCertFile,
		ClientKeyFile:      credentials.ClientKeyFile,
		InsecureSkipVerify: credentials.InsecureSkipVerify,
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
		MaxAge:      opts.MaxAge,
		Credentials: chartCredentialsFromInternal(opts.Credentials),
	})
	return chart.Result{
		ChartDir:   result.ChartDir,
		Repository: result.Repository,
		Name:       result.Name,
		Version:    result.Version,
		Kind:       chartKindToInternal(result.Kind),
		FromCache:  result.FromCache,
		CacheKey:   result.CacheKey,
		FetchedAt:  result.FetchedAt,
		Stale:      result.Stale,
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
