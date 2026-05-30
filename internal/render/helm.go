package render

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/avpcompat"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type HelmRenderer struct{}

//nolint:gocyclo // Coordinates Helm loading, values, capabilities, and manifest decoding in render order.
func (HelmRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
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
	chart, cleanup, err := prepareHelmDependencyWorkspace(ctx, chartPath, manifestPath, chart, opts)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return nil, nil, err
	}
	pathMap, err := helmChartPathMap(manifestPath, chart)
	if err != nil {
		return nil, nil, err
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

	inlineValues := cloneValues(opts.ValuesObject)
	loadValueFiles, err := shouldLoadHelmValueFiles(opts.ValuesMergeMode, inlineValues)
	if err != nil {
		return nil, nil, err
	}
	fileValues := map[string]any{}
	if loadValueFiles {
		fileValues, err = loadHelmValueFiles(ctx, source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts), opts.RefRoots, opts.ValueFiles, opts.IgnoreMissingValueFiles, opts)
		if err != nil {
			return nil, nil, err
		}
	}
	inputValues, err := mergeHelmValues(fileValues, inlineValues, opts.ValuesMergeMode)
	if err != nil {
		return nil, nil, err
	}
	if err := applyHelmParameters(ctx, source, opts, inputValues); err != nil {
		return nil, nil, err
	}
	avpDiags := applyAVPCompatToHelmValues(inputValues, opts)
	if err := processHelmDependencies(chart, inputValues, manifestPath); err != nil {
		return nil, nil, err
	}

	values, err := chartutil.ToRenderValuesWithSchemaValidation(chart, inputValues, common.ReleaseOptions{
		Name:      releaseName,
		Namespace: opts.Namespace,
		Revision:  1,
		IsInstall: true,
		IsUpgrade: false,
	}, capabilities, opts.SkipSchemaValidation)
	if err != nil {
		return nil, nil, fmt.Errorf("helm render values %s: %w", manifestPath, err)
	}

	rendered, err := engine.Render(chart, values)
	if err != nil {
		return nil, nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}

	manifests, diags, err := decodeHelmManifests(pathMap, chart, rendered, opts)
	diags = append(avpDiags, diags...)
	return manifests, diags, err
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

func decodeHelmManifests(pathMap map[string]string, chrt helmchart.Charter, rendered map[string]string, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	var out []Manifest
	if shouldIncludeCRDs(opts) {
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
				out = appendHelmDocuments(out, docs, opts)
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
				out = appendHelmDocuments(out, docs, opts)
			}
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
		docs, err := decodeRenderedHelmTemplate(path, rendered[name])
		if err != nil {
			return nil, nil, err
		}
		out = appendHelmDocuments(out, docs, opts)
	}
	return out, nil, nil
}

func decodeRenderedHelmTemplate(path, rendered string) ([]manifest.Document, error) {
	var out []manifest.Document
	for _, document := range splitHelmRenderedManifests(rendered) {
		docs, err := manifest.DecodeDocuments(path, strings.NewReader(document))
		if err != nil {
			return nil, err
		}
		out = append(out, docs...)
	}
	return out, nil
}

