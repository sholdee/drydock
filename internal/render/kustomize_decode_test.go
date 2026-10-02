package render

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// These tests pin that drydock reads a kustomization file the way kustomize
// does. krusty decodes kustomization.yaml through JSON: keys match fields
// case-insensitively, an unknown key fails, and FixKustomization folds the
// deprecated fields into their replacements. A walk that reads the file any
// other way disagrees with the render about which files it reads: the
// persistent render cache misses an input, changed-only selection misses an
// owner, boundary validation misses a ref, and the prepared workspace
// rewrites the kustomization without the content it never decoded.

func decodeTestConfigMap(name, value string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\ndata:\n  value: " + value + "\n"
}

func writeDecodeTestFixture(t *testing.T, root, kustomization string, files map[string]string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), kustomization)
	for file, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(file)), content)
	}
}

func renderDecodeTestFixture(t *testing.T, root string, opts RenderOptions) []Manifest {
	t.Helper()
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, opts)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return manifests
}

func configMapValue(manifests []Manifest, name string) (string, bool) {
	configMap := findManifest(manifests, "ConfigMap", name)
	if configMap == nil {
		return "", false
	}
	value, _, _ := unstructured.NestedString(configMap.Object, "data", "value")
	return value, true
}

func renderedConfigMapNames(manifests []Manifest) []string {
	configMaps := filterObjects(manifests, "ConfigMap")
	names := make([]string, 0, len(configMaps))
	for _, configMap := range configMaps {
		names = append(names, configMap.GetName())
	}
	slices.Sort(names)
	return names
}

func assertSelectionPathsInclude(t *testing.T, root string, want ...string) {
	t.Helper()
	selection, err := KustomizeSelectionPaths(context.Background(), root, "apps/demo", nil)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	for _, path := range want {
		if !slices.Contains(selection, path) {
			t.Errorf("KustomizeSelectionPaths() = %v, missing %q", selection, path)
		}
	}
}

// TestKustomizeWalksReadKeysCaseInsensitively pins that a kustomization key
// kustomize matches case-insensitively is walked: the strict digest and
// changed-only selection include what it names, and the render reads it.
func TestKustomizeWalksReadKeysCaseInsensitively(t *testing.T) {
	baseFiles := map[string]string{
		"base/kustomization.yaml": "resources:\n  - cm.yaml\n",
		"base/cm.yaml":            decodeTestConfigMap("demo", "base"),
	}
	for _, tt := range []struct {
		name          string
		kustomization string
		files         map[string]string
		want          []string
		wantValue     string
	}{
		{
			name:          "Resources",
			kustomization: "Resources:\n  - ../../base\n",
			files:         baseFiles,
			want:          []string{"base", "base/kustomization.yaml", "base/cm.yaml"},
			wantValue:     "base",
		},
		{
			name:          "deprecated Bases",
			kustomization: "Bases:\n  - ../../base\n",
			files:         baseFiles,
			want:          []string{"base", "base/kustomization.yaml", "base/cm.yaml"},
			wantValue:     "base",
		},
		{
			name:          "Transformers",
			kustomization: "resources:\n  - cm.yaml\nTransformers:\n  - transformer.yaml\n",
			files: map[string]string{
				"apps/demo/cm.yaml":          decodeTestConfigMap("demo", "base"),
				"apps/demo/transformer.yaml": "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: patch\npath: patch.yaml\n",
				"apps/demo/patch.yaml":       decodeTestConfigMap("demo", "patched"),
			},
			want:      []string{"apps/demo/transformer.yaml", "apps/demo/patch.yaml"},
			wantValue: "patched",
		},
		{
			name:          "nested generator keys and deprecated env",
			kustomization: "ConfigMapGenerator:\n  - Name: demo\n    Env: demo.env\n    Options:\n      DisableNameSuffixHash: true\n",
			files:         map[string]string{"apps/demo/demo.env": "value=env\n"},
			want:          []string{"apps/demo/demo.env"},
			wantValue:     "env",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeDecodeTestFixture(t, root, tt.kustomization, tt.files)

			if got, ok := configMapValue(renderDecodeTestFixture(t, root, RenderOptions{}), "demo"); !ok || got != tt.wantValue {
				t.Fatalf("rendered ConfigMap demo value = %q (found %v), want %q", got, ok, tt.wantValue)
			}
			assertRequiredDigestPaths(t, pluginDigestPaths(t, root, "apps/demo"), tt.want...)
			assertSelectionPathsInclude(t, root, tt.want...)
		})
	}
}

