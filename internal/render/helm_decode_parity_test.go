package render

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/engine"
	release "helm.sh/helm/v4/pkg/release/v1"
	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// upstreamHelmTemplate is what `helm template --include-crds [--skip-tests]`
// from helm v4.3.1, the version Argo CD v3.6.1 bundles, prints for the chart
// in chartDir, with a release name and namespace. It renders the templates with the helm engine, as the install
// action does in client-only mode, and then copies how
// Configuration.renderResources (pkg/action/action.go, without a
// post-renderer and an output directory) builds the release manifest and how
// pkg/cmd/template.go prints the release. The helm install action itself is
// not used, because its package pulls in many modules that drydock does not
// otherwise need.
func upstreamHelmTemplate(t *testing.T, chartDir string, skipTests bool, releaseName, namespace string) (string, error) {
	t.Helper()
	ch, err := loader.Load(chartDir)
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	options := common.ReleaseOptions{Name: releaseName, Namespace: namespace, Revision: 1, IsInstall: true}
	values, err := chartutil.ToRenderValuesWithSchemaValidation(ch, map[string]any{}, options, common.DefaultCapabilities.Copy(), false)
	if err != nil {
		return "", err
	}
	var e engine.Engine
	files, err := e.RenderWithContext(context.Background(), ch, values)
	if err != nil {
		return "", err
	}

	// renderResources
	for k := range files {
		if strings.HasSuffix(k, "NOTES.txt") {
			delete(files, k)
		}
	}
	hs, manifests, err := releaseutil.SortManifests(files, nil, releaseutil.InstallOrder)
	if err != nil {
		return "", err
	}
	b := bytes.NewBuffer(nil)
	for _, crd := range ch.CRDObjects() {
		fmt.Fprintf(b, "---\n# Source: %s\n%s\n", crd.Filename, string(crd.File.Data))
	}
	for _, m := range manifests {
		fmt.Fprintf(b, "---\n# Source: %s\n%s\n", m.Name, m.Content)
	}
	relManifest := b.String()

	// pkg/cmd/template.go
	var out bytes.Buffer
	fmt.Fprintln(&out, strings.TrimSpace(relManifest))
	for _, m := range hs {
		if skipTests && slices.Contains(m.Events, release.HookTest) {
			continue
		}
		fmt.Fprintf(&out, "---\n# Source: %s\n%s\n", m.Path, m.Manifest)
	}
	return out.String(), nil
}

// upstreamHelmOutcome is what Argo CD v3.6.1 does with a Helm source:
// helm template, then kube.SplitYAML (helmTemplate,
// reposerver/repository/repository.go:1444), then the GenerateManifests
// loop and the controller read-back.
func upstreamHelmOutcome(t *testing.T, chartDir string, skipTests bool) ([]*unstructured.Unstructured, error) {
	t.Helper()
	return upstreamHelmOutcomeFor(t, chartDir, skipTests, "parity", "parity", "default")
}

// upstreamHelmOutcomeFor is upstreamHelmOutcome for a release name, an
// Application instance name, and a destination namespace.
func upstreamHelmOutcomeFor(t *testing.T, chartDir string, skipTests bool, releaseName, appName, namespace string) ([]*unstructured.Unstructured, error) {
	t.Helper()
	out, err := upstreamHelmTemplate(t, chartDir, skipTests, releaseName, namespace)
	if err != nil {
		return nil, err
	}
	return upstreamGeneratedOutcomeFor(out, appName, namespace)
}

func writeParityChart(t *testing.T, chartDir string, templates map[string]string) {
	t.Helper()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: parity\nversion: 0.1.0\n")
	for name, content := range templates {
		writeFile(t, filepath.Join(chartDir, filepath.FromSlash(name)), content)
	}
}

func renderParityChart(t *testing.T, templates map[string]string, skipTests bool) (string, []Manifest, error) {
	t.Helper()
	root := t.TempDir()
	chartDir := filepath.Join(root, "chart")
	writeParityChart(t, chartDir, templates)
	result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "chart"}, RenderOptions{
		AppName:       "parity",
		Namespace:     "default",
		SkipTests:     skipTests,
		TrackManifest: upstreamTrackManifest(v1alpha1.TrackingMethodAnnotation),
	})
	return chartDir, result, err
}

const helmParityConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
`

func helmHookConfigMap(name, hook string) string {
	return fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  annotations:\n    helm.sh/hook: %q\n", name, hook)
}

// TestHelmRendererHooksMatchUpstream renders a chart with hook and test
// documents with drydock and with helm template plus the Argo CD path, with
// and without skipTests. helm decides which documents are hooks from the
// annotations of the document itself, so the items of a List keep their
// hook annotations and are emitted, and a List with a test hook annotation
// is dropped as a whole under skipTests. A document whose hook annotation
// names an event that helm does not know, or is empty, is always dropped.
func TestHelmRendererHooksMatchUpstream(t *testing.T) {
	templates := map[string]string{
		"templates/plain.yaml": fmt.Sprintf(helmParityConfigMap, "plain"),
		"templates/hooks.yaml": helmHookConfigMap("pre-install", "pre-install") + "---\n" +
			helmHookConfigMap("mixed-case", " Pre-Install ") + "---\n" +
			helmHookConfigMap("test", "test") + "---\n" +
			helmHookConfigMap("test-success", "pre-install, test-success") + "---\n" +
			helmHookConfigMap("test-failure", "test-failure") + "---\n" +
			helmHookConfigMap("crd-install", "crd-install") + "---\n" +
			helmHookConfigMap("unknown-second", "pre-install,bogus") + "---\n" +
			helmHookConfigMap("empty-hook", ""),
		// A List without annotations whose items are test hooks: helm keeps
		// the List, and Argo CD emits the items.
		"templates/list-test-items.yaml": `apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: item-test
      annotations:
        helm.sh/hook: test
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: item-plain
`,
		// A List that is a test hook: --skip-tests drops it with its items.
		"templates/test-list.yaml": `apiVersion: v1
