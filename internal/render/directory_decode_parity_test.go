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

	"github.com/argoproj/argo-cd/gitops-engine/v3/pkg/utils/kube"
	"github.com/argoproj/argo-cd/v3/common"
	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoutil "github.com/argoproj/argo-cd/v3/util/argo"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
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

// upstreamApplicationObjects carries objects from
// upstreamGetObjsFromYAMLOrJSON through the rest of the Argo CD v3.6.1 path.
// The loop is a copy of the manifest loop in GenerateManifests
// (reposerver/repository/repository.go:1817-1852): Lists flatten into their
// items, a null-items List emits nothing, and every other object is emitted
// with annotation tracking. The tracking call is the upstream
// util/argo SetAppInstance itself. The controller then reads each manifest
// back with the upstream v1alpha1.UnmarshalToUnstructured (unmarshalManifests,
// controller/state.go:562-571), which fails on an object without a kind.
func upstreamApplicationObjects(objs []*unstructured.Unstructured, appName, namespace string) ([]*unstructured.Unstructured, error) {
	tracking := argoutil.NewResourceTracking()
	var manifests []string
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		var targets []*unstructured.Unstructured
		switch {
		case obj.IsList():
			err := obj.EachListItem(func(object runtime.Object) error {
				unstructuredObj, ok := object.(*unstructured.Unstructured)
				if ok {
					targets = append(targets, unstructuredObj)
					return nil
				}
				return errors.New("resource list item has unexpected type")
			})
			if err != nil {
				return nil, err
			}
		case upstreamIsNullList(obj):
		default:
			targets = []*unstructured.Unstructured{obj}
		}
		for _, target := range targets {
			if !kube.IsCRD(target) {
				if err := tracking.SetAppInstance(target, common.LabelKeyAppInstance, appName, namespace, v1alpha1.TrackingMethodAnnotation, ""); err != nil {
					return nil, fmt.Errorf("failed to set app instance tracking info on manifest: %w", err)
				}
			}
			manifest, err := json.Marshal(target.Object)
			if err != nil {
				return nil, err
			}
			manifests = append(manifests, string(manifest))
		}
	}
	out := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, manifest := range manifests {
		obj, err := v1alpha1.UnmarshalToUnstructured(manifest)
		if err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, nil
}

// upstreamIsNullList is isNullList from Argo CD v3.6.1
// reposerver/repository/repository.go:1988-2000.
func upstreamIsNullList(obj *unstructured.Unstructured) bool {
	if _, ok := obj.Object["spec"]; ok {
		return false
	}
	if _, ok := obj.Object["status"]; ok {
		return false
	}
	field, ok := obj.Object["items"]
	if !ok {
		return false
	}
	return field == nil
}

// upstreamDirectoryFileOutcome is what Argo CD v3.6.1 does with one
// directory file: the objects the controller reads back, or the error that
// fails the Application.
func upstreamDirectoryFileOutcome(data []byte, filename string) ([]*unstructured.Unstructured, error) {
	objs, err := upstreamGetObjsFromYAMLOrJSON(data, filename)
	if err != nil {
		return nil, err
	}
	return upstreamApplicationObjects(objs, "parity", "default")
}