// TestKustomizeWalksResolveFoldedDuplicateKeysLikeKustomize pins that when
// two keys fold to one field, the walks keep the one kustomize keeps, taken
// from the render rather than assumed: the digest includes exactly the
// files the render reads.
func TestKustomizeWalksResolveFoldedDuplicateKeysLikeKustomize(t *testing.T) {
	files := map[string]string{
		"apps/demo/a.yaml": decodeTestConfigMap("a", "a"),
		"apps/demo/b.yaml": decodeTestConfigMap("b", "b"),
	}
	for _, tt := range []struct {
		name          string
		kustomization string
	}{
		{name: "lowercase first", kustomization: "resources:\n  - a.yaml\nResources:\n  - b.yaml\n"},
		{name: "capitalized first", kustomization: "Resources:\n  - b.yaml\nresources:\n  - a.yaml\n"},
		{name: "two capitalizations", kustomization: "RESOURCES:\n  - a.yaml\nResources:\n  - b.yaml\n"},
		{name: "exact duplicate", kustomization: "resources:\n  - a.yaml\nresources:\n  - b.yaml\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeDecodeTestFixture(t, root, tt.kustomization, files)

			rendered := renderedConfigMapNames(renderDecodeTestFixture(t, root, RenderOptions{}))
			if len(rendered) != 1 {
				t.Fatalf("rendered ConfigMaps = %v, want exactly one duplicate key to win", rendered)
			}
			paths := pluginDigestPaths(t, root, "apps/demo")
			for _, name := range []string{"a", "b"} {
				file := "apps/demo/" + name + ".yaml"
				if name == rendered[0] {
					assertRequiredDigestPaths(t, paths, file)
				} else {
					assertDigestPathsExclude(t, paths, file)
				}
			}
		})
	}
}

// TestKustomizeUnknownKustomizationFieldFailsLikeKustomize pins that a key
// kustomize rejects fails every walk: the digest (the source is not cached),
// selection (ownership falls back to the source path) and both render paths.
// The prepared path decoded the file leniently and rewrote it without the
// key, so it rendered what kustomize refuses to.
func TestKustomizeUnknownKustomizationFieldFailsLikeKustomize(t *testing.T) {
	root := t.TempDir()
	writeDecodeTestFixture(t, root, "resources:\n  - cm.yaml\nresourcez:\n  - other.yaml\n", map[string]string{
		"apps/demo/cm.yaml":    decodeTestConfigMap("demo", "base"),
		"apps/demo/other.yaml": decodeTestConfigMap("other", "other"),
	})
	const want = `unknown field "resourcez"`

	if _, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, RenderOptions{}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("KustomizeInputDigestPaths() error = %v, want %q", err, want)
	}
	if _, err := KustomizeSelectionPaths(context.Background(), root, "apps/demo", nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("KustomizeSelectionPaths() error = %v, want %q", err, want)
	}
	for name, opts := range map[string]RenderOptions{
		"plain":    {},
		"prepared": {Kustomize: &argoappv1.ApplicationSourceKustomize{NamePrefix: "p-"}},
	} {
		if _, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, opts); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s Render() error = %v, want %q", name, err, want)
		}
	}
}

