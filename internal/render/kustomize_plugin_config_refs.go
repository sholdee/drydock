package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sholdee/drydock/internal/format"
	goyaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/types"
	sigsyaml "sigs.k8s.io/yaml"
)

// Kustomize configures every plugin a transformers:, generators: or
// validators: entry names with the LISTING kustomization's loader
// (kusttarget configureExternalGenerators / configureExternalTransformers
// pass kt.ldr to the plugin loader), so the files a builtin plugin config
// reads — a PatchTransformer path:, ConfigMapGenerator files:, and so on —
// resolve relative to the kustomization directory that lists the entry: not
// the config file's own directory, and for a directory entry not the entry's
// kustomization. Those referents are render inputs outside the config file.

// kustomizePluginConfigRef is one referent of a builtin plugin config: a file
// the plugin reads through the listing kustomization's loader.
type kustomizePluginConfigRef struct {
	// Kind is the builtin plugin kind, e.g. PatchTransformer.
	Kind string
	// Field names the config field, e.g. path or replacements.path.
	Field string
	// Path is the referent exactly as the plugin reads it, never empty.
	Path string
}

// rawPluginReferent is one referent as a builtin plugin reads it, before
// kustomizePluginConfigRefs drops it when empty.
type rawPluginReferent struct {
	field string
	path  string
}

// pluginReferentExtractor decodes one builtin plugin config document and
// returns the files its plugin reads through the loader.
type pluginReferentExtractor func(content []byte) ([]rawPluginReferent, error)

// builtinPluginReferentExtractors maps every builtin plugin kind krusty can
// load (builtinhelpers GeneratorFactories and TransformerFactories) except
// HelmChartInflationGenerator to the extractor of the files its Config or
// Generate reads through the loader; nil means the kind reads no files.
// Extractors decode with sigs.k8s.io/yaml into structs mirroring the plugin's
// json tags — the same decoder the plugins' Config uses, so keys match with
// the same case-insensitive JSON semantics — and derive each path the way
// the plugin does. Both the digest walk and the render walk's referent check
// (validatePluginConfigResources) use them, so the digest collects exactly
// the files render reads.
var builtinPluginReferentExtractors = map[string]pluginReferentExtractor{
	"AnnotationsTransformer":  nil,
	"HashTransformer":         nil,
	"IAMPolicyGenerator":      nil,
	"ImageTagTransformer":     nil,
	"LabelTransformer":        nil,
	"NamespaceTransformer":    nil,
	"PrefixSuffixTransformer": nil,
	"PrefixTransformer":       nil,
	"ReplicaCountTransformer": nil,
	"SuffixTransformer":       nil,

	"PatchTransformer":               extractPatchReferents,
	"PatchJson6902Transformer":       extractPatchReferents,
	"PatchStrategicMergeTransformer": extractPatchStrategicMergeReferents,
	"ReplacementTransformer":         extractReplacementReferents,
	"ConfigMapGenerator":             extractKvGeneratorReferents,
	"SecretGenerator":                extractKvGeneratorReferents,
	// ValueAddTransformer loads targetFilePath through h.Loader().Load.
	"ValueAddTransformer": extractValueAddReferents,
}

// kustomizePluginConfigRefs returns the referents of the plugin config
// documents one transformers:, generators: or validators: entry names.
// Builtin kinds (apiVersion: builtin) are enumerated field by field. KSOPS
// generator documents contribute nothing here: collectKSOPSGeneratorFileRefs
// collects their files: (resolved like ksops-compat emulation does), and
// krusty rejects them anywhere but generators:. Every other document — a
// HelmChartInflationGenerator (rendering runs builtins with Helm disabled),
// an unknown builtin kind, or a non-builtin exec/container/Go plugin — is an
// error: its inputs cannot be enumerated, so it must not key a cache entry.
// Referents are kept exactly as the plugin reads them (kustomize never trims
// a path) and empty ones dropped: an inline replacement has no path, and a
// generator often lists no files.
func kustomizePluginConfigRefs(docs []*goyaml.Node) ([]kustomizePluginConfigRef, error) {
	var refs []kustomizePluginConfigRef
	for _, doc := range docs {
		root := yamlDocumentRoot(doc)
		if isEmptyPluginConfigDocument(root) {
			continue
		}
		if root.Kind != goyaml.MappingNode {
			return nil, fmt.Errorf("plugin config document is not a mapping")
		}
		class, apiVersion, kind := classifyGeneratorDocument(root)
		switch class {
		case generatorDocumentKSOPS:
			continue
		case generatorDocumentUnsupported:
			return nil, fmt.Errorf("plugin %s/%s is not a kustomize builtin plugin; its inputs cannot be enumerated", apiVersion, kind)
		case generatorDocumentBuiltin:
		}
		docRefs, err := builtinPluginDocumentRefs(kind, root)
		if err != nil {
			return nil, err
		}
		refs = append(refs, docRefs...)
	}
	return refs, nil
}

