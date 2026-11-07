package render

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/sholdee/drydock/internal/avpcompat"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/manifestyaml"
	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/engine"
	release "helm.sh/helm/v4/pkg/release/v1"
	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

type HelmRenderer struct{}

func (HelmRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	if err := helmRenderStartError(ctx, opts); err != nil {
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

	capabilities, err := helmCapabilities(opts)
	if err != nil {
		return nil, nil, err
	}
	// The repo-server resolves the value file and file parameter paths
	// before it runs helm, and helm reads the values before it loads the
	// chart (helmTemplate, reposerver/repository/repository.go:1292-1410 at
	// v3.6.1, and helm pkg/cmd/install.go runInstall), so path and value
	// errors come before chart errors. The helm version check and the
	// release name come between the paths and the values.
	inputValues, releaseName, avpDiags, err := assembleHelmInputValues(ctx, source, manifestPath, opts)
	if err != nil {
		return nil, nil, err
	}

	chart, err := loadInstallableHelmChart(chartPath, manifestPath, opts)
	if err != nil {
		return nil, nil, err
	}
	chart, cleanup, err := prepareHelmDependencyWorkspace(ctx, chartPath, manifestPath, chart, opts)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return nil, nil, err
	}
	v2chart, err := checkHelmChartRelease(chart, releaseName)
	if err != nil {
		return nil, nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}
	pathMap, err := helmChartPathMap(manifestPath, chart)
	if err != nil {
		return nil, nil, err
	}
	if err := processHelmDependencies(chart, inputValues, manifestPath); err != nil {
		return nil, nil, err
	}
	values, err := helmTemplateValues(chart, inputValues, capabilities, releaseName, opts, manifestPath)
	if err != nil {
		return nil, nil, err
	}
	if err := checkHelmChartKubeVersion(v2chart, capabilities, opts.KubeVersion); err != nil {
		return nil, nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}

	rendered, err := engine.Render(chart, values)
	if err != nil {
		return nil, nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}

	manifests, diags, err := decodeHelmManifests(manifestPath, pathMap, chart, rendered, opts)
	diags = append(avpDiags, diags...)
	return manifests, diags, err
}

func helmCapabilities(opts RenderOptions) (*common.Capabilities, error) {
	capabilities := common.DefaultCapabilities.Copy()
	if opts.KubeVersion != "" {
		kubeVersion, err := common.ParseKubeVersion(opts.KubeVersion)
		if err != nil {
			return nil, fmt.Errorf("parse helm kube version %q: %w", opts.KubeVersion, err)
		}
		capabilities.KubeVersion = *kubeVersion
	}
	capabilities.APIVersions = append(capabilities.APIVersions, opts.APIVersions...)
	return capabilities, nil
}

// assembleHelmInputValues resolves the value file and file parameter
// paths, in that order, and then reads the value files and applies the
// inline values and parameters, as the repo-server and helm do.
//
// Between the paths and the values, it checks spec.source.helm.version, as
// the repo-server does when it makes the helm command (NewHelmApp,
// repository.go:1414 at v3.6.1), and returns the release name, which helm
// template works out before it reads the values (NameAndChart, runInstall
// in pkg/cmd/install.go at helm v4.3.1).
func assembleHelmInputValues(ctx context.Context, source ResolvedSource, manifestPath string, opts RenderOptions) (map[string]any, string, []diagnostic.Diagnostic, error) {
	inlineValues := cloneValues(opts.ValuesObject)
	loadValueFiles, err := shouldLoadHelmValueFiles(opts.ValuesMergeMode, inlineValues)
	if err != nil {
		return nil, "", nil, err
	}
	loader := newHelmValueFileLoader(ctx, source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts), opts.RefRoots, opts.IgnoreMissingValueFiles, opts, parseHelmValueFile)
	var plans []plannedHelmValueFile
	if loadValueFiles {
		if plans, err = loader.planAll(opts.ValueFiles); err != nil {
			return nil, "", nil, err
		}
	}
	targets, err := resolveHelmFileParameters(source, opts)
	if err != nil {
		return nil, "", nil, err
	}
	if err := checkHelmCommandVersion(opts.HelmVersion); err != nil {
		return nil, "", nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}
	releaseName, err := helmReleaseName(opts)
	if err != nil {
		return nil, "", nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}
	fileValues, err := loader.readAll(plans)
	if err != nil {
		return nil, "", nil, err
	}
	inputValues, err := mergeHelmValues(fileValues, inlineValues, opts.ValuesMergeMode)
	if err != nil {
		return nil, "", nil, err
	}
	if err := applyHelmParameters(ctx, opts, targets, inputValues); err != nil {
		return nil, "", nil, err
	}
	return inputValues, releaseName, applyAVPCompatToHelmValues(inputValues, opts), nil
}