// TestKustomizePreparedWorkspaceRendersLikeKustomize pins that the prepared
// workspace, which rewrites the kustomization, renders what kustomize
// renders from the original file: the same kustomization rendered with a
// source namePrefix (prepared) and with the prefix written into it (plain,
// kustomize reads the original) must agree, capitalized, duplicated and
// deprecated keys included.
func TestKustomizePreparedWorkspaceRendersLikeKustomize(t *testing.T) {
	baseFiles := map[string]string{
		"base/kustomization.yaml": "resources:\n  - cm.yaml\n",
		"base/cm.yaml":            decodeTestConfigMap("demo", "base"),
	}
	for _, tt := range []struct {
		name          string
		kustomization string
		files         map[string]string
	}{
		{name: "Resources", kustomization: "Resources:\n  - ../../base\n", files: baseFiles},
		{
			name:          "folded duplicate",
			kustomization: "RESOURCES:\n  - a.yaml\nResources:\n  - b.yaml\n",
			files:         map[string]string{"apps/demo/a.yaml": decodeTestConfigMap("a", "a"), "apps/demo/b.yaml": decodeTestConfigMap("b", "b")},
		},
		// Each deprecated field is folded into its replacement exactly once:
		// read twice, the base would be accumulated twice and the env keys
		// generated twice, and kustomize rejects both.
		{
			name:          "deprecated fields",
			kustomization: "bases:\n  - ../../base\nimageTags:\n  - name: nginx\n    newTag: \"1.0\"\nconfigMapGenerator:\n  - name: generated\n    env: generated.env\n    options:\n      disableNameSuffixHash: true\nresources:\n  - deployment.yaml\n",
			files: map[string]string{
				"base/kustomization.yaml":   baseFiles["base/kustomization.yaml"],
				"base/cm.yaml":              baseFiles["base/cm.yaml"],
				"apps/demo/generated.env":   "value=env\n",
				"apps/demo/deployment.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: nginx\n",
			},
		},
		// An inline patch whose text starts with a blank line is a block
		// scalar inside a patches: item; the rewrite must encode it so
		// kustomize can read it back (at yaml.v3's default indent it could
		// not: "did not find expected key").
		{
			name:          "inline patch with leading blank line",
			kustomization: "resources:\n  - cm.yaml\npatches:\n  - target:\n      kind: ConfigMap\n      name: demo\n    patch: |\n\n      apiVersion: v1\n      kind: ConfigMap\n      metadata:\n        name: demo\n      data:\n        value: patched\n",
			files:         map[string]string{"apps/demo/cm.yaml": decodeTestConfigMap("demo", "base")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plainRoot := t.TempDir()
			writeDecodeTestFixture(t, plainRoot, "namePrefix: p-\n"+tt.kustomization, tt.files)
			preparedRoot := t.TempDir()
			writeDecodeTestFixture(t, preparedRoot, tt.kustomization, tt.files)

			plain := renderDecodeTestFixture(t, plainRoot, RenderOptions{})
			if len(plain) == 0 {
				t.Fatal("plain render is empty")
			}
			prepared := renderDecodeTestFixture(t, preparedRoot, RenderOptions{Kustomize: &argoappv1.ApplicationSourceKustomize{NamePrefix: "p-"}})
			if !reflect.DeepEqual(manifestObjects(prepared), manifestObjects(plain)) {
				t.Fatalf("prepared render = %v\nwant plain render %v", manifestObjects(prepared), manifestObjects(plain))
			}
		})
	}
}

func manifestObjects(manifests []Manifest) []map[string]any {
	out := make([]map[string]any, 0, len(manifests))
	for _, manifest := range manifests {
		out = append(out, manifest.Object.Object)
	}
	return out
}

