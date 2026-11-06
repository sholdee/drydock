package render

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/gitops-engine/v3/pkg/utils/kube"
	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// upstreamGeneratedOutcome is what Argo CD v3.6.1 does with Kustomize or
// config management plugin output: gitops-engine kube.SplitYAML itself, then
// the GenerateManifests loop and the controller read-back (see
// upstreamApplicationObjectsWithTracking), with annotation tracking for the
// Application "parity" in namespace "default".
func upstreamGeneratedOutcome(data string) ([]*unstructured.Unstructured, error) {
	objs, err := kube.SplitYAML([]byte(data))
	if err != nil {
		return nil, err
	}
	return upstreamApplicationObjects(objs, "parity", "default")
}

// trackedManifestObjects adds the upstream tracking metadata to a copy of
// each drydock manifest, as the Application layer does, so the objects can
// be compared with what the controller reads back upstream.
func trackedManifestObjects(t *testing.T, manifests []Manifest) []*unstructured.Unstructured {
	t.Helper()
	track := upstreamTrackManifest(v1alpha1.TrackingMethodAnnotation)
	out := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, manifest := range manifests {
		obj := manifest.Object.DeepCopy()
		if err := track(obj); err != nil {
			t.Fatalf("track %s: %v", manifest.Object.GetName(), err)
		}
		out = append(out, obj)
	}
	return out
}

func unstructuredMaps(objs []*unstructured.Unstructured) []map[string]any {
	out := make([]map[string]any, 0, len(objs))
	for _, obj := range objs {
		out = append(out, obj.Object)
	}
	return out
}