// isEmptyPluginConfigDocument mirrors kyaml's IsYNodeNilOrEmpty: kustomize's
// resource factory drops null, {} and [] documents (dropBadNodes) before
// configuring any plugin.
func isEmptyPluginConfigDocument(root *goyaml.Node) bool {
	if root == nil {
		return true
	}
	if root.Kind == goyaml.ScalarNode {
		return root.ShortTag() == "!!null"
	}
	return (root.Kind == goyaml.MappingNode || root.Kind == goyaml.SequenceNode) && len(root.Content) == 0
}

// builtinPluginDocumentRefs returns the non-empty referents of one
// apiVersion: builtin document of the given kind.
func builtinPluginDocumentRefs(kind string, root *goyaml.Node) ([]kustomizePluginConfigRef, error) {
	if kind == "HelmChartInflationGenerator" {
		return nil, fmt.Errorf("builtin HelmChartInflationGenerator is unsupported")
	}
	extract, known := builtinPluginReferentExtractors[kind]
	if !known {
		return nil, fmt.Errorf("builtin plugin kind %q is unknown; its inputs cannot be enumerated", kind)
	}
	if extract == nil {
		return nil, nil
	}
	content, err := format.MarshalYAML(root)
	if err != nil {
		return nil, fmt.Errorf("builtin %s config: %w", kind, err)
	}
	referents, err := extract(content)
	if err != nil {
		return nil, fmt.Errorf("decode builtin %s config: %w", kind, err)
	}
	refs := make([]kustomizePluginConfigRef, 0, len(referents))
	for _, referent := range referents {
		if referent.path == "" {
			continue
		}
		refs = append(refs, kustomizePluginConfigRef{Kind: kind, Field: referent.field, Path: referent.path})
	}
	return refs, nil
}

// builtinPluginReferents returns the referents of one builtin config
// document of a known kind; a kind that reads no files has none.
func builtinPluginReferents(kind string, content []byte) ([]rawPluginReferent, error) {
	extract := builtinPluginReferentExtractors[kind]
	if extract == nil {
		return nil, nil
	}
	return extract(content)
}

func extractPatchReferents(content []byte) ([]rawPluginReferent, error) {
	var config struct {
		Path string `json:"path,omitempty"`
	}
	if err := sigsyaml.Unmarshal(content, &config); err != nil {
		return nil, err
	}
	return []rawPluginReferent{{field: "path", path: config.Path}}, nil
}

// extractPatchStrategicMergeReferents skips paths: entries holding patch
// content — the plugin's legacy form tries each entry as content before
// treating it as a file (loadFromPaths; isInlineStrategicMergePatch).
func extractPatchStrategicMergeReferents(content []byte) ([]rawPluginReferent, error) {
	var config struct {
		Paths []types.PatchStrategicMerge `json:"paths,omitempty"`
	}
	if err := sigsyaml.Unmarshal(content, &config); err != nil {
		return nil, err
	}
	out := make([]rawPluginReferent, 0, len(config.Paths))
	for _, path := range config.Paths {
		if isInlineStrategicMergePatch(string(path)) {
			continue
		}
		out = append(out, rawPluginReferent{field: "paths", path: string(path)})
	}
	return out, nil
}

func extractReplacementReferents(content []byte) ([]rawPluginReferent, error) {
	var config struct {
		Replacements []types.ReplacementField `json:"replacements,omitempty"`
	}
	if err := sigsyaml.Unmarshal(content, &config); err != nil {
		return nil, err
	}
	out := make([]rawPluginReferent, 0, len(config.Replacements))
	for _, replacement := range config.Replacements {
		out = append(out, rawPluginReferent{field: "replacements.path", path: replacement.Path})
	}
	return out, nil
}

