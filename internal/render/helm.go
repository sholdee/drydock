package render

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/avpcompat"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/remote"
	"go.yaml.in/yaml/v4"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/engine"
	"helm.sh/helm/v4/pkg/strvals"
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
	for _, part := range strings.Split(hook, ",") {
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
	for key, value := range replacedMap {
		values[key] = value
	}
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

func helmValueFilesBaseDir(source ResolvedSource, opts RenderOptions) string {
	if opts.ValueFilesBaseDir != "" {
		return opts.ValueFilesBaseDir
	}
	return source.Path
}

func helmValueFilesBoundaryRoot(source ResolvedSource, opts RenderOptions) string {
	if opts.ValueFilesBoundaryRoot != "" {
		return opts.ValueFilesBoundaryRoot
	}
	return helmValueFilesBaseDir(source, opts)
}

func shouldLoadHelmValueFiles(mode string, inlineValues map[string]any) (bool, error) {
	switch mode {
	case "", "override", "merge":
		return true, nil
	case "replace":
		return len(inlineValues) == 0, nil
	default:
		return false, fmt.Errorf("unsupported helm values merge mode %q", mode)
	}
}

func applyHelmParameters(ctx context.Context, source ResolvedSource, opts RenderOptions, values map[string]any) error {
	for _, parameter := range opts.HelmParameters {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return fmt.Errorf("helm parameter name is required")
		}
		rawValue := parameter.Value
		value := opts.ArgoEnv.Envsubst(rawValue)
		expression := name + "=" + cleanHelmSetParameter(value)
		var err error
		if parameter.ForceString {
			err = strvals.ParseIntoString(expression, values)
		} else {
			err = strvals.ParseInto(expression, values)
		}
		if err != nil {
			return fmt.Errorf("helm parameter %q failed to parse: %s", name, redactHelmParameterError(err.Error(), rawValue, value))
		}
	}

	if len(opts.HelmFileParameters) == 0 {
		return nil
	}
	reader := helmFileParameterReader(ctx, source, opts)
	for _, parameter := range opts.HelmFileParameters {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return fmt.Errorf("helm file parameter name is required")
		}
		rawPath := parameter.Path
		filePath := envsubstHelmValueFilePath(rawPath, opts, opts.RefRoots)
		if err := strvals.ParseIntoFile(name+"="+cleanHelmSetParameter(filePath), values, reader); err != nil {
			return fmt.Errorf("helm file parameter %q failed to parse: %s", name, redactHelmParameterError(err.Error(), rawPath, filePath))
		}
	}
	return nil
}

func helmFileParameterReader(ctx context.Context, source ResolvedSource, opts RenderOptions) strvals.RunesValueReader {
	return func(input []rune) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := string(input)
		if isRemoteHelmValueFile(file) {
			return nil, fmt.Errorf("helm file parameter %q must reference a local or $ref file", remote.RedactURL(file))
		}
		root, resolved, err := resolveHelmValueFile(source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts), opts.RefRoots, file)
		if err != nil {
			return nil, err
		}
		if err := rejectSymlinkedPath(root, resolved); err != nil {
			return nil, fmt.Errorf("helm file parameter %q: %w", file, err)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read helm file parameter %q: %w", file, err)
		}
		return string(data), nil
	}
}

func redactHelmParameterError(message string, sensitiveValues ...string) string {
	for _, value := range sensitiveValues {
		if value == "" {
			continue
		}
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return message
}

func cleanHelmSetParameter(value string) string {
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		return value
	}
	return replaceRuneWithLookbehind(value, ',', `\,`, '\\')
}

func replaceRuneWithLookbehind(value string, old rune, replacement string, lookbehind rune) string {
	var out strings.Builder
	var previous rune
	for _, current := range value {
		if current == old {
			if previous != lookbehind {
				out.WriteString(replacement)
			} else {
				out.WriteRune(current)
			}
		} else {
			out.WriteRune(current)
		}
		previous = current
	}
	return out.String()
}

