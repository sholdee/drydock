package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/engine"
)

type HelmRenderer struct{}

func (HelmRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(opts.ValueFiles) != 0 {
		return nil, nil, fmt.Errorf("helm value files are not supported yet")
	}

	chartPath, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	manifestPath, err := relativeManifestPath(source.RepoRoot, chartPath)
	if err != nil {
		return nil, nil, err
	}

	if err := validateHelmChartTree(chartPath); err != nil {
		return nil, nil, err
	}

	chart, err := loader.Load(chartPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load helm chart %s: %w", manifestPath, err)
	}

	releaseName := opts.ReleaseName
	if releaseName == "" {
		releaseName = opts.AppName
	}

	capabilities := common.DefaultCapabilities.Copy()
	if opts.KubeVersion != "" {
		kubeVersion, err := common.ParseKubeVersion(opts.KubeVersion)
		if err != nil {
			return nil, nil, fmt.Errorf("parse helm kube version %q: %w", opts.KubeVersion, err)
		}
		capabilities.KubeVersion = *kubeVersion
	}
	capabilities.APIVersions = append(capabilities.APIVersions, opts.APIVersions...)

	inputValues := cloneValues(opts.ValuesObject)
	if err := processHelmDependencies(chart, inputValues, manifestPath); err != nil {
		return nil, nil, err
	}

	values, err := chartutil.ToRenderValuesWithSchemaValidation(chart, inputValues, common.ReleaseOptions{
		Name:      releaseName,
		Namespace: opts.Namespace,
		Revision:  1,
		IsInstall: true,
		IsUpgrade: false,
	}, capabilities, true)
	if err != nil {
		return nil, nil, fmt.Errorf("helm render values %s: %w", manifestPath, err)
	}

	rendered, err := engine.Render(chart, values)
	if err != nil {
		return nil, nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}

	return decodeHelmManifests(source.RepoRoot, chartPath, chart, rendered)
}

type helmCRDProvider interface {
	CRDs() []*common.File
}

type helmCRDObjectProvider interface {
	CRDObjects() []chartv2.CRD
}

func validateHelmChartTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			return fmt.Errorf("helm chart path %q is a symlink", rel)
		}
		return nil
	})
}

func processHelmDependencies(chrt helmchart.Charter, values map[string]any, manifestPath string) error {
	chart, ok := chrt.(*chartv2.Chart)
	if !ok {
		return nil
	}
	if err := chartv2util.ProcessDependencies(chart, common.Values(values)); err != nil {
		return fmt.Errorf("helm chart dependencies %s: %w", manifestPath, err)
	}
	return nil
}

func decodeHelmManifests(repoRoot, chartPath string, chrt helmchart.Charter, rendered map[string]string) ([]Manifest, []diagnostic.Diagnostic, error) {
	var out []Manifest
	pathMap, err := helmChartPathMap(repoRoot, chartPath, chrt)
	if err != nil {
		return nil, nil, err
	}
	if provider, ok := chrt.(helmCRDObjectProvider); ok {
		for _, crd := range provider.CRDObjects() {
			path, err := helmManifestPath(pathMap, crd.Filename)
			if err != nil {
				return nil, nil, err
			}
			docs, err := manifest.DecodeDocuments(path, bytes.NewReader(crd.File.Data))
			if err != nil {
				return nil, nil, err
			}
			out = appendHelmDocuments(out, docs)
		}
	} else if provider, ok := chrt.(helmCRDProvider); ok {
		for _, crd := range provider.CRDs() {
			path, err := helmManifestPath(pathMap, crd.Name)
			if err != nil {
				return nil, nil, err
			}
			docs, err := manifest.DecodeDocuments(path, bytes.NewReader(crd.Data))
			if err != nil {
				return nil, nil, err
			}
			out = appendHelmDocuments(out, docs)
		}
	}

	names := make([]string, 0, len(rendered))
	for name := range rendered {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if path.Base(name) == "NOTES.txt" {
			continue
		}
		path, err := helmManifestPath(pathMap, name)
		if err != nil {
			return nil, nil, err
		}
		docs, err := manifest.DecodeDocuments(path, bytes.NewReader([]byte(rendered[name])))
		if err != nil {
			return nil, nil, err
		}
		out = appendHelmDocuments(out, docs)
	}
	return out, nil, nil
}

func appendHelmDocuments(out []Manifest, docs []manifest.Document) []Manifest {
	for _, doc := range docs {
		out = append(out, Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	return out
}

func helmChartPathMap(repoRoot, chartPath string, chrt helmchart.Charter) (map[string]string, error) {
	chartRel, err := relativeManifestPath(repoRoot, chartPath)
	if err != nil {
		return nil, err
	}
	root, err := helmchart.NewAccessor(chrt)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	collectHelmChartPaths(out, root, root.ChartFullPath(), filepath.ToSlash(chartRel))
	return out, nil
}

func collectHelmChartPaths(out map[string]string, accessor helmchart.Accessor, rootFullPath, sourceRoot string) {
	fullPath := path.Clean(accessor.ChartFullPath())
	sourcePath := sourceRoot
	if fullPath != rootFullPath {
		suffix := strings.TrimPrefix(fullPath, rootFullPath)
		suffix = strings.TrimPrefix(suffix, "/")
		sourcePath = path.Join(sourceRoot, suffix)
	}
	out[fullPath] = sourcePath

	for _, dependency := range accessor.Dependencies() {
		child, err := helmchart.NewAccessor(dependency)
		if err != nil {
			continue
		}
		collectHelmChartPaths(out, child, rootFullPath, sourceRoot)
	}
}

func helmManifestPath(pathMap map[string]string, name string) (string, error) {
	clean := path.Clean(name)
	if clean == "." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("helm manifest path %q escapes chart root", name)
	}

	prefixes := make([]string, 0, len(pathMap))
	for prefix := range pathMap {
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})

	for _, prefix := range prefixes {
		if clean != prefix && !strings.HasPrefix(clean, prefix+"/") {
			continue
		}
		suffix := strings.TrimPrefix(clean, prefix)
		suffix = strings.TrimPrefix(suffix, "/")
		return filepath.FromSlash(path.Join(pathMap[prefix], suffix)), nil
	}
	return "", fmt.Errorf("helm manifest path %q does not match a loaded chart", name)
}

func cloneValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
