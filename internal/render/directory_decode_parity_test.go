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
	"github.com/google/go-jsonnet"
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

// upstreamApplicationObjects carries objects from
// upstreamGetObjsFromYAMLOrJSON through the rest of the Argo CD v3.6.1 path
// with annotation tracking. See upstreamApplicationObjectsWithTracking.
func upstreamApplicationObjects(objs []*unstructured.Unstructured, appName, namespace string) ([]*unstructured.Unstructured, error) {
	return upstreamApplicationObjectsWithTracking(objs, appName, namespace, v1alpha1.TrackingMethodAnnotation)
}

// upstreamApplicationObjectsWithTracking carries the objects that
// findManifests collects through the rest of the Argo CD v3.6.1 path. The
// loop is a copy of the manifest loop in GenerateManifests
// (reposerver/repository/repository.go:1816-1852): Lists flatten into their
// items, a null-items List emits nothing, and every other object is emitted
// with tracking metadata. The tracking call is the upstream util/argo
// SetAppInstance itself. The controller then reads each manifest back with
// the upstream v1alpha1.UnmarshalToUnstructured (unmarshalManifests,
// controller/state.go:562-571), which fails on an object without a kind.
func upstreamApplicationObjectsWithTracking(objs []*unstructured.Unstructured, appName, namespace string, method v1alpha1.TrackingMethod) ([]*unstructured.Unstructured, error) {
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
				if err := tracking.SetAppInstance(target, common.LabelKeyAppInstance, appName, namespace, method, ""); err != nil {
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

// upstreamJsonnetObjects is the Jsonnet branch of findManifests (Argo CD
// v3.6.1 reposerver/repository/repository.go:2019-2042) after the file is
// evaluated: the output is decoded as an array of Unstructured objects, or
// else as one Unstructured object.
func upstreamJsonnetObjects(jsonStr, filename string) ([]*unstructured.Unstructured, error) {
	var jsonObjs []*unstructured.Unstructured
	err := json.Unmarshal([]byte(jsonStr), &jsonObjs)
	if err == nil {
		return jsonObjs, nil
	}
	var jsonObj unstructured.Unstructured
	if err := json.Unmarshal([]byte(jsonStr), &jsonObj); err != nil {
		return nil, fmt.Errorf("Failed to unmarshal generated json %q: %w", filename, err)
	}
	return []*unstructured.Unstructured{&jsonObj}, nil
}

// upstreamFindManifests is findManifests (Argo CD v3.6.1
// reposerver/repository/repository.go:2005-2051) over the files of one flat
// directory, read in the lexical order of filepath.Walk. A Jsonnet file is
// evaluated with a plain go-jsonnet VM, which is enough for files without
// imports, ext vars, or TLAs.
func upstreamFindManifests(files map[string]string) ([]*unstructured.Unstructured, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var objs []*unstructured.Unstructured
	for _, name := range names {
		if strings.HasSuffix(name, ".jsonnet") {
			jsonStr, err := jsonnet.MakeVM().EvaluateAnonymousSnippet(name, files[name])
			if err != nil {
				return nil, fmt.Errorf("Failed to evaluate jsonnet %q: %w", name, err)
			}
			fileObjs, err := upstreamJsonnetObjects(jsonStr, name)
			if err != nil {
				return nil, err
			}
			objs = append(objs, fileObjs...)
			continue
		}
		fileObjs, err := upstreamGetObjsFromYAMLOrJSON([]byte(files[name]), name)
		if err != nil {
			return nil, err
		}
		objs = append(objs, fileObjs...)
	}
	return objs, nil
}

// upstreamDirectoryOutcome is what Argo CD v3.6.1 does with a flat directory
// source: the objects the controller reads back, or the error that fails the
// Application.
func upstreamDirectoryOutcome(files map[string]string, method v1alpha1.TrackingMethod) ([]*unstructured.Unstructured, error) {
	return upstreamDirectoryOutcomeFor(files, "parity", "default", method)
}

// upstreamDirectoryOutcomeFor is upstreamDirectoryOutcome for a given
// Application instance name and destination namespace.
func upstreamDirectoryOutcomeFor(files map[string]string, appName, namespace string, method v1alpha1.TrackingMethod) ([]*unstructured.Unstructured, error) {
	objs, err := upstreamFindManifests(files)
	if err != nil {
		return nil, err
	}
	return upstreamApplicationObjectsWithTracking(objs, appName, namespace, method)
}

// upstreamTrackManifest is the TrackManifest check the Application layer
// passes to the directory renderer, for the test Application "parity" with
// destination namespace "default".
func upstreamTrackManifest(method v1alpha1.TrackingMethod) func(*unstructured.Unstructured) error {
	return upstreamTrackManifestFor("parity", "default", method)
}

// upstreamTrackManifestFor is the TrackManifest check for one Application:
// the upstream SetAppInstance, skipped for a CRD as GenerateManifests skips
// it, with the error text of GenerateManifests.
func upstreamTrackManifestFor(appName, namespace string, method v1alpha1.TrackingMethod) func(*unstructured.Unstructured) error {
	tracking := argoutil.NewResourceTracking()
	return func(obj *unstructured.Unstructured) error {
		if kube.IsCRD(obj) {
			return nil
		}
		if err := tracking.SetAppInstance(obj, common.LabelKeyAppInstance, appName, namespace, method, ""); err != nil {
			return fmt.Errorf("failed to set app instance tracking info on manifest: %w", err)
		}
		return nil
	}
}

// renderFlatDirectory renders files as one flat directory source with
// drydock, with the upstream tracking check.
func renderFlatDirectory(t *testing.T, files map[string]string, method v1alpha1.TrackingMethod, opts RenderOptions) ([]Manifest, error) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		writeFile(t, filepath.Join(root, "apps", name), content)
	}
	opts.TrackManifest = upstreamTrackManifest(method)
	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, opts)
	return result, err
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
// the Argo CD render parity smoke (testdata/argocd-parity) offline, with the
// Application's instance name, destination namespace, and annotation
// tracking. drydock must fail with the drydockMessage the smoke expects. The
// upstream oracle must also fail on the same directory with the
// argocdMessage the smoke expects. For a case whose Argo CD message quotes or
// names the object, that message comes from upstream code, so it must match
// exactly. The other messages come from a copy of upstream code whose
// wording differs in case, so they are compared without case. The smoke
// checks them against Argo CD itself.
func TestDirectoryRendererParityDecodeFixtures(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "testdata", "argocd-parity", "repo")
	const namespace = "parity-directory-decode-workloads"
	// TestGeneratedParityDecodeFixtures covers the cases of other sources.
	for _, tc := range readParityDecodeCases(t, true) {
		t.Run(tc.Name, func(t *testing.T) {
			caseDir := strings.TrimPrefix(tc.Name, "parity-decode-")
			sourcePath := filepath.Join("workloads", "directory-decode", caseDir)
			// A parity-directory-decode Application is outside the controller
			// namespace, so its instance name is <namespace>_<name>.
			appName := "parity-directory-decode_" + tc.Name
			_, _, drydockErr := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: sourcePath}, RenderOptions{
				TrackManifest: upstreamTrackManifestFor(appName, namespace, v1alpha1.TrackingMethodAnnotation),
			})
			if drydockErr == nil || !strings.Contains(drydockErr.Error(), tc.DrydockMessage) {
				t.Fatalf("drydock error = %v, want %q", drydockErr, tc.DrydockMessage)
			}
			entries, err := os.ReadDir(filepath.Join(repoRoot, sourcePath))
			if err != nil {
				t.Fatalf("ReadDir() error = %v", err)
			}
			files := map[string]string{}
			for _, entry := range entries {
				content, err := os.ReadFile(filepath.Join(repoRoot, sourcePath, entry.Name()))
				if err != nil {
					t.Fatalf("ReadFile() error = %v", err)
				}
				files[entry.Name()] = string(content)
			}
			_, upstreamErr := upstreamDirectoryOutcomeFor(files, appName, namespace, v1alpha1.TrackingMethodAnnotation)
			if upstreamErr == nil {
				t.Fatalf("upstream error = nil, want %q", tc.ArgoCDMessage)
			}
			if tc.ArgoCDQuotesObject || tc.ArgoCDNamesObject != "" {
				if !strings.Contains(upstreamErr.Error(), tc.ArgoCDMessage) {
					t.Fatalf("upstream error = %v, want %q", upstreamErr, tc.ArgoCDMessage)
				}
				return
			}
			if !strings.Contains(strings.ToLower(upstreamErr.Error()), strings.ToLower(tc.ArgoCDMessage)) {
				t.Fatalf("upstream error = %v, want %q in any case", upstreamErr, tc.ArgoCDMessage)
			}
		})
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
		// A document needs a kind but not an apiVersion. A document with
		// only an apiVersion stops the stream, so the file is skipped unless
		// it holds every manifest marker.
		"kind-only.yaml":             "kind: ConfigMap\nmetadata:\n  name: kind-only\n",
		"kind-only.json":             `{"kind":"ConfigMap","metadata":{"name":"kind-only-json"}}`,
		"apiversion-only.yaml":       "apiVersion: v1\nmetadata:\n  name: apiversion-only\n",
		"apiversion-markers.yaml":    "apiVersion: v1\nmetadata:\n  name: apiversion-markers\n  labels:\n    kind: x\n",
		"empty-kind-nometa.yaml":     "apiVersion: v1\nkind: \"\"\nfoo: bar\n",
		"empty-kind-meta.yaml":       "apiVersion: v1\nkind: \"\"\nmetadata:\n  name: x\n",
		"no-apiversion-list.yaml":    "kind: List\nitems:\n  - apiVersion: v1\n    kind: ConfigMap\n    metadata:\n      name: no-apiversion-list\n",
		"no-apiversion-list.json":    `{"kind":"List","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"no-apiversion-list-json"}}]}`,
		"no-apiversion-item.yaml":    "apiVersion: v1\nkind: List\nitems:\n  - kind: ConfigMap\n    metadata:\n      name: no-apiversion-item\n",
		"no-apiversion-noitems.yaml": "kind: List\nmetadata:\n  name: bare-list\n",
		// An object with a kind whose items field is neither an array nor
		// null is not a list, so it is emitted unchanged.
		"scalar-items-kind.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: scalar-items\nitems: text\n",
		"map-items-kind.yaml":    "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: map-items\nitems:\n  a: b\n",
		"scalar-items-kind.json": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"scalar-items-json"},"items":5}`,
		"null-items-spec.yaml":   "apiVersion: v1\nkind: Foo\nmetadata:\n  name: null-items-spec\nspec: {}\nitems: null\n",
		"null-items-list.yaml":   "apiVersion: v1\nkind: List\nitems: null\n",
		// A list item that is not an object fails the list split.
		"null-item.json":   `{"items":[null]}`,
		"null-item.yaml":   "apiVersion: v1\nkind: List\nitems:\n  - null\n",
		"scalar-item.yaml": "apiVersion: v1\nkind: List\nitems:\n  - 1\n",
		"array-item.yaml":  "apiVersion: v1\nkind: List\nitems:\n  - [a]\n",
		// Lists inside list items are not split again.
		"nested-list.json":          `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"List","items":[{"name":"x"}]}]}`,
		"nested-kindless-list.json": `{"apiVersion":"v1","kind":"List","items":[{"items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"nested"}}]}]}`,
		// Tracking metadata that cannot be set fails the source in the
		// repo-server, on a list item or on a top-level object, before the
		// controller checks the kind.
		"annotations-string-item.json":     `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"q","annotations":"x"}}]}`,
		"annotations-string-kindless.json": `{"apiVersion":"v1","kind":"List","items":[{"metadata":{"annotations":"x"}}]}`,
		"annotations-string.yaml":          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  annotations: x\n",
		"annotations-list.yaml":            "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  annotations:\n    - x\n",
		"annotations-number.yaml":          "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  annotations:\n    a: 1\n",
		"labels-string.yaml":               "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  labels: x\n",
		"metadata-string.yaml":             "apiVersion: v1\nkind: ConfigMap\nmetadata: text\n",
		"crd-annotations-string.yaml":      "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: crd\n  annotations: x\n",
		// The Unstructured decode checks the kind of the parsed group version
		// kind, which is empty when the apiVersion has two or more slashes.
		"bad-apiversion.json":        `{"apiVersion":"apps/v1/","kind":"Deployment","metadata":{"name":"typo"}}`,
		"bad-apiversion.yaml":        "apiVersion: a/b/c\nkind: ConfigMap\nmetadata:\n  name: x\n",
		"bad-apiversion-nometa.yaml": "apiVersion: a/b/c\nkind: ConfigMap\ndata:\n  a: b\n",
		"bad-apiversion-after.yaml":  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ok\n---\napiVersion: a/b/c\nkind: ConfigMap\n",
		"bad-apiversion-item.yaml":   "apiVersion: v1\nkind: List\nitems:\n  - apiVersion: a/b/c\n    kind: ConfigMap\n    metadata:\n      name: x\n",
		"bad-apiversion-list.yaml":   "apiVersion: a/b/c\nkind: List\nmetadata:\n  name: l\nitems: []\n",
		"slash-apiversion.yaml":      "apiVersion: /\nkind: ConfigMap\nmetadata:\n  name: slash\n",
		"number-apiversion.yaml":     "apiVersion: 5\nkind: ConfigMap\nmetadata:\n  name: number-apiversion\n",
		"utf16le.yaml":               utf16Text(t, xunicode.LittleEndian, "kind: Namespace\r\napiVersion: v1\r\nmetadata:\r\n  name: le\r\n---\r\nkind: ServiceAccount\r\napiVersion: v1\r\nmetadata:\r\n  name: le-sa\r\n"),
		"utf16be.yaml":               utf16Text(t, xunicode.BigEndian, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: be\n"),
		"utf16le.json":               utf16Text(t, xunicode.LittleEndian, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"le-json"}}`),
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
				TrackManifest:                   upstreamTrackManifest(v1alpha1.TrackingMethodAnnotation),
			})
			if (upstreamErr != nil) != (drydockErr != nil) {
				t.Fatalf("upstream error = %v, drydock error = %v", upstreamErr, drydockErr)
			}
			if upstreamErr != nil {
				return
			}
			if got, want := directoryManifestIDs(result), upstreamRenderedIDs(upstreamObjs); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered %#v, upstream %#v", got, want)
			}
		})
	}
}

// upstreamRenderedIDs lists the apiVersion, kind, and name of each object
// the controller reads back, sorted.
func upstreamRenderedIDs(objs []*unstructured.Unstructured) []string {
	ids := []string{}
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		ids = append(ids, obj.GetAPIVersion()+"/"+obj.GetKind()+"/"+obj.GetName())
	}
	sort.Strings(ids)
	return ids
}

// directoryManifestIDs is upstreamRenderedIDs for drydock's output.
func directoryManifestIDs(manifests []Manifest) []string {
	ids := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		ids = append(ids, manifest.Object.GetAPIVersion()+"/"+manifest.Object.GetKind()+"/"+manifest.Object.GetName())
	}
	sort.Strings(ids)
	return ids
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

// TestDirectoryRendererJsonnetMatchesUpstream compares a directory that holds
// one Jsonnet file with the upstream findManifests and GenerateManifests path.
// Jsonnet output joins the same list of objects as every other file, so a
// List is split into its items, a null list emits nothing, and an item
// without a kind fails in the controller. Each Unstructured decode needs a
// kind, so an object without a kind fails in the repo-server.
func TestDirectoryRendererJsonnetMatchesUpstream(t *testing.T) {
	const cm = `{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "j"}}`
	cases := map[string]struct {
		jsonnet string
		// wantErr is the text drydock's error must contain.
		wantErr string
	}{
		"list":                 {jsonnet: `{apiVersion: "v1", kind: "List", items: [` + cm + `]}`},
		"list-kindless-item":   {jsonnet: `{apiVersion: "v1", kind: "List", items: [{name: "web"}]}`, wantErr: "a.jsonnet output list item /items/0 has no kind"},
		"list-scalar-item":     {jsonnet: `{apiVersion: "v1", kind: "List", items: [1]}`, wantErr: "a.jsonnet output list item /items/0 is not an object"},
		"list-null-items":      {jsonnet: `{apiVersion: "v1", kind: "List", items: null}`},
		"list-empty-items":     {jsonnet: `{apiVersion: "v1", kind: "List", items: []}`},
		"list-no-apiversion":   {jsonnet: `{kind: "List", items: [` + cm + `]}`},
		"kindless-list":        {jsonnet: `{items: [` + cm + `]}`, wantErr: `failed to unmarshal generated json "apps/a.jsonnet": JSON object is missing kind`},
		"kindless-object":      {jsonnet: `{name: "x"}`, wantErr: `failed to unmarshal generated json "apps/a.jsonnet": JSON object is missing kind`},
		"apiversion-only":      {jsonnet: `{apiVersion: "v1", metadata: {name: "x"}}`, wantErr: "JSON object is missing kind"},
		"empty-object":         {jsonnet: `{}`, wantErr: "JSON object is missing kind"},
		"number-kind":          {jsonnet: `{apiVersion: "v1", kind: 5, metadata: {name: "x"}}`, wantErr: "JSON object is missing kind"},
		"kind-only":            {jsonnet: `{kind: "ConfigMap", metadata: {name: "kind-only"}}`},
		"scalar-items":         {jsonnet: `{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "scalar-items"}, items: "text"}`},
		"array":                {jsonnet: `[` + cm + `, {apiVersion: "v1", kind: "List", items: [{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "k"}}]}]`},
		"array-null":           {jsonnet: `[null, ` + cm + `]`},
		"array-kindless-item":  {jsonnet: `[` + cm + `, {name: "x"}]`, wantErr: `failed to unmarshal generated json "apps/a.jsonnet": output item 1: JSON object is missing kind`},
		"array-scalar":         {jsonnet: `[` + cm + `, 1]`, wantErr: "output item 1: JSON value is not an object"},
		"array-list-kindless":  {jsonnet: `[{apiVersion: "v1", kind: "List", items: [{name: "web"}]}]`, wantErr: "a.jsonnet output item 0 list item /items/0 has no kind"},
		"array-empty":          {jsonnet: `[]`},
		"null":                 {jsonnet: `null`},
		"scalar":               {jsonnet: `"text"`, wantErr: "JSON value is not an object"},
		"annotations-string":   {jsonnet: `{apiVersion: "v1", kind: "List", items: [{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "q", annotations: "x"}}]}`, wantErr: "a.jsonnet output list item /items/0: failed to set app instance tracking info on manifest"},
		"bad-apiversion":       {jsonnet: `{apiVersion: "apps/v1/", kind: "Deployment", metadata: {name: "x"}}`, wantErr: "JSON object is missing kind"},
		"bad-apiversion-item":  {jsonnet: `{apiVersion: "v1", kind: "List", items: [{apiVersion: "a/b/c", kind: "ConfigMap", metadata: {name: "x"}}]}`, wantErr: `a.jsonnet output list item /items/0 has apiVersion "a/b/c", which is not a valid group version, so it has no kind`},
		"tracking-before-kind": {jsonnet: `{apiVersion: "v1", kind: "List", items: [{name: "web"}, {apiVersion: "v1", kind: "ConfigMap", metadata: {name: "q", labels: {}, annotations: "x"}}]}`, wantErr: "a.jsonnet output list item /items/1: failed to set app instance tracking info on manifest"},
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tc := cases[name]
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"a.jsonnet": tc.jsonnet}
			upstreamObjs, upstreamErr := upstreamDirectoryOutcome(files, v1alpha1.TrackingMethodAnnotation)
			result, drydockErr := renderFlatDirectory(t, files, v1alpha1.TrackingMethodAnnotation, RenderOptions{})
			if (upstreamErr != nil) != (drydockErr != nil) {
				t.Fatalf("upstream error = %v, drydock error = %v", upstreamErr, drydockErr)
			}
			if tc.wantErr != "" {
				if drydockErr == nil || !strings.Contains(drydockErr.Error(), tc.wantErr) {
					t.Fatalf("drydock error = %v, want %q", drydockErr, tc.wantErr)
				}
				return
			}
			if upstreamErr != nil {
				t.Fatalf("upstream error = %v, want success", upstreamErr)
			}
			if got, want := directoryManifestIDs(result), upstreamRenderedIDs(upstreamObjs); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered %#v, upstream %#v", got, want)
			}
		})
	}
}

// TestDirectoryRendererJsonnetListItemsKeepTheirList pins that a List item
// from Jsonnet output carries the List as RootObject, as a List item from a
// YAML file does, and is emitted without tracking metadata: the Application
// layer adds it.
func TestDirectoryRendererJsonnetListItemsKeepTheirList(t *testing.T) {
	files := map[string]string{"a.jsonnet": `{apiVersion: "v1", kind: "List", items: [{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "j"}}]}`}
	result, err := renderFlatDirectory(t, files, v1alpha1.TrackingMethodAnnotation, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("rendered %d manifests, want 1", len(result))
	}
	if result[0].RootObject == nil || result[0].RootObject.GetKind() != "List" {
		t.Fatalf("RootObject = %#v, want the List", result[0].RootObject)
	}
	if result[0].Path != filepath.Join("apps", "a.jsonnet") {
		t.Fatalf("Path = %q, want apps/a.jsonnet", result[0].Path)
	}
	if annotations := result[0].Object.GetAnnotations(); len(annotations) != 0 {
		t.Fatalf("annotations = %#v, want none from the tracking check", annotations)
	}
}

// TestDirectoryRendererErrorOrderMatchesUpstream checks which file fails the
// source when several files fail at different stages. Argo CD v3.6.1 reads
// every file before it splits lists and sets tracking metadata, and the
// controller checks kinds last. So a file that fails an earlier stage wins,
// even when it comes later in the directory. Each case names the text each
// side's error must contain.
func TestDirectoryRendererErrorOrderMatchesUpstream(t *testing.T) {
	const badAnnotations = `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"bad-annotations","annotations":"x"}}]}`
	for _, tc := range []struct {
		name         string
		files        map[string]string
		wantUpstream string
		wantDrydock  string
	}{
		{
			name:         "decode beats list split",
			files:        map[string]string{"a.json": `{"items":[1]}`, "b.json": `{"apiVersion":"v1",`},
			wantUpstream: `failed to unmarshal "b.json"`,
			wantDrydock:  "apps/b.json: decode JSON object failed",
		},
		{
			name:         "decode beats tracking",
			files:        map[string]string{"a.json": badAnnotations, "b.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ok\n---\nfoo: bar\n"},
			wantUpstream: `failed to unmarshal "b.yaml"`,
			wantDrydock:  "apps/b.yaml document 1 is missing kind",
		},
		{
			name:         "decode beats kind",
			files:        map[string]string{"a.json": `{}`, "b.json": `[]`},
			wantUpstream: `failed to unmarshal "b.json"`,
			wantDrydock:  "apps/b.json: decode JSON object failed",
		},
		{
			name:         "jsonnet decode beats list split",
			files:        map[string]string{"a.json": `{"items":[1]}`, "b.jsonnet": `{name: "x"}`},
			wantUpstream: `Failed to unmarshal generated json "b.jsonnet"`,
			wantDrydock:  `failed to unmarshal generated json "apps/b.jsonnet"`,
		},
		{
			name:         "tracking beats kind",
			files:        map[string]string{"a.json": `{}`, "b.json": badAnnotations},
			wantUpstream: "failed to set app instance tracking info on manifest: failed to get annotations for /v1, Kind=ConfigMap /bad-annotations",
			wantDrydock:  "apps/b.json document 0 list item /items/0: failed to set app instance tracking info on manifest: failed to get annotations for /v1, Kind=ConfigMap /bad-annotations",
		},
		{
			name:         "list split beats kind",
			files:        map[string]string{"a.yaml": "apiVersion: v1\nkind: List\nitems:\n  - name: web\n", "b.json": `{"items":[1]}`},
			wantUpstream: "items member is not an object",
			wantDrydock:  "apps/b.json document 0 list item /items/0 is not an object",
		},
		{
			name:         "first list split wins",
			files:        map[string]string{"a.json": `{"items":[1]}`, "b.json": badAnnotations},
			wantUpstream: "items member is not an object",
			wantDrydock:  "apps/a.json document 0 list item /items/0 is not an object",
		},
		{
			name:         "first tracking failure wins",
			files:        map[string]string{"a.json": badAnnotations, "b.json": `{"items":[1]}`},
			wantUpstream: "Kind=ConfigMap /bad-annotations",
			wantDrydock:  "apps/a.json document 0 list item /items/0: failed to set app instance tracking info",
		},
		{
			name:         "first missing kind wins",
			files:        map[string]string{"a.yaml": "apiVersion: v1\nkind: List\nitems:\n  - name: first\n", "b.json": `{"name":"second"}`},
			wantUpstream: `Object 'Kind' is missing in '{"metadata":{"annotations":{"argocd.argoproj.io/tracking-id":"parity:/:default/"}},"name":"first"}`,
			wantDrydock:  "apps/a.yaml document 0 list item /items/0 has no kind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, upstreamErr := upstreamDirectoryOutcome(tc.files, v1alpha1.TrackingMethodAnnotation)
			if upstreamErr == nil || !strings.Contains(upstreamErr.Error(), tc.wantUpstream) {
				t.Fatalf("upstream error = %v, want %q", upstreamErr, tc.wantUpstream)
			}
			_, drydockErr := renderFlatDirectory(t, tc.files, v1alpha1.TrackingMethodAnnotation, RenderOptions{})
			if drydockErr == nil || !strings.Contains(drydockErr.Error(), tc.wantDrydock) {
				t.Fatalf("drydock error = %v, want %q", drydockErr, tc.wantDrydock)
			}
		})
	}
}

