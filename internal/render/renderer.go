package render

import (
	"context"
	"errors"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type ResolvedSource struct {
	RepoRoot       string
	Path           string
	Chart          string
	RepoURL        string
	TargetRevision string
}

type RenderOptions struct {
	AppName                      string
	AppNamespace                 string
	SourceIndex                  int
	SourceName                   string
	Project                      string
	Namespace                    string
	EnableAVPCompat              bool
	QuietAVPCompat               bool
	EnablePlugins                bool
	Plugin                       *PluginConfig
	KubeVersion                  string
	APIVersions                  []string
	BuildOptions                 []string
	RefRoots                     map[string]string
	RefSources                   map[string]ResolvedSource
	ReleaseName                  string
	ValuesObject                 map[string]any
	ValuesMergeMode              string
	ValueFiles                   []string
	ValueFilesBaseDir            string
	ValueFilesBoundaryRoot       string
	IgnoreMissingValueFiles      bool
	DirectoryRecurse             bool
	DirectoryInclude             string
	DirectoryExclude             string
	ChartCacheDir                string
	OfflineCharts                bool
	RefreshCharts                bool
	ChartCredentials             chart.ChartCredentials
	ChartAcquirer                chart.Acquirer
	OCIChartRepositories         map[string]bool
	RemoteResourceCacheDir       string
	OfflineRemoteResources       bool
	RefreshRemoteResources       bool
	RemoteResourceForbiddenRoots []string
	RemoteResourceCredentials    remote.Credentials
	RemoteResourceGitCredentials remote.GitCredentials
	RemoteResourceAcquirer       remote.Acquirer
	CacheEventRecorder           *cacheevent.Recorder
	IncludeCRDs                  bool
	IncludeCRDsSet               bool
	SkipHooks                    bool
	SkipTests                    bool
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
}

type PluginRenderer interface {
	RenderPlugin(ctx context.Context, request PluginRequest) ([]Manifest, []diagnostic.Diagnostic, error)
}

var ErrUnsupportedPlugin = errors.New("unsupported config management plugin")

type Manifest struct {
	SourceIndex int
	SourceName  string
	Path        string
	Object      *unstructured.Unstructured
}

type Renderer interface {
	Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error)
}

type Provider interface {
	RenderSource(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error)
}
