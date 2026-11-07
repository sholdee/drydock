package render

import (
	"context"
	"errors"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/remote"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type ResolvedSource struct {
	RepoRoot       string
	Path           string
	Chart          string
	RepoURL        string
	TargetRevision string
	ExplicitType   argoappv1.ApplicationSourceType
	// HydratorSync marks the sync source of an Application with
	// spec.sourceHydrator. Its TargetRevision is syncSource.targetBranch,
	// and it renders from the local tree only when that branch is the one
	// checked out there (localProvider.resolveHydratorSyncSource).
	HydratorSync bool
}

type RenderOptions struct {
	AppName         string
	AppNamespace    string
	SourceIndex     int
	SourceName      string
	Project         string
	Namespace       string
	EnableAVPCompat bool
	QuietAVPCompat  bool
	// KeepOutputLineBreak keeps the line break at the end of Helm and
	// Kustomize output. Argo CD removes it when it reads the output of
	// helm template or kustomize build itself (util/exec/exec.go:285 at
	// v3.6.1). A config management plugin such as argocd-vault-plugin
	// reads that output through a pipe instead, so the line break stays.
	// The AVP compatibility plugin paths set it.
	KeepOutputLineBreak bool
	EnableKSOPSCompat   bool
	EnablePlugins       bool
	Plugin              *PluginConfig
	KubeVersion         string
	APIVersions         []string
	BuildOptions        []string
	Kustomize           *argoappv1.ApplicationSourceKustomize
	Jsonnet             argoappv1.ApplicationSourceJsonnet
	ArgoEnv             argoappv1.Env
	RefRoots            map[string]string
	RefSources          map[string]ResolvedSource
	ReleaseName         string
	// HelmVersion is spec.source.helm.version. Argo CD v3.6.1 accepts "",
	// "v3", and "v4", and fails the source for any other value.
	HelmVersion            string
	ValuesObject           map[string]any
	ValuesMergeMode        string
	ValueFiles             []string
	ValueFilesBaseDir      string
	ValueFilesBoundaryRoot string
	// LiteralValueFilePaths reads ValueFiles as written, without the Argo
	// CD build environment substitution of a Helm source. The values files
	// of a Kustomize helmCharts entry set it: kustomize loads them as
	// written, and Argo CD substitutes only images, commonLabels, and
	// commonAnnotations of a Kustomize source (util/kustomize/kustomize.go
	// at v3.6.1).
	LiteralValueFilePaths        bool
	IgnoreMissingValueFiles      bool
	HelmParameters               []argoappv1.HelmParameter
	HelmFileParameters           []argoappv1.HelmFileParameter
	HelmValueFileSchemes         []string
	HelmValueFileSchemesSet      bool
	SkipSchemaValidation         bool
	PassCredentials              bool
	DirectoryRecurse             bool
	DirectoryInclude             string
	DirectoryExclude             string
	ChartCacheDir                string
	OfflineCharts                bool
	RefreshCharts                bool
	ChartCacheMaxAge             time.Duration
	ChartForbiddenRoots          []string
	ChartCredentials             chart.ChartCredentials
	ChartAcquirer                chart.Acquirer
	HelmChartLoadCache           *HelmChartLoadCache
	OCIChartRepositories         map[string]bool
	RemoteResourceCacheDir       string
	OfflineRemoteResources       bool
	RefreshRemoteResources       bool
	RemoteResourceForbiddenRoots []string
	RemoteResourceCredentials    remote.Credentials
	RemoteResourceGitCredentials remote.GitCredentials
	RemoteResourceAcquirer       remote.Acquirer
	CacheEventRecorder           *cacheevent.Recorder
	AcquisitionCollector         *cacheevent.AcquisitionCollector
	IncludeCRDs                  bool
	IncludeCRDsSet               bool
	SkipHooks                    bool
	SkipTests                    bool

	// DirectoryDisableExtensionFilter is spec.source.directory.
	// disableExtensionFilter: the built-in extension filter is skipped and
	// every file passing include/exclude is read.
	DirectoryDisableExtensionFilter bool

	// DirectoryJsonnetDisabled reports that argocd-cm jsonnet.enable turns
	// Jsonnet off. A directory source then skips its .jsonnet files.
	DirectoryJsonnetDisabled bool

	// DirectoryMaxCombinedManifestsSize is the repo-server limit on the
	// combined size of the files of one directory source, in bytes. Zero
	// means no limit.
	DirectoryMaxCombinedManifestsSize int64

	// TrackManifest adds the Argo CD tracking metadata to one manifest, as
	// the repo-server's GenerateManifests does. The directory renderer calls
	// it on a copy of each manifest it emits, only to fail the source at the
	// same point as Argo CD when the metadata cannot be set. The renderer
	// output never carries the result. Nil skips the check.
	TrackManifest func(*unstructured.Unstructured) error

	// helmOutputForKustomize marks a helm render whose manifests become
	// kustomize input (helmCharts). kustomize reads helm's output with YAML
	// 1.2 rules, so it is decoded with those rather than Argo CD's.
	helmOutputForKustomize bool
}

type PluginConfig struct {
	Name       string
	Env        argoappv1.Env
	Parameters argoappv1.ApplicationSourcePluginParameters
}

type PluginRequest struct {
	AppName      string
	AppNamespace string
	Project      string
	Namespace    string
	Source       ResolvedSource
	Plugin       PluginConfig
	RefRoots     map[string]string
	RefSources   map[string]ResolvedSource
	KubeVersion  string
	APIVersions  []string
}

type PluginRenderer interface {
	RenderPlugin(ctx context.Context, request PluginRequest) ([]Manifest, []diagnostic.Diagnostic, error)
}

var ErrUnsupportedPlugin = errors.New("unsupported config management plugin")

type Manifest struct {
	SourceIndex                  int
	SourceName                   string
	Path                         string
	NamespaceBeforeNormalization string
	Object                       *unstructured.Unstructured
	// RootObject is the enclosing document when Object was flattened out of
	// a `kind: List` (manifest.Document.RootObject), nil when Object was the
	// document root. AVP compatibility reads its per-object context (kind and
	// annotations) from it, because argocd-vault-plugin processes a List as
	// one object. The render cache does not carry it: it stores manifests
	// after that pass.
	RootObject *unstructured.Unstructured
}

// NewDocumentManifest builds the Manifest for a decoded document, recording
// the List it was flattened out of as RootObject.
func NewDocumentManifest(doc manifest.Document) Manifest {
	rendered := Manifest{Path: doc.Path, Object: doc.Object}
	if doc.RootObject != nil && doc.RootObject != doc.Object {
		rendered.RootObject = doc.RootObject
	}
	return rendered
}

type Renderer interface {
	Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error)
}

type Provider interface {
	RenderSource(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error)
}