// TestDecodeGeneratedOutputMatchesUpstream compares drydock's reading of
// generated output with kube.SplitYAML and the rest of the upstream path.
// Both must fail on the same input, and when they render, both must emit the
// same objects in the same order. wantErr names the document drydock must
// report, which is the document upstream fails on.
func TestDecodeGeneratedOutputMatchesUpstream(t *testing.T) {
	const cm = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n"
	configMap := func(name string) string { return strings.ReplaceAll(cm, "%s", name) }
	for _, tt := range []struct {
		name    string
		input   string
		wantErr []string
		// wantUpstreamErr is part of the upstream error, which the smoke
		// expects Argo CD to report.
		wantUpstreamErr string
	}{
		{name: "two objects", input: configMap("a") + "---\n" + configMap("b")},
		{name: "empty, comment-only, and null documents", input: "---\n---\n# only a comment\n---\nnull\n---\n~\n---\n" + configMap("a")},
		{name: "empty stream", input: ""},
		{
			name:            "document without a kind",
			input:           configMap("a") + "---\nfoo: bar\n",
			wantErr:         []string{"out document 1 has no kind", `"Object 'Kind' is missing"`},
			wantUpstreamErr: `Object 'Kind' is missing in '{"foo":"bar"}'`,
		},
		{
			name:            "empty object",
			input:           configMap("a") + "---\n{}\n",
			wantErr:         []string{"out document 1 has no kind"},
			wantUpstreamErr: `Object 'Kind' is missing in '{}'`,
		},
		{
			name:            "apiVersion that does not parse",
			input:           "apiVersion: apps/v1/\nkind: Deployment\nmetadata:\n  name: typo\n",
			wantErr:         []string{`out document 0 has apiVersion "apps/v1/", which is not a valid group version, so it has no kind`},
			wantUpstreamErr: `Object 'Kind' is missing in '{"apiVersion":"apps/v1/","kind":"Deployment"`,
		},
		{
			name:    "kind that is not a string",
			input:   "apiVersion: v1\nkind: 5\nmetadata:\n  name: number\n",
			wantErr: []string{"out document 0 has no kind"},
		},
		{
			name:    "scalar document",
			input:   configMap("a") + "---\nhello\n",
			wantErr: []string{"out document 1: failed to unmarshal manifest: error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string"},
		},
		{
			name:    "array document",
			input:   "- apiVersion: v1\n  kind: ConfigMap\n  metadata:\n    name: a\n",
			wantErr: []string{"out document 0: failed to unmarshal manifest: error unmarshaling JSON: while decoding JSON: json: cannot unmarshal array"},
		},
		{
			name:    "syntax error wins over an earlier document without a kind",
			input:   "foo: bar\n---\nkey: [unclosed\n",
			wantErr: []string{"out document 1: failed to unmarshal manifest: error converting YAML to JSON"},
		},
		{
			// The reader fails on the separator line before it returns the
			// document above it.
			name:    "invalid document separator",
			input:   configMap("a") + "--- kind: Secret\n",
			wantErr: []string{"out document 0: failed to unmarshal manifest: invalid YAML document separator"},
		},
		{
			name:    "document without a kind wins over earlier tracking failure",
			input:   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  annotations: text\n---\nfoo: bar\n",
			wantErr: []string{"out document 1 has no kind"},
		},
		{
			name:    "tracking failure",
			input:   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  annotations: text\n",
			wantErr: []string{"out document 0: failed to set app instance tracking info on manifest"},
		},
		{
			name:  "tracking is not set on a CRD",
			input: "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.example.com\n  annotations: text\n",
		},
		{
			name:  "List is split into its items",
			input: "apiVersion: v1\nkind: List\nitems:\n  - " + strings.ReplaceAll(strings.TrimSuffix(configMap("item-a"), "\n"), "\n", "\n    ") + "\n  - " + strings.ReplaceAll(strings.TrimSuffix(configMap("item-b"), "\n"), "\n", "\n    ") + "\n",
		},
		{
			name:            "List item without a kind fails in the controller",
			input:           "apiVersion: v1\nkind: List\nitems:\n  - metadata:\n      name: kindless\n",
			wantErr:         []string{"out document 0 list item /items/0 has no kind"},
			wantUpstreamErr: `Object 'Kind' is missing in '{"metadata":{"annotations":{"argocd.argoproj.io/tracking-id":`,
		},
		{
			name:    "List item without a kind is reported after a later tracking failure",
			input:   "apiVersion: v1\nkind: List\nitems:\n  - metadata:\n      name: kindless\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n  annotations: text\n",
			wantErr: []string{"out document 1: failed to set app instance tracking info on manifest"},
		},
		{
			name:    "List item that is not an object",
			input:   "apiVersion: v1\nkind: List\nitems:\n  - text\n",
			wantErr: []string{"out document 0 list item /items/0 is not an object"},
		},
		{
			name:    "List whose apiVersion does not parse fails before it is split",
			input:   "apiVersion: v1/x/y\nkind: List\nitems: []\n",
			wantErr: []string{`out document 0 has apiVersion "v1/x/y"`},
		},
		{name: "null List emits nothing", input: "apiVersion: v1\nkind: List\nitems: null\n---\n" + configMap("a")},
		{name: "null items with spec is kept", input: "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: w\nitems: null\nspec: {}\n"},
		{name: "items that are a map are kept", input: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\nitems:\n  x: y\n"},
		{
			name:  "JSON stream",
			input: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"a"},"data":{"one":1.0,"mil":1e6,"big":1e999}}` + "\n" + `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"b"}}`,
		},
		{
			name:    "JSON stream object without a kind",
			input:   `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"a"}}` + "\n" + `{"metadata":{"name":"b"}}`,
			wantErr: []string{"out document 1 has no kind"},
		},
		{
			name:  "JSON stream indented with tabs",
			input: "{\n\t\"apiVersion\": \"v1\",\n\t\"kind\": \"ConfigMap\",\n\t\"metadata\": {\"name\": \"tabs\"}\n}\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream, upstreamErr := upstreamGeneratedOutcome(tt.input)
			manifests, err := DecodeGeneratedOutput("out", []byte(tt.input), upstreamTrackManifest(v1alpha1.TrackingMethodAnnotation))
			if (upstreamErr != nil) != (err != nil) {
				t.Fatalf("upstream error = %v, drydock error = %v", upstreamErr, err)
			}
			if tt.wantUpstreamErr != "" && (upstreamErr == nil || !strings.Contains(upstreamErr.Error(), tt.wantUpstreamErr)) {
				t.Fatalf("upstream error = %v, want it to contain %q", upstreamErr, tt.wantUpstreamErr)
			}
			if err != nil {
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("drydock error = %q, want it to contain %q (upstream error %v)", err, want, upstreamErr)
					}
				}
				if len(tt.wantErr) == 0 {
					t.Fatalf("drydock error = %v, want a case wantErr (upstream error %v)", err, upstreamErr)
				}
				return
			}
			if len(tt.wantErr) != 0 {
				t.Fatalf("drydock rendered, want error %q", tt.wantErr)
			}
			if got, want := unstructuredMaps(trackedManifestObjects(t, manifests)), unstructuredMaps(upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered %#v, upstream %#v", got, want)
			}
		})
	}
}

// TestDecodeGeneratedOutputDoesNotEchoContent checks that the errors name
// the document but do not repeat its content. The upstream missing-kind
// error quotes the whole object, which can hold a Secret.
func TestDecodeGeneratedOutputDoesNotEchoContent(t *testing.T) {
	const secretValue = "secret-token-value"
	for _, input := range []string{
		"metadata:\n  name: s\nstringData:\n  token: " + secretValue + "\n",
		"apiVersion: v1/x/y\nkind: Secret\nstringData:\n  token: " + secretValue + "\n",
		`{"kind": "Secret", "data": {"token": ` + secretValue + `}`,
		"--- {kind: Secret, data: {token: " + secretValue + "}}\n",
	} {
		_, err := DecodeGeneratedOutput("out", []byte(input), nil)
		if err == nil {
			t.Fatalf("DecodeGeneratedOutput(%q) error = nil, want error", input)
		}
		if strings.Contains(err.Error(), secretValue) {
			t.Fatalf("DecodeGeneratedOutput(%q) error leaked manifest data: %q", input, err)
		}
	}
}

// TestKustomizeRendererDecodeMatchesUpstream renders kustomizations whose
// build output Argo CD reads with kube.SplitYAML (util/kustomize/
// kustomize.go:419). The oracle runs the same kustomize build and the
// upstream path on its output. kustomize keeps an apiVersion that does not
// parse as a group version, so the repo-server fails on it, and it expands a
// List into its items, so a List with no items fails nowhere.
func TestKustomizeRendererDecodeMatchesUpstream(t *testing.T) {
	for _, tt := range []struct {
		name      string
		resources map[string]string
		wantErr   string
	}{
		{
			name: "apiVersion that does not parse",
			resources: map[string]string{
				"config.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: config\n",
				"typo.yaml":   "apiVersion: apps/v1/\nkind: Deployment\nmetadata:\n  name: typo\n",
			},
			wantErr: `apps/kustomization.yaml document 1 has apiVersion "apps/v1/", which is not a valid group version, so it has no kind`,
		},
		{
			name: "renders",
			resources: map[string]string{
				"config.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: config\n",
				"list.yaml":   "apiVersion: v1\nkind: List\nitems:\n  - apiVersion: v1\n    kind: ConfigMap\n    metadata:\n      name: from-list\n",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			names := make([]string, 0, len(tt.resources))
			for name, content := range tt.resources {
				writeFile(t, filepath.Join(root, "apps", name), content)
				names = append(names, name)
			}
			sort.Strings(names)
			writeFile(t, filepath.Join(root, "apps", "kustomization.yaml"), "resources:\n  - "+strings.Join(names, "\n  - ")+"\n")

			resMap, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(filesys.MakeFsOnDisk(), filepath.Join(root, "apps"))
			if err != nil {
				t.Fatalf("kustomize build error = %v", err)
			}
			built, err := resMap.AsYaml()
			if err != nil {
				t.Fatalf("AsYaml() error = %v", err)
			}
			upstream, upstreamErr := upstreamGeneratedOutcome(string(built))

			result, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{
				TrackManifest: upstreamTrackManifest(v1alpha1.TrackingMethodAnnotation),
			})
			if (upstreamErr != nil) != (err != nil) {
				t.Fatalf("upstream error = %v, drydock error = %v", upstreamErr, err)
			}
			if err != nil {
				if tt.wantErr == "" || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("drydock error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if tt.wantErr != "" {
				t.Fatalf("drydock rendered, want error %q", tt.wantErr)
			}
			if got, want := unstructuredMaps(trackedManifestObjects(t, result)), unstructuredMaps(upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered %#v, upstream %#v", got, want)
			}
		})
	}
}
