package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	goyaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/types"
)

// KSOPS generator manifests are matched by the exact apiVersion/kind strings
// the KSOPS README documents (apiVersion: viaduct.ai/v1, kind: ksops).
// Upstream KSOPS never validates these fields itself — kustomize dispatches on
// the config.kubernetes.io/function exec annotation and ksops.go unmarshals
// only files/secretFrom — so the README convention is the only identity the
// manifest shape carries. Exact matching fails closed: a variant-cased
// manifest is classified unsupported and rejected loudly instead of being
// silently dropped or handed to krusty unclassified.
const (
	ksopsGeneratorAPIVersion = "viaduct.ai/v1"
	ksopsGeneratorKind       = "ksops"

	ksopsRedactedPrefix       = "drydock-ksops-redacted-"
	ksopsEncryptedValuePrefix = "ENC["
)

// ksopsGeneratorOptionAnnotations are KSOPS's documented generator-option
// annotations. Real KSOPS output flows through kustomize's generator
// merge/hash machinery (AbsorbAll), which placeholder resources: injection
// cannot reproduce — manifests carrying them are rejected in both modes.
var ksopsGeneratorOptionAnnotations = []string{
	"kustomize.config.k8s.io/behavior",
	"kustomize.config.k8s.io/needs-hash",
}

type generatorDocumentClass int

const (
	generatorDocumentBuiltin generatorDocumentClass = iota
	generatorDocumentKSOPS
	generatorDocumentUnsupported
)

// classifyGeneratorDocument sorts one generator document into drydock's
// three-way scheme: KSOPS manifests (emulated under --enable-ksops-compat,
// actionable diagnostic without it), kustomize builtin generator configs
// (left in place — krusty's PluginRestrictionsBuiltinsOnly +
// BploUseStaticallyLinked options load them natively and they render today),
// and everything else (exec/container KRM functions, unknown kinds), which
// fails the source in both modes.
//
// CMP-native-fallback decision: a config management plugin wrapping
// kustomize+ksops is deliberately NOT forced into ksops-compat the way
// AVP-shaped CMPs force EnableAVPCompat (app/plugin_render_native.go). A
// ksops CMP's semantics are decryption; silently substituting placeholders
// under a policy plugin the operator explicitly trusted would be surprising.
// The mode-off diagnostic (actionable, names the flag) is the intended
// outcome there.
func classifyGeneratorDocument(root *goyaml.Node) (generatorDocumentClass, string, string) {
	apiVersion := yamlMappingString(root, "apiVersion")
	kind := yamlMappingString(root, "kind")
	switch {
	case apiVersion == ksopsGeneratorAPIVersion && kind == ksopsGeneratorKind:
		return generatorDocumentKSOPS, apiVersion, kind
	case apiVersion == konfig.BuiltinPluginApiVersion:
		return generatorDocumentBuiltin, apiVersion, kind
	default:
		return generatorDocumentUnsupported, apiVersion, kind
	}
}

func kustomizeGraphHasGenerators(graph []kustomizeGraphNode) bool {
	for _, node := range graph {
		if len(node.Kustomization.Generators) != 0 {
			return true
		}
	}
	return false
}

func ksopsCompatDiagnostic(substituted int) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     "kustomize.ksops-compat-substituted",
		Severity: diagnostic.SeverityWarning,
		Category: "kustomize",
		Message:  fmt.Sprintf("KSOPS generators were rendered as deterministic placeholder manifests without decryption (%d sops files substituted)", substituted),
	}
}

// prepareKustomizeGenerators classifies every generators: entry of a prepared
// kustomization and rewrites the kustomization so krusty never sees a
// generator entry drydock has not classified: KSOPS entries are dropped and
// replaced by generated placeholder resource files (mode ON) or fail the
// source (mode OFF); builtin generator configs are kept for krusty; anything
// else fails the source in both modes.
func (w *kustomizeWorkspace) prepareKustomizeGenerators(ctx context.Context, dir, boundaryRoot string, graphIndex int, kustomization *types.Kustomization) error {
	if len(kustomization.Generators) == 0 {
		return nil
	}
	remaining := make([]string, 0, len(kustomization.Generators))
	for entryIndex, entry := range kustomization.Generators {
		if err := ctx.Err(); err != nil {
			return err
		}
		keep, generated, err := w.prepareKustomizeGeneratorEntry(dir, boundaryRoot, graphIndex, entryIndex, entry)
		if err != nil {
			return err
		}
		if keep {
			remaining = append(remaining, entry)
		}
		kustomization.Resources = append(kustomization.Resources, generated...)
	}
	kustomization.Generators = remaining
	return nil
}

