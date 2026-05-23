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
	"go.yaml.in/yaml/v4"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type HelmRenderer struct{}

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
	pathMap, err := helmChartPathMap(source.RepoRoot, chartPath, chart)
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

	fileValues, err := loadHelmValueFiles(source.RepoRoot, helmValueFilesBaseDir(source, opts), opts.RefRoots, opts.ValueFiles)
	if err != nil {
		return nil, nil, err
	}
	inputValues, err := mergeHelmValues(fileValues, cloneValues(opts.ValuesObject), opts.ValuesMergeMode)
	if err != nil {
		return nil, nil, err
	}
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

	return decodeHelmManifests(pathMap, chart, rendered, opts)
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
		docs, err := manifest.DecodeDocuments(path, bytes.NewReader([]byte(rendered[name])))
		if err != nil {
			return nil, nil, err
		}
		out = appendHelmDocuments(out, docs, opts)
	}
	return out, nil, nil
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
	for _, part := range strings.Split(hook, ",") {
		switch strings.TrimSpace(part) {
		case "test", "test-success", "test-failure":
			return true
		}
	}
	return false
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

func helmValueFilesBaseDir(source ResolvedSource, opts RenderOptions) string {
	if opts.ValueFilesBaseDir != "" {
		return opts.ValueFilesBaseDir
	}
	return source.Path
}

func loadHelmValueFiles(repoRoot, baseDir string, refRoots map[string]string, files []string) (map[string]any, error) {
	out := map[string]any{}
	for _, file := range files {
		root, resolved, err := resolveHelmValueFile(repoRoot, baseDir, refRoots, file)
		if err != nil {
			return nil, err
		}
		if err := rejectSymlinkedPath(root, resolved); err != nil {
			return nil, fmt.Errorf("helm value file %q: %w", file, err)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read helm value file %q: %w", file, err)
		}

		values := map[string]any{}
		if err := yaml.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("parse helm value file %q: %w", file, err)
		}
		if err := mergeHelmValueMap(out, values); err != nil {
			return nil, fmt.Errorf("merge helm value file %q: %w", file, err)
		}
	}
	return out, nil
}

func resolveHelmValueFile(repoRoot, baseDir string, refRoots map[string]string, file string) (string, string, error) {
	if strings.HasPrefix(file, "$") {
		ref, refPath, ok := strings.Cut(strings.TrimPrefix(file, "$"), "/")
		if !ok || ref == "" || refPath == "" {
			return "", "", fmt.Errorf("helm value file %q must use $ref/path syntax", file)
		}
		root, ok := refRoots[ref]
		if !ok || root == "" {
			return "", "", fmt.Errorf("helm value file %q references unknown ref %q", file, ref)
		}
		return resolveHelmValueFileUnderRoot(filepath.Clean(root), refPath, file)
	}

	cleanBase, err := cleanSourcePath(baseDir)
	if err != nil {
		return "", "", fmt.Errorf("helm value files base dir %q: %w", baseDir, err)
	}
	if filepath.IsAbs(file) {
		return "", "", fmt.Errorf("helm value file %q must be relative", file)
	}
	cleanFile := filepath.Clean(file)
	if cleanFile == "." {
		return "", "", fmt.Errorf("helm value file %q escapes value files root", file)
	}
	cleanPath, err := cleanSourcePath(filepath.Join(cleanBase, cleanFile))
	if err != nil {
		return "", "", fmt.Errorf("helm value file %q: %w", file, err)
	}
	return filepath.Clean(repoRoot), filepath.Join(repoRoot, cleanPath), nil
}

func resolveHelmValueFileUnderRoot(root, file, display string) (string, string, error) {
	if filepath.IsAbs(file) {
		return "", "", fmt.Errorf("helm value file %q must be relative", display)
	}
	clean := filepath.Clean(file)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("helm value file %q escapes value files root", display)
	}
	resolved := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("helm value file %q escapes value files root", display)
	}
	return root, resolved, nil
}

func mergeHelmValues(fileValues, inlineValues map[string]any, mode string) (map[string]any, error) {
	switch mode {
	case "", "override":
		out := cloneValues(fileValues)
		if err := mergeHelmValueMap(out, inlineValues); err != nil {
			return nil, err
		}
		return out, nil
	case "merge":
		out := cloneValues(inlineValues)
		if err := mergeHelmValueMap(out, fileValues); err != nil {
			return nil, err
		}
		return out, nil
	case "replace":
		if len(inlineValues) != 0 {
			return cloneValues(inlineValues), nil
		}
		return cloneValues(fileValues), nil
	default:
		return nil, fmt.Errorf("unsupported helm values merge mode %q", mode)
	}
}

func mergeHelmValueMap(dst, src map[string]any) error {
	for key, srcValue := range src {
		srcMap, srcIsMap := helmValueMap(srcValue)
		dstMap, dstIsMap := helmValueMap(dst[key])
		if srcIsMap && dstIsMap {
			if err := mergeHelmValueMap(dstMap, srcMap); err != nil {
				return err
			}
			dst[key] = dstMap
			continue
		}
		if srcIsMap {
			dst[key] = cloneValues(srcMap)
			continue
		}
		dst[key] = srcValue
	}
	return nil
}

func helmValueMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case common.Values:
		return map[string]any(typed), true
	default:
		return nil, false
	}
}

func cloneValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		if valueMap, ok := helmValueMap(value); ok {
			out[key] = cloneValues(valueMap)
			continue
		}
		out[key] = value
	}
	return out
}
