package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/format"
	goyaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/hasher"
	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/fn/runtime/runtimeutil"
	kyaml "sigs.k8s.io/kustomize/kyaml/yaml"
)

// kustomizePluginAction is what the kustomize binary does with one plugin
// configuration document of a generators: or transformers: entry.
type kustomizePluginAction int

const (
	// kustomizePluginBuiltin is a builtin plugin, which krusty loads.
	kustomizePluginBuiltin kustomizePluginAction = iota
	// kustomizePluginKSOPS is a KSOPS generator, which drydock emulates.
	kustomizePluginKSOPS
	// kustomizePluginSkip is a function that kustomize skips, so it gives
	// nothing and changes nothing.
	kustomizePluginSkip
	// kustomizePluginUnsupported is a plugin that drydock does not run,
	// when drydock cannot tell the build flags.
	kustomizePluginUnsupported
)

// kustomizePluginField is a kustomization field of plugin configurations.
type kustomizePluginField struct {
	name string
	// errorPrefix is the text that kustomize puts before a load error of
	// the field (KustTarget.configureExternalGenerators and
	// configureExternalTransformers at kustomize/api v0.21.3).
	errorPrefix string
	// accumulatePrefix is the text that kustomize puts before an error
	// that it gets when it reads the entries of the field.
	accumulatePrefix string
}

var (
	kustomizeGeneratorsField   = kustomizePluginField{name: "generators", errorPrefix: "loading generator plugins: failed to load generator: ", accumulatePrefix: "loading generator plugins: "}
	kustomizeTransformersField = kustomizePluginField{name: "transformers"}
)

// classifyKustomizePlugin decides what the kustomize v5.8.3 binary does
// with one plugin configuration, with the flags of the build.
//
// A builtin plugin always loads. Any other plugin needs
// --enable-alpha-plugins, or kustomize fails with "external plugins
// disabled" (Loader.loadAndConfigurePlugin, api/internal/plugins/loader/
// loader.go:204-215). Then kustomize reads the function annotation
// (fnplugin.GetFunctionSpec), and a malformed one fails. A container
// function runs in Docker, also when it names an exec program too
// (RunFns.getFilterProvider in kyaml). An exec function runs only with
// --enable-exec, and kustomize skips it without that flag. A plugin
// without a function annotation is a legacy plugin program.
//
// drydock runs no plugin program, so a plugin that kustomize would run
// fails closed, except a KSOPS generator, which drydock emulates under
// --enable-ksops-compat. Without a found argocd-cm, drydock cannot tell
// the flags: it emulates KSOPS generators, keeps builtins, and reports
// any other plugin as unsupported, as before.
func (w *kustomizeWorkspace) classifyKustomizePlugin(field kustomizePluginField, describe string, root *goyaml.Node) (kustomizePluginAction, error) {
	apiVersion := yamlMappingString(root, "apiVersion")
	kind := yamlMappingString(root, "kind")
	ksops := field == kustomizeGeneratorsField && apiVersion == ksopsGeneratorAPIVersion && kind == ksopsGeneratorKind
	if apiVersion == konfig.BuiltinPluginApiVersion {
		return kustomizePluginBuiltin, nil
	}
	if w.opts.KustomizeCaller == KustomizeCallerArgoCDAssumed {
		if ksops {
			return kustomizePluginKSOPS, nil
		}
		return kustomizePluginUnsupported, nil
	}
	if !w.buildSettings.EnableAlphaPlugins {
		return 0, fmt.Errorf("%sexternal plugins disabled; unable to load external plugin '%s'", field.errorPrefix, kind)
	}
	spec, err := runtimeutil.GetFunctionSpec(kyaml.NewRNode(root))
	if err != nil {
		return 0, fmt.Errorf("%sloader: failed to get FunctionSpec: %w", field.errorPrefix, err)
	}
	switch {
	case spec == nil:
		if ksops {
			return kustomizePluginKSOPS, nil
		}
		return 0, fmt.Errorf("kustomize %s entry %q: %s/%s is a kustomize plugin program, which drydock does not run", field.name, describe, apiVersion, kind)
	case spec.Container.Image != "":
		return 0, fmt.Errorf("kustomize %s entry %q: %s/%s is a container function, which kustomize runs in a container and drydock does not run", field.name, describe, apiVersion, kind)
	case spec.Exec.Path != "" && w.buildSettings.EnableExec:
		if ksops {
			return kustomizePluginKSOPS, nil
		}
		return 0, fmt.Errorf("kustomize %s entry %q: %s/%s is an exec function, which drydock does not run", field.name, describe, apiVersion, kind)
	case spec.Exec.Path != "":
		w.skippedExecFunctions++
		return kustomizePluginSkip, nil
	default:
		// A function annotation with neither a container nor an exec
		// program gives kustomize no function to run, with any flags.
		w.skippedEmptyFunctions++
		return kustomizePluginSkip, nil
	}
}