// TestKustomizePreparedWorkspaceHelmChartsKeepCapitalizedKeys pins the
// helmCharts form of the prepared-workspace rewrite: capitalized content
// next to helmCharts survives, and a capitalized helmCharts key is inflated
// by drydock like a lowercase one instead of reaching kustomize, which runs
// with Helm disabled.
func TestKustomizePreparedWorkspaceHelmChartsKeepCapitalizedKeys(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		want          []string
	}{
		{name: "Resources next to helmCharts", kustomization: "Resources:\n  - ../../base\nhelmCharts:\n  - name: demo\n    releaseName: chart\n", want: []string{"chart", "demo"}},
		{name: "HelmCharts", kustomization: "HelmCharts:\n  - Name: demo\n    ReleaseName: chart\n", want: []string{"chart"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeDecodeTestFixture(t, root, tt.kustomization, map[string]string{
				"base/kustomization.yaml": "resources:\n  - cm.yaml\n",
				"base/cm.yaml":            decodeTestConfigMap("demo", "base"),
			})
			writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n")

			if got := renderedConfigMapNames(renderDecodeTestFixture(t, root, RenderOptions{})); !slices.Equal(got, tt.want) {
				t.Fatalf("rendered ConfigMaps = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestKustomizeSourceImagesReplaceDeprecatedImageTags pins Argo CD's source
// image override semantics against a deprecated imageTags entry: kustomize
// edit set image reads the kustomization with imageTags already folded into
// images, so an override naming the same image replaces the entry instead
// of being applied before it.
func TestKustomizeSourceImagesReplaceDeprecatedImageTags(t *testing.T) {
	root := t.TempDir()
	writeDecodeTestFixture(t, root, "resources:\n  - deployment.yaml\nimageTags:\n  - name: nginx\n    newTag: \"1.0\"\n", map[string]string{
		"apps/demo/deployment.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: nginx\n",
	})

	manifests := renderDecodeTestFixture(t, root, RenderOptions{Kustomize: &argoappv1.ApplicationSourceKustomize{Images: argoappv1.KustomizeImages{"nginx:2.0"}}})
	deployment := findManifest(manifests, "Deployment", "web")
	if deployment == nil {
		t.Fatalf("rendered manifests = %#v, want Deployment web", manifests)
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("containers = %#v, want one", containers)
	}
	container, ok := containers[0].(map[string]any)
	if !ok || container["image"] != "nginx:2.0" {
		t.Fatalf("container = %#v, want the source override image nginx:2.0", containers[0])
	}
}

// TestKustomizeCapitalizedKeysCannotEscapeRepository pins that boundary
// validation reads the refs kustomize follows. A walk that skipped a
// capitalized, all-caps or deprecated key let the plain path read outside
// the repository through it (or fetch a remote resource itself, offline
// mode or not), while the prepared path dropped it from the rewrite. Each
// row fails closed with and without a source option (the plain render and
// the prepared workspace); where kustomize keeps an in-repo duplicate, only
// that renders. No row reaches the network.
func TestKustomizeCapitalizedKeysCannotEscapeRepository(t *testing.T) {
	loadRestrictionsNone := []string{"--load-restrictor=LoadRestrictionsNone"}
	for _, tt := range []struct {
		name          string
		kustomization string // {{outside}} and {{remote}} expand to the outside directory and a local HTTP server
		opts          RenderOptions
		symlink       bool   // apps/demo/linked links to the outside directory
		wantErr       string // empty: the render keeps only the in-repo ConfigMap safe
	}{
		{name: "Resources", kustomization: "Resources:\n  - ../../../outside\n", wantErr: "escapes repository root"},
		{name: "RESOURCES", kustomization: "RESOURCES:\n  - ../../../outside\n", wantErr: "escapes repository root"},
		{name: "Components", kustomization: "Components:\n  - ../../../outside-component\n", wantErr: "escapes repository root"},
		{name: "deprecated Bases", kustomization: "Bases:\n  - ../../../outside\n", wantErr: "escapes repository root"},
		{name: "duplicate keeps the escaping key", kustomization: "RESOURCES:\n  - safe.yaml\nResources:\n  - ../../../outside\n", wantErr: "escapes repository root"},
		{name: "duplicate keeps the lowercase key", kustomization: "Resources:\n  - ../../../outside\nresources:\n  - safe.yaml\n"},
		{name: "symlinked directory", kustomization: "Resources:\n  - linked\n", symlink: true, wantErr: "symlink"},
		{name: "offline remote HTTP resource", kustomization: "Resources:\n  - {{remote}}/cm.yaml\n", opts: RenderOptions{OfflineRemoteResources: true}, wantErr: "offline cache miss"},
		{name: "LoadRestrictionsNone absolute generator file", kustomization: "ConfigMapGenerator:\n  - name: outside\n    files:\n      - {{outside}}/data.txt\n", opts: RenderOptions{BuildOptions: loadRestrictionsNone}, wantErr: "must be relative"},
		{name: "LoadRestrictionsNone escaping generator file", kustomization: "ConfigMapGenerator:\n  - name: outside\n    files:\n      - ../../../outside/data.txt\n", opts: RenderOptions{BuildOptions: loadRestrictionsNone}, wantErr: "escapes repository root"},
	} {
		for _, prepared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prepared=%v", tt.name, prepared), func(t *testing.T) {
				var requests atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					_, _ = io.WriteString(w, decodeTestConfigMap("outside", "remote"))
				}))
				t.Cleanup(server.Close)

				top := t.TempDir()
				root := filepath.Join(top, "repo")
				outside := filepath.Join(top, "outside")
				writeFile(t, filepath.Join(outside, "kustomization.yaml"), "resources:\n  - cm.yaml\n")
				writeFile(t, filepath.Join(outside, "cm.yaml"), decodeTestConfigMap("outside", "outside"))
				writeFile(t, filepath.Join(outside, "data.txt"), "outside\n")
				writeFile(t, filepath.Join(top, "outside-component", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - cm.yaml\n")
				writeFile(t, filepath.Join(top, "outside-component", "cm.yaml"), decodeTestConfigMap("outside", "component"))
				kustomization := strings.NewReplacer("{{outside}}", filepath.ToSlash(outside), "{{remote}}", server.URL).Replace(tt.kustomization)
				writeDecodeTestFixture(t, root, kustomization, map[string]string{"apps/demo/safe.yaml": decodeTestConfigMap("safe", "safe")})
				if tt.symlink {
					symlink(t, outside, filepath.Join(root, "apps", "demo", "linked"))
				}

				opts := tt.opts
				opts.RemoteResourceCacheDir = t.TempDir()
				want := []string{"safe"}
				if prepared {
					opts.Kustomize = &argoappv1.ApplicationSourceKustomize{NamePrefix: "p-"}
					want = []string{"p-safe"}
				}
				manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, opts)
				rendered := renderedConfigMapNames(manifests)
				switch {
				case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
					t.Errorf("Render() rendered ConfigMaps %v, error %v; want error containing %q", rendered, err, tt.wantErr)
				case tt.wantErr == "" && (err != nil || !slices.Equal(rendered, want)):
					t.Errorf("Render() rendered ConfigMaps %v, error %v; want %v", rendered, err, want)
				}
				if got := requests.Load(); got != 0 {
					t.Errorf("remote server received %d requests, want none", got)
				}
			})
		}
	}
}