// TestDirectoryRendererTrackingMethodsMatchUpstream checks malformed
// annotations and labels on a list item and on a top-level object under each
// tracking method. Annotation tracking reads the annotations, label tracking
// reads the labels, and annotation+label tracking reads both. The upstream
// SetAppInstance decides each case.
func TestDirectoryRendererTrackingMethodsMatchUpstream(t *testing.T) {
	files := map[string]string{
		"item-annotations.json": `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"q","annotations":"x"}}]}`,
		"item-labels.json":      `{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"q","labels":"x"}}]}`,
		"annotations.yaml":      "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  annotations: x\n",
		"labels.yaml":           "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  labels: [x]\n",
		"label-number.yaml":     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  labels:\n    a: 1\n",
		"metadata.yaml":         "apiVersion: v1\nkind: ConfigMap\nmetadata: text\n",
		"null-annotations.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: q\n  annotations: null\n  labels: null\n",
	}
	methods := []v1alpha1.TrackingMethod{
		v1alpha1.TrackingMethodAnnotation,
		v1alpha1.TrackingMethodLabel,
		v1alpha1.TrackingMethodAnnotationAndLabel,
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, method := range methods {
		for _, name := range names {
			t.Run(string(method)+"/"+name, func(t *testing.T) {
				fileSet := map[string]string{name: files[name]}
				upstreamObjs, upstreamErr := upstreamDirectoryOutcome(fileSet, method)
				result, drydockErr := renderFlatDirectory(t, fileSet, method, RenderOptions{})
				if (upstreamErr != nil) != (drydockErr != nil) {
					t.Fatalf("upstream error = %v, drydock error = %v", upstreamErr, drydockErr)
				}
				if upstreamErr != nil {
					return
				}
				if got, want := directoryManifestIDs(result), upstreamRenderedIDs(upstreamObjs); !reflect.DeepEqual(got, want) {
					t.Fatalf("drydock rendered %#v, upstream %#v", got, want)
				}
			})
		}
	}
}

// TestDirectoryRendererParityRenderFixturesMatchUpstream renders the
// directories of the parity-directory-decode-skips and
// parity-directory-decode-jsonnet Applications of the Argo CD render parity
// smoke offline. drydock's output, with the same tracking annotation added,
// must equal what the upstream oracle emits, object for object.
func TestDirectoryRendererParityRenderFixturesMatchUpstream(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "testdata", "argocd-parity", "repo")
	for _, tc := range []struct {
		appName   string
		path      string
		namespace string
		want      int
	}{
		{appName: "parity-directory-decode-skips", path: "workloads/directory-decode/skips", namespace: "parity-directory-decode-skips", want: 9},
		{appName: "parity-directory-decode-jsonnet", path: "workloads/directory-decode/jsonnet", namespace: "parity-directory-decode-jsonnet", want: 5},
	} {
		t.Run(tc.appName, func(t *testing.T) {
			entries, err := os.ReadDir(filepath.Join(repoRoot, tc.path))
			if err != nil {
				t.Fatalf("ReadDir() error = %v", err)
			}
			files := map[string]string{}
			for _, entry := range entries {
				content, err := os.ReadFile(filepath.Join(repoRoot, tc.path, entry.Name()))
				if err != nil {
					t.Fatalf("ReadFile() error = %v", err)
				}
				files[entry.Name()] = string(content)
			}
			upstreamObjs, err := upstreamDirectoryOutcomeFor(files, tc.appName, tc.namespace, v1alpha1.TrackingMethodAnnotation)
			if err != nil {
				t.Fatalf("upstream error = %v", err)
			}
			track := upstreamTrackManifestFor(tc.appName, tc.namespace, v1alpha1.TrackingMethodAnnotation)
			result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: tc.path}, RenderOptions{TrackManifest: track})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			var got, want []string
			for _, manifest := range result {
				obj := manifest.Object.DeepCopy()
				if err := track(obj); err != nil {
					t.Fatalf("track %s: %v", manifest.Path, err)
				}
				got = append(got, marshalObject(t, obj))
			}
			for _, obj := range upstreamObjs {
				want = append(want, marshalObject(t, obj))
			}
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered\n%s\nupstream\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if len(got) != tc.want {
				t.Fatalf("rendered %d objects, want %d (update the README count too)", len(got), tc.want)
			}
		})
	}
}