// skippedFunctionsDiagnostic reports the functions that kustomize skips. A
// skipped generator adds only its own configuration, marked
// config.kubernetes.io/local-config, which kustomize drops at the end of
// the build. A skipped transformer changes no resource. kustomize still
// lists it in the transformations annotation of buildMetadata
// transformerAnnotations, which drydock does not (recorded divergence).
func skippedFunctionsDiagnostic(execFunctions, emptyFunctions int) diagnostic.Diagnostic {
	var reasons []string
	if execFunctions != 0 {
		reasons = append(reasons, fmt.Sprintf("%d exec functions, because kustomize.buildOptions has --enable-alpha-plugins and no --enable-exec", execFunctions))
	}
	if emptyFunctions != 0 {
		reasons = append(reasons, fmt.Sprintf("%d functions whose annotation names neither a container nor an exec program", emptyFunctions))
	}
	return diagnostic.Diagnostic{
		Code:     KustomizeExecFunctionSkippedCode,
		Severity: diagnostic.SeverityWarning,
		Category: "kustomize",
		Message:  "kustomize skips " + strings.Join(reasons, ", and ") + "; a skipped generator adds only its own configuration, which kustomize drops at the end of the build, and a skipped transformer changes no resource",
	}
}

// KustomizeExecFunctionSkippedCode identifies the warning for functions of
// generators and transformers that kustomize skips.
const KustomizeExecFunctionSkippedCode = "kustomize.exec-function-skipped"

// kustomizePluginEntry is the plugin configurations of one generators: or
// transformers: entry.
type kustomizePluginEntry struct {
	roots       []*goyaml.Node
	describe    string
	manifestDir string
}

// readKustomizePluginEntry reads the plugin configurations of one
// generators: or transformers: entry as kustomize reads them
// (KustTarget.configureExternalGenerators and
// configureExternalTransformers): an inline configuration or a file.
// kustomize parses them with its resource factory, which expands a List
// into its items. A file outside the kustomization directory fails under
// LoadRestrictionsRootOnly, with the text of KustTarget.accumulateResources.
//
// kustomize also accepts a kustomization directory, which it builds.
// drydock does not build it here, because such a build would read files
// that no graph check has seen. A directory entry fails here; a
// transformers directory stays in the kustomization for krusty
// (prepareKustomizeTransformers), which walks it with the graph checks.
func (w *kustomizeWorkspace) readKustomizePluginEntry(field kustomizePluginField, dir, boundaryRoot, entry string) (kustomizePluginEntry, error) {
	if _, inline := inlineKustomizeGeneratorDocuments(entry); inline {
		roots, err := kustomizePluginRoots([]byte(entry))
		if err != nil {
			return kustomizePluginEntry{}, fmt.Errorf("kustomize %s entry: %w", field.name, err)
		}
		return kustomizePluginEntry{roots: roots, describe: describeInlineGeneratorEntry(roots), manifestDir: dir}, nil
	}
	describe := strings.TrimSpace(entry)
	path, info, err := validateLocalKustomizeRef(boundaryRoot, dir, field.name, entry)
	if err != nil {
		return kustomizePluginEntry{}, err
	}
	singular := strings.TrimSuffix(field.name, "s")
	if info == nil {
		return kustomizePluginEntry{}, fmt.Errorf("kustomize %s %q: %s manifest not found", field.name, entry, singular)
	}
	if info.IsDir() {
		return kustomizePluginEntry{}, fmt.Errorf("kustomize %s entry %q is a kustomization directory; drydock does not expand directory entries of %s, which can hold plugin functions", field.name, entry, field.name)
	}
	if !info.Mode().IsRegular() {
		return kustomizePluginEntry{}, fmt.Errorf("kustomize %s %q: %s manifest must be a regular file", field.name, entry, singular)
	}
	loader := kustomizeValuesFileLoader{repoRoot: w.tempRepoRoot, dir: dir, rootOnly: w.buildSettings.LoadRestrictions != types.LoadRestrictionsNone}
	if err := loader.checkRoot(path); err != nil {
		return kustomizePluginEntry{}, fmt.Errorf("%saccumulation err='accumulating resources from '%s': %s': must build at directory: '%s': file is not directory", field.accumulatePrefix, entry, err.Error(), loader.display(path))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return kustomizePluginEntry{}, err
	}
	roots, err := kustomizePluginRoots(content)
	if err != nil {
		return kustomizePluginEntry{}, fmt.Errorf("kustomize %s %q: decode %s manifest: %w", field.name, entry, singular, err)
	}
	return kustomizePluginEntry{roots: roots, describe: describe, manifestDir: filepath.Dir(path)}, nil
}