// extractKvGeneratorReferents covers ConfigMapGenerator and SecretGenerator:
// files: and envs:, read through kv.NewLoader over the plugin loader at
// Generate time. A files: entry is split the way generators.ParseFileSource
// splits it: no "=" makes the whole entry the path; exactly one "=" inside
// makes the path what follows it (a URL's query "=" included); any other
// "=" fails the plugin before it reads anything. The singular env: is NOT a
// referent of a plugin config: only FixKustomization merges it into envs:,
// for kustomization-level generators, and kv.Load reads EnvSources alone — a
// builtin config's env: file is never read.
func extractKvGeneratorReferents(content []byte) ([]rawPluginReferent, error) {
	var config types.KvPairSources
	if err := sigsyaml.Unmarshal(content, &config); err != nil {
		return nil, err
	}
	out := make([]rawPluginReferent, 0, len(config.FileSources)+len(config.EnvSources))
	for _, source := range config.FileSources {
		switch separators := strings.Count(source, "="); {
		case separators == 0:
			out = append(out, rawPluginReferent{field: "files", path: source})
		case separators == 1 && !strings.HasPrefix(source, "=") && !strings.HasSuffix(source, "="):
			_, path, _ := strings.Cut(source, "=")
			out = append(out, rawPluginReferent{field: "files", path: path})
		}
	}
	for _, source := range config.EnvSources {
		out = append(out, rawPluginReferent{field: "envs", path: source})
	}
	return out, nil
}

func extractValueAddReferents(content []byte) ([]rawPluginReferent, error) {
	var config struct {
		TargetFilePath string `json:"targetFilePath,omitempty"`
	}
	if err := sigsyaml.Unmarshal(content, &config); err != nil {
		return nil, err
	}
	return []rawPluginReferent{{field: "targetFilePath", path: config.TargetFilePath}}, nil
}

// collectPluginConfigEntry collects one transformers:, generators: or
// validators: entry: a local path entry is digested by content, and the
// referents of the builtin plugin configs it names join the collection,
// resolved against the listing kustomization directory dir. Inline entries
// (kustomize tries each entry as YAML documents before treating it as a path)
// live in the kustomization file, which is always collected; only their
// referents are added. A config whose referents cannot be enumerated fails a
// strict digest walk and is skipped by a best-effort one, which keeps the
// entry itself.
func (c *kustomizeInputCollector) collectPluginConfigEntry(ctx context.Context, dir, field, ref string) error {
	// A blank entry is an empty inline document to kustomize: nothing is
	// read. Any other entry is used exactly as written.
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	if docs, inline := inlineKustomizeGeneratorDocuments(ref); inline {
		return c.collectPluginConfigReferents(ctx, dir, field, docs)
	}
	if err := c.addKustomizeRef(ctx, dir, field, ref, false); err != nil {
		return err
	}
	docs, err := c.pluginConfigEntryDocuments(ctx, dir, field, ref)
	if err != nil {
		return c.skip(ctx, err)
	}
	return c.collectPluginConfigReferents(ctx, dir, field, docs)
}

