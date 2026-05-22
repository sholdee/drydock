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
	if provider, ok := chrt.(helmCRDProvider); ok {
		for _, crd := range provider.CRDs() {
			path, err := helmManifestPath(repoRoot, chartPath, crd.Name)
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
		path, err := helmManifestPath(repoRoot, chartPath, name)
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

func helmManifestPath(repoRoot, chartPath, name string) (string, error) {
	clean := path.Clean(name)
	if clean == "." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("helm manifest path %q escapes chart root", name)
	}

	chartRel, err := relativeManifestPath(repoRoot, chartPath)
	if err != nil {
		return "", err
	}
	chartRelSlash := filepath.ToSlash(chartRel)
	if clean == chartRelSlash || strings.HasPrefix(clean, chartRelSlash+"/") {
		return filepath.FromSlash(clean), nil
	}
	return filepath.Join(chartRel, filepath.FromSlash(clean)), nil
}

func cloneValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
