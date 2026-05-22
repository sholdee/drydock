package render

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestHelmRendererRendersInlineValues(t *testing.T) {
	renderer := HelmRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications", "helm"),
		Path:     "simple",
		Chart:    "simple",
	}
	opts := RenderOptions{
		AppName:   "demo",
		Namespace: "demo-ns",
		ValuesObject: map[string]any{
			"value": "from-values-object",
		},
	}

	result, diags, err := renderer.Render(context.Background(), source, opts)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	obj := result[0].Object
	if obj.GetName() != "demo-config" || obj.GetNamespace() != "demo-ns" {
		t.Fatalf("unexpected metadata: %s/%s", obj.GetNamespace(), obj.GetName())
	}
	if got, want := result[0].Path, filepath.Join("simple", "templates", "configmap.yaml"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	value, _, _ := unstructured.NestedString(obj.Object, "data", "value")
	if value != "from-values-object" {
		t.Fatalf("data.value = %q", value)
	}
}

func TestHelmRendererUsesReleaseNameOverride(t *testing.T) {
	renderer := HelmRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications", "helm"),
		Path:     "simple",
		Chart:    "simple",
	}
	opts := RenderOptions{
		AppName:     "demo",
		Namespace:   "demo-ns",
		ReleaseName: "custom",
		ValuesObject: map[string]any{
			"value": "from-values-object",
		},
	}

	result, diags, err := renderer.Render(context.Background(), source, opts)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if got, want := result[0].Object.GetName(), "custom-config"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
}

func TestHelmRendererValuesObjectOverridesChartDefaults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "simple", "Chart.yaml"), `
apiVersion: v2
name: simple
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "simple", "values.yaml"), `
value: from-default
`)
	writeFile(t, filepath.Join(root, "simple", "templates", "configmap.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-config
  namespace: {{ .Release.Namespace }}
data:
  value: {{ .Values.value | quote }}
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "simple",
		Chart:    "simple",
	}, RenderOptions{
		AppName:   "demo",
		Namespace: "demo-ns",
		ValuesObject: map[string]any{
			"value": "from-values-object",
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	value, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "value")
	if value != "from-values-object" {
		t.Fatalf("data.value = %q, want valuesObject override", value)
	}
}

func TestHelmRendererUsesSourcePathForChartNamedDifferentlyThanDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "foo", "Chart.yaml"), `
apiVersion: v2
name: bar
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "apps", "foo", "templates", "configmap.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-config
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "foo"),
		Chart:    "bar",
	}, RenderOptions{AppName: "demo"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if got, want := result[0].Path, filepath.Join("apps", "foo", "templates", "configmap.yaml"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestHelmRendererRejectsValueFilesUntilSupported(t *testing.T) {
	renderer := HelmRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications", "helm"),
		Path:     "simple",
		Chart:    "simple",
	}

	result, diags, err := renderer.Render(context.Background(), source, RenderOptions{
		AppName:    "demo",
		Namespace:  "demo-ns",
		ValueFiles: []string{"values.yaml"},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want value files unsupported error")
	}
	if !strings.Contains(err.Error(), "value files") {
		t.Fatalf("Render() error = %v, want value files unsupported error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestHelmRendererRejectsSourcePathEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(outside, "Chart.yaml"), `
apiVersion: v2
name: outside
version: 0.1.0
`)

	renderer := HelmRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     ".." + string(filepath.Separator) + "outside",
		Chart:    "outside",
	}, RenderOptions{AppName: "demo"})
	if err == nil {
		t.Fatal("Render() error = nil, want path escape error")
	}
	if !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("Render() error = %v, want escape error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestHelmRendererRejectsSymlinkedSourcePathComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "Chart.yaml"), `
apiVersion: v2
name: outside
version: 0.1.0
`)
	symlink(t, outside, filepath.Join(root, "apps"))

	renderer := HelmRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
		Chart:    "outside",
	}, RenderOptions{AppName: "demo"})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestHelmRendererRejectsSymlinkedChartFiles(t *testing.T) {
	for _, tt := range []struct {
		name       string
		link       string
		targetData string
	}{
		{
			name: "chart metadata",
			link: "Chart.yaml",
			targetData: `
apiVersion: v2
name: simple
version: 0.1.0
`,
		},
		{
			name: "template",
			link: filepath.Join("templates", "configmap.yaml"),
			targetData: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`,
		},
		{
			name: "crd",
			link: filepath.Join("crds", "widgets.yaml"),
			targetData: `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  names:
    kind: Widget
    plural: widgets
  scope: Namespaced
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`,
		},
		{
			name: "values",
			link: "values.yaml",
			targetData: `
value: outside
`,
		},
		{
			name: "schema",
			link: "values.schema.json",
			targetData: `
{
  "type": "object"
}
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			chartDir := filepath.Join(root, "simple")
			if tt.link != "Chart.yaml" {
				writeFile(t, filepath.Join(chartDir, "Chart.yaml"), `
apiVersion: v2
name: simple
version: 0.1.0
`)
			}
			writeFile(t, filepath.Join(chartDir, "templates", "local.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: local
`)
			outside := t.TempDir()
			target := filepath.Join(outside, filepath.Base(tt.link))
			writeFile(t, target, tt.targetData)
			symlink(t, target, filepath.Join(chartDir, tt.link))

			result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "simple",
				Chart:    "simple",
			}, RenderOptions{AppName: "demo"})
			if err == nil {
				t.Fatal("Render() error = nil, want symlink error")
			}
			if !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("Render() error = %v, want symlink error", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if len(result) != 0 {
				t.Fatalf("result = %#v, want no manifests", result)
			}
		})
	}
}

