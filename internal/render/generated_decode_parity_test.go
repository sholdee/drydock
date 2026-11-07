package render

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
	"sigs.k8s.io/yaml"
)

// upstreamGeneratedOutcome is what Argo CD v3.6.1 does with Kustomize or
// config management plugin output: gitops-engine kube.SplitYAML itself, then
// the GenerateManifests loop and the controller read-back (see
// upstreamApplicationObjectsWithTracking), with annotation tracking for the
// Application "parity" in namespace "default".
func upstreamGeneratedOutcome(data string) ([]*unstructured.Unstructured, error) {
	return upstreamGeneratedOutcomeFor(data, "parity", "default")
}

// upstreamGeneratedOutcomeFor is upstreamGeneratedOutcome for an Application
// instance name and destination namespace.
//
// data is what the command prints. Argo CD first removes one line break
// from its end (util/exec/exec.go:285 for helm template and kustomize
// build, cmpserver/plugin/plugin.go:155 for a plugin).
func upstreamGeneratedOutcomeFor(data, appName, namespace string) ([]*unstructured.Unstructured, error) {
	data = strings.TrimSuffix(data, "\n")
	objs, err := kube.SplitYAML([]byte(data))
	if err != nil {
		return nil, err
	}
	return upstreamApplicationObjectsWithTracking(objs, appName, namespace, v1alpha1.TrackingMethodAnnotation)
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
			// Argo CD removes one line break from the end of the output.
			name:  "last document ends with a keep block scalar",
			input: configMap("first") + "---\n" + configMap("last") + "data:\n  kept: |+\n    x\n\n",
		},
		{
			name:  "JSON stream indented with tabs",
			input: "{\n\t\"apiVersion\": \"v1\",\n\t\"kind\": \"ConfigMap\",\n\t\"metadata\": {\"name\": \"tabs\"}\n}\n",
		},
		// kube.SplitYAML reads each document a second time with the
		// go.yaml.in/yaml/v2 scanner, which has rules of its own. The
		// first reading writes NEL (U+0085, the YAML escape \N) as is into
		// the JSON text. The second reading takes it as a line break, so
		// it folds a value and fails on a key.
		{
			name:  "NEL in a value is folded by the second reading",
			input: configMap("nel") + "data:\n  a: \"one\\Ntwo\"\n",
		},
		{
			name:    "NEL in a key fails the second reading",
			input:   configMap("nel") + "data:\n  \"one\\Ntwo\": a\n",
			wantErr: []string{"out document 0: failed to unmarshal manifest: error converting YAML to JSON: yaml:"},
		},
		// The second reading allows a simple key of at most 1024
		// characters (scannerc.go). The JSON key has two quotes, so a key
		// of 1022 characters passes and one of 1023 characters fails.
		// kustomize writes a key over 128 characters as an explicit "? "
		// key, which the first reading allows at any length.
		{
			name:  "key of 1022 characters",
			input: configMap("long") + "data:\n  ? " + strings.Repeat("k", 1022) + "\n  : a\n",
		},
		{
			name:    "key of 1023 characters",
			input:   configMap("long") + "data:\n  ? " + strings.Repeat("k", 1023) + "\n  : a\n",
			wantErr: []string{"out document 0: failed to unmarshal manifest: error converting YAML to JSON: yaml:"},
		},
		{
			// encoding/json writes "<" as the six characters \u003c.
			name:    "key that JSON escapes past 1024 characters",
			input:   configMap("long") + "data:\n  ? \"" + strings.Repeat("<", 200) + "\"\n  : a\n",
			wantErr: []string{"out document 0: failed to unmarshal manifest: error converting YAML to JSON: yaml:"},
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
		"apiVersion: v1\nkind: Secret\ndata:\n  ? {key: " + secretValue + "}\n  : c\n",
		"apiVersion: v1\nkind: Secret\ndata:\n  token: *" + secretValue + "\n",
		"apiVersion: v1\nkind: Secret\ndata:\n  token: !!int " + secretValue + "\n",
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
			// Argo CD removes one line break from the end of the build
			// output, so the value is "x\n", not "x\n\n". kustomize sorts
			// the fields, so spec is the last one.
			name: "last document ends with a keep block scalar",
			resources: map[string]string{
				"widget.yaml": "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: widget\nspec:\n  kept: |+\n    x\n\n",
			},
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

// parityDecodeCase is one case of testdata/argocd-parity/
// directory-decode-expected.yaml.
type parityDecodeCase struct {
	Name               string `json:"name"`
	Source             string `json:"source"`
	File               string `json:"file"`
	ArgoCDMessage      string `json:"argocdMessage"`
	DrydockMessage     string `json:"drydockMessage"`
	ArgoCDQuotesObject bool   `json:"argocdQuotesObject"`
	ArgoCDNamesObject  string `json:"argocdNamesObject"`
	Repository         string `json:"repository"`
}

// readParityDecodeCases reads the decode smoke cases whose source is
// directory (an empty source) when directory is set, and the other cases
// otherwise. A case with its own repository fails on the whole repository,
// not on a file of the source, so TestOutOfBoundsSymlinkSmokeFixture covers
// it instead.
func readParityDecodeCases(t *testing.T, directory bool) []parityDecodeCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "argocd-parity", "directory-decode-expected.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var expected struct {
		Cases []parityDecodeCase `json:"cases"`
	}
	if err := yaml.Unmarshal(data, &expected); err != nil {
		t.Fatalf("parse expected file: %v", err)
	}
	var out []parityDecodeCase
	for _, tc := range expected.Cases {
		// A spec case renders nothing: the controller rejects the spec
		// first. TestParitySpecCasesMatchUpstream in internal/app covers it.
		if tc.Repository != "" || tc.Source == "spec" {
			continue
		}
		// helm and kustomize fail a render case and a Kustomize
		// helmCharts case themselves, so there is no offline upstream
		// oracle. TestHelmRenderParityDecodeFixtures checks drydock, and
		// the smoke checks Argo CD.
		if tc.Source == "render" || parityCaseHasHelmCharts(t, tc) {
			continue
		}
		if (tc.Source == "" || tc.Source == "directory") == directory {
			out = append(out, tc)
		}
	}
	return out
}

