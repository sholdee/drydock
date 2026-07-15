package app

import (
	"time"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/rendercache"
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

// RenderCacheOptions controls the persistent render-output cache. The zero
// value leaves the persistent tier off; CLI and pkg/drydock default it on.
type RenderCacheOptions struct {
	RenderCacheEnabled  bool
	RenderCacheDir      string
	RenderCacheMaxBytes int64
	RefreshRenders      bool
	EngineFingerprint   rendercache.EngineFingerprint
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
	EnableKSOPSCompat        bool
	EnablePlugins            bool
	PluginCacheDir           string
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

type TrackingOptions struct {
	Method              string
	InstanceLabelKey    string
	InstallationID      string
	ControllerNamespace string
}

// CapabilityOptions overrides Kubernetes capabilities for rendering so output
// matches a target cluster. KubeVersion (when set) overrides per-app kubeVersion;
// APIVersions are unioned (deduped, sorted) with per-app apiVersions.
type CapabilityOptions struct {
	KubeVersion string
	APIVersions []string
}

type ApplicationRenderOptions struct {
	PluginOptions     PluginOptions
	TrackingOptions   TrackingOptions
	CapabilityOptions CapabilityOptions

	persistent persistentRenderOptions
}

type ExecutionOptions struct {
	Parallelism int
}

// CRDScopeOptions controls post-render CRD scope normalization. The zero value
// leaves normalization ON (it is a correctness fix); Disabled is the hidden
// --no-crd-scope escape hatch for safety-revert and perf A/B testing.
type CRDScopeOptions struct {
	Disabled bool
}