//nolint:gocyclo // Coordinates the inline-vs-path branch and three-way generator classification explicitly.
func (w *kustomizeWorkspace) prepareKustomizeGeneratorEntry(dir, boundaryRoot string, graphIndex, entryIndex int, entry string) (bool, []string, error) {
	docs, inline := inlineKustomizeGeneratorDocuments(entry)
	manifestDir := dir
	describe := strings.TrimSpace(entry)
	if inline {
		describe = describeInlineGeneratorEntry(docs)
	} else {
		path, info, err := validateWorkspaceLocalRef(boundaryRoot, dir, "generators", entry)
		if err != nil {
			return false, nil, err
		}
		if info == nil {
			return false, nil, fmt.Errorf("kustomize generators %q: generator manifest not found", entry)
		}
		if !info.Mode().IsRegular() {
			return false, nil, fmt.Errorf("kustomize generators %q: generator manifest must be a regular file", entry)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return false, nil, err
		}
		docs, err = decodeYAMLDocumentNodes(content)
		if err != nil {
			return false, nil, fmt.Errorf("kustomize generators %q: decode generator manifest: %w", entry, err)
		}
		manifestDir = filepath.Dir(path)
	}

	ksopsDocs := make([]*goyaml.Node, 0, len(docs))
	builtins := 0
	for _, doc := range docs {
		root := yamlDocumentRoot(doc)
		if root == nil {
			continue
		}
		class, apiVersion, kind := classifyGeneratorDocument(root)
		switch class {
		case generatorDocumentBuiltin:
			builtins++
		case generatorDocumentKSOPS:
			ksopsDocs = append(ksopsDocs, root)
		case generatorDocumentUnsupported:
			return false, nil, fmt.Errorf("kustomize generators entry %q: generator %s/%s is unsupported (drydock supports KSOPS generators via --enable-ksops-compat and kustomize builtin generator configs)", describe, apiVersion, kind)
		}
	}
	if len(ksopsDocs) == 0 && builtins == 0 {
		return false, nil, fmt.Errorf("kustomize generators entry %q: generator manifest has no generator documents", describe)
	}
	if len(ksopsDocs) != 0 && builtins != 0 {
		// Splitting a mixed manifest would require rewriting the workspace
		// copy of the file, which is hard-linked to the original repository
		// tree; reject loudly rather than render incompletely.
		return false, nil, fmt.Errorf("kustomize generators entry %q mixes KSOPS and builtin generator documents; split them into separate generator manifests", describe)
	}
	if len(ksopsDocs) == 0 {
		return true, nil, nil
	}
	if !w.opts.EnableKSOPSCompat {
		return false, nil, fmt.Errorf("kustomize generators entry %q is a KSOPS generator; pass --enable-ksops-compat to render deterministic placeholder manifests without decryption", describe)
	}
	generated := make([]string, 0, len(ksopsDocs))
	fileIndex := 0
	for _, root := range ksopsDocs {
		files, err := ksopsGeneratorFilesFromDocument(describe, root)
		if err != nil {
			return false, nil, err
		}
		for _, fileRef := range files {
			rel, err := w.emulateKSOPSGeneratorFile(dir, boundaryRoot, manifestDir, graphIndex, entryIndex, fileIndex, fileRef)
			if err != nil {
				return false, nil, err
			}
			fileIndex++
			generated = append(generated, rel)
		}
	}
	return false, generated, nil
}

func ksopsGeneratorFilesFromDocument(describe string, root *goyaml.Node) ([]string, error) {
	if annotation, ok := generatorOptionAnnotation(root); ok {
		return nil, fmt.Errorf("kustomize generators entry %q: generator option annotation %q is unsupported", describe, annotation)
	}
	for _, field := range []string{"secretFrom", "binaryFiles", "envs"} {
		if yamlMappingValue(root, field) != nil {
			return nil, fmt.Errorf("kustomize generators entry %q: ksops %s is unsupported (only files: entries referencing YAML sops manifests are supported)", describe, field)
		}
	}
	files := yamlMappingValue(root, "files")
	if files == nil || files.Kind != goyaml.SequenceNode || len(files.Content) == 0 {
		return nil, fmt.Errorf("kustomize generators entry %q: ksops manifest requires a files: list", describe)
	}
	out := make([]string, 0, len(files.Content))
	for _, item := range files.Content {
		if item.Kind != goyaml.ScalarNode || strings.TrimSpace(item.Value) == "" {
			return nil, fmt.Errorf("kustomize generators entry %q: ksops files entries must be relative paths", describe)
		}
		out = append(out, strings.TrimSpace(item.Value))
	}
	return out, nil
}

