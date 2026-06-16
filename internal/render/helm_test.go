package render

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/chart"
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

func TestHelmRendererAVPCompatibilitySubstitutesInputValuesBeforeRender(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "simple", "Chart.yaml"), `
apiVersion: v2
name: simple
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "simple", "templates", "configmap.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  domain: {{ .Values.domain | quote }}
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "simple",
		Chart:    "simple",
	}, RenderOptions{
		AppName:         "demo",
		EnableAVPCompat: true,
		ValuesObject: map[string]any{
			"domain": "argocd.<path:vaults/Kubernetes/items/cluster#domain>",
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	value, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "domain")
	if !strings.HasPrefix(value, "argocd.drydock-redacted-") {
		t.Fatalf("data.domain = %q, want redacted AVP value", value)
	}
	for _, forbidden := range []string{"vaults", "Kubernetes", "cluster", "<path:"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("data.domain = %q contains forbidden placeholder material %q", value, forbidden)
		}
	}
	if len(diags) != 1 || diags[0].Code != "plugin.avp-compat-substituted" {
		t.Fatalf("diagnostics = %#v, want AVP compatibility warning", diags)
	}
	if strings.Contains(diags[0].Message, "vaults") || strings.Contains(diags[0].Message, "<path:") {
		t.Fatalf("diagnostic leaked placeholder material: %#v", diags[0])
	}
}

func TestHelmRendererAVPCompatibilitySubstitutesValueFilesBeforeRender(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "simple", "Chart.yaml"), `
apiVersion: v2
name: simple
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "simple", "values.yaml"), `
domain: argocd.<path:vaults/Kubernetes/items/cluster#domain>
`)
	writeFile(t, filepath.Join(root, "simple", "templates", "configmap.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  domain: {{ .Values.domain | quote }}
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "simple",
		Chart:    "simple",
	}, RenderOptions{
		AppName:         "demo",
		EnableAVPCompat: true,
		ValueFiles:      []string{"values.yaml"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	value, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "domain")
	if !strings.HasPrefix(value, "argocd.drydock-redacted-") {
		t.Fatalf("data.domain = %q, want redacted AVP value", value)
	}
	if len(diags) != 1 || diags[0].Code != "plugin.avp-compat-substituted" {
		t.Fatalf("diagnostics = %#v, want AVP compatibility warning", diags)
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

func TestHelmRendererPreservesNullChartDefaultsBehindHasKeyGuard(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "simple", "Chart.yaml"), "apiVersion: v2\nname: simple\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(root, "simple", "values.yaml"), "debugVerbose:\n")
	writeFile(t, filepath.Join(root, "simple", "templates", "configmap.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-config\n  namespace: {{ .Release.Namespace }}\ndata:\n{{- if hasKey .Values \"debugVerbose\" }}\n  debug-verbose: \"{{ .Values.debugVerbose }}\"\n{{- end }}\n")
	result, diags, err := (HelmRenderer{}).Render(context.Background(),
		ResolvedSource{RepoRoot: root, Path: "simple", Chart: "simple"},
		RenderOptions{AppName: "demo", Namespace: "demo-ns"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	// Cilium's pattern is `key: "{{ .Values.x }}"` (literal quotes): with a null chart default
	// the key renders as an empty STRING (matching Argo CD's helm v3), not YAML null.
	val, found, err := unstructured.NestedString(result[0].Object.Object, "data", "debug-verbose")
	if err != nil {
		t.Fatalf("data.debug-verbose accessor error: %v", err)
	}
	if !found {
		t.Fatalf("data.debug-verbose missing: helm stripped the null chart default behind the hasKey guard")
	}
	if val != "" {
		t.Fatalf("data.debug-verbose = %q, want empty string", val)
	}
}

func TestHelmRendererSplitsCompactTemplateSeparators(t *testing.T) {
	for _, tt := range []struct {
		name     string
		template string
	}{
		{
			name: "separator before comment",
			template: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
{{- if .Values.enabled -}}# Permission generated by chart
apiVersion: v1
kind: ConfigMap
metadata:
  name: second
{{- end }}
`,
		},
		{
			name: "separator before apiVersion",
			template: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
{{- if .Values.enabled -}}apiVersion: v1
kind: ConfigMap
metadata:
  name: second
{{- end }}
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "chart", "Chart.yaml"), `
apiVersion: v2
name: chart
version: 0.1.0
`)
			writeFile(t, filepath.Join(root, "chart", "values.yaml"), `
enabled: true
`)
			writeFile(t, filepath.Join(root, "chart", "templates", "multi.yaml"), tt.template)

			result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "chart",
			}, RenderOptions{AppName: "demo"})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if got, want := manifestNames(result), []string{"first", "second"}; strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("manifest names = %#v, want %#v", got, want)
			}
		})
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

func TestHelmRendererAppliesHelmParametersAndFileParameters(t *testing.T) {
	root := t.TempDir()
	writeParameterChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(root, "chart", "message,one.txt"), "from-file")

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName: "demo",
		ArgoEnv: argoappv1.Env{
			{Name: "ARGOCD_APP_NAME", Value: "demo-app"},
		},
		HelmParameters: []argoappv1.HelmParameter{
			{Name: "value", Value: "from,parameter"},
			{Name: "flag", Value: "true", ForceString: true},
			{Name: "appName", Value: "$ARGOCD_APP_NAME"},
			{Name: "fileValue", Value: "from-parameter"},
		},
		HelmFileParameters: []argoappv1.HelmFileParameter{
			{Name: "fileValue", Path: "message,one.txt"},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	for key, want := range map[string]string{
		"value":     "from,parameter",
		"flag":      "string:true",
		"appName":   "demo-app",
		"fileValue": "from-file",
	} {
		got, _, _ := unstructured.NestedString(result[0].Object.Object, "data", key)
		if got != want {
			t.Fatalf("data[%q] = %#v, want %q", key, got, want)
		}
	}
}

func TestCleanHelmSetParameterMatchesArgoEscaping(t *testing.T) {
	for input, want := range map[string]string{
		"val":        "val",
		"not, clean": `not\, clean`,
		`a\,b,c`:     `a\,b\,c`,
		"{a,b,c}":    "{a,b,c}",
		`,,,,,\,`:    `\,\,\,\,\,\,`,
		`\,,\\,,`:    `\,\,\\,\,`,
	} {
		if got := cleanHelmSetParameter(input); got != want {
			t.Fatalf("cleanHelmSetParameter(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHelmRendererAppliesRefHelmFileParameters(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	writeParameterChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(valuesRoot, "message.txt"), "from-ref-file")

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName: "demo",
		RefRoots: map[string]string{
			"$values": valuesRoot,
		},
		HelmFileParameters: []argoappv1.HelmFileParameter{
			{Name: "fileValue", Path: "$values/message.txt"},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	value, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "fileValue")
	if value != "from-ref-file" {
		t.Fatalf("data.fileValue = %q, want from-ref-file", value)
	}
}

func TestHelmRendererRejectsSymlinkedHelmFileParameters(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	outside := t.TempDir()
	writeParameterChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(outside, "message.txt"), "outside")
	symlink(t, filepath.Join(outside, "message.txt"), filepath.Join(valuesRoot, "message.txt"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName: "demo",
		RefRoots: map[string]string{
			"$values": valuesRoot,
		},
		HelmFileParameters: []argoappv1.HelmFileParameter{
			{Name: "fileValue", Path: "$values/message.txt"},
		},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink error", err)
	}
	if strings.Contains(err.Error(), "outside") {
		t.Fatalf("Render() error = %v leaked file contents", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
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
	// A path that escapes the repository root must be rejected even though it is
	// within the repository root's parent directory.  The chart lives at
	// root/charts/demo so ../../.. exits root entirely.
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "charts", "demo"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "charts/demo",
	}, RenderOptions{
		AppName:    "demo",
		ValueFiles: []string{"../../../outside.yaml"},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want path escape error")
	}
	if !strings.Contains(err.Error(), "escapes value files") {
		t.Fatalf("Render() error = %v, want value files escape", err)
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

func TestHelmRendererRejectsSymlinkedRefRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, filepath.Join(outside, "values.yaml"), `
value: outside
`)
	symlink(t, outside, filepath.Join(root, "values-link"))

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:    "demo",
		RefRoots:   map[string]string{"$values": filepath.Join(root, "values-link")},
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

func TestHelmRendererAppliesValueFileGlobsInOrder(t *testing.T) {
	for _, tt := range []struct {
		name      string
		files     map[string]string
		valueRefs []string
		want      string
	}{
		{
			name: "later explicit value overrides expanded glob",
			files: map[string]string{
				"values/00.yaml": "value: zero\n",
				"values/10.yaml": "value: ten\n",
				"override.yaml":  "value: explicit\n",
			},
			valueRefs: []string{"values/*.yaml", "override.yaml"},
			want:      "explicit",
		},
		{
			name: "later expanded glob overrides earlier explicit value",
			files: map[string]string{
				"values/00.yaml": "value: zero\n",
				"values/10.yaml": "value: ten\n",
				"override.yaml":  "value: explicit\n",
			},
			valueRefs: []string{"override.yaml", "values/*.yaml"},
			want:      "ten",
		},
		{
			name: "recursive glob preserves flat before nested expansion",
			files: map[string]string{
				"values/root.yaml":         "value: root\n",
				"values/nested/child.yaml": "value: nested\n",
			},
			valueRefs: []string{"values/**/*.yaml"},
			want:      "nested",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeValueChart(t, filepath.Join(root, "chart"))
			for file, data := range tt.files {
				writeFile(t, filepath.Join(root, "chart", file), data)
			}

			result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "chart",
			}, RenderOptions{
				AppName:    "demo",
				ValueFiles: tt.valueRefs,
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if value := renderedValue(t, result); value != tt.want {
				t.Fatalf("data.value = %q, want %q", value, tt.want)
			}
		})
	}
}

func TestHelmRendererLoadsRemoteValueFiles(t *testing.T) {
	root := t.TempDir()
	remoteFile := filepath.Join(t.TempDir(), "values.yaml")
	writeValueChart(t, filepath.Join(root, "chart"))
	writeFile(t, remoteFile, "value: from-remote\n")
	acquirer := &fakeRemoteAcquirer{path: remoteFile}

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{
		AppName:                "demo",
		ValueFiles:             []string{"https://values.example.test/team/demo.yaml"},
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: t.TempDir(),
		RefreshRemoteResources: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if value := renderedValue(t, result); value != "from-remote" {
		t.Fatalf("data.value = %q, want from-remote", value)
	}
	if len(acquirer.requests) != 1 || acquirer.requests[0].URL != "https://values.example.test/team/demo.yaml" {
		t.Fatalf("remote requests = %#v", acquirer.requests)
	}
	if len(acquirer.options) != 1 || !acquirer.options[0].Refresh {
		t.Fatalf("remote options = %#v, want refresh option", acquirer.options)
	}
}

func TestHelmRendererRejectsDisallowedRemoteValueFileSchemes(t *testing.T) {
	for _, tt := range []struct {
		name       string
		opts       RenderOptions
		valueFile  string
		wantErr    string
		wantNoCall bool
	}{
		{
			name:       "default allows only http and https",
			opts:       RenderOptions{},
			valueFile:  "s3://bucket/values.yaml",
			wantErr:    `scheme "s3" is not allowed`,
			wantNoCall: true,
		},
		{
			name: "explicit empty disables remote value file URLs",
			opts: RenderOptions{
				HelmValueFileSchemesSet: true,
			},
			valueFile:  "https://values.example.test/team/demo.yaml",
			wantErr:    `scheme "https" is not allowed`,
			wantNoCall: true,
		},
		{
			name: "configured non-http schemes fail closed",
			opts: RenderOptions{
				HelmValueFileSchemes:    []string{"s3"},
				HelmValueFileSchemesSet: true,
			},
			valueFile:  "s3://bucket/values.yaml",
			wantErr:    `scheme "s3" is configured but not supported`,
			wantNoCall: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeValueChart(t, filepath.Join(root, "chart"))
			acquirer := &fakeRemoteAcquirer{path: filepath.Join(t.TempDir(), "values.yaml")}
			opts := tt.opts
			opts.AppName = "demo"
			opts.ValueFiles = []string{tt.valueFile}
			opts.RemoteResourceAcquirer = acquirer

			result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "chart",
			}, opts)
			if err == nil {
				t.Fatal("Render() error = nil, want scheme error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Render() error = %v, want %q", err, tt.wantErr)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if len(result) != 0 {
				t.Fatalf("result = %#v, want no manifests", result)
			}
			if tt.wantNoCall && len(acquirer.requests) != 0 {
				t.Fatalf("remote requests = %#v, want none", acquirer.requests)
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

func TestHelmRendererValidatesSchemaByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "simple", "Chart.yaml"), `
apiVersion: v2
name: simple
version: 0.1.0
`)
	writeFile(t, filepath.Join(root, "simple", "values.yaml"), `
value: not-an-integer
`)
	writeFile(t, filepath.Join(root, "simple", "values.schema.json"), `
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "value": {
      "type": "integer"
    }
  }
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
	if err == nil {
		t.Fatal("Render() error = nil, want schema validation error")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Fatalf("Render() error = %v, want schema context", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestHelmRendererSkipsSchemaValidationWhenRequested(t *testing.T) {
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
	}, RenderOptions{AppName: "demo", SkipSchemaValidation: true})
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

func TestHelmRendererRejectsDeclaredDependenciesMissingFromChartsDirectory(t *testing.T) {
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
	writeFile(t, filepath.Join(root, "parent", "templates", "parent.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: parent-config
`)

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{AppName: "demo"})
	if err == nil {
		t.Fatal("Render() error = nil, want missing dependency error")
	}
	if !strings.Contains(err.Error(), "missing in charts") || !strings.Contains(err.Error(), "require vendored charts") {
		t.Fatalf("Render() error = %v, want vendored dependency context", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestHelmRendererAcquiresMissingOCIChartDependency(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(root, "acquired", "postgres")
	writeFile(t, filepath.Join(parent, "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: postgres
    version: '>=0.12.0'
    repository: oci://registry-1.docker.io/cloudpirates
`)
	writeFile(t, filepath.Join(parent, "Chart.lock"), `
dependencies:
  - name: postgres
    repository: oci://registry-1.docker.io/cloudpirates
    version: 0.12.4
digest: sha256:example
generated: "2026-01-01T00:00:00Z"
`)
	writeFile(t, filepath.Join(parent, "templates", "parent.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: parent-config
`)
	writeNamedTestChart(t, child, "postgres", "0.12.4", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: postgres-config
`)

	acquirer := &fakeChartAcquirer{chartDir: child, fromCache: true}
	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{
		AppName:             "demo",
		ChartAcquirer:       acquirer,
		ChartCacheDir:       filepath.Join(root, "..", "chart-cache"),
		OfflineCharts:       true,
		RefreshCharts:       true,
		ChartForbiddenRoots: []string{root},
		ChartCredentials:    chart.ChartCredentials{Username: "helm-user"},
		PassCredentials:     true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	assertHelmDependencyAcquireRequest(t, acquirer)
	assertPathMissing(t, filepath.Join(parent, "charts", "postgres"))
	configMaps := filterObjects(result, "ConfigMap")
	if len(configMaps) != 2 {
		t.Fatalf("ConfigMaps = %d, want parent and dependency manifests: %#v", len(configMaps), result)
	}
	if !containsManifest(result, "ConfigMap", "parent-config") || !containsManifest(result, "ConfigMap", "postgres-config") {
		t.Fatalf("ConfigMaps = %#v, want parent-config and postgres-config", configMaps)
	}
}

func TestHelmRendererAcquiresIncompatibleVendoredOCIChartDependency(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(root, "acquired", "postgres")
	writeFile(t, filepath.Join(parent, "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: postgres
    version: 0.12.4
    repository: oci://registry-1.docker.io/cloudpirates
`)
	writeFile(t, filepath.Join(parent, "templates", "parent.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: parent-config
`)
	writeNamedTestChart(t, filepath.Join(parent, "charts", "postgres"), "postgres", "0.1.0", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: stale-postgres-config
`)
	writeNamedTestChart(t, child, "postgres", "0.12.4", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: postgres-config
`)

	acquirer := &fakeChartAcquirer{chartDir: child, fromCache: true}
	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{
		AppName:       "demo",
		ChartAcquirer: acquirer,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got, want := len(acquirer.requests), 1; got != want {
		t.Fatalf("chart acquire calls = %d, want %d", got, want)
	}
	if !containsManifest(result, "ConfigMap", "postgres-config") || containsManifest(result, "ConfigMap", "stale-postgres-config") {
		t.Fatalf("manifests = %#v, want acquired compatible dependency only", manifestNames(result))
	}
}

func TestHelmRendererUsesChartLockWhenVendoredVersionSatisfiesRange(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(root, "acquired", "postgres")
	writeFile(t, filepath.Join(parent, "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: postgres
    version: '>=0.12.0'
    repository: oci://registry-1.docker.io/cloudpirates
`)
	writeFile(t, filepath.Join(parent, "Chart.lock"), `
dependencies:
  - name: postgres
    repository: oci://registry-1.docker.io/cloudpirates
    version: 0.12.4
digest: sha256:example
generated: "2026-01-01T00:00:00Z"
`)
	writeFile(t, filepath.Join(parent, "templates", "parent.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: parent-config
`)
	writeNamedTestChart(t, filepath.Join(parent, "charts", "postgres"), "postgres", "0.12.5", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: unlocked-postgres-config
`)
	writeNamedTestChart(t, child, "postgres", "0.12.4", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: locked-postgres-config
`)

	acquirer := &fakeChartAcquirer{chartDir: child, fromCache: true}
	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{
		AppName:       "demo",
		ChartAcquirer: acquirer,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got, want := len(acquirer.requests), 1; got != want {
		t.Fatalf("chart acquire calls = %d, want %d", got, want)
	}
	if !containsManifest(result, "ConfigMap", "locked-postgres-config") || containsManifest(result, "ConfigMap", "unlocked-postgres-config") {
		t.Fatalf("manifests = %#v, want locked dependency only", manifestNames(result))
	}
}

func TestHelmRendererRemovesIncompatibleVendoredChartArchive(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(root, "acquired", "postgres")
	writeFile(t, filepath.Join(parent, "Chart.yaml"), `
apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: postgres
    version: 0.12.4
    repository: oci://registry-1.docker.io/cloudpirates
`)
	writeFile(t, filepath.Join(parent, "templates", "parent.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: parent-config
`)
	writeArchivedTestChart(t, filepath.Join(parent, "charts"), "postgres", "0.1.0", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: archived-postgres-config
`)
	writeNamedTestChart(t, child, "postgres", "0.12.4", `
apiVersion: v1
kind: ConfigMap
metadata:
  name: postgres-config
`)

	acquirer := &fakeChartAcquirer{chartDir: child, fromCache: true}
	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "parent",
		Chart:    "parent",
	}, RenderOptions{
		AppName:       "demo",
		ChartAcquirer: acquirer,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got, want := len(acquirer.requests), 1; got != want {
		t.Fatalf("chart acquire calls = %d, want %d", got, want)
	}
	if !containsManifest(result, "ConfigMap", "postgres-config") || containsManifest(result, "ConfigMap", "archived-postgres-config") {
		t.Fatalf("manifests = %#v, want acquired dependency only", manifestNames(result))
	}
}

func TestHelmDependencyChartPathRejectsUnsafeName(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../postgres", "nested/postgres", `nested\postgres`, "postgres*"} {
		if got, err := helmDependencyChartPath(t.TempDir(), name); err == nil {
			t.Fatalf("helmDependencyChartPath(%q) = %q, nil error; want unsafe name error", name, got)
		}
	}
}

func assertHelmDependencyAcquireRequest(t *testing.T, acquirer *fakeChartAcquirer) {
	t.Helper()
	if got, want := len(acquirer.requests), 1; got != want {
		t.Fatalf("chart acquire calls = %d, want %d", got, want)
	}
	if got := acquirer.requests[0]; got.Repository != "oci://registry-1.docker.io/cloudpirates" || got.Name != "postgres" || got.Version != "0.12.4" || got.Kind != chart.RepositoryOCI {
		t.Fatalf("chart request = %#v", got)
	}
	if got := acquirer.options[0]; !got.Offline || !got.Refresh || !got.PassCredentials || got.CacheDir == "" || len(got.ForbiddenRoots) != 1 || got.Credentials.Username != "helm-user" {
		t.Fatalf("chart options = %#v", got)
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

func writeParameterChart(t *testing.T, chartDir string) {
	t.Helper()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), `
apiVersion: v2
name: parameters
version: 0.1.0
`)
	writeFile(t, filepath.Join(chartDir, "values.yaml"), `
value: default
flag: false
appName: unset
fileValue: unset
`)
	writeFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: {{ .Values.value | quote }}
  flag: {{ printf "%T:%v" .Values.flag .Values.flag | quote }}
  appName: {{ .Values.appName | quote }}
  fileValue: {{ .Values.fileValue | quote }}
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

// TestHelmRendererAllowsValueFileOutsideChartDirWithinRepoRoot verifies that a
// valueFile using a relative path that traverses above the chart directory is
// accepted when the resolved path remains inside the repository root.  This
// mirrors Argo CD v3 behaviour where the boundary is the repo root, not the
// chart directory.
func TestHelmRendererAllowsValueFileOutsideChartDirWithinRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "charts", "demo"))
	writeFile(t, filepath.Join(root, "values", "shared.yaml"), "value: from-shared\n")

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "charts/demo",
	}, RenderOptions{
		AppName:    "demo",
		ValueFiles: []string{"../../values/shared.yaml"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if value := renderedValue(t, result); value != "from-shared" {
		t.Fatalf("data.value = %q, want from-shared", value)
	}
}

// TestHelmRendererRejectsValueFileEscapingRepoRoot verifies that a valueFile
// whose resolved path falls outside the repository root is rejected.
func TestHelmRendererRejectsValueFileEscapingRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeValueChart(t, filepath.Join(root, "charts", "demo"))

	_, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "charts/demo",
	}, RenderOptions{
		AppName:    "demo",
		ValueFiles: []string{"../../../outside.yaml"},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want repo root escape error")
	}
	if !strings.Contains(err.Error(), "escapes value files") {
		t.Fatalf("Render() error = %v, want value files escape", err)
	}
}

// TestHelmRendererAllowsFileParameterOutsideChartDirWithinRepoRoot verifies
// that a fileParameter path traversing above the chart dir is accepted when
// the resolved path stays inside the repository root.
func TestHelmRendererAllowsFileParameterOutsideChartDirWithinRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeParameterChart(t, filepath.Join(root, "charts", "demo"))
	writeFile(t, filepath.Join(root, "shared-files", "message.txt"), "from-shared-file")

	result, diags, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "charts/demo",
	}, RenderOptions{
		AppName: "demo",
		HelmFileParameters: []argoappv1.HelmFileParameter{
			{Name: "fileValue", Path: "../../shared-files/message.txt"},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	got, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "fileValue")
	if got != "from-shared-file" {
		t.Fatalf("data.fileValue = %q, want from-shared-file", got)
	}
}

// TestHelmRendererRejectsFileParameterEscapingRepoRoot verifies that a
// fileParameter path whose resolved location falls outside the repository root
// is rejected.
func TestHelmRendererRejectsFileParameterEscapingRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeParameterChart(t, filepath.Join(root, "charts", "demo"))

	_, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "charts/demo",
	}, RenderOptions{
		AppName: "demo",
		HelmFileParameters: []argoappv1.HelmFileParameter{
			{Name: "fileValue", Path: "../../../outside.txt"},
		},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want repo root escape error")
	}
	if !strings.Contains(err.Error(), "escapes value files") {
		t.Fatalf("Render() error = %v, want value files escape", err)
	}
}
