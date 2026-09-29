package render

import (
	"context"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestKustomizeRendererHelmChartValuesMatchKustomizeCLI pins how helmCharts
// values reach the chart. Expected strings come from `kustomize build
// --enable-helm` with kustomize v5.8.1 and helm v4.2.1 (the versions
// quay.io/argoproj/argocd:v3.5.3 bundles, argo-cd hack/tool-versions.sh).
// Helm reads valuesFile, additionalValuesFiles, and kustomize's merged
// valuesInline file as --values (sigs.k8s.io/yaml: numbers float64, YAML 1.1
// yes/no booleans). When valuesInline is set, kustomize first reads valuesFile
// (or the chart's values.yaml) through kyaml (YAML 1.2), so its yes/no stay
// strings in the merged file.
func TestKustomizeRendererHelmChartValuesMatchKustomizeCLI(t *testing.T) {
	for _, tt := range []struct {
		name  string
		chart string
		want  map[string]string
	}{
		{
			name:  "valuesFile and additionalValuesFiles",
			chart: "    valuesFile: vf.yaml\n    additionalValuesFiles: [av.yaml]\n",
			want: map[string]string{
				"av.big": "float64=1e+06", "av.flag": "bool=false",
				"chart.big": "float64=1e+06", "chart.flag": "bool=true", "chart.mid": "float64=1.234567e+06",
				"vf.big": "float64=1e+06", "vf.flag": "bool=false",
			},
		},
		{
			name:  "valuesInline override with valuesFile",
			chart: "    valuesFile: vf.yaml\n    additionalValuesFiles: [av.yaml]\n    valuesInline:\n      inl:\n        big: 1000000\n",
			want: map[string]string{
				"av.big": "float64=1e+06", "av.flag": "bool=false",
				"chart.big": "float64=1e+06", "chart.flag": "bool=true", "chart.mid": "float64=1.234567e+06",
				"inl.big": "float64=1e+06",
				"vf.big":  "float64=1e+06", "vf.flag": "string=no",
			},
		},
		{
			name:  "valuesInline over chart values.yaml",
			chart: "    additionalValuesFiles: [av.yaml]\n    valuesInline:\n      inl:\n        big: 1000000\n",
			want: map[string]string{
				"av.big": "float64=1e+06", "av.flag": "bool=false",
				"chart.big": "float64=1e+06", "chart.flag": "string=yes", "chart.mid": "float64=1.234567e+06",
				"inl.big": "float64=1e+06",
			},
		},
		{
			name:  "valuesInline merge",
			chart: "    valuesFile: vf.yaml\n    valuesMerge: merge\n    valuesInline:\n      vf:\n        mid: 1234567\n",
			want: map[string]string{
				"chart.big": "float64=1e+06", "chart.flag": "bool=true", "chart.mid": "float64=1.234567e+06",
				"vf.big": "float64=1e+06", "vf.flag": "string=no", "vf.mid": "float64=1.234567e+06",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			appDir := filepath.Join(root, "apps", "demo")
			chartDir := filepath.Join(appDir, "charts", "nums")
			writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: nums\nversion: 0.1.0\n")
			writeFile(t, filepath.Join(chartDir, "values.yaml"), "chart:\n  big: 1000000\n  mid: 1234567\n  flag: yes\nvf: {}\nav: {}\ninl: {}\n")
			writeFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: nums
data:
{{- range $ch := list "chart" "vf" "av" "inl" }}
{{- range $k, $v := index $.Values $ch }}
  {{ $ch }}.{{ $k }}: "{{ kindOf $v }}={{ $v }}"
{{- end }}
{{- end }}
`)
			writeFile(t, filepath.Join(appDir, "vf.yaml"), "vf:\n  big: 1000000\n  flag: no\n")
			writeFile(t, filepath.Join(appDir, "av.yaml"), "av:\n  big: 1000000\n  flag: no\n")
			writeFile(t, filepath.Join(appDir, "kustomization.yaml"), "helmCharts:\n  - name: nums\n    releaseName: nums\n"+tt.chart)

			result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     filepath.Join("apps", "demo"),
			}, RenderOptions{})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			configMaps := filterObjects(result, "ConfigMap")
			if len(configMaps) != 1 {
				t.Fatalf("len(configMaps) = %d, want 1", len(configMaps))
			}
			got, _, err := unstructured.NestedStringMap(configMaps[0].Object, "data")
			if err != nil {
				t.Fatalf("data: %v", err)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("data[%q] = %q, want %q (kustomize v5.8.1 + helm v4.2.1)", key, got[key], want)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("rendered %d data keys, want %d: %v", len(got), len(tt.want), got)
			}
		})
	}
}
