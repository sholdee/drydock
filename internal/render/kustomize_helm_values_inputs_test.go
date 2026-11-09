package render

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// KustomizeHelmValues lists the values files that the helmCharts render
// reads: valuesFile only when valuesMerge allows it, and additionalValuesFiles.
// kustomize reads the paths as written, so a $ARGOCD_APP_NAME in a path
// stays literal, and a glob is a file name.
func TestKustomizeHelmValuesReadsValuesFilesLikeTheRenderer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chart string
		want  []string
	}{
		{
			name:  "valuesFile and additionalValuesFiles",
			chart: "    valuesFile: values.yaml\n    additionalValuesFiles:\n      - extra.yaml\n",
			want:  []string{"install/extra.yaml", "install/values.yaml"},
		},
		{
			name:  "replace with valuesInline skips valuesFile",
			chart: "    valuesFile: values.yaml\n    valuesMerge: replace\n    valuesInline:\n      a: 1\n    additionalValuesFiles:\n      - extra.yaml\n",
			want:  []string{"install/extra.yaml"},
		},
		{
			name:  "merge with valuesInline reads valuesFile",
			chart: "    valuesFile: values.yaml\n    valuesMerge: merge\n    valuesInline:\n      a: 1\n",
			want:  []string{"install/values.yaml"},
		},
		{
			name:  "glob in additionalValuesFiles is a file name",
			chart: "    additionalValuesFiles:\n      - layers/*.yaml\n",
			want:  []string{"install/layers/*.yaml"},
		},
		{
			name:  "glob in valuesFile is a file name",
			chart: "    valuesFile: layers/?.yaml\n",
			want:  nil,
		},
		{
			name:  "build environment variable stays literal in valuesFile with valuesInline",
			chart: "    valuesFile: values-$ARGOCD_APP_NAME.yaml\n    valuesMerge: merge\n    valuesInline:\n      a: 1\n",
			want:  []string{"install/values-$ARGOCD_APP_NAME.yaml"},
		},
		{
			name:  "build environment variable stays literal in additionalValuesFiles",
			chart: "    additionalValuesFiles:\n      - values-$ARGOCD_APP_NAME.yaml\n",
			want:  []string{"install/values-$ARGOCD_APP_NAME.yaml"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "install", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nhelmCharts:\n  - name: demo\n    releaseName: demo\n"+tc.chart)
			for _, file := range []string{"values.yaml", "extra.yaml", "layers/a.yaml", "layers/b.yaml", "layers/*.yaml", "values-demo.yaml", "values-$ARGOCD_APP_NAME.yaml"} {
				writeFile(t, filepath.Join(root, "install", filepath.FromSlash(file)), "a: 1\n")
			}
			values, err := KustomizeHelmValues(context.Background(), root, "install", nil)
			if err != nil {
				t.Fatalf("KustomizeHelmValues() error = %v", err)
			}
			if !slices.Equal(values.ValuesFiles, tc.want) {
				t.Fatalf("ValuesFiles = %v, want %v", values.ValuesFiles, tc.want)
			}
		})
	}
}

// KustomizeHelmValues names the parser that the render reads each values
// file with: the Kustomize parser for a valuesFile merged with valuesInline,
// and Helm for the rest.
func TestKustomizeHelmValuesSplitsFilesByParser(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "install", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nhelmCharts:\n  - name: demo\n    releaseName: demo\n    valuesFile: values.yaml\n    valuesInline:\n      a: 1\n    additionalValuesFiles:\n      - extra.yaml\n")
	writeFile(t, filepath.Join(root, "install", "values.yaml"), "a: 1\n")
	writeFile(t, filepath.Join(root, "install", "extra.yaml"), "a: 1\n")
	values, err := KustomizeHelmValues(context.Background(), root, "install", nil)
	if err != nil {
		t.Fatalf("KustomizeHelmValues() error = %v", err)
	}
	if want := []string{"install/values.yaml"}; !slices.Equal(values.KustomizeParsedValuesFiles, want) {
		t.Fatalf("KustomizeParsedValuesFiles = %v, want %v", values.KustomizeParsedValuesFiles, want)
	}
	if want := []string{"install/extra.yaml"}; !slices.Equal(values.HelmParsedValuesFiles, want) {
		t.Fatalf("HelmParsedValuesFiles = %v, want %v", values.HelmParsedValuesFiles, want)
	}
}

// A helmCharts entry in a component that spec.source.kustomize.components
// adds renders with the source, so its chart name and values files count.
// Under ignoreMissingComponents the render adds only the first valid
// component, so a later one does not count.
func TestKustomizeHelmValuesReadsSourceKustomizeComponents(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ignoreMissing bool
		wantCharts    []string
		wantValues    []string
	}{
		{name: "every component", wantCharts: []string{"argo-cd", "other"}, wantValues: []string{"components/a/values.yaml", "components/b/values.yaml"}},
		{name: "first valid component under ignoreMissingComponents", ignoreMissing: true, wantCharts: []string{"argo-cd"}, wantValues: []string{"components/a/values.yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "install", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
			for dir, chart := range map[string]string{"a": "argo-cd", "b": "other"} {
				writeFile(t, filepath.Join(root, "components", dir, "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nhelmCharts:\n  - name: "+chart+"\n    releaseName: "+chart+"\n    valuesFile: values.yaml\n")
				writeFile(t, filepath.Join(root, "components", dir, "values.yaml"), "a: 1\n")
			}
			values, err := KustomizeHelmValues(context.Background(), root, "install", &argoappv1.ApplicationSourceKustomize{
				Components:              []string{"../components/missing", "../components/a", "../components/b"},
				IgnoreMissingComponents: tc.ignoreMissing,
			})
			if err != nil {
				t.Fatalf("KustomizeHelmValues() error = %v", err)
			}
			charts := append([]string(nil), values.ChartNames...)
			slices.Sort(charts)
			if !slices.Equal(charts, tc.wantCharts) {
				t.Fatalf("ChartNames = %v, want %v", charts, tc.wantCharts)
			}
			if !slices.Equal(values.ValuesFiles, tc.wantValues) {
				t.Fatalf("ValuesFiles = %v, want %v", values.ValuesFiles, tc.wantValues)
			}
		})
	}
}