func marshalObject(t *testing.T, obj *unstructured.Unstructured) string {
	t.Helper()
	data, err := json.Marshal(obj.Object)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(data)
}

// TestDirectoryRendererNamesAnApiVersionThatDoesNotParse pins the drydock
// message for an object whose apiVersion has two or more slashes. Argo CD
// v3.6.1 reports such an object as one without a kind, because the
// Unstructured decode checks the kind of the parsed group version kind.
func TestDirectoryRendererNamesAnApiVersionThatDoesNotParse(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"json":      {files: map[string]string{"a.json": `{"apiVersion":"apps/v1/","kind":"Deployment","metadata":{"name":"x"}}`}, want: `apps/a.json: JSON object has apiVersion "apps/v1/", which is not a valid group version, so it has no kind, so Argo CD fails the Application with "Object 'Kind' is missing"`},
		"yaml":      {files: map[string]string{"a.yaml": "apiVersion: a/b/c\nkind: ConfigMap\nmetadata:\n  name: x\n"}, want: `apps/a.yaml document 0 has apiVersion "a/b/c", which is not a valid group version, so it has no kind`},
		"list item": {files: map[string]string{"a.yaml": "apiVersion: v1\nkind: List\nitems:\n  - apiVersion: a/b/c\n    kind: ConfigMap\n"}, want: `apps/a.yaml document 0 list item /items/0 has apiVersion "a/b/c", which is not a valid group version, so it has no kind, so Argo CD fails`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderFlatDirectory(t, tc.files, v1alpha1.TrackingMethodAnnotation, RenderOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Render() error = %v, want %q", err, tc.want)
			}
		})
	}
}