func loadHelmValueFiles(ctx context.Context, repoRoot, baseDir, boundaryDir string, refRoots map[string]string, files []string, ignoreMissing bool, opts RenderOptions) (map[string]any, error) {
	loader := helmValueFileLoader{
		ctx:           ctx,
		repoRoot:      repoRoot,
		baseDir:       baseDir,
		boundaryDir:   boundaryDir,
		refRoots:      refRoots,
		ignoreMissing: ignoreMissing,
		opts:          opts,
		explicit:      map[string]struct{}{},
		globSeen:      map[string]struct{}{},
	}
	if err := loader.collectExplicitIdentities(files); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, file := range files {
		valuesList, err := loader.load(file)
		if err != nil {
			return nil, err
		}
		for _, values := range valuesList {
			if err := mergeHelmValueMap(out, values.values); err != nil {
				return nil, fmt.Errorf("merge helm value file %q: %w", values.display, err)
			}
		}
	}
	return out, nil
}

type helmValueFileLoader struct {
	ctx           context.Context
	repoRoot      string
	baseDir       string
	boundaryDir   string
	refRoots      map[string]string
	ignoreMissing bool
	opts          RenderOptions
	explicit      map[string]struct{}
	globSeen      map[string]struct{}
}

type loadedHelmValueFile struct {
	display string
	values  map[string]any
}

func (l *helmValueFileLoader) collectExplicitIdentities(files []string) error {
	for _, raw := range files {
		file := envsubstHelmValueFilePath(raw, l.opts, l.refRoots)
		if hasHelmValueGlob(file) || isRemoteHelmValueFile(file) {
			continue
		}
		root, resolved, err := resolveHelmValueFile(l.repoRoot, l.baseDir, l.boundaryDir, l.refRoots, file)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(resolved); err != nil {
			if l.ignoreMissing && os.IsNotExist(err) {
				continue
			}
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		identity, err := localHelmValueFileIdentity(root, resolved, file)
		if err != nil {
			return err
		}
		l.explicit[identity] = struct{}{}
	}
	return nil
}

func (l *helmValueFileLoader) load(raw string) ([]loadedHelmValueFile, error) {
	file := envsubstHelmValueFilePath(raw, l.opts, l.refRoots)
	if isRemoteHelmValueFile(file) {
		values, err := l.loadRemote(file)
		if err != nil {
			return nil, err
		}
		return []loadedHelmValueFile{values}, nil
	}
	if hasHelmValueGlob(file) {
		return l.loadGlob(file)
	}
	values, ok, err := l.loadLocal(file, file)
	if err != nil || !ok {
		return nil, err
	}
	return []loadedHelmValueFile{values}, nil
}

func (l *helmValueFileLoader) loadGlob(pattern string) ([]loadedHelmValueFile, error) {
	root, resolvedPattern, err := resolveHelmValueFile(l.repoRoot, l.baseDir, l.boundaryDir, l.refRoots, pattern)
	if err != nil {
		return nil, err
	}
	matches, err := doublestar.FilepathGlob(resolvedPattern)
	if err != nil {
		return nil, fmt.Errorf("helm value file glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		if l.ignoreMissing {
			return nil, nil
		}
		return nil, fmt.Errorf("helm value file glob %q matched no files", pattern)
	}
	out := make([]loadedHelmValueFile, 0, len(matches))
	for _, match := range matches {
		identity, err := localHelmValueFileIdentity(root, match, pattern)
		if err != nil {
			return nil, err
		}
		if _, ok := l.explicit[identity]; ok {
			continue
		}
		if _, ok := l.globSeen[identity]; ok {
			continue
		}
		l.globSeen[identity] = struct{}{}
		display := displayHelmValueGlobMatch(root, match, pattern)
		values, ok, err := l.loadLocalResolved(root, match, display)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, values)
		}
	}
	return out, nil
}

func (l *helmValueFileLoader) loadLocal(file, display string) (loadedHelmValueFile, bool, error) {
	root, resolved, err := resolveHelmValueFile(l.repoRoot, l.baseDir, l.boundaryDir, l.refRoots, file)
	if err != nil {
		return loadedHelmValueFile{}, false, err
	}
	return l.loadLocalResolved(root, resolved, display)
}

