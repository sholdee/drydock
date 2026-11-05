package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// upstreamGetObjsFromYAMLOrJSON is getObjsFromYAMLOrJSON and splitYAMLOrJSON
// from Argo CD v3.6.1 reposerver/repository/repository.go, with
// utfutil.OpenFile(..., utfutil.UTF8) replaced by the reader utfutil v1.0.0
// NewReader builds for that hint, unicode.BOMOverride over the x/text UTF-8
// decoder (utfutil is not in drydock's module graph).
// TestDirectoryRendererDecodesUpstreamUTF16Fixture pins the UTF-16 case
// against upstream's own expectation rather than this copy.
func upstreamGetObjsFromYAMLOrJSON(data []byte, filename string) ([]*unstructured.Unstructured, error) {
	data, _, err := transform.Bytes(xunicode.BOMOverride(xunicode.UTF8.NewDecoder()), data)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(filename, ".json") {
		var obj unstructured.Unstructured
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoderErr := decoder.Decode(&obj)
		if decoderErr != nil {
			manifest := imagev1.Manifest{}
			if err := json.NewDecoder(bytes.NewReader(data)).Decode(&manifest); err != nil {
				return nil, fmt.Errorf("failed to unmarshal %q: %w", filename, decoderErr)
			}
		}
		if decoder.More() {
			return nil, fmt.Errorf("found multiple objects in %q", filename)
		}
		return []*unstructured.Unstructured{&obj}, nil
	}
	decoder := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var objs []*unstructured.Unstructured
	for {
		u := &unstructured.Unstructured{}
		if err := decoder.Decode(&u); err != nil {
			if errors.Is(err, io.EOF) {
				return objs, nil
			}
			if len(objs) > 0 {
				return nil, fmt.Errorf("failed to unmarshal %q: %w", filename, err)
			}
			if bytes.Contains(data, []byte("apiVersion:")) &&
				bytes.Contains(data, []byte("kind:")) &&
				bytes.Contains(data, []byte("metadata:")) {
				return nil, fmt.Errorf("failed to unmarshal %q: %w", filename, err)
			}
			return nil, nil
		}
		if u == nil {
			continue
		}
		objs = append(objs, u)
	}
}