// TestDirectoryRendererParityDecodeFixtures renders each failing directory of
// the Argo CD render parity smoke (testdata/argocd-parity) offline. drydock
// must fail with the drydockMessage the smoke expects. For a case whose Argo
// CD message quotes the manifest, the upstream oracle must also fail with
// the argocdMessage the smoke expects, given the Application's instance name
// and destination namespace. The repo-server messages of the other cases come
// from a copy of upstream code, so the smoke alone checks them.
func TestDirectoryRendererParityDecodeFixtures(t *testing.T) {
	parityRoot := filepath.Join("..", "..", "testdata", "argocd-parity")
	data, err := os.ReadFile(filepath.Join(parityRoot, "directory-decode-expected.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var expected struct {
		Cases []struct {
			Name               string `json:"name"`
			File               string `json:"file"`
			ArgoCDMessage      string `json:"argocdMessage"`
			DrydockMessage     string `json:"drydockMessage"`
			ArgoCDQuotesObject bool   `json:"argocdQuotesObject"`
		} `json:"cases"`
	}
	if err := yaml.Unmarshal(data, &expected); err != nil {
		t.Fatalf("parse expected file: %v", err)
	}
	repoRoot := filepath.Join(parityRoot, "repo")
	quoted := 0
	for _, tc := range expected.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			caseDir := strings.TrimPrefix(tc.Name, "parity-decode-")
			sourcePath := filepath.Join("workloads", "directory-decode", caseDir)
			_, _, drydockErr := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: sourcePath}, RenderOptions{})
			if drydockErr == nil || !strings.Contains(drydockErr.Error(), tc.DrydockMessage) {
				t.Fatalf("drydock error = %v, want %q", drydockErr, tc.DrydockMessage)
			}
			if !tc.ArgoCDQuotesObject {
				return
			}
			quoted++
			// A parity-directory-decode Application is outside the controller
			// namespace, so its instance name is <namespace>_<name>.
			appName := "parity-directory-decode_" + tc.Name
			entries, err := os.ReadDir(filepath.Join(repoRoot, sourcePath))
			if err != nil {
				t.Fatalf("ReadDir() error = %v", err)
			}
			var objs []*unstructured.Unstructured
			for _, entry := range entries {
				content, err := os.ReadFile(filepath.Join(repoRoot, sourcePath, entry.Name()))
				if err != nil {
					t.Fatalf("ReadFile() error = %v", err)
				}
				fileObjs, err := upstreamGetObjsFromYAMLOrJSON(content, entry.Name())
				if err != nil {
					t.Fatalf("upstream repo-server error = %v, want the controller to fail", err)
				}
				objs = append(objs, fileObjs...)
			}
			_, upstreamErr := upstreamApplicationObjects(objs, appName, "parity-directory-decode-workloads")
			if upstreamErr == nil || !strings.Contains(upstreamErr.Error(), tc.ArgoCDMessage) {
				t.Fatalf("upstream error = %v, want %q", upstreamErr, tc.ArgoCDMessage)
			}
		})
	}
	if quoted == 0 {
		t.Fatal("expected file has no argocdQuotesObject case")
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
		// ".json" content without a kind that passes the OCI image manifest
		// test: the repo-server emits it and the controller fails on it,
		// unless it is a List shape.
		"empty-object.json":         `{}`,
		"null.json":                 `null`,
		"package.json":              `{"name":"web","version":"1.0.0","private":true,"scripts":{"build":"vite build"},"dependencies":{"vite":"^5.0.0"}}`,
		"tsconfig.json":             `{"compilerOptions":{"strict":true,"target":"ES2022"},"include":["src"]}`,
		"apiversion-only.json":      `{"apiVersion":"v1","metadata":{"name":"no-kind"}}`,
		"empty-kind.json":           `{"apiVersion":"v1","kind":"","metadata":{"name":"empty-kind"}}`,
		"number-kind.json":          `{"apiVersion":"v1","kind":5,"metadata":{"name":"number-kind"}}`,
		"null-items-spec.json":      `{"items":null,"spec":{}}`,
		"scalar-items.json":         `{"items":"text"}`,
		"bad-metadata.json":         `{"metadata":"text"}`,
		"kindless-list.json":        `{"items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"kindless-list"}}]}`,
		"apiversion-list.json":      `{"apiVersion":"v1","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"apiversion-list"}}]}`,
		"kindless-empty-list.json":  `{"items":[]}`,
		"kindless-null-items.json":  `{"items":null}`,
		"kindless-item-scalar.json": `{"items":[1]}`,
		// A list item without a kind is emitted with the tracking
		// annotation, and the controller fails on it. This holds for a list
		// with or without a kind of its own, in a ".json" file or a stream.
		"kindless-list-empty-item.json":      `{"items":[{}]}`,
		"kindless-list-named-item.json":      `{"items":[{"name":"web"}]}`,
		"kindless-list-apiversion-item.json": `{"items":[{"apiVersion":"v1","metadata":{"name":"no-kind"}}]}`,
		"kindless-list-mixed-items.json":     `{"items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"mixed"}},{"name":"web"}]}`,
		"list-named-item.json":               `{"apiVersion":"v1","kind":"List","items":[{"name":"web"}]}`,
		"list-empty-kind-item.json":          `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"","metadata":{"name":"empty-kind"}}]}`,
		"list-number-kind-item.json":         `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":5,"metadata":{"name":"number-kind"}}]}`,
		"list-bad-metadata-item.json":        `{"apiVersion":"v1","kind":"List","items":[{"metadata":"text"}]}`,
		"list-named-item.yaml":               "apiVersion: v1\nkind: List\nitems:\n  - name: web\n",
		"list-empty-item.yaml":               "apiVersion: v1\nkind: List\nitems:\n  - {}\n",
		"list-mixed-items.yaml":              "apiVersion: v1\nkind: List\nitems:\n  - apiVersion: v1\n    kind: ConfigMap\n    metadata:\n      name: mixed\n  - name: web\n",
		"configmap-items.yaml":               "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: items\nitems:\n  - name: web\n",
		// A number out of float64 range fails the Unstructured decode before
		// the kind check and leaves no object, so even a ConfigMap takes
		// this path.
		"out-of-range.json": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"out-of-range"},"data":{"n":1e400}}`,
		// ".json" content without a kind that fails the OCI image manifest
		// test: the repo-server fails the source with the decode error.
		"oci-string-config.json":     `{"name":"web","config":"text"}`,
		"oci-number-annotation.json": `{"annotations":{"count":1}}`,
		"oci-string-version.json":    `{"apiVersion":"v1","schemaVersion":"2"}`,
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

			upstreamObjs, upstreamErr := upstreamDirectoryFileOutcome(data, name)
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

// upstreamRenderedNames lists the names of the objects the controller reads
// back. Files whose outcome turns on a document with only one of apiVersion
// and kind are not compared: drydock fails them where the repo-server renders
// the document (kind only) or skips the file (apiVersion only, with nothing
// decoded before it and not every manifest marker), both recorded
// divergences.
func upstreamRenderedNames(objs []*unstructured.Unstructured) []string {
	names := []string{}
	for _, obj := range objs {
		if obj == nil {
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
