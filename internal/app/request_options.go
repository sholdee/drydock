package app

import (
	"fmt"
	"time"

	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/ociartifact"
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
	DiscoverIgnoreGlobs    []string
	// DiscoverManifestPaths are operator-supplied Application/ApplicationSet
	// files outside the repository. Both diff sides share one load.
	DiscoverManifestPaths []string
	// ArgoCDNamespace is the --argocd-namespace flag: the namespace where
	// Argo CD runs. It wins over the argocd-cmd-params-cm ConfigMaps, and
	// only with it does drydock skip settings objects and AppProjects in
	// other namespaces (loadSettingsFromDiscovery). Empty lets the
	// repository decide (config.ArgoSettings.ControllerNamespace).
	ArgoCDNamespace string

	discoverManifests *discovery.Result
}

type AcquisitionOptions struct {
	Offline       bool
	RefreshCharts bool
	// ChartCacheMaxAge, when positive, refetches cached charts fetched longer
	// ago than it, and keeps the persistent render cache from serving output
	// rendered from them. Under Offline an expired chart is reused with a
	// source.chart-cache-stale warning. Zero never expires cached charts.
	ChartCacheMaxAge             time.Duration
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
	OCICacheDir                  string
	OCICredentials               ociartifact.Credentials
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
// matches a target cluster. KubeVersion stands for the cluster version, so a
// per-app kubeVersion wins over it, as in Argo CD. APIVersions stands for
// the cluster API versions, so per-app apiVersions replace it, as in Argo
// CD (GetAPIVersionsOrDefault).
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

// validateKubeVersion checks --kube-version once per run. A per-app kubeVersion wins
// over it, so the render alone would check it only for Applications
// without one.
// validateRemoteResourceCredentials fails when a --remote-* credential is
// set and no --remote-auth-host names a host that receives it. Build,
// ListApplications, and the diff entry points run it first, so the run
// fails before it fetches anything.
func (options AcquisitionOptions) validateRemoteResourceCredentials() error {
	return options.RemoteResourceCredentials.Validate()
}

func (capabilities CapabilityOptions) validateKubeVersion() error {
	if _, err := parseKubeVersion(capabilities.KubeVersion); err != nil {
		return fmt.Errorf("--kube-version: %w", err)
	}
	return nil
}
