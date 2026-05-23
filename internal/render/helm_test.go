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

func TestHelmRendererAppliesValueFiles(t *testing.T) {
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(root, "chart", "values.yaml"), `
value: from-file
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:    "demo",
		ValueFiles: []string{"values.yaml"},
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
	if value := renderedValue(t, result); value != "from-file" {
		t.Fatalf("data.value = %q, want from-file", value)
	}
}

func TestHelmRendererAppliesPrefixedRefValueFiles(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(valuesRoot, "overrides.yaml"), `
value: from-ref
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:    "demo",
		RefRoots:   map[string]string{"$values": valuesRoot},
		ValueFiles: []string{"$values/overrides.yaml"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if value := renderedValue(t, result); value != "from-ref" {
		t.Fatalf("data.value = %q, want from-ref", value)
	}
}

func TestHelmRendererIgnoresMissingValueFilesWhenEnabled(t *testing.T) {
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:                 "demo",
		ValueFiles:              []string{"missing.yaml"},
		IgnoreMissingValueFiles: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if value := renderedValue(t, result); value != "default" {
		t.Fatalf("data.value = %q, want chart default", value)
	}
}

func TestHelmRendererRejectsMissingValueFilesByDefault(t *testing.T) {
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:    "demo",
		ValueFiles: []string{"missing.yaml"},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want missing value file error")
	}
	if !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("Render() error = %v, want missing file context", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestHelmRendererRejectsValueFilePathEscape(t *testing.T) {
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(root, "outside.yaml"), `
value: outside
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:    "demo",
		ValueFiles: []string{"../outside.yaml"},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want path escape error")
	}
	if !strings.Contains(err.Error(), "escapes value files root") {
		t.Fatalf("Render() error = %v, want value files root escape", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestHelmRendererRejectsSymlinkedValueFiles(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	outside := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(outside, "values.yaml"), `
value: outside
`)
	symlink(t, filepath.Join(outside, "values.yaml"), filepath.Join(valuesRoot, "values.yaml"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:    "demo",
		RefRoots:   map[string]string{"$values": valuesRoot},
		ValueFiles: []string{"$values/values.yaml"},
	})
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

func TestHelmRendererValueFilePrecedence(t *testing.T) {
	for _, tt := range []struct {
		name              string
		valuesMergeMode   string
		valueFiles        map[string]string
		valueFileOrder    []string
		inlineValues      map[string]any
		wantRenderedValue string
	}{
		{
			name: "later value files override earlier files",
			valueFiles: map[string]string{
				"first.yaml":  "value: first\n",
				"second.yaml": "value: second\n",
			},
			valueFileOrder:    []string{"first.yaml", "second.yaml"},
			wantRenderedValue: "second",
		},
		{
			name: "inline values override files by default",
			valueFiles: map[string]string{
				"values.yaml": "value: from-file\n",
			},
			valueFileOrder:    []string{"values.yaml"},
			inlineValues:      map[string]any{"value": "from-inline"},
			wantRenderedValue: "from-inline",
		},
		{
			name:            "merge mode lets file values override inline defaults",
			valuesMergeMode: "merge",
			valueFiles: map[string]string{
				"values.yaml": "value: from-file\n",
			},
			valueFileOrder:    []string{"values.yaml"},
			inlineValues:      map[string]any{"value": "from-inline"},
			wantRenderedValue: "from-file",
		},
		{
			name:            "replace mode ignores files when inline values are non-empty",
			valuesMergeMode: "replace",
			valueFiles: map[string]string{
				"values.yaml": "value: from-file\n",
			},
			valueFileOrder:    []string{"values.yaml"},
			inlineValues:      map[string]any{"value": "from-inline"},
			wantRenderedValue: "from-inline",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeValueChart(t, filepath.Join(root, "chart"))
			for file, data := range tt.valueFiles {
				writeFile(t, filepath.Join(root, "chart", file), data)
			}

			result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "chart",
			}, RenderOptions{
				AppName:         "demo",
				ValuesMergeMode: tt.valuesMergeMode,
				ValueFiles:      tt.valueFileOrder,
				ValuesObject:    tt.inlineValues,
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if value := renderedValue(t, result); value != tt.wantRenderedValue {
				t.Fatalf("data.value = %q, want %q", value, tt.wantRenderedValue)
			}
		})
	}
}

func TestHelmRendererReplaceModeSkipsValueFileLoadingWithInlineValues(t *testing.T) {
	for _, tt := range []struct {
		name      string
		file      string
		fileData  string
		writeFile bool
	}{
		{
			name: "missing file",
			file: "missing.yaml",
		},
		{
			name:      "invalid file",
			file:      "invalid.yaml",
			fileData:  "- not\n- a\n- mapping\n",
			writeFile: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeValueChart(t, filepath.Join(root, "chart"))
			if tt.writeFile {
				writeFile(t, filepath.Join(root, "chart", tt.file), tt.fileData)
			}

			result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "chart",
			}, RenderOptions{
				AppName:         "demo",
				ValuesMergeMode: "replace",
				ValueFiles:      []string{tt.file},
				ValuesObject:    map[string]any{"value": "from-inline"},
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if value := renderedValue(t, result); value != "from-inline" {
				t.Fatalf("data.value = %q, want from-inline", value)
			}
		})
	}
}

func TestHelmRendererRejectsNonMappingValueFiles(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
	}{
		{name: "scalar", data: "not-a-map\n"},
		{name: "list", data: "- not\n- a\n- map\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeValueChart(t, filepath.Join(root, "chart"))
			writeFile(t, filepath.Join(root, "chart", "override.yaml"), tt.data)

			result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "chart",
			}, RenderOptions{
				AppName:    "demo",
				ValueFiles: []string{"override.yaml"},
			})
			if err == nil {
				t.Fatal("Render() error = nil, want YAML mapping error")
			}
			if !strings.Contains(err.Error(), "YAML mapping") {
				t.Fatalf("Render() error = %v, want YAML mapping error", err)
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

func TestHelmRendererRejectsSymlinkedValueFilesBaseDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(outside, "values.yaml"), `
value: outside
`)
	symlink(t, outside, filepath.Join(root, "linked-values"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:           "demo",
		ValueFilesBaseDir: "linked-values",
		ValueFiles:        []string{"values.yaml"},
	})
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

func TestHelmRendererOmitsCRDsWhenDisabled(t *testing.T) {
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(root, "chart", "crds", "widgets.yaml"), `
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
		Path:     "chart",
	}, RenderOptions{
		AppName:        "demo",
		IncludeCRDsSet: true,
		IncludeCRDs:    false,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want only template manifest", len(result))
	}
	if got, want := result[0].Object.GetKind(), "ConfigMap"; got != want {
		t.Fatalf("kind = %q, want %q", got, want)
	}
}

func TestHelmRendererSkipsHookManifests(t *testing.T) {
	root := t.TempDir()
	writeHookChart(t, filepath.Join(root, "chart"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:   "demo",
		SkipHooks: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := manifestNames(result); strings.Join(got, ",") != "normal" {
		t.Fatalf("manifest names = %#v, want only normal", got)
	}
}

func TestHelmRendererSkipsTestHooksOnly(t *testing.T) {
	root := t.TempDir()
	writeHookChart(t, filepath.Join(root, "chart"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:   "demo",
		SkipTests: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := manifestNames(result); strings.Join(got, ",") != "normal,pre-install" {
		t.Fatalf("manifest names = %#v, want normal and non-test hook", got)
	}
}

func TestHelmRendererUsesSourcePathForAliasedSubcharts(t *testing.T) {
	for _, tt := range []struct {
		name       string
		repository string
	}{
		{name: "file repository", repository: "file://charts/child"},
		{name: "remote repository", repository: "https://charts.example.com"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "parent", "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: child
    alias: childalias
    version: 0.1.0
    repository: `+tt.repository+`
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
		})
	}
}

func TestHelmRendererUsesSourcePathForRepeatedAliasedSubcharts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "parent", "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: child
    alias: childa
    version: 0.1.0
    repository: https://charts.example.com
  - name: child
    alias: childb
    version: 0.1.0
    repository: https://charts.example.com
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
  name: {{ .Chart.Name }}-config
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
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want one manifest for each alias", len(result))
	}
	for _, manifest := range result {
		if got, want := manifest.Path, filepath.Join("parent", "charts", "child", "templates", "child.yaml"); got != want {
			t.Fatalf("Path = %q, want %q", got, want)
		}
	}
}

func writeValueChart(t *testing.T, chartDir string) {
	t.Helper()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), `
apiVersion: v2
name: chart
version: 0.1.0
`)
	writeFile(t, filepath.Join(chartDir, "values.yaml"), `
value: default
`)
	writeFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
`)
}

func writeHookChart(t *testing.T, chartDir string) {
	t.Helper()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), `
apiVersion: v2
name: hooks
version: 0.1.0
`)
	writeFile(t, filepath.Join(chartDir, "templates", "normal.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: normal
`)
	writeFile(t, filepath.Join(chartDir, "templates", "pre-install.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: pre-install
  annotations:
    helm.sh/hook: pre-install
`)
	writeFile(t, filepath.Join(chartDir, "templates", "test.yaml"), `
apiVersion: v1
kind: Pod
metadata:
  name: test
  annotations:
    helm.sh/hook: pre-install, test-success
spec:
  containers:
    - name: test
      image: example/test:latest
  restartPolicy: Never
`)
}

func renderedValue(t *testing.T, result []Manifest) string {
	t.Helper()
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	value, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "value")
	return value
}

func manifestNames(result []Manifest) []string {
	names := make([]string, 0, len(result))
	for _, manifest := range result {
		names = append(names, manifest.Object.GetName())
	}
	return names
}