// emulateKSOPSGeneratorFile renders one KSOPS files: referent as deterministic
// placeholder manifests in the prepared workspace and returns the generated
// resource path (relative to the kustomization directory). KSOPS emits
// finished manifests — kustomize's nameReference and generator-option
// machinery does not treat generator output differently from raw resources
// unless the generator-option annotations (rejected above) are present — so
// resources: injection is faithful to real KSOPS semantics.
func (w *kustomizeWorkspace) emulateKSOPSGeneratorFile(dir, boundaryRoot, manifestDir string, graphIndex, entryIndex, fileIndex int, fileRef string) (string, error) {
	// KSOPS resolves files: entries relative to the generator manifest's
	// directory (README: paths are "relative to manifest files"; ksops.go
	// reads them from the exec function's working directory).
	path, info, err := validateWorkspaceLocalRef(boundaryRoot, manifestDir, "generators.files", fileRef)
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", fmt.Errorf("kustomize generators.files %q: sops file not found", fileRef)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("kustomize generators.files %q must be a regular file", fileRef)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	relPath, err := relativeManifestPath(w.tempRepoRoot, path)
	if err != nil {
		return "", err
	}
	substituted, err := substituteKSOPSSopsFile(filepath.ToSlash(relPath), content)
	if err != nil {
		return "", fmt.Errorf("kustomize generators.files %q: %w", fileRef, err)
	}
	// The fail-closed create in writeGeneratedKustomizeWorkspaceFile is
	// load-bearing: workspace files are hard links, so a repo-committed file
	// at this predictable path would otherwise be written through.
	generatedRel := filepath.ToSlash(filepath.Join(".drydock", "ksops", fmt.Sprintf("%03d-%03d-%03d-%s", graphIndex, entryIndex, fileIndex, safeGeneratedKSOPSBaseName(fileRef))))
	if err := writeGeneratedKustomizeWorkspaceFile(dir, generatedRel, substituted); err != nil {
		return "", fmt.Errorf("write ksops-compat manifests %s: %w", generatedRel, err)
	}
	w.ksopsSubstitutedFiles++
	return generatedRel, nil
}

// substituteKSOPSSopsFile derives deterministic placeholder manifests from a
// sops-encrypted YAML file without decryption: per document, the top-level
// sops metadata key is stripped and every string leaf beginning with ENC[ is
// replaced by an identity-derived marker. Identical input bytes yield
// identical output bytes (the yaml.Node round trip preserves document
// structure and key order).
func substituteKSOPSSopsFile(relPath string, content []byte) ([]byte, error) {
	docs, err := decodeYAMLDocumentNodes(content)
	if err != nil {
		return nil, fmt.Errorf("decode sops file: %w", err)
	}
	var buffer bytes.Buffer
	emitted := 0
	for docIndex, doc := range docs {
		root := yamlDocumentRoot(doc)
		if root == nil {
			continue
		}
		if annotation, ok := generatorOptionAnnotation(root); ok {
			return nil, fmt.Errorf("generator option annotation %q is unsupported", annotation)
		}
		removeYAMLMappingKey(root, "sops")
		// base64 applies only where Kubernetes requires it: Secret
		// data:/binaryData: and ConfigMap binaryData: (ConfigMap data: values
		// are plain strings — a base64'd marker there would be valid but not
		// grep-able).
		kind := yamlMappingString(root, "kind")
		substituteEncryptedYAMLValues(root, relPath, docIndex, nil, kind, false)
		data, err := goyaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("encode placeholder manifest: %w", err)
		}
		buffer.WriteString("---\n")
		buffer.Write(data)
		emitted++
	}
	if emitted == 0 {
		return nil, errors.New("sops file has no YAML documents")
	}
	return buffer.Bytes(), nil
}