// TestGeneratedParityDecodeFixtures renders each Helm, Kustomize, and plugin
// case of the Argo CD render parity smoke (testdata/argocd-parity) offline,
// with the Application's instance name, destination namespace, and
// annotation tracking. drydock must fail with the drydockMessage the smoke
// expects, and the upstream oracle must fail with the argocdMessage the smoke
// expects. A plugin case runs the plugin program with awk, as the parity-env
// sidecar and drydock's exec engine do.
func TestGeneratedParityDecodeFixtures(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range readParityDecodeCases(t, false) {
		seen[tc.Source] = true
		t.Run(tc.Name, func(t *testing.T) {
			drydockErr, upstreamErr := renderGeneratedParityCase(t, tc)
			if drydockErr == nil || !strings.Contains(drydockErr.Error(), tc.DrydockMessage) {
				t.Fatalf("drydock error = %v, want %q", drydockErr, tc.DrydockMessage)
			}
			if upstreamErr == nil || !strings.Contains(upstreamErr.Error(), tc.ArgoCDMessage) {
				t.Fatalf("upstream error = %v, want %q", upstreamErr, tc.ArgoCDMessage)
			}
		})
	}
	for _, source := range []string{"helm", "kustomize", "plugin"} {
		if !seen[source] {
			t.Fatalf("the expected file has no %s case", source)
		}
	}
}

