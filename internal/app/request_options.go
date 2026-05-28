package app

import (
	"time"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

type DiscoveryOptions struct {
	DiscoveryMode          string
	MaxDiscoveryDepth      int
	MaxDiscoveryDepthSet   bool
	DiscoverKustomizePaths []string
}

type AcquisitionOptions struct {
	Offline                      bool
	RefreshCharts                bool
	ChartCacheDir                string
	ChartCredentials             chart.ChartCredentials
	RepoMaps                     []sourcepkg.RepoMap
	GitCacheDir                  string
	RefreshGit                   bool
	GitCredentials               sourcepkg.GitCredentials
	RefreshRemoteResources       bool
	RemoteResourceCacheDir       string
	RemoteResourceForbiddenRoots []string
	RemoteResourceCredentials    remote.Credentials
	RemoteResourceGitCredentials remote.GitCredentials
	RecordCacheEvents            bool
}

type FilterOptions struct {
	SkipKinds   []string
	SkipCRDs    bool
	SkipSecrets bool
}

type ApplicationSetOptions struct {
	ApplicationSetProviderFixtures []string
	ApplicationSetProviderData     appset.ProviderData
}

type PluginOptions struct {
	PluginTimeout            time.Duration
	EnableAVPCompat          bool
	EnablePlugins            bool
	PluginPolicyPath         string
	PluginPolicyPathExplicit bool
	PluginPolicyRef          string
	PluginPolicyRepo         string
	DisablePluginPolicy      bool

	pluginPolicyLoaded      bool
	pluginPolicy            pluginpolicy.Policy
	pluginPolicyFingerprint string
	pluginPolicyExecTrusted bool
}

type ExecutionOptions struct {
	Parallelism int
}