// kustomizePluginRoots parses plugin configurations with the resource
// factory of kustomize, which drops empty documents and expands Lists.
func kustomizePluginRoots(content []byte) ([]*goyaml.Node, error) {
	nodes, err := resource.NewFactory(&hasher.Hasher{}).RNodesFromBytes(content)
	if err != nil {
		return nil, err
	}
	roots := make([]*goyaml.Node, 0, len(nodes))
	for _, node := range nodes {
		roots = append(roots, node.YNode())
	}
	return roots, nil
}

// writeKustomizePluginDocuments writes the plugin configurations that
// remain of an entry to a generated file and returns its path.
func writeKustomizePluginDocuments(dir, rel string, roots []*goyaml.Node) (string, error) {
	var buf bytes.Buffer
	for i, root := range roots {
		if i > 0 {
			buf.WriteString("---\n")
		}
		data, err := format.MarshalYAML(root)
		if err != nil {
			return "", err
		}
		buf.Write(data)
	}
	rel = filepath.ToSlash(rel)
	if err := writeGeneratedKustomizeWorkspaceFile(dir, rel, buf.Bytes()); err != nil {
		return "", err
	}
	return rel, nil
}

// needsKustomizeTransformerPrep reports whether the transformers: entries of
// the build need prepareKustomizeTransformers: with --enable-alpha-plugins,
// kustomize may skip an exec function that krusty, which drydock runs with
// builtins only, would refuse.
func needsKustomizeTransformerPrep(graph []kustomizeGraphNode, opts RenderOptions, settings kustomizeBuildSettings) bool {
	if opts.KustomizeCaller == KustomizeCallerArgoCDAssumed || !settings.EnableAlphaPlugins {
		return false
	}
	for _, node := range graph {
		if len(node.Kustomization.Transformers) != 0 {
			return true
		}
	}
	return false
}

// prepareKustomizeTransformers drops the transformer configurations that
// kustomize skips and fails the ones that it would run, when drydock knows
// the build flags and they hold --enable-alpha-plugins. Without that flag,
// krusty loads the entries and fails a plugin as kustomize does. An entry
// whose configurations change becomes a generated file, also an empty one:
// it keeps the kustomization from being empty, which krusty fails and
// kustomize does not, because the original entry is still there.
func (w *kustomizeWorkspace) prepareKustomizeTransformers(ctx context.Context, dir, boundaryRoot string, graphIndex int, kustomization *types.Kustomization) error {
	if w.opts.KustomizeCaller == KustomizeCallerArgoCDAssumed || !w.buildSettings.EnableAlphaPlugins {
		return nil
	}
	remaining := make([]string, 0, len(kustomization.Transformers))
	for entryIndex, entry := range kustomization.Transformers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if isKustomizeDirectoryEntry(dir, boundaryRoot, entry) {
			// krusty builds the directory with the graph checks of the
			// build. A plugin function in it fails there
			// (kustomizeDirectoryPluginError).
			remaining = append(remaining, entry)
			continue
		}
		read, err := w.readKustomizePluginEntry(kustomizeTransformersField, dir, boundaryRoot, entry)
		if err != nil {
			return err
		}
		kept := make([]*goyaml.Node, 0, len(read.roots))
		skipped := 0
		for _, root := range read.roots {
			action, err := w.classifyKustomizePlugin(kustomizeTransformersField, read.describe, root)
			if err != nil {
				return err
			}
			if action == kustomizePluginSkip {
				skipped++
				continue
			}
			kept = append(kept, root)
		}
		if skipped == 0 {
			remaining = append(remaining, entry)
			continue
		}
		rel, err := writeKustomizePluginDocuments(dir, filepath.Join(".drydock", "transformers", fmt.Sprintf("%03d-%03d.yaml", graphIndex, entryIndex)), kept)
		if err != nil {
			return err
		}
		w.recordRewrittenEntry(dir, rel, entry)
		remaining = append(remaining, rel)
	}
	kustomization.Transformers = remaining
	return nil
}