func TestHelmRendererSkipsSchemaValidationForOfflineSafety(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "simple", "Chart.yaml"), `
apiVersion: v2
name: simple
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "simple", "values.schema.json"), `
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "$ref": "file:///definitely/not/inside/the/repo/schema.json"
}
`)
	writeFile(t, filepath.Join(root, "simple", "templates", "configmap.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-config
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "simple",
		Chart:    "simple",
	}, RenderOptions{AppName: "demo"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
}

func TestHelmRendererProcessesDisabledDependencies(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "parent", "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: child
    version: 0.1.0
    repository: file://charts/child
    condition: child.enabled
`)
	writeFile(t, filepath.Join(root, "parent", "templates", "parent.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: parent-config
`)
	writeFile(t, filepath.Join(root, "parent", "charts", "child", "Chart.yaml"), `
apiVersion: v2
name: child
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "parent", "charts", "child", "templates", "child.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: child-config
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{
		AppName: "demo",
		ValuesObject: map[string]any{
			"child": map[string]any{
				"enabled": false,
			},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want only parent manifest", len(result))
	}
	if got, want := result[0].Object.GetName(), "parent-config"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
}

func TestHelmRendererUsesSubchartPathForSubchartCRDs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "parent", "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: child
    version: 0.1.0
    repository: file://charts/child
`)
	writeFile(t, filepath.Join(root, "parent", "charts", "child", "Chart.yaml"), `
apiVersion: v2
name: child
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "parent", "charts", "child", "crds", "widgets.yaml"), `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  names:
    kind: Widget
    plural: widgets
  scope: Namespaced
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{AppName: "demo"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if got, want := result[0].Path, filepath.Join("parent", "charts", "child", "crds", "widgets.yaml"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestHelmRendererUsesSourcePathForAliasedSubcharts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "parent", "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: child
    alias: childalias
    version: 0.1.0
    repository: file://charts/child
`)
	writeFile(t, filepath.Join(root, "parent", "charts", "child", "Chart.yaml"), `
apiVersion: v2
name: child
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "parent", "charts", "child", "crds", "widgets.yaml"), `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  names:
    kind: Widget
    plural: widgets
  scope: Namespaced
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`)
	writeFile(t, filepath.Join(root, "parent", "charts", "child", "templates", "child.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: child-config
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{AppName: "demo"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}

	gotPaths := map[string]struct{}{}
	for _, manifest := range result {
		gotPaths[manifest.Path] = struct{}{}
	}
	for _, want := range []string{
		filepath.Join("parent", "charts", "child", "crds", "widgets.yaml"),
		filepath.Join("parent", "charts", "child", "templates", "child.yaml"),
	} {
		if _, ok := gotPaths[want]; !ok {
			t.Fatalf("paths = %#v, missing %q", gotPaths, want)
		}
	}
}