// collectPluginConfigReferents adds the referents of docs one document at a
// time, so a best-effort walk keeps the referents of the documents it can
// enumerate.
func (c *kustomizeInputCollector) collectPluginConfigReferents(ctx context.Context, dir, field string, docs []*goyaml.Node) error {
	for _, doc := range docs {
		refs, err := kustomizePluginConfigRefs([]*goyaml.Node{doc})
		if err != nil {
			if err := c.skip(ctx, fmt.Errorf("kustomize %s: %w", field, err)); err != nil {
				return err
			}
			continue
		}
		for _, ref := range refs {
			if err := c.addPluginConfigReferent(ctx, dir, field+"."+ref.Kind+"."+ref.Field, ref.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

// addPluginConfigReferent adds one builtin plugin config referent resolved
// against the listing kustomization directory. Kustomize reads referents as
// files (Loader.Load), so an empty referent is skipped and a directory is
// rejected rather than collected: either would own the listing directory
// whole — at the repository root, every path.
func (c *kustomizeInputCollector) addPluginConfigReferent(ctx context.Context, dir, field, ref string) error {
	// Defense in depth: kustomizePluginConfigRefs already drops empty
	// referents, but addLocalRef has no empty check of its own and this is
	// the last guard before the listing directory gets owned.
	if ref == "" {
		return nil
	}
	path, err := c.resolveLocalRef(dir, field, ref)
	if err != nil {
		return c.skip(ctx, err)
	}
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		return c.skip(ctx, fmt.Errorf("kustomize %s %q is a directory; builtin plugin configs read files", field, ref))
	}
	return c.addAbsPath(ctx, path, false)
}

// pluginConfigEntryDocuments reads the plugin config documents a local path
// entry names: a file's documents, or a kustomization directory's resources.
// A remote entry is an error even when pinned: its builtin configs read their
// referents from the local listing directory, and they cannot be enumerated
// without acquiring the entry.
func (c *kustomizeInputCollector) pluginConfigEntryDocuments(ctx context.Context, dir, field, ref string) ([]*goyaml.Node, error) {
	if isRemoteKustomizeRef(ref) {
		return nil, fmt.Errorf("kustomize %s %q is a remote plugin config; the referents of its builtin configs cannot be enumerated", field, redactKustomizeRef(ref))
	}
	if filepath.IsAbs(ref) {
		return nil, fmt.Errorf("kustomize %s %q must be relative", field, ref)
	}
	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	if err := rejectPathOutsideBoundary("kustomize "+field, path, c.repoRoot); err != nil {
		return nil, err
	}
	if err := rejectSymlinkedPath(c.repoRoot, path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return c.pluginConfigDirectoryDocuments(ctx, field, ref, path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("kustomize %s %q is not a regular file or directory", field, ref)
	}
	return readPluginConfigDocuments(field, ref, path)
}

// pluginConfigDirectoryDocuments enumerates the config documents of a
// kustomization-directory entry. Kustomize builds that directory as a full
// target and configures every resulting resource as a plugin, so only a
// graph of plain local resources: is enumerable: any other field could
// generate or rewrite the config documents themselves. The graph's
// kustomization files and resources join the collection; the configs'
// referents still resolve against the outer listing directory.
func (c *kustomizeInputCollector) pluginConfigDirectoryDocuments(ctx context.Context, field, ref, path string) ([]*goyaml.Node, error) {
	_, graph, err := collectKustomizeGraph(ctx, c.repoRoot, path, kustomizeInputWalk)
	if err != nil {
		return nil, fmt.Errorf("kustomize %s %q: %w", field, ref, err)
	}
	for _, node := range graph {
		if err := requirePlainResourcesKustomization(node); err != nil {
			return nil, fmt.Errorf("kustomize %s %q: %w, so its plugin config documents cannot be enumerated", field, ref, err)
		}
	}
	var docs []*goyaml.Node
	for _, node := range graph {
		if err := c.collectNode(ctx, node); err != nil {
			return nil, err
		}
		for _, resource := range node.Kustomization.Resources {
			resourceDocs, err := c.plainResourceDocuments(node.Dir, field, resource)
			if err != nil {
				return nil, fmt.Errorf("kustomize %s %q: %s: %w", field, ref, node.ManifestPath, err)
			}
			docs = append(docs, resourceDocs...)
		}
	}
	return docs, nil
}

// requirePlainResourcesKustomization rejects a kustomization that sets any
// field besides apiVersion, kind, metadata and resources. An empty list or
// map (patches: []) builds like an absent field, so it does not count as set.
func requirePlainResourcesKustomization(node kustomizeGraphNode) error {
	rest := node.Kustomization
	rest.TypeMeta = types.TypeMeta{}
	rest.MetaData = nil
	rest.Resources = nil
	for _, field := range reflect.ValueOf(rest).Fields() {
		if isUnsetKustomizationField(field) {
			continue
		}
		return fmt.Errorf("%s sets fields other than resources", node.ManifestPath)
	}
	return nil
}

func isUnsetKustomizationField(field reflect.Value) bool {
	if kind := field.Kind(); kind == reflect.Slice || kind == reflect.Map {
		return field.Len() == 0
	}
	return field.IsZero()
}

// plainResourceDocuments reads one resources: entry of a plain plugin config
// kustomization. Directories are graph nodes of their own and contribute
// through their own resources.
func (c *kustomizeInputCollector) plainResourceDocuments(dir, field, resource string) ([]*goyaml.Node, error) {
	if resource == "" {
		return nil, nil
	}
	if isRemoteKustomizeRef(resource) {
		return nil, fmt.Errorf("resource %q is remote", redactKustomizeRef(resource))
	}
	if filepath.IsAbs(resource) {
		return nil, fmt.Errorf("resource %q must be relative", resource)
	}
	path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(resource)))
	if err := rejectPathOutsideBoundary("kustomize "+field+" resources", path, c.repoRoot); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("resource %q is not a regular file or directory", resource)
	}
	return readPluginConfigDocuments(field, resource, path)
}

func readPluginConfigDocuments(field, ref, path string) ([]*goyaml.Node, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	docs, err := decodeYAMLDocumentNodes(content)
	if err != nil {
		return nil, fmt.Errorf("kustomize %s %q: decode plugin config: %w", field, ref, err)
	}
	return docs, nil
}