func helmTemplateValues(chart helmchart.Charter, inputValues map[string]any, capabilities *common.Capabilities, releaseName string, opts RenderOptions, manifestPath string) (map[string]any, error) {
	chartAccessor, err := helmchart.NewAccessor(chart)
	if err != nil {
		return nil, fmt.Errorf("helm render values %s: %w", manifestPath, err)
	}
	// CoalesceValues strips null chart defaults, matching the helm v4 CLI that Argo CD's
	// repo-server bundles since v3.5: guarded keys ({{- if hasKey .Values "x" }}) with null
	// defaults vanish on both sides. Argo CD v3.4 and earlier bundled helm v3, which retained
	// them — the nil-preserving MergeValues sibling matched that era.
	mergedValues, err := chartutil.CoalesceValues(chart, inputValues)
	if err != nil {
		return nil, fmt.Errorf("helm render values %s: %w", manifestPath, err)
	}
	if !opts.SkipSchemaValidation {
		if err := chartutil.ValidateAgainstSchema(chart, mergedValues); err != nil {
			return nil, fmt.Errorf("helm render values %s: values don't meet the chart schema(s): %w", manifestPath, err)
		}
	}
	return map[string]any{
		"Chart":        chartAccessor.MetadataAsMap(),
		"Capabilities": capabilities,
		"Release": map[string]any{
			"Name": releaseName, "Namespace": helmReleaseNamespace(opts), "IsUpgrade": false,
			"IsInstall": true, "Revision": 1, "Service": "Helm",
		},
		"Values": mergedValues,
	}, nil
}

type helmCRDProvider interface {
	CRDs() []*common.File
}

type helmCRDObjectProvider interface {
	CRDObjects() []chartv2.CRD
}