kind: List
metadata:
  annotations:
    helm.sh/hook: test
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: in-test-list
`,
		// helm drops every file whose name ends in NOTES.txt.
		"templates/extra-NOTES.txt": "not: [yaml\n",
	}
	for _, tt := range []struct {
		skipTests bool
		want      []string
	}{
		{skipTests: false, want: []string{"empty-hook!", "crd-install!", "test-failure!", "unknown-second!", "in-test-list", "item-plain", "item-test", "mixed-case", "plain", "pre-install", "test", "test-success"}},
		{skipTests: true, want: []string{"empty-hook!", "crd-install!", "test-failure!", "unknown-second!", "test!", "test-success!", "in-test-list!", "item-plain", "item-test", "mixed-case", "plain", "pre-install"}},
	} {
		t.Run(fmt.Sprintf("skipTests=%t", tt.skipTests), func(t *testing.T) {
			chartDir, manifests, err := renderParityChart(t, templates, tt.skipTests)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			upstream, err := upstreamHelmOutcome(t, chartDir, tt.skipTests)
			if err != nil {
				t.Fatalf("upstream error = %v", err)
			}
			if got, want := sortedUnstructuredJSON(t, trackedManifestObjects(t, manifests)), sortedUnstructuredJSON(t, upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered\n%s\nupstream\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			names := map[string]bool{}
			for _, manifest := range manifests {
				names[manifest.Object.GetName()] = true
			}
			for _, name := range tt.want {
				if dropped, ok := strings.CutSuffix(name, "!"); ok {
					if names[dropped] {
						t.Fatalf("drydock rendered %s, want it dropped", dropped)
					}
					continue
				}
				if !names[name] {
					t.Fatalf("drydock did not render %s", name)
				}
			}
		})
	}
}

// TestHelmRendererEmitsHelmTemplateOrder pins drydock's output order for
// Helm to the order of helm template output: CRDs, then the other documents
// sorted by kind, then the hooks. Lists come last among the other
// documents, because List is not a kind that helm sorts.
func TestHelmRendererEmitsHelmTemplateOrder(t *testing.T) {
	chartDir, manifests, err := renderParityChart(t, map[string]string{
		"templates/a.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: a-deploy\n---\n" + fmt.Sprintf(helmParityConfigMap, "a-config"),
		"templates/b.yaml": helmHookConfigMap("b-hook", "pre-install") + "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: b-namespace\n",
		"templates/c.yaml": "apiVersion: v1\nkind: List\nitems:\n  - " + strings.ReplaceAll(strings.TrimSuffix(fmt.Sprintf(helmParityConfigMap, "c-item"), "\n"), "\n", "\n    ") + "\n",
	}, false)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	upstream, err := upstreamHelmOutcome(t, chartDir, false)
	if err != nil {
		t.Fatalf("upstream error = %v", err)
	}
	want := make([]string, 0, len(upstream))
	for _, obj := range upstream {
		want = append(want, obj.GetName())
	}
	if got := manifestNames(manifests); !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest names = %#v, upstream %#v", got, want)
	}
	if got, want := manifestNames(manifests), []string{"b-namespace", "a-config", "a-deploy", "c-item", "b-hook"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest names = %#v, want %#v", got, want)
	}
}

// lastCopies keeps the last copy of each repeated resource, in the order of
// first appearance, as the Argo CD controller keeps it
// (NormalizeTargetObjects, controller/state.go:575-623 at v3.6.1). The
// controller returns the copies in map order, which does not matter here.
func lastCopies(t *testing.T, objs []*unstructured.Unstructured) []string {
	t.Helper()
	index := map[string]int{}
	var out []string
	for _, obj := range objs {
		key := obj.GroupVersionKind().GroupKind().String() + "/" + obj.GetNamespace() + "/" + obj.GetName()
		body := marshalObject(t, obj)
		if i, ok := index[key]; ok {
			out[i] = body
			continue
		}
		index[key] = len(out)
		out = append(out, body)
	}
	sort.Strings(out)
	return out
}

// TestHelmRendererRepeatedResourceWinnerMatchesUpstream renders charts that
// hold a resource twice. Argo CD keeps the last copy in the order of helm
// template output. A List sorts after a ConfigMap, and a hook prints after
// every other document, so the copy in the later file does not always win.
// The hook copy here also has argocd.argoproj.io/hook: Skip, so it is not
// an Argo CD hook and stays a managed resource.
func TestHelmRendererRepeatedResourceWinnerMatchesUpstream(t *testing.T) {
	for name, templates := range map[string]map[string]string{
		"List before a ConfigMap": {
			"templates/a-list.yaml": "apiVersion: v1\nkind: List\nitems:\n  - apiVersion: v1\n    kind: ConfigMap\n    metadata:\n      name: dup\n    data:\n      from: list\n",
			"templates/b-cm.yaml":   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dup\ndata:\n  from: plain\n",
		},
		"helm hook before a plain copy": {
			"templates/a-hook.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dup\n  annotations:\n    helm.sh/hook: pre-install\n    argocd.argoproj.io/hook: Skip\ndata:\n  from: hook\n",
			"templates/b-cm.yaml":   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dup\ndata:\n  from: plain\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			chartDir, manifests, err := renderParityChart(t, templates, false)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			upstream, err := upstreamHelmOutcome(t, chartDir, false)
			if err != nil {
				t.Fatalf("upstream error = %v", err)
			}
			got, want := lastCopies(t, trackedManifestObjects(t, manifests)), lastCopies(t, upstream)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock keeps\n%s\nupstream keeps\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if len(want) != 1 || strings.Contains(want[0], `"from":"plain"`) {
				t.Fatalf("upstream keeps %v, want the copy that helm prints last", want)
			}
		})
	}
}

// TestHelmRendererTrimsTheEndOfTheReleaseManifest renders charts whose last
// CRD or other document ends with a "|+" block scalar and blank lines. helm
// template trims the white space at the end of the CRDs and the other
// documents together (pkg/cmd/template.go:122), so the value loses its
// trailing lines. A hook prints after the trim and keeps them.
func TestHelmRendererTrimsTheEndOfTheReleaseManifest(t *testing.T) {
	keep := func(kind, name, extra string) string {
		return "apiVersion: v1\nkind: " + kind + "\nmetadata:\n  name: " + name + "\n" + extra + "data:\n  kept: |+\n    x\n    \t\n\n\n"
	}
	for name, templates := range map[string]map[string]string{
		"last document": {
			"templates/a.yaml": keep("ConfigMap", "first", ""),
			"templates/b.yaml": keep("ConfigMap", "last", ""),
		},
		"last document before a hook": {
			"templates/a.yaml": keep("ConfigMap", "plain", ""),
			"templates/b.yaml": keep("ConfigMap", "hook", "  annotations:\n    helm.sh/hook: pre-install\n"),
		},
		"CRD without templates": {
			"crds/crd.yaml": "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.example.com\nspec:\n  group: example.com\n  note: |+\n    x\n\n\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			chartDir, manifests, err := renderParityChart(t, templates, false)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			upstream, err := upstreamHelmOutcome(t, chartDir, false)
			if err != nil {
				t.Fatalf("upstream error = %v", err)
			}
			if got, want := unstructuredMaps(trackedManifestObjects(t, manifests)), unstructuredMaps(upstream); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock rendered %#v, upstream %#v", got, want)
			}
		})
	}
}

// TestHelmRendererDecodeMatchesUpstream renders charts whose output Argo CD
// cannot read. drydock must fail where helm template plus the Argo CD path
// fail, and name the document that upstream fails on.
func TestHelmRendererDecodeMatchesUpstream(t *testing.T) {
	for _, tt := range []struct {
		name      string
		templates map[string]string
		wantErr   string
		// wantUpstreamErr is part of the upstream error, which the smoke
		// expects Argo CD to report.
		wantUpstreamErr string
	}{
		{
			name:            "document without a kind",
			templates:       map[string]string{"templates/kindless.yaml": "metadata:\n  name: kindless\ndata:\n  note: no kind\n"},
			wantErr:         "chart/templates/kindless.yaml document 0 has no kind",
			wantUpstreamErr: `failed to unmarshal manifest: error unmarshaling JSON: while decoding JSON: Object 'Kind' is missing in '{"data":{"note":"no kind"},"metadata":{"name":"kindless"}}'`,
		},
		{
			name:            "empty object",
			templates:       map[string]string{"templates/empty.yaml": fmt.Sprintf(helmParityConfigMap, "first") + "---\n{}\n"},
			wantErr:         "chart/templates/empty.yaml document 1 has no kind",
			wantUpstreamErr: `Object 'Kind' is missing in '{}'`,
		},
		{
			name:            "apiVersion that does not parse",
			templates:       map[string]string{"templates/typo.yaml": "apiVersion: apps/v1/\nkind: Deployment\nmetadata:\n  name: typo\n"},
			wantErr:         `chart/templates/typo.yaml document 0 has apiVersion "apps/v1/", which is not a valid group version, so it has no kind`,
			wantUpstreamErr: `Object 'Kind' is missing in '{"apiVersion":"apps/v1/","kind":"Deployment","metadata":{"name":"typo"}}'`,
		},
		{
			name:            "List item without a kind",
			templates:       map[string]string{"templates/list.yaml": "apiVersion: v1\nkind: List\nitems:\n  - metadata:\n      name: kindless-item\n"},
			wantErr:         "chart/templates/list.yaml document 0 list item /items/0 has no kind",
			wantUpstreamErr: `Object 'Kind' is missing in '{"metadata":{"annotations":{"argocd.argoproj.io/tracking-id":"parity:/:default/kindless-item"`,
		},
		{
			name: "List item tracking failure",
			templates: map[string]string{"templates/list.yaml": `apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: bad-annotations
      annotations: text
`},
			wantErr:         "chart/templates/list.yaml document 0 list item /items/0: failed to set app instance tracking info on manifest",
			wantUpstreamErr: "failed to set app instance tracking info on manifest",
		},
		{
			// helm reads the head of each document and fails on
			// annotations that are not strings.
			name:            "head that helm cannot read",
			templates:       map[string]string{"templates/bad.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bad\n  annotations:\n    nested:\n      key: value\n"},
			wantErr:         "helm template chart: YAML parse error on parity/templates/bad.yaml",
			wantUpstreamErr: "YAML parse error on parity/templates/bad.yaml",
		},
		{
			// helm template prints the Deployment before the kindless
			// object, which sorts last, so Argo CD fails on b.yaml first.
			name: "failure order follows helm template output",
			templates: map[string]string{
				"templates/a.yaml": "metadata:\n  name: kindless\n",
				"templates/b.yaml": "apiVersion: apps/v1/\nkind: Deployment\nmetadata:\n  name: typo\n",
			},
			wantErr:         "chart/templates/b.yaml document 0 has apiVersion",
			wantUpstreamErr: `Object 'Kind' is missing in '{"apiVersion":"apps/v1/"`,
		},
		{
			// A syntax error wins over an earlier document without a kind.
			// helm's head reading accepts the flow sequence that the
			// repo-server's reader rejects after the separator.
			name: "repo-server split error wins over a missing kind",
			templates: map[string]string{
				"templates/a.yaml": "metadata:\n  name: kindless\n",
				"crds/b.yaml":      "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.example.com\n--- text\n",
			},
			wantErr:         "chart/crds/b.yaml document 0: failed to unmarshal manifest: invalid YAML document separator",
			wantUpstreamErr: "invalid Yaml document separator",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chartDir, _, err := renderParityChart(t, tt.templates, false)
			_, upstreamErr := upstreamHelmOutcome(t, chartDir, false)
			if upstreamErr == nil || !strings.Contains(upstreamErr.Error(), tt.wantUpstreamErr) {
				t.Fatalf("upstream error = %v, want it to contain %q", upstreamErr, tt.wantUpstreamErr)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("drydock error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// sortedUnstructuredJSON lists objects as sorted JSON text, for a comparison
// that does not depend on order.
func sortedUnstructuredJSON(t *testing.T, objs []*unstructured.Unstructured) []string {
	t.Helper()
	out := make([]string, 0, len(objs))
	for _, obj := range objs {
		out = append(out, marshalObject(t, obj))
	}
	sort.Strings(out)
	return out
}

// TestHelmRendererParityRenderFixtureMatchesUpstream renders the
// parity-helm-generated-decode chart of the Argo CD render parity smoke with
// drydock and with the upstream oracle, with skipTests as the Application
// sets it. Both must keep the same copy of each repeated resource, as the
// Argo CD controller keeps the last copy.
func TestHelmRendererParityRenderFixtureMatchesUpstream(t *testing.T) {
	const (
		appName   = "parity-helm-generated-decode"
		namespace = "parity-helm-generated-decode"
		path      = "charts/helm-generated-decode"
	)
	repoRoot := filepath.Join("..", "..", "testdata", "argocd-parity", "repo")
	track := upstreamTrackManifestFor(appName, namespace, v1alpha1.TrackingMethodAnnotation)
	result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: path}, RenderOptions{
		AppName:       appName,
		Namespace:     namespace,
		SkipTests:     true,
		TrackManifest: track,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	upstream, err := upstreamHelmOutcomeFor(t, filepath.Join(repoRoot, path), true, appName, appName, namespace)
	if err != nil {
		t.Fatalf("upstream error = %v", err)
	}
	objs := make([]*unstructured.Unstructured, 0, len(result))
	for _, manifest := range result {
		obj := manifest.Object.DeepCopy()
		if err := track(obj); err != nil {
			t.Fatalf("track %s: %v", manifest.Path, err)
		}
		objs = append(objs, obj)
	}
	got := lastCopies(t, objs)
	if want := lastCopies(t, upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("drydock rendered\n%s\nupstream\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(got) != 6 {
		t.Fatalf("rendered %d objects, want 6 (update the README count too)", len(got))
	}
}

// TestHelmRendererErrorsDoNotEchoContent renders templates whose document
// head helm cannot read. helm template fails on each one with "YAML parse
// error on <file>" (pkg/release/v1/util/manifest_sorter.go:151-153 at helm
// v4.3.1), and the wrapped sigs.k8s.io/yaml error can copy a map key, an
// anchor name, or a value. drydock keeps the prefix and drops the content.
func TestHelmRendererErrorsDoNotEchoContent(t *testing.T) {
	const secretValue = "hunter2secret"
	for name, template := range map[string]string{
		"map key":           "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  ? {topsecret: " + secretValue + "}\n  : c\n",
		"unknown anchor":    "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  a: *" + secretValue + "\n",
		"value against tag": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  a: !!int " + secretValue + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			chartDir, _, err := renderParityChart(t, map[string]string{"templates/cm.yaml": template}, false)
			if _, upstreamErr := upstreamHelmOutcome(t, chartDir, false); upstreamErr == nil {
				t.Fatal("upstream error = nil, want a failure")
			}
			if err == nil || !strings.Contains(err.Error(), "helm template chart: YAML parse error on parity/templates/cm.yaml: ") {
				t.Fatalf("drydock error = %v, want the helm prefix", err)
			}
			if strings.Contains(err.Error(), secretValue) {
				t.Fatalf("drydock error leaked manifest data: %q", err)
			}
		})
	}
}
