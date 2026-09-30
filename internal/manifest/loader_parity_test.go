package manifest

import (
	"bytes"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/gitops-engine/pkg/utils/kube"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// The expected objects in this file are what Argo CD v3.5.3's own readers
// return for the same text:
//   - gitops-engine kube.SplitYAML, which the repo-server applies to helm,
//     kustomize and config management plugin output (reposerver/repository/
//     repository.go:1411 and :2358, util/kustomize/kustomize.go:399);
//   - the directory source's splitYAMLOrJSON (repository.go:2083-2100), which
//     reads the manifests the way kubectl's StreamVisitor does
//     (cli-runtime pkg/resource/visitor.go).
//
// All of them split the stream with apimachinery's YAMLOrJSONDecoder and
// convert YAML through sigs.k8s.io/yaml, which parses YAML 1.1
// (go.yaml.in/yaml/v2). The test also runs both readers live, so the pinned
// expectations stay tied to the linked gitops-engine and apimachinery.

type argoReaderCase struct {
	name  string
	input string
	// want is what every reader produces.
	want []map[string]any
	// wantGenerated, when set, is what kube.SplitYAML (and
	// DecodeGeneratedDocuments) produces where it differs from the
	// kubectl/directory reading in want.
	wantGenerated []map[string]any
}

func TestDecodeDocumentsMatchesArgoCDManifestReaders(t *testing.T) {
	for _, tt := range []argoReaderCase{
		{
			name: "YAML 1.1 booleans",
			input: `apiVersion: example.com/v1
kind: Widget
metadata:
  name: bools
spec:
  a: no
  b: on
  c: y
  d: Off
  e: yes
  f: "yes"
  g: N
  h: true
`,
			want: []map[string]any{{
				"apiVersion": "example.com/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "bools"},
				"spec": map[string]any{
					"a": false, "b": true, "c": true, "d": false,
					"e": true, "f": "yes", "g": false, "h": true,
				},
			}},
		},
		{
			name: "non-string keys become strings",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: keys
data:
  y: a
  on: b
  8080: c
  1.5: d
  no: e
`,
			want: []map[string]any{{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": "keys"},
				"data":     map[string]any{"true": "b", "8080": "c", "1.5": "d", "false": "e"},
			}},
		},
		{
			name: "timestamps keep their text",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: dates
data:
  d1: 2024-01-02
  d2: 2024-01-02 03:04:05
  d3: 2024-01-02T03:04:05.000Z
`,
			want: []map[string]any{{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": "dates"},
				"data": map[string]any{
					"d1": "2024-01-02", "d2": "2024-01-02 03:04:05", "d3": "2024-01-02T03:04:05.000Z",
				},
			}},
		},
		{
			name: "duplicate keys keep the last value",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: first
metadata:
  name: second
data:
  k: one
  k: two
`,
			want: []map[string]any{{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": "second"},
				"data":     map[string]any{"k": "two"},
			}},
		},
		{
			name: "numbers",
			input: `apiVersion: example.com/v1
kind: Widget
metadata:
  name: numbers
spec:
  one: 1.0
  mil: 1e6
  big: 9223372036854775808
  neg: -9223372036854775809
  max: 9223372036854775807
  half: 1.5
  hex: 0x1F
  oct: 017
  int: 42
`,
			want: []map[string]any{{
				"apiVersion": "example.com/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "numbers"},
				"spec": map[string]any{
					"one": int64(1), "mil": int64(1000000),
					"big": float64(9223372036854775808), "neg": float64(-9223372036854775809),
					"max": int64(math.MaxInt64), "half": 1.5,
					"hex": int64(31), "oct": int64(15), "int": int64(42),
				},
			}},
		},
		{
			name: "empty documents between separators",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: a
---
---
# only a comment
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: b
`,
			want: []map[string]any{
				{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "a"}},
				{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "b"}},
			},
		},
		{
			name:  "JSON stream",
			input: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"json"},"data":{"one":1.0,"mil":1e6,"int":7}}` + "\n",
			want: []map[string]any{{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": "json"},
				"data":     map[string]any{"one": float64(1), "mil": float64(1000000), "int": int64(7)},
			}},
			wantGenerated: []map[string]any{{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": "json"},
				"data":     map[string]any{"one": int64(1), "mil": int64(1000000), "int": int64(7)},
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantGenerated := tt.wantGenerated
			if wantGenerated == nil {
				wantGenerated = tt.want
			}

			assertDocumentObjects(t, "DecodeDocuments", tt.input, DecodeDocuments, tt.want)
			assertDocumentObjects(t, "DecodeGeneratedDocuments", tt.input, DecodeGeneratedDocuments, wantGenerated)

			split, err := kube.SplitYAML([]byte(tt.input))
			if err != nil {
				t.Fatalf("kube.SplitYAML() error = %v", err)
			}
			if got := unstructuredObjects(split); !reflect.DeepEqual(got, wantGenerated) {
				t.Fatalf("kube.SplitYAML() = %#v, want %#v (pinned expectation no longer matches gitops-engine)", got, wantGenerated)
			}
			direct, err := argoSplitYAMLOrJSON(tt.input)
			if err != nil {
				t.Fatalf("splitYAMLOrJSON() error = %v", err)
			}
			if got := unstructuredObjects(direct); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitYAMLOrJSON() = %#v, want %#v (pinned expectation no longer matches apimachinery)", got, tt.want)
			}
		})
	}
}

func TestDecodeDocumentsRejectsContentAfterDocumentSeparator(t *testing.T) {
	const secretValue = "secret-token-value"
	input := "--- {apiVersion: v1, kind: Secret, metadata: {name: s}, data: {token: " + secretValue + "}}\n"

	// Argo CD v3.5.3 refuses the same text: apimachinery's YAMLReader allows
	// only a comment after --- (util/yaml/decoder.go:413-422).
	if _, err := kube.SplitYAML([]byte(input)); err == nil {
		t.Fatal("kube.SplitYAML() error = nil, want invalid separator error")
	}
	for name, decode := range map[string]func(string, io.Reader) ([]Document, error){
		"DecodeDocuments":          DecodeDocuments,
		"DecodeDocumentRoots":      DecodeDocumentRoots,
		"DecodeGeneratedDocuments": DecodeGeneratedDocuments,
	} {
		_, err := decode("secret.yaml", strings.NewReader(input))
		if err == nil {
			t.Fatalf("%s() error = nil, want invalid separator error", name)
		}
		for _, want := range []string{"secret.yaml document 0", "decode YAML document failed", "invalid YAML document separator"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s() error = %q, want %q", name, err, want)
			}
		}
		if strings.Contains(err.Error(), secretValue) {
			t.Fatalf("%s() error leaked manifest data: %q", name, err)
		}
	}
}

func TestDecodeDocumentsUnsupportedKeyErrorDoesNotIncludeManifestValue(t *testing.T) {
	const secretValue = "secret-token-value"
	input := `apiVersion: v1