func (l *helmValueFileLoader) loadLocalResolved(root, resolved, display string) (loadedHelmValueFile, bool, error) {
	if err := rejectSymlinkedPath(root, resolved); err != nil {
		return loadedHelmValueFile{}, false, fmt.Errorf("helm value file %q: %w", display, err)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		if l.ignoreMissing && os.IsNotExist(err) {
			return loadedHelmValueFile{}, false, nil
		}
		return loadedHelmValueFile{}, false, fmt.Errorf("read helm value file %q: %w", display, err)
	}
	values, err := parseHelmValueFile(display, data)
	if err != nil {
		return loadedHelmValueFile{}, false, err
	}
	return loadedHelmValueFile{display: display, values: values}, true, nil
}

func (l *helmValueFileLoader) loadRemote(file string) (loadedHelmValueFile, error) {
	parsed, err := url.Parse(file)
	if err != nil {
		return loadedHelmValueFile{}, fmt.Errorf("parse helm value file URL %q: %w", remote.RedactURL(file), err)
	}
	if !helmValueFileSchemeAllowed(parsed.Scheme, l.opts) {
		return loadedHelmValueFile{}, fmt.Errorf("helm value file URL scheme %q is not allowed", parsed.Scheme)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return loadedHelmValueFile{}, fmt.Errorf("helm value file URL scheme %q is configured but not supported by drydock", parsed.Scheme)
	}
	acquirer := l.opts.RemoteResourceAcquirer
	if acquirer == nil {
		acquirer = remote.DefaultAcquirer{}
	}
	forbiddenRoots := l.opts.RemoteResourceForbiddenRoots
	if len(forbiddenRoots) == 0 {
		forbiddenRoots = []string{l.repoRoot}
	}
	request := remote.Request{URL: file, Kind: remote.RequestHTTPFile}
	acquired, err := acquirer.Acquire(l.ctx, request, remote.Options{
		CacheDir:       l.opts.RemoteResourceCacheDir,
		Offline:        l.opts.OfflineRemoteResources,
		Refresh:        l.opts.RefreshRemoteResources,
		ForbiddenRoots: forbiddenRoots,
		Credentials:    l.opts.RemoteResourceCredentials,
		GitCredentials: l.opts.RemoteResourceGitCredentials,
	})
	if err != nil {
		recordRemoteCacheEvent(l.opts, request, err, remote.Result{})
		return loadedHelmValueFile{}, fmt.Errorf("acquire helm value file %s: %s", remote.RedactURL(file), remote.RedactCredentialError(err.Error(), l.opts.RemoteResourceCredentials, l.opts.RemoteResourceGitCredentials))
	}
	release := acquired.Release
	defer func() {
		if release != nil {
			release()
		}
	}()
	recordRemoteCacheEvent(l.opts, request, nil, acquired)
	if err := rejectSymlinkedPath(filepath.Dir(acquired.Path), acquired.Path); err != nil {
		return loadedHelmValueFile{}, fmt.Errorf("helm value file %s: %w", remote.RedactURL(file), err)
	}
	data, err := os.ReadFile(acquired.Path)
	if err != nil {
		return loadedHelmValueFile{}, fmt.Errorf("read helm value file %s: %w", remote.RedactURL(file), err)
	}
	values, err := parseHelmValueFile(remote.RedactURL(file), data)
	if err != nil {
		return loadedHelmValueFile{}, err
	}
	return loadedHelmValueFile{display: remote.RedactURL(file), values: values}, nil
}