// renderGeneratedParityCase renders one generated-output case with drydock
// and with the upstream oracle and returns both errors.
func renderGeneratedParityCase(t *testing.T, tc parityDecodeCase) (drydockErr, upstreamErr error) {
	t.Helper()
	const namespace = "parity-directory-decode-workloads"
	repoRoot := filepath.Join("..", "..", "testdata", "argocd-parity", "repo")
	sourcePath := "workloads/directory-decode/" + strings.TrimPrefix(tc.Name, "parity-decode-")
	sourceDir := filepath.Join(repoRoot, filepath.FromSlash(sourcePath))
	appName := "parity-directory-decode_" + tc.Name
	track := upstreamTrackManifestFor(appName, namespace, v1alpha1.TrackingMethodAnnotation)
	opts := RenderOptions{AppName: tc.Name, Namespace: namespace, TrackManifest: track}
	source := ResolvedSource{RepoRoot: repoRoot, Path: sourcePath}
	switch tc.Source {
	case "helm":
		_, _, drydockErr = (HelmRenderer{}).Render(context.Background(), source, opts)
		_, upstreamErr = upstreamHelmOutcomeFor(t, sourceDir, false, tc.Name, appName, namespace)
	case "helm-file-parameters":
		parameters := readParityFileParameters(t, filepath.Join(repoRoot, "directory-decode-applications", strings.TrimPrefix(tc.Name, "parity-decode-")+".yaml"))
		opts.HelmFileParameters = parameters
		_, _, drydockErr = (HelmRenderer{}).Render(context.Background(), source, opts)
		_, upstreamErr = upstreamHelmFileParameterValues(sourceDir, repoRoot, nil, nil, parameters)
	case "kustomize":
		_, _, drydockErr = (KustomizeRenderer{}).Render(context.Background(), source, opts)
		resMap, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(filesys.MakeFsOnDisk(), sourceDir)
		if err != nil {
			t.Fatalf("kustomize build error = %v", err)
		}
		built, err := resMap.AsYaml()
		if err != nil {
			t.Fatalf("AsYaml() error = %v", err)
		}
		_, upstreamErr = upstreamGeneratedOutcomeFor(string(built), appName, namespace)
	case "plugin":
		cmd := exec.Command("awk", "-f", "generate.awk")
		cmd.Dir = sourceDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("awk error = %v", err)
		}
		_, drydockErr = DecodeGeneratedOutput("plugin/parity-env/path/"+sourcePath+"/generate-output", out, track)
		_, upstreamErr = upstreamGeneratedOutcomeFor(string(out), appName, namespace)
	default:
		t.Fatalf("unknown source %q", tc.Source)
	}
	return drydockErr, upstreamErr
}

// parityCaseHasHelmCharts reports a Kustomize case whose kustomization has
// helmCharts.
func parityCaseHasHelmCharts(t *testing.T, tc parityDecodeCase) bool {
	t.Helper()
	if tc.Source != "kustomize" {
		return false
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "argocd-parity", "repo", "workloads", "directory-decode", strings.TrimPrefix(tc.Name, "parity-decode-"), "kustomization.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return strings.Contains(string(data), "\nhelmCharts:\n")
}

// readParityApplicationSource returns the single source of the Application
// in the file at path.
func readParityApplicationSource(t *testing.T, path string) v1alpha1.ApplicationSource {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var application v1alpha1.Application
	if err := yaml.Unmarshal(data, &application); err != nil {
		t.Fatalf("parse Application %s: %v", path, err)
	}
	if application.Spec.Source == nil {
		t.Fatalf("Application %s has no single source", path)
	}
	return *application.Spec.Source
}

// readParityFileParameters returns the helm.fileParameters of the single
// source of the Application in the file at path.
func readParityFileParameters(t *testing.T, path string) []v1alpha1.HelmFileParameter {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var application v1alpha1.Application
	if err := yaml.Unmarshal(data, &application); err != nil {
		t.Fatalf("parse Application %s: %v", path, err)
	}
	if application.Spec.Source == nil || application.Spec.Source.Helm == nil || len(application.Spec.Source.Helm.FileParameters) == 0 {
		t.Fatalf("Application %s sets no helm.fileParameters", path)
	}
	return application.Spec.Source.Helm.FileParameters
}

// BenchmarkDecodeGeneratedOutput reads a 7 MB stream of large CRDs, the
// shape of Kustomize output that vendors CRDs. kube.SplitYAML reads each
// document twice, and drydock does the same.
func BenchmarkDecodeGeneratedOutput(b *testing.B) {
	var builder strings.Builder
	for doc := range 20 {
		fmt.Fprintf(&builder, "---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: x%d.example.com\nspec:\n  versions:\n  - name: v1\n    schema:\n      openAPIV3Schema:\n        properties:\n", doc)
		for field := range 3000 {
			fmt.Fprintf(&builder, "          field%d:\n            type: string\n            description: some long description text for field %d that goes on\n", field, field)
		}
	}
	data := []byte(builder.String())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := DecodeGeneratedOutput("bench", data, nil); err != nil {
			b.Fatal(err)
		}
	}
}