// substituteEncryptedYAMLValues replaces every string leaf beginning with
// ENC[ — sops repositories vary encrypted_regex, so replacement is by value
// prefix, not by field name. Leaves under a Secret's data: or binaryData: map
// are base64-encoded (Kubernetes requires valid base64 there); everywhere
// else — including ConfigMap data: — the plain grep-able marker is emitted.
func substituteEncryptedYAMLValues(node *goyaml.Node, relPath string, docIndex int, path []string, kind string, underB64 bool) {
	switch node.Kind {
	case goyaml.DocumentNode:
		for _, child := range node.Content {
			substituteEncryptedYAMLValues(child, relPath, docIndex, path, kind, underB64)
		}
	case goyaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			childB64 := underB64 || ksopsBase64Eligible(kind, path, key)
			substituteEncryptedYAMLValues(node.Content[i+1], relPath, docIndex, append(path, key), kind, childB64)
		}
	case goyaml.SequenceNode:
		for i, child := range node.Content {
			substituteEncryptedYAMLValues(child, relPath, docIndex, append(path, strconv.Itoa(i)), kind, underB64)
		}
	case goyaml.ScalarNode:
		if node.Tag != "!!str" || !strings.HasPrefix(node.Value, ksopsEncryptedValuePrefix) {
			return
		}
		marker := ksopsRedactedValue(relPath, docIndex, strings.Join(path, "."))
		if underB64 {
			marker = base64.StdEncoding.EncodeToString([]byte(marker))
		}
		node.SetString(marker)
	case goyaml.AliasNode:
		// Alias targets are rewritten at their anchor definition.
	}
}

// ksopsBase64Eligible reports whether values under a top-level mapping key
// must be base64-encoded: Secret data: and binaryData:, and ConfigMap
// binaryData: (Kubernetes requires valid base64 there); everywhere else the
// plain marker stays grep-able.
func ksopsBase64Eligible(kind string, path []string, key string) bool {
	if len(path) != 0 {
		return false
	}
	switch kind {
	case "Secret":
		return key == "data" || key == "binaryData"
	case "ConfigMap":
		return key == "binaryData"
	default:
		return false
	}
}

// ksopsRedactedValue derives the deterministic placeholder for one encrypted
// string leaf. The identity is the repository-relative sops file path, the
// document index within it, and the YAML key path to the leaf — NOT the
// ciphertext — so value-only sops rotations render byte-identical
// placeholders while structural edits (new keys, renames) change the
// placeholder set.
func ksopsRedactedValue(sopsRelPath string, docIndex int, keyPath string) string {
	sum := sha256.Sum256([]byte(sopsRelPath + "\x00" + strconv.Itoa(docIndex) + "\x00" + keyPath))
	return ksopsRedactedPrefix + hex.EncodeToString(sum[:])[:12]
}

func safeGeneratedKSOPSBaseName(fileRef string) string {
	base := filepath.Base(filepath.FromSlash(strings.TrimSpace(fileRef)))
	base = strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(base)
	if base == "" || base == "." || base == ".." {
		base = "sops"
	}
	if !strings.HasSuffix(base, ".yaml") && !strings.HasSuffix(base, ".yml") {
		base += ".yaml"
	}
	return base
}

// inlineKustomizeGeneratorDocuments mirrors kustomize's inline-vs-path branch:
// kusttarget tries NewResMapFromBytes(entry) before treating an entry as a
// path (kustomize api internal/target/kusttarget.go,
// configureExternalGenerators), and only YAML mapping documents with
// apiVersion and kind parse as resources. Entries decoding to anything else
// (scalars, mappings without apiVersion/kind, invalid YAML) are paths.
func inlineKustomizeGeneratorDocuments(entry string) ([]*goyaml.Node, bool) {
	docs, err := decodeYAMLDocumentNodes([]byte(entry))
	if err != nil || len(docs) == 0 {
		return nil, false
	}
	seen := false
	for _, doc := range docs {
		root := yamlDocumentRoot(doc)
		if root == nil {
			continue
		}
		if root.Kind != goyaml.MappingNode {
			return nil, false
		}
		if yamlMappingString(root, "apiVersion") == "" || yamlMappingString(root, "kind") == "" {
			return nil, false
		}
		seen = true
	}
	if !seen {
		return nil, false
	}
	return docs, true
}

func isInlineKustomizeGeneratorEntry(entry string) bool {
	_, inline := inlineKustomizeGeneratorDocuments(entry)
	return inline
}

func describeInlineGeneratorEntry(docs []*goyaml.Node) string {
	for _, doc := range docs {
		root := yamlDocumentRoot(doc)
		if root == nil {
			continue
		}
		return fmt.Sprintf("inline %s/%s", yamlMappingString(root, "apiVersion"), yamlMappingString(root, "kind"))
	}
	return "inline generator"
}