// TestKustomizeHelmChartInflationGeneratorRejectionIsPolicyError pins the
// rejection's wording: it names the kustomization like drydock's other
// policy errors and is not reported as a decode failure.
func TestKustomizeHelmChartInflationGeneratorRejectionIsPolicyError(t *testing.T) {
	root := t.TempDir()
	writeDecodeTestFixture(t, root, "helmChartInflationGenerator:\n  - chartName: demo\n", nil)
	const want = "apps/demo/kustomization.yaml: helmChartInflationGenerator is deprecated and unsupported"

	if _, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, RenderOptions{}); err == nil || err.Error() != want {
		t.Fatalf("Render() error = %v, want %q", err, want)
	}
}

// TestKustomizeHelmValuesInlineNumbersRenderLikeArgoCD pins valuesInline
// numbers to Argo CD's output. Helm reads values through JSON, so every
// number reaches the template as a float64 and one of 1e6 or more prints in
// exponent form: kustomize 5.8.1 with helm 4.1.4 renders large as "1e+06"
// and mid as "1.234567e+06". The kustomization's JSON decode already yields
// float64s, which the generated values file keeps.
func TestKustomizeHelmValuesInlineNumbersRenderLikeArgoCD(t *testing.T) {
	root := t.TempDir()
	writeTestChart(t, filepath.Join(root, "apps", "demo", "charts", "demo"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  large: {{ .Values.large | quote }}
  mid: {{ .Values.mid | quote }}
  small: {{ .Values.small | quote }}
  fraction: {{ .Values.fraction | quote }}
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "helmCharts:\n  - name: demo\n    valuesInline:\n      large: 1000000\n      mid: 1234567\n      small: 999999\n      fraction: 1.5\n")

	manifests := renderDecodeTestFixture(t, root, RenderOptions{})
	configMap := findManifest(manifests, "ConfigMap", "demo")
	if configMap == nil {
		t.Fatalf("rendered manifests = %#v, want ConfigMap demo", manifests)
	}
	for key, want := range map[string]string{"large": "1e+06", "mid": "1.234567e+06", "small": "999999", "fraction": "1.5"} {
		if got, _, _ := unstructured.NestedString(configMap.Object, "data", key); got != want {
			t.Errorf("data[%q] = %q, want %q", key, got, want)
		}
	}
}
