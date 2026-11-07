package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/format"
	goyaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/konfig"
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
}

var (
	kustomizeGeneratorsField   = kustomizePluginField{name: "generators", errorPrefix: "loading generator plugins: failed to load generator: "}
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
	default:
		w.skippedExecFunctions++
		return kustomizePluginSkip, nil
	}
}

func execFunctionsSkippedDiagnostic(skipped int) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     KustomizeExecFunctionSkippedCode,
		Severity: diagnostic.SeverityWarning,
		Category: "kustomize",
		Message:  fmt.Sprintf("kustomize skips %d exec functions of generators or transformers, because kustomize.buildOptions has --enable-alpha-plugins and no --enable-exec; they give no resources and change nothing", skipped),
	}
}

// KustomizeExecFunctionSkippedCode identifies the warning for exec
// functions that kustomize skips without --enable-exec.
const KustomizeExecFunctionSkippedCode = "kustomize.exec-function-skipped"

// readKustomizePluginEntry reads the documents of one generators: or
// transformers: entry: an inline configuration or a local file.
func readKustomizePluginEntry(field kustomizePluginField, dir, boundaryRoot, entry string) (docs []*goyaml.Node, describe, manifestDir string, err error) {
	docs, inline := inlineKustomizeGeneratorDocuments(entry)
	if inline {
		return docs, describeInlineGeneratorEntry(docs), dir, nil
	}
	describe = strings.TrimSpace(entry)
	path, info, err := validateLocalKustomizeRef(boundaryRoot, dir, field.name, entry)
	if err != nil {
		return nil, describe, "", err
	}
	if info == nil {
		return nil, describe, "", fmt.Errorf("kustomize %s %q: %s manifest not found", field.name, entry, strings.TrimSuffix(field.name, "s"))
	}
	if !info.Mode().IsRegular() {
		return nil, describe, "", fmt.Errorf("kustomize %s %q: %s manifest must be a regular file", field.name, entry, strings.TrimSuffix(field.name, "s"))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, describe, "", err
	}
	docs, err = decodeYAMLDocumentNodes(content)
	if err != nil {
		return nil, describe, "", fmt.Errorf("kustomize %s %q: decode %s manifest: %w", field.name, entry, strings.TrimSuffix(field.name, "s"), err)
	}
	return docs, describe, filepath.Dir(path), nil
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
// that names a directory, a kustomization of transformer configurations,
// stays as it is.
func (w *kustomizeWorkspace) prepareKustomizeTransformers(ctx context.Context, dir, boundaryRoot string, graphIndex int, kustomization *types.Kustomization) error {
	if w.opts.KustomizeCaller == KustomizeCallerArgoCDAssumed || !w.buildSettings.EnableAlphaPlugins {
		return nil
	}
	remaining := make([]string, 0, len(kustomization.Transformers))
	for entryIndex, entry := range kustomization.Transformers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, inline := inlineKustomizeGeneratorDocuments(entry); !inline {
			if _, info, err := validateLocalKustomizeRef(boundaryRoot, dir, "transformers", entry); err == nil && info != nil && info.IsDir() {
				remaining = append(remaining, entry)
				continue
			}
		}
		kept, skipped, err := w.classifyKustomizeTransformerEntry(dir, boundaryRoot, entry)
		if err != nil {
			return err
		}
		switch {
		case skipped == 0:
			remaining = append(remaining, entry)
		case len(kept) != 0:
			rel, err := writeKustomizePluginDocuments(dir, filepath.Join(".drydock", "transformers", fmt.Sprintf("%03d-%03d.yaml", graphIndex, entryIndex)), kept)
			if err != nil {
				return err
			}
			remaining = append(remaining, rel)
		}
	}
	kustomization.Transformers = remaining
	return nil
}

// classifyKustomizeTransformerEntry returns the transformer configurations
// of an entry that kustomize loads, and how many it skips.
func (w *kustomizeWorkspace) classifyKustomizeTransformerEntry(dir, boundaryRoot, entry string) ([]*goyaml.Node, int, error) {
	docs, describe, _, err := readKustomizePluginEntry(kustomizeTransformersField, dir, boundaryRoot, entry)
	if err != nil {
		return nil, 0, err
	}
	kept := make([]*goyaml.Node, 0, len(docs))
	skipped := 0
	for _, doc := range docs {
		root := yamlDocumentRoot(doc)
		if root == nil {
			continue
		}
		action, err := w.classifyKustomizePlugin(kustomizeTransformersField, describe, root)
		if err != nil {
			return nil, 0, err
		}
		if action == kustomizePluginSkip {
			skipped++
			continue
		}
		kept = append(kept, root)
	}
	return kept, skipped, nil
}
