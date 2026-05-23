package render

import (
	"context"

	"github.com/home-operations/argocd-local/internal/chart"
	"github.com/home-operations/argocd-local/internal/diagnostic"
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
	AppName                 string
	Namespace               string
	KubeVersion             string
	APIVersions             []string
	BuildOptions            []string
	RefRoots                map[string]string
	ReleaseName             string
	ValuesObject            map[string]any
	ValuesMergeMode         string
	ValueFiles              []string
	ValueFilesBaseDir       string
	IgnoreMissingValueFiles bool
	ChartCacheDir           string
	OfflineCharts           bool
	RefreshCharts           bool
	ChartAcquirer           chart.Acquirer
	IncludeCRDs             bool
	IncludeCRDsSet          bool
	SkipHooks               bool
	SkipTests               bool
}

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