// TestDirectoryRendererDecodeMatchesUpstream compares, file by file, whether
// Argo CD v3.6.1 fails a directory source and which objects it renders with
// drydock's directory renderer under disableExtensionFilter, where any file
// name reaches the decoder.
func TestDirectoryRendererDecodeMatchesUpstream(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	cases := map[string]string{
		// ".json" suffix: one JSON object with a kind, or an OCI image manifest.
		"object.json":    `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"object"}}`,
		"empty.json":     ``,
		"blank.json":     "  \n",
		"array.json":     `[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"a"}}]`,
		"scalar.json":    `"text"`,
		"yaml.json":      "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: yamljson\n",
		"broken.json":    `{"apiVersion":"v1","kind":"ConfigMap",`,
		"multi.json":     "{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"m1\"}}\n{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"m2\"}}\n",
		"bom-multi.json": bom + "{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"b1\"}}\n{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"b2\"}}\n",
		"bom.json":       bom + `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"bom-json"}}`,
		"trailing.json":  "{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"t\"}}\n---\n",
		"oci.json":       `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`,
		"list.json":      `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"li"}}]}`,
		// Every other file name: a YAML/JSON stream.
		"bom.yaml":            bom + "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bom-yaml\n",
		"bom-stream.yaml":     bom + "{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"s1\"}}\n{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"s2\"}}\n",
		"jsonstream.manifest": "{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"j1\"}}\n{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"j2\"}}\n",
		"flow.sealed":         "{apiVersion: v1, kind: ConfigMap, metadata: {name: flow}}\n",
		"array.yaml":          `["plain", "array"]`,
		"script.sh":           "#!/bin/sh\necho hi\n",
		"Makefile":            "build:\n\tgo build ./...\n",
		"gitkeep":             "",
		"notes.txt":           "kind: of a note\nsee apiVersion: and metadata: fields\n",
		"jsonish.txt":         `{"apiVersion": 3}`,
		"values.yaml.tmpl":    "replicas: {{ .Values.replicas }}\n",
		"partial.txt":         "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ok\n---\n: bad: [\n",
		"partial-nometa.txt":  "apiVersion: v1\nkind: Foo\n---\n: bad: [\n",
		"broken-flat.yaml":    "apiVersion: v1\nkind: ConfigMap\ndata: [unclosed\n",
		"quoted-kind.json5":   "{\n  // renovate\n  \"kind\": \"x\",\n  extends: [\"a\"],\n}\n",
		"broken-json.yaml":    `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "x"`,
		"lib.libsonnet":       "local k = import 'k.libsonnet';\n{\n  configMap(name):: {\n    apiVersion: 'v1',\n    kind: 'ConfigMap',\n    metadata: { name: name },\n  },\n}\n",
		"readme.md":           "# Deploy\n\n```yaml\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n```\n",
		"readme-indented.md":  "# Usage\n\n1. Create:\n   ```yaml\n   apiVersion: v1\n   kind: ConfigMap\n   metadata:\n     name: x\n   ```\n",
		"tmpl.tpl":            "{{- define \"cm\" -}}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .name }}\n{{- end }}\n",
		"heredoc.sh":          "#!/bin/sh\ncat <<EOT | kubectl apply -f -\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: x\nEOT\n",
		// A non-empty document without a string kind stops the stream.
		"kindless-after.yaml":     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\nfoo: bar\n",
		"kindless-before.yaml":    "foo: bar\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n",
		"kindless-nometa.yaml":    "foo: bar\n---\napiVersion: v1\nkind: ConfigMap\n",
		"empty-map-after.yaml":    "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n---\n{}\n",
		"empty-map.yaml":          "{}\n",
		"values.yaml":             "replicas: 2\nimage:\n  tag: v1\n",
		"apiversion-only-cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: d\n---\napiVersion: v1\nfoo: bar\n",
		"numeric-kind.yaml":       "apiVersion: v1\nkind: 5\nmetadata:\n  name: e\n",
		"null-docs.yaml":          "---\nnull\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: f\n---\n",
		"sequence-after.yaml":     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: g\n---\n- x\n",
		"invalid-utf8.yaml":       "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: h\ndata:\n  k: \"\xff\"\n",
		"utf16le.yaml":            utf16Text(t, xunicode.LittleEndian, "kind: Namespace\r\napiVersion: v1\r\nmetadata:\r\n  name: le\r\n---\r\nkind: ServiceAccount\r\napiVersion: v1\r\nmetadata:\r\n  name: le-sa\r\n"),
		"utf16be.yaml":            utf16Text(t, xunicode.BigEndian, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: be\n"),
		"utf16le.json":            utf16Text(t, xunicode.LittleEndian, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"le-json"}}`),
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		content := cases[name]
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "apps", name)
			writeFile(t, path, content)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}

			upstreamObjs, upstreamErr := upstreamGetObjsFromYAMLOrJSON(data, name)
			result, _, drydockErr := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{
				DirectoryDisableExtensionFilter: true,
				DirectoryInclude:                "*",
			})
			if (upstreamErr != nil) != (drydockErr != nil) {
				t.Fatalf("upstream error = %v, drydock error = %v", upstreamErr, drydockErr)
			}
			if upstreamErr != nil {
				return
			}
			if got, want := directoryManifestNames(result), upstreamRenderedNames(upstreamObjs); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered %#v, upstream %#v", got, want)
			}
		})
	}
}

// upstreamRenderedNames flattens Lists as GenerateManifests does and drops
// the kind-less object a ".json" OCI image manifest leaves behind, which
// drydock skips (a recorded divergence). Files whose outcome turns on a
// document with only one of apiVersion and kind are not compared: drydock
// fails them where the repo-server renders the document (kind only) or skips
// the file (apiVersion only, with nothing decoded before it and not every
// manifest marker), both recorded divergences.
func upstreamRenderedNames(objs []*unstructured.Unstructured) []string {
	names := []string{}
	for _, obj := range objs {
		if obj == nil || obj.GetKind() == "" {
			continue
		}
		if obj.IsList() {
			_ = obj.EachListItem(func(item runtime.Object) error {
				accessor, ok := item.(*unstructured.Unstructured)
				if !ok {
					return fmt.Errorf("list item has type %T", item)
				}
				names = append(names, accessor.GetName())
				return nil
			})
			continue
		}
		names = append(names, obj.GetName())
	}
	sort.Strings(names)
	return names
}

// utf16Text encodes text as UTF-16 with a byte order mark, as Windows
// editors save it.
func utf16Text(t *testing.T, order xunicode.Endianness, text string) string {
	t.Helper()
	encoded, _, err := transform.Bytes(xunicode.UTF16(order, xunicode.UseBOM).NewEncoder(), []byte(text))
	if err != nil {
		t.Fatalf("encode UTF-16: %v", err)
	}
	return string(encoded)
}

// TestDirectoryRendererDecodesUpstreamUTF16Fixture is Argo CD v3.6.1
// reposerver/repository TestGenerateFromUTF16: testdata/utf-16/utf-16.yaml, a
// UTF-16LE file with a byte order mark and CRLF line endings, renders both of
// its objects. A UTF-16 file holding the skip-file-rendering marker is
// skipped.
func TestDirectoryRendererDecodesUpstreamUTF16Fixture(t *testing.T) {
	text := "kind: Namespace\r\napiVersion: v1\r\nmetadata:\r\n  name: linkerd\r\n  annotations:\r\n    linkerd.io/inject: disabled\r\n---\r\n" +
		"kind: ServiceAccount\r\napiVersion: v1\r\nmetadata:\r\n  name: linkerd-controller\r\n  namespace: linkerd\r\n"
	data := utf16Text(t, xunicode.LittleEndian, text)
	if !strings.HasPrefix(data, "\xff\xfek\x00") {
		t.Fatalf("fixture does not start with a UTF-16LE byte order mark: %q", data[:4])
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "utf-16.yaml"), data)
	// getPotentiallyValidManifestFile looks for the skip marker in the text
	// utfutil.ReadFile decodes.
	writeFile(t, filepath.Join(root, "apps", "skipped.yaml"), utf16Text(t, xunicode.LittleEndian,
		"# +argocd:skip-file-rendering\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: skipped\n"))
	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got, want := directoryManifestNames(result), []string{"linkerd", "linkerd-controller"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered %#v, want %#v", got, want)
	}
}