// ksopsGeneratorFileRefs best-effort parses a local generator manifest and
// returns the files: referents of any KSOPS documents inside it. Errors are
// deliberately swallowed: prepareKustomizeGenerators is the authoritative
// gate and reports them loudly; here (graph validation, input digest, and
// workspace-copy walks) the refs only widen boundary validation, cache-key
// coverage, and the copy set.
func ksopsGeneratorFileRefs(path string) []string {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	docs, err := decodeYAMLDocumentNodes(content)
	if err != nil {
		return nil
	}
	return ksopsGeneratorFileRefsFromDocuments(docs)
}

// ksopsGeneratorFileRefsFromDocuments collects the files: referents of the
// KSOPS documents among docs. For inline generators: entries these referents
// resolve relative to the kustomization directory — the same base directory
// prepareKustomizeGeneratorEntry uses during emulation (manifestDir stays the
// kustomization dir for inline entries).
func ksopsGeneratorFileRefsFromDocuments(docs []*goyaml.Node) []string {
	var refs []string
	for _, doc := range docs {
		root := yamlDocumentRoot(doc)
		if root == nil {
			continue
		}
		if class, _, _ := classifyGeneratorDocument(root); class != generatorDocumentKSOPS {
			continue
		}
		refs = append(refs, yamlMappingStringSequence(root, "files")...)
	}
	return refs
}

// ksopsGeneratorFilePaths resolves a generator entry's KSOPS files: referents
// to cleaned absolute paths for the workspace copier. The copier walks source
// directories and directly referenced files only; a sops file living outside
// every copied path (legal under repo-root containment) would otherwise be
// absent from the temp tree when ksops-compat emulation reads it.
func ksopsGeneratorFilePaths(dir, ref string) []string {
	ref = strings.TrimSpace(ref)
	if ref == "" || isRemoteKustomizeRef(ref) || filepath.IsAbs(ref) {
		return nil
	}
	manifestPath := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	manifestDir := filepath.Dir(manifestPath)
	fileRefs := ksopsGeneratorFileRefs(manifestPath)
	out := make([]string, 0, len(fileRefs))
	for _, fileRef := range fileRefs {
		fileRef = strings.TrimSpace(fileRef)
		if fileRef == "" || isRemoteKustomizeRef(fileRef) || filepath.IsAbs(fileRef) {
			continue
		}
		out = append(out, filepath.Clean(filepath.Join(manifestDir, filepath.FromSlash(fileRef))))
	}
	return out
}

func generatorOptionAnnotation(root *goyaml.Node) (string, bool) {
	annotations := yamlMappingValue(yamlMappingValue(root, "metadata"), "annotations")
	for _, annotation := range ksopsGeneratorOptionAnnotations {
		if yamlMappingValue(annotations, annotation) != nil {
			return annotation, true
		}
	}
	return "", false
}

func decodeYAMLDocumentNodes(content []byte) ([]*goyaml.Node, error) {
	decoder := goyaml.NewDecoder(bytes.NewReader(content))
	var docs []*goyaml.Node
	for {
		var node goyaml.Node
		if err := decoder.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			return nil, err
		}
		docs = append(docs, &node)
	}
}

func yamlDocumentRoot(doc *goyaml.Node) *goyaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == goyaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		return doc.Content[0]
	}
	if doc.Kind == 0 {
		return nil
	}
	return doc
}

func yamlMappingValue(node *goyaml.Node, key string) *goyaml.Node {
	if node == nil || node.Kind != goyaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func yamlMappingString(node *goyaml.Node, key string) string {
	value := yamlMappingValue(node, key)
	if value == nil || value.Kind != goyaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(value.Value)
}

func yamlMappingStringSequence(node *goyaml.Node, key string) []string {
	value := yamlMappingValue(node, key)
	if value == nil || value.Kind != goyaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind == goyaml.ScalarNode {
			out = append(out, item.Value)
		}
	}
	return out
}

func removeYAMLMappingKey(node *goyaml.Node, key string) {
	if node == nil || node.Kind != goyaml.MappingNode {
		return
	}
	filtered := make([]*goyaml.Node, 0, len(node.Content))
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			continue
		}
		filtered = append(filtered, node.Content[i], node.Content[i+1])
	}
	node.Content = filtered
}