// validateHelmChartTree rejects symlinks anywhere in the chart directory and
// reports whether the tree holds a .git entry (case-insensitively, at any
// depth), which it does not descend into. Helm's directory loader reads every
// file of the chart into .Files — its default ignore rules spare only
// templates/.?*, and .helmignore is the chart author's — so a chart that is a
// repository root (path: .) would expose .git/config, where actions/checkout
// persists its token, to {{ .Files.Get }} and .Files.Glob. Such a chart is
// loaded from a copy without .git (loadHelmChartDir).
func validateHelmChartTree(root string) (bool, error) {
	hasGit := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if strings.EqualFold(entry.Name(), ".git") {
			hasGit = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
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
	return hasGit, err
}

// decodeHelmManifests reads the rendered templates as Argo CD v3.6.1 reads
// the output of helm template. See helmTemplateStream for what helm template
// writes. When the manifests are kustomize input (helmCharts), each document
// is decoded with the YAML 1.2 rules kustomize uses. Otherwise the stream
// goes through the same stages as the repo-server: kube.SplitYAML
// (reposerver/repository/repository.go:1444), then the GenerateManifests loop
// and the controller kind check (see DecodeGeneratedOutput).
//
// The stages run in the order of helm template output, so the first failure
// is the one Argo CD reports. The manifests come out in the same order, so
// the Application layer keeps the same copy of a repeated resource as the
// Argo CD controller, which keeps the last one (NormalizeTargetObjects,
// controller/state.go:575-623 at v3.6.1).
func decodeHelmManifests(manifestPath string, pathMap map[string]string, chrt helmchart.Charter, rendered map[string]string, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	chunks, err := helmTemplateStream(manifestPath, pathMap, chrt, rendered, opts)
	if err != nil {
		return nil, nil, err
	}
	if opts.helmOutputForKustomize {
		var targets []generatedTarget
		for _, chunk := range chunks {
			// helm template prints a line break after each document.
			docs, err := manifest.DecodeYAML12Documents(chunk.path, strings.NewReader(chunk.content+"\n"))
			if err != nil {
				return nil, nil, err
			}
			for _, doc := range docs {
				targets = append(targets, generatedTarget{manifest: NewDocumentManifest(doc)})
			}
		}
		return targetManifests(targets), nil, nil
	}
	if !opts.KeepOutputLineBreak {
		trimCommandOutput(chunks)
	}
	var docs []generatedDocument
	for _, chunk := range chunks {
		docs, err = splitGeneratedDocuments(docs, chunk.path, utilyaml.NewYAMLToJSONDecoder(strings.NewReader(chunk.text)), chunk.firstIndex)
		if err != nil {
			return nil, nil, err
		}
	}
	manifests, err := generatedManifests(docs, opts.TrackManifest)
	if err != nil {
		return nil, nil, err
	}
	return manifests, nil, nil
}

// helmStreamChunk is one "---\n# Source: <file>\n<document>\n" part of the
// output of helm template.
type helmStreamChunk struct {
	// path is the repository path of the template or CRD file.
	path string
	// text is the chunk as helm template writes it.
	text string
	// content is the document, or the whole CRD file, without the line
	// break that helm template prints after it.
	content string
	// firstIndex is the index of the first document of the chunk in its
	// file. A template chunk holds one document from helm's split of the
	// file; a CRD chunk holds the whole file.
	firstIndex int
}

// helmEntryKey names one document of a rendered template, as helm's
// SplitManifests returns it.
type helmEntryKey struct {
	name    string
	content string
}

// helmTemplateStream returns the chunks that helm template (helm v4.3.1, the
// version Argo CD v3.6.1 bundles) writes for the rendered templates, in the
// same order:
//
//  1. renderResources (pkg/action/action.go) drops every file whose name
//     ends in NOTES.txt.
//  2. releaseutil.SortManifests splits each other file into documents with
//     SplitManifests and reads the head of each document. A head that does
//     not parse fails helm template with "YAML parse error on <file>". A
//     document whose helm.sh/hook annotation names a hook event that helm
//     does not know, or is empty, is dropped. A document with a known hook
//     event is a hook. helm reads the annotations of the document itself, so
//     for a List it reads the List, not its items.
//  3. The output holds the files from the chart's crds directories when
//     CRDs are included, then the other documents sorted by kind, then the
//     hooks sorted by kind (pkg/cmd/template.go). --skip-tests (Argo CD's
//     spec.source.helm.skipTests) drops a hook with the test event.
//     --no-hooks (kustomize's skipHooks) drops every hook.
//  4. helm template trims the white space at the end of the CRDs and the
//     other documents together, before it prints the hooks
//     (strings.TrimSpace(rel.Manifest), pkg/cmd/template.go:122). So the
//     last of these chunks loses the white space at its end, such as the
//     blank lines that a "|+" block scalar keeps.
//
// Argo CD also removes one line break from the end of the whole output.
// decodeHelmManifests does that (see trimCommandOutput), because kustomize
// helmCharts output keeps it.
func helmTemplateStream(manifestPath string, pathMap map[string]string, chrt helmchart.Charter, rendered map[string]string, opts RenderOptions) ([]helmStreamChunk, error) {
	stream := &helmStream{manifestPath: manifestPath, pathMap: pathMap}
	if shouldIncludeCRDs(opts) {
		if err := stream.addCRDs(chrt); err != nil {
			return nil, err
		}
	}

	files := make(map[string]string, len(rendered))
	for name, content := range rendered {
		if strings.HasSuffix(name, helmNotesFileSuffix) {
			continue
		}
		files[name] = content
	}
	hooks, sorted, err := releaseutil.SortManifests(files, nil, releaseutil.InstallOrder)
	if err != nil {
		return nil, fmt.Errorf("helm template %s: %w", manifestPath, sanitizeHelmSortError(err))
	}
	stream.indexTemplates(files)
	for _, entry := range sorted {
		if err := stream.addTemplateDocument(entry.Name, entry.Content); err != nil {
			return nil, err
		}
	}
	stream.trimLastChunk()
	if opts.SkipHooks {
		return stream.chunks, nil
	}
	for _, hook := range hooks {
		if opts.SkipTests && slices.Contains(hook.Events, release.HookTest) {
			continue
		}
		if err := stream.addTemplateDocument(hook.Path, hook.Manifest); err != nil {
			return nil, err
		}
	}
	return stream.chunks, nil
}

// trimCommandOutput removes one line break from the end of the stream, as
// Argo CD does with the output of helm template (util/exec/exec.go:285 at
// v3.6.1, through util/helm/cmd.go). kustomize reads the output of helm
// itself, so helmCharts output keeps the line break.
func trimCommandOutput(chunks []helmStreamChunk) {
	if len(chunks) == 0 {
		return
	}
	last := &chunks[len(chunks)-1]
	last.text = strings.TrimSuffix(last.text, "\n")
}

// helmStream collects the chunks of helm template output for
// helmTemplateStream.
type helmStream struct {
	manifestPath string
	pathMap      map[string]string
	chunks       []helmStreamChunk
	// positions holds, for each template document, the index of each copy
	// of it in its file, in file order.
	positions map[helmEntryKey][]int
}

func (s *helmStream) addCRDs(chrt helmchart.Charter) error {
	if provider, ok := chrt.(helmCRDObjectProvider); ok {
		for _, crd := range provider.CRDObjects() {
			if err := s.addCRD(crd.Filename, crd.File.Data); err != nil {
				return err
			}
		}
		return nil
	}
	if provider, ok := chrt.(helmCRDProvider); ok {
		for _, crd := range provider.CRDs() {
			if err := s.addCRD(crd.Name, crd.Data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *helmStream) addCRD(name string, data []byte) error {
	crdPath, err := helmManifestPath(s.pathMap, name)
	if err != nil {
		return err
	}
	s.chunks = append(s.chunks, helmStreamChunk{
		path:    crdPath,
		text:    fmt.Sprintf("---\n# Source: %s\n%s\n", name, data),
		content: string(data),
	})
	return nil
}

// indexTemplates gives each document of the rendered templates its index in
// its file, split as helm splits the file, for the document labels in
// errors.
func (s *helmStream) indexTemplates(files map[string]string) {
	s.positions = make(map[helmEntryKey][]int)
	for name, content := range files {
		entries := releaseutil.SplitManifests(content)
		keys := make([]string, 0, len(entries))
		for key := range entries {
			keys = append(keys, key)
		}
		sort.Sort(releaseutil.BySplitManifestsOrder(keys))
		for index, key := range keys {
			entryKey := helmEntryKey{name: name, content: entries[key]}
			s.positions[entryKey] = append(s.positions[entryKey], index)
		}
	}
}

// addTemplateDocument adds one document that SortManifests returned. helm
// sorts stably, so copies of the same document in one file keep their order.
func (s *helmStream) addTemplateDocument(name, content string) error {
	entryKey := helmEntryKey{name: name, content: content}
	queue := s.positions[entryKey]
	if len(queue) == 0 {
		return fmt.Errorf("helm template %s: cannot find a document of %s in its file", s.manifestPath, name)
	}
	s.positions[entryKey] = queue[1:]
	entryPath, err := helmManifestPath(s.pathMap, name)
	if err != nil {
		return err
	}
	s.chunks = append(s.chunks, helmStreamChunk{
		path:       entryPath,
		text:       fmt.Sprintf("---\n# Source: %s\n%s\n", name, content),
		content:    content,
		firstIndex: queue[0],
	})
	return nil
}

// trimLastChunk trims the white space at the end of the last chunk, as
// helm template trims the release manifest (see helmTemplateStream).
func (s *helmStream) trimLastChunk() {
	if len(s.chunks) == 0 {
		return
	}
	last := &s.chunks[len(s.chunks)-1]
	last.text = strings.TrimRightFunc(last.text, unicode.IsSpace) + "\n"
	last.content = strings.TrimRightFunc(last.content, unicode.IsSpace)
}

// sanitizeHelmSortError keeps the "YAML parse error on <file>: " prefix that
// releaseutil.SortManifests (pkg/release/v1/util/manifest_sorter.go:151-153
// at helm v4.3.1) puts on a sigs.k8s.io/yaml error, and drops the manifest
// content that the wrapped error can copy, such as a map key (see
// manifestyaml.SanitizeDecodeError).
func sanitizeHelmSortError(err error) error {
	inner := errors.Unwrap(err)
	if inner == nil {
		return manifestyaml.SanitizeDecodeError(err)
	}
	prefix, ok := strings.CutSuffix(err.Error(), inner.Error())
	if !ok {
		return manifestyaml.SanitizeDecodeError(err)
	}
	return fmt.Errorf("%s%w", prefix, manifestyaml.SanitizeDecodeError(inner))
}

// helmNotesFileSuffix is the suffix of the files that helm's renderResources
// drops from the rendered templates (notesFileSuffix, pkg/action/install.go).
const helmNotesFileSuffix = "NOTES.txt"

func shouldIncludeCRDs(opts RenderOptions) bool {
	return !opts.IncludeCRDsSet || opts.IncludeCRDs
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
