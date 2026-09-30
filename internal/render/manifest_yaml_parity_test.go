package render

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// Argo CD v3.5.3 splits rendered manifests with apimachinery's
// YAMLOrJSONDecoder and sigs.k8s.io/yaml (YAML 1.1): the directory source
// through splitYAMLOrJSON (reposerver/repository/repository.go:2083-2100), helm
// output through gitops-engine kube.SplitYAML (repository.go:1411). The
// expected objects below are what those readers return for the same text.

func TestDirectoryRendererDecodesManifestsLikeArgoCD(t *testing.T) {
	for _, tt := range []struct {
		name     string
		manifest string
		kind     string
		field    string
		want     map[string]any
	}{
		{
			name: "YAML 1.1 booleans and timestamps",
			manifest: `apiVersion: example.com/v1
kind: Widget
metadata:
  name: widget
spec:
  enabled: yes
  paused: off
  date: 2024-01-02
`,
			kind:  "Widget",
			field: "spec",
			want:  map[string]any{"enabled": true, "paused": false, "date": "2024-01-02"},
		},
		{
			name: "integer key",
			manifest: `apiVersion: v1
kind: ConfigMap
metadata:
  name: tcp-services
data:
  9000: "ingress/controller:9000"
`,
			kind:  "ConfigMap",
			field: "data",
			want:  map[string]any{"9000": "ingress/controller:9000"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", "manifest.yaml"), tt.manifest)

			manifests, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "apps",
			}, RenderOptions{})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			objects := filterObjects(manifests, tt.kind)
			if len(objects) != 1 {
				t.Fatalf("len(%s) = %d, want 1", tt.kind, len(objects))
			}
			if got := objects[0].Object[tt.field]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("%s %s = %#v, want %#v", tt.kind, tt.field, got, tt.want)
			}
		})
	}
}

// TestHelmRendererDecodesJSONTemplateOutputLikeSplitYAML pins a template that
// renders JSON. helm v4.2.1 prints it after "---\n# Source: ...", so
// kube.SplitYAML reads the whole stream as YAML and its numbers come back as
// they would from YAML: 1.0 and 1e6 are int64.
func TestHelmRendererDecodesJSONTemplateOutputLikeSplitYAML(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: json\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(root, "chart", "templates", "widget.yaml"), `{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": {"name": "json"}, "spec": {"one": 1.0, "mil": 1e6, "half": 1.5}}
`)

	manifests, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "chart",
	}, RenderOptions{AppName: "json"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	widgets := filterObjects(manifests, "Widget")
	if len(widgets) != 1 {
		t.Fatalf("len(widgets) = %d, want 1", len(widgets))
	}
	wantSpec := map[string]any{"one": int64(1), "mil": int64(1000000), "half": 1.5}
	if got := widgets[0].Object["spec"]; !reflect.DeepEqual(got, wantSpec) {
		t.Fatalf("Widget spec = %#v, want %#v", got, wantSpec)
	}
}

// TestKustomizeRendererHelmChartOutputKeepsKustomizeReading pins how
// helmCharts output reaches the final manifests. kustomize reads helm's output
// through kyaml (YAML 1.2) and prints yes/off as quoted strings and dates as
// RFC 3339, so Argo CD's SplitYAML of the kustomize output sees strings. The
// expected values are `kustomize build --enable-helm` output from kustomize
// v5.8.1 with helm v4.2.1 (argo-cd hack/tool-versions.sh).
func TestKustomizeRendererHelmChartOutputKeepsKustomizeReading(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "demo")
	chartDir := filepath.Join(appDir, "charts", "values")
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: values\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(chartDir, "templates", "widget.yaml"), `apiVersion: example.com/v1
kind: Widget
metadata:
  name: w
spec:
  plainYes: yes
  plainOff: off
  quoted: "yes"
  date: 2024-01-02
  mil: 1e6
  one: 1.0
`)
	writeFile(t, filepath.Join(appDir, "kustomization.yaml"), "helmCharts:\n  - name: values\n    releaseName: values\n")

	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	widgets := filterObjects(manifests, "Widget")
	if len(widgets) != 1 {
		t.Fatalf("len(widgets) = %d, want 1", len(widgets))
	}
	wantSpec := map[string]any{
		"plainYes": "yes", "plainOff": "off", "quoted": "yes",
		"date": "2024-01-02T00:00:00Z", "mil": int64(1000000), "one": int64(1),
	}
	if got := widgets[0].Object["spec"]; !reflect.DeepEqual(got, wantSpec) {
		t.Fatalf("Widget spec = %#v, want %#v (kustomize v5.8.1 + helm v4.2.1)", got, wantSpec)
	}
}

// TestKustomizeRendererHelmChartCRDsKeepKustomizeReading pins the same reading
// for a chart's crds/ files, which helm prints ahead of the templates when
// includeCRDs is set. The expected spec is `kustomize build --enable-helm`
// output from kustomize v5.8.1 with helm v4.2.1.
func TestKustomizeRendererHelmChartCRDsKeepKustomizeReading(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "demo")
	chartDir := filepath.Join(appDir, "charts", "values")
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: values\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(chartDir, "crds", "crd.yaml"), `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  plainYes: yes
  date: 2024-01-02
`)
	writeFile(t, filepath.Join(appDir, "kustomization.yaml"), "helmCharts:\n  - name: values\n    releaseName: values\n    includeCRDs: true\n")

	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	crds := filterObjects(manifests, "CustomResourceDefinition")
	if len(crds) != 1 {
		t.Fatalf("len(crds) = %d, want 1", len(crds))
	}
	wantSpec := map[string]any{"group": "example.com", "plainYes": "yes", "date": "2024-01-02T00:00:00Z"}
	if got := crds[0].Object["spec"]; !reflect.DeepEqual(got, wantSpec) {
		t.Fatalf("CustomResourceDefinition spec = %#v, want %#v (kustomize v5.8.1 + helm v4.2.1)", got, wantSpec)
	}
}