func parseHelmValueFile(display string, data []byte) (map[string]any, error) {
	values := map[string]any{}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("helm value file %q must be a YAML mapping: %w", display, err)
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

func hasHelmValueGlob(file string) bool {
	return strings.ContainsAny(file, "*?[")
}

func isRemoteHelmValueFile(file string) bool {
	parsed, err := url.Parse(file)
	return err == nil && parsed.IsAbs() && parsed.Scheme != ""
}

func envsubstHelmValueFilePath(raw string, opts RenderOptions, refRoots map[string]string) string {
	ref, rest, ok := helmValueFileRefPrefix(raw, refRoots)
	if !ok {
		return opts.ArgoEnv.Envsubst(raw)
	}
	return ref + opts.ArgoEnv.Envsubst(rest)
}

func helmValueFileRefPrefix(raw string, refRoots map[string]string) (string, string, bool) {
	if !strings.HasPrefix(raw, "$") {
		return "", "", false
	}
	ref, rest, ok := strings.Cut(raw, "/")
	if !ok || ref == "" || rest == "" {
		return "", "", false
	}
	if _, ok := refRoots[ref]; ok {
		return ref, "/" + rest, true
	}
	if _, ok := refRoots[strings.TrimPrefix(ref, "$")]; ok {
		return ref, "/" + rest, true
	}
	return "", "", false
}

func localHelmValueFileIdentity(root, resolved, display string) (string, error) {
	if err := rejectSymlinkedPath(root, resolved); err != nil {
		return "", fmt.Errorf("helm value file %q: %w", display, err)
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

func displayHelmValueGlobMatch(root, match, pattern string) string {
	rel, err := filepath.Rel(root, match)
	if err != nil {
		return pattern
	}
	return filepath.ToSlash(rel)
}

func helmValueFileSchemeAllowed(scheme string, opts RenderOptions) bool {
	allowed := opts.HelmValueFileSchemes
	if !opts.HelmValueFileSchemesSet {
		allowed = []string{"https", "http"}
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), scheme) {
			return true
		}
	}
	return false
}

func resolveHelmValueFile(repoRoot, baseDir, boundaryDir string, refRoots map[string]string, file string) (string, string, error) {
	if strings.HasPrefix(file, "$") {
		ref, refPath, ok := strings.Cut(strings.TrimPrefix(file, "$"), "/")
		if !ok || ref == "" || refPath == "" {
			return "", "", fmt.Errorf("helm value file %q must use $ref/path syntax", file)
		}
		refKey := "$" + ref
		root, ok := refRoots[refKey]
		if !ok || root == "" {
			root, ok = refRoots[ref]
		}
		if !ok || root == "" {
			return "", "", fmt.Errorf("helm value file %q references unknown ref %q", file, refKey)
		}
		cleanRoot := filepath.Clean(root)
		if err := rejectHelmRefRootSymlink(cleanRoot); err != nil {
			return "", "", fmt.Errorf("helm value file %q ref root %q: %w", file, refKey, err)
		}
		return resolveHelmValueFileUnderRoot(cleanRoot, refPath, file)
	}

	cleanBase, err := cleanSourcePath(baseDir)
	if err != nil {
		return "", "", fmt.Errorf("helm value files base dir %q: %w", baseDir, err)
	}
	if err := rejectHelmValueBaseDirSymlinkComponents(repoRoot, cleanBase); err != nil {
		return "", "", fmt.Errorf("helm value files base dir %q: %w", baseDir, err)
	}
	cleanBoundary, err := cleanSourcePath(boundaryDir)
	if err != nil {
		return "", "", fmt.Errorf("helm value files boundary root %q: %w", boundaryDir, err)
	}
	if err := rejectHelmValueBaseDirSymlinkComponents(repoRoot, cleanBoundary); err != nil {
		return "", "", fmt.Errorf("helm value files boundary root %q: %w", boundaryDir, err)
	}
	baseRoot := filepath.Join(repoRoot, cleanBase)
	boundaryRoot := filepath.Join(repoRoot, cleanBoundary)
	if cleanBoundary == cleanBase {
		return resolveHelmValueFileUnderRoot(baseRoot, file, file)
	}
	return resolveHelmValueFileUnderBoundary(baseRoot, boundaryRoot, file, file)
}

func rejectHelmRefRootSymlink(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is a symlink", root)
	}
	return nil
}

func rejectHelmValueBaseDirSymlinkComponents(repoRoot, sourcePath string) error {
	if sourcePath == "." {
		return nil
	}

	current := filepath.Clean(repoRoot)
	for _, component := range strings.Split(sourcePath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q includes symlink component %q", sourcePath, component)
		}
	}
	return nil
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

func resolveHelmValueFileUnderBoundary(baseRoot, boundaryRoot, file, display string) (string, string, error) {
	if filepath.IsAbs(file) {
		return "", "", fmt.Errorf("helm value file %q must be relative", display)
	}
	clean := filepath.Clean(file)
	if strings.TrimSpace(file) == "" || clean == "." {
		return "", "", fmt.Errorf("helm value file %q escapes value files boundary", display)
	}
	resolved := filepath.Clean(filepath.Join(baseRoot, clean))
	rel, err := filepath.Rel(boundaryRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("helm value file %q escapes value files boundary", display)
	}
	return boundaryRoot, resolved, nil
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