func appendHelmDocuments(out []Manifest, docs []manifest.Document, opts RenderOptions) []Manifest {
	for _, doc := range docs {
		if shouldSkipHelmDocument(doc.Object, opts) {
			continue
		}
		out = append(out, Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	return out
}

func shouldIncludeCRDs(opts RenderOptions) bool {
	return !opts.IncludeCRDsSet || opts.IncludeCRDs
}

func shouldSkipHelmDocument(obj *unstructured.Unstructured, opts RenderOptions) bool {
	if obj == nil {
		return false
	}
	hook := obj.GetAnnotations()["helm.sh/hook"]
	if hook == "" {
		return false
	}
	if opts.SkipHooks {
		return true
	}
	if !opts.SkipTests {
		return false
	}
	for part := range strings.SplitSeq(hook, ",") {
		switch strings.TrimSpace(part) {
		case "test", "test-success", "test-failure":
			return true
		}
	}
	return false
}

func applyAVPCompatToHelmValues(values map[string]any, opts RenderOptions) []diagnostic.Diagnostic {
	if !opts.EnableAVPCompat {
		return nil
	}
	replaced, changed := avpcompat.ReplaceValue(values)
	if !changed {
		return nil
	}
	replacedMap, ok := replaced.(map[string]any)
	if !ok {
		return nil
	}
	for key := range values {
		delete(values, key)
	}
	maps.Copy(values, replacedMap)
	if opts.QuietAVPCompat {
		return nil
	}
	return []diagnostic.Diagnostic{{
		Code:     "plugin.avp-compat-substituted",
		Severity: diagnostic.SeverityWarning,
		Category: "plugin",
		Message:  "argocd-vault-plugin placeholders were replaced with deterministic redacted values",
	}}
}

func helmChartPathMap(chartRel string, chrt helmchart.Charter) (map[string]string, error) {
	root, err := helmchart.NewAccessor(chrt)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	collectHelmChartPaths(out, chrt, root.ChartFullPath(), filepath.ToSlash(chartRel))
	return out, nil
}

func collectHelmChartPaths(out map[string]string, chrt helmchart.Charter, renderedFullPath, sourceRoot string) {
	accessor, err := helmchart.NewAccessor(chrt)
	if err != nil {
		return
	}
	fullPath := path.Clean(renderedFullPath)
	out[fullPath] = sourceRoot

	type childChart struct {
		chart    helmchart.Charter
		accessor helmchart.Accessor
	}
	children := make([]childChart, 0, len(accessor.Dependencies()))
	for _, dependency := range accessor.Dependencies() {
		child, err := helmchart.NewAccessor(dependency)
		if err != nil {
			continue
		}
		children = append(children, childChart{chart: dependency, accessor: child})
	}

	usedRenderedPaths := make(map[string]struct{})
	for _, dependencyPath := range helmDependencySourcePaths(accessor, sourceRoot) {
		for _, child := range children {
			if child.accessor.Name() != dependencyPath.sourceName {
				continue
			}
			childRenderedPath := path.Join(fullPath, "charts", dependencyPath.renderedName)
			childSourcePath := dependencyPath.sourcePath
			if childSourcePath == "" {
				childSourcePath = path.Join(sourceRoot, "charts", dependencyPath.sourceName)
			}
			collectHelmChartPaths(out, child.chart, childRenderedPath, childSourcePath)
			usedRenderedPaths[childRenderedPath] = struct{}{}
			break
		}
	}

	for _, child := range children {
		childRenderedPath := path.Join(fullPath, "charts", child.accessor.Name())
		if _, ok := usedRenderedPaths[childRenderedPath]; ok {
			continue
		}
		collectHelmChartPaths(out, child.chart, childRenderedPath, path.Join(sourceRoot, "charts", child.accessor.Name()))
	}
}

type helmDependencyPath struct {
	sourceName   string
	renderedName string
	sourcePath   string
}

func helmDependencySourcePaths(accessor helmchart.Accessor, parentSourcePath string) []helmDependencyPath {
	out := make([]helmDependencyPath, 0, len(accessor.MetaDependencies()))
	for _, dependency := range accessor.MetaDependencies() {
		dependencyAccessor, err := helmchart.NewDependencyAccessor(dependency)
		if err != nil {
			continue
		}
		sourceName := dependencyAccessor.Name()
		renderedName := dependencyAccessor.Name()
		if alias := dependencyAccessor.Alias(); alias != "" {
			renderedName = alias
		}
		sourcePath := helmDependencySourcePath(parentSourcePath, dependency)
		out = append(out, helmDependencyPath{
			sourceName:   sourceName,
			renderedName: renderedName,
			sourcePath:   sourcePath,
		})
	}
	return out
}

func helmDependencySourcePath(parentSourcePath string, dependency helmchart.Dependency) string {
	switch dep := dependency.(type) {
	case chartv2.Dependency:
		return helmV2DependencySourcePath(parentSourcePath, &dep)
	case *chartv2.Dependency:
		return helmV2DependencySourcePath(parentSourcePath, dep)
	default:
		return ""
	}
}

func helmV2DependencySourcePath(parentSourcePath string, dependency *chartv2.Dependency) string {
	if !strings.HasPrefix(dependency.Repository, "file://") {
		return path.Join(parentSourcePath, "charts", dependency.Name)
	}
	sourcePath := path.Clean(strings.TrimPrefix(dependency.Repository, "file://"))
	if sourcePath == "." || path.IsAbs(sourcePath) || strings.HasPrefix(sourcePath, "../") {
		return ""
	}
	return path.Join(parentSourcePath, sourcePath)
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