// isKustomizeDirectoryEntry reports whether a transformers: or generators:
// entry names a local directory.
func isKustomizeDirectoryEntry(dir, boundaryRoot, entry string) bool {
	if _, inline := inlineKustomizeGeneratorDocuments(entry); inline {
		return false
	}
	_, info, err := validateLocalKustomizeRef(boundaryRoot, dir, "transformers", entry)
	return err == nil && info != nil && info.IsDir()
}

// kustomizeDirectoryPluginError gives the krusty error of a build an
// accurate text. With a found argocd-cm and --enable-alpha-plugins, drydock
// prepared every plugin file entry, so the only plugin that krusty can
// still refuse with "external plugins disabled" is one in a transformers:
// directory entry. kustomize would load it, so the text of krusty is wrong
// there: drydock does not expand such directories.
func kustomizeDirectoryPluginError(err error, opts RenderOptions, settings kustomizeBuildSettings) error {
	if err == nil || opts.KustomizeCaller == KustomizeCallerArgoCDAssumed || !settings.EnableAlphaPlugins || !strings.Contains(err.Error(), "external plugins disabled") {
		return err
	}
	return fmt.Errorf("a transformers: entry that is a kustomization directory holds a plugin; drydock does not expand directory entries that hold plugin functions (krusty: %w)", err)
}

// recordRewrittenEntry records that the generated file rel stands for a
// generators: or transformers: entry of the kustomization in dir, so the
// origin annotations and errors of the build can name the entry
// (restoreRewrittenEntryPaths). kustomize names the kustomization file for
// an inline entry, as configuredIn.
func (w *kustomizeWorkspace) recordRewrittenEntry(dir, rel, entry string) {
	if w.rewrittenEntries == nil {
		w.rewrittenEntries = map[string]string{}
	}
	name := strings.TrimSpace(entry)
	if _, inline := inlineKustomizeGeneratorDocuments(entry); inline {
		name = kustomizationFileNames[0]
		if file, err := findKustomizationFile(dir); err == nil {
			name = filepath.Base(file)
		}
	}
	w.rewrittenEntries[filepath.ToSlash(rel)] = name
}

// kustomizeOriginAnnotations are the annotations of buildMetadata
// originAnnotations and transformerAnnotations, which name the file of a
// generator or transformer configuration.
var kustomizeOriginAnnotations = []string{"config.kubernetes.io/origin", "alpha.config.kubernetes.io/transformations"}

// restoreRewrittenEntryText replaces the paths of the generated files that
// stand for entries with the entry paths, and the workspace root with
// nothing, so text names the repository files.
func (w *kustomizeWorkspace) restoreRewrittenEntryText(text string) string {
	for rel, entry := range w.rewrittenEntries {
		text = strings.ReplaceAll(text, rel, entry)
	}
	for _, root := range w.workspaceRoots() {
		text = strings.ReplaceAll(text, root+string(filepath.Separator), "")
		text = strings.ReplaceAll(text, root, ".")
	}
	return text
}

// workspaceRoots returns the temp repository root with its symlinks
// resolved and as written, as krusty can name either. The longer one comes
// first: on macOS /private/var/... holds /var/..., which must not be
// replaced first.
func (w *kustomizeWorkspace) workspaceRoots() []string {
	roots := []string{w.tempRepoRoot}
	if resolved, err := filepath.EvalSymlinks(w.tempRepoRoot); err == nil && resolved != w.tempRepoRoot {
		roots = append(roots, resolved)
	}
	slices.SortFunc(roots, func(a, b string) int { return len(b) - len(a) })
	return roots
}

// restoreRewrittenEntryPaths names the entries instead of their generated
// files in the origin annotations of the output.
func (w *kustomizeWorkspace) restoreRewrittenEntryPaths(manifests []Manifest) {
	if len(w.rewrittenEntries) == 0 {
		return
	}
	for _, manifest := range manifests {
		annotations := manifest.Object.GetAnnotations()
		changed := false
		for _, key := range kustomizeOriginAnnotations {
			if value, ok := annotations[key]; ok {
				if restored := w.restoreRewrittenEntryText(value); restored != value {
					annotations[key] = restored
					changed = true
				}
			}
		}
		if changed {
			manifest.Object.SetAnnotations(annotations)
		}
	}
}

// workspaceError is an error of the prepared workspace whose text names the
// repository files instead of the workspace files.
type workspaceError struct {
	text string
	err  error
}

func (e workspaceError) Error() string { return e.text }
func (e workspaceError) Unwrap() error { return e.err }

func (w *kustomizeWorkspace) restoreErrorPaths(err error) error {
	if err == nil {
		return nil
	}
	text := w.restoreRewrittenEntryText(err.Error())
	if text == err.Error() {
		return err
	}
	return workspaceError{text: text, err: err}
}