kind: Secret
metadata:
  name: credentials
stringData:
  ~: ` + secretValue + `
`
	// sigs.k8s.io/yaml cannot turn a null key into a JSON key, so Argo CD
	// v3.5.3 rejects the document too.
	if _, err := kube.SplitYAML([]byte(input)); err == nil {
		t.Fatal("kube.SplitYAML() error = nil, want unsupported key error")
	}
	_, err := DecodeDocuments("secret.yaml", strings.NewReader(input))
	if err == nil {
		t.Fatal("DecodeDocuments() error = nil, want unsupported key error")
	}
	if !strings.Contains(err.Error(), "YAML object key is not a string, number, or boolean") {
		t.Fatalf("error = %q, want unsupported key message", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}

func TestDecodeDocumentsJSONSyntaxErrorDoesNotIncludeManifestValue(t *testing.T) {
	// Invalid as JSON and as YAML, so the decoder reports the JSON error.
	input := `{"kind": "Secret", "data": {"token": secret-token-value}`

	_, err := DecodeDocuments("secret.json", strings.NewReader(input))
	if err == nil {
		t.Fatal("DecodeDocuments() error = nil, want syntax error")
	}
	if !strings.Contains(err.Error(), "invalid JSON at offset") {
		t.Fatalf("error = %q, want JSON offset message", err)
	}
	if strings.Contains(err.Error(), "'s'") || strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}

func TestDecodeDocumentsOutOfRangeJSONNumberMatchesArgoCDReaders(t *testing.T) {
	input := `{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "s"}, "data": {"token": 1e999}}`

	// kubectl cannot hold 1e999 in a float64 and fails (cli-runtime v0.36.1
	// resource builder: "json: cannot unmarshal number 1e999 into Go value of
	// type float64"); the drydock error must not repeat the number.
	_, err := DecodeDocuments("secret.json", strings.NewReader(input))
	if err == nil {
		t.Fatal("DecodeDocuments() error = nil, want out-of-range number error")
	}
	if !strings.Contains(err.Error(), "JSON number is out of range") {
		t.Fatalf("error = %q, want out-of-range message", err)
	}
	if strings.Contains(err.Error(), "1e999") {
		t.Fatalf("error leaked manifest data: %q", err)
	}

	// kube.SplitYAML re-reads the JSON as YAML, where 1e999 is not a float
	// and stays a string.
	want := []map[string]any{{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "s"},
		"data":     map[string]any{"token": "1e999"},
	}}
	split, err := kube.SplitYAML([]byte(input))
	if err != nil {
		t.Fatalf("kube.SplitYAML() error = %v", err)
	}
	if got := unstructuredObjects(split); !reflect.DeepEqual(got, want) {
		t.Fatalf("kube.SplitYAML() = %#v, want %#v", got, want)
	}
	assertDocumentObjects(t, "DecodeGeneratedDocuments", input, DecodeGeneratedDocuments, want)
}

func TestDecodeDocumentsCountsDocumentsAsTheManifestSplitterDoes(t *testing.T) {
	// A document index names the n-th document the splitter returns, so a
	// comment-only preamble is a document and "---" directly after another
	// "---" is not; config's per-document settings readers count alike.
	input := `# preamble
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: second
`
	docs, err := DecodeDocumentRoots("settings.yaml", strings.NewReader(input))
	if err != nil {
		t.Fatalf("DecodeDocumentRoots() error = %v", err)
	}
	got := map[string]int{}
	for _, doc := range docs {
		got[doc.Object.GetName()] = doc.Index
	}
	if want := map[string]int{"first": 1, "second": 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("document indexes = %v, want %v", got, want)
	}
}

func assertDocumentObjects(t *testing.T, name, input string, decode func(string, io.Reader) ([]Document, error), want []map[string]any) {
	t.Helper()
	docs, err := decode("input.yaml", strings.NewReader(input))
	if err != nil {
		t.Fatalf("%s() error = %v", name, err)
	}
	got := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		got = append(got, doc.Object.Object)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s() = %#v, want %#v", name, got, want)
	}
	for _, doc := range docs {
		if clone := doc.Object.DeepCopy(); !reflect.DeepEqual(clone.Object, doc.Object.Object) {
			t.Fatalf("%s() DeepCopy() = %#v, want %#v", name, clone.Object, doc.Object.Object)
		}
	}
}

// argoSplitYAMLOrJSON is argo-cd v3.5.3 reposerver/repository/repository.go
// splitYAMLOrJSON (:2083-2100), which is unexported.
func argoSplitYAMLOrJSON(input string) ([]*unstructured.Unstructured, error) {
	d := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(input)), 4096)
	var objs []*unstructured.Unstructured
	for {
		u := &unstructured.Unstructured{}
		if err := d.Decode(&u); err != nil {
			if errors.Is(err, io.EOF) {
				return objs, nil
			}
			return objs, err
		}
		if u == nil {
			continue
		}
		objs = append(objs, u)
	}
}

func unstructuredObjects(objs []*unstructured.Unstructured) []map[string]any {
	out := make([]map[string]any, 0, len(objs))
	for _, obj := range objs {
		out = append(out, obj.Object)
	}
	return out
}
