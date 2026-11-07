package render

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// kustomize adds the output of configMapGenerator and secretGenerator
// before the output of helmCharts and generators:, so a merge or replace
// into that output fails, as the kustomize v5.8.3 binary fails it with the
// same kustomization. A merge into a resource works.
func TestKustomizeGeneratorMergeIntoLaterOutput(t *testing.T) {
	const helmChart = "helmCharts:\n  - name: shared\n    releaseName: x\n"
	const ksopsGenerator = "generators:\n  - ./secret-generator.yaml\n"
	for _, tc := range []struct {
		name          string
		kustomization string
		wantErr       string
		wantData      string
	}{
		{name: "merge into helm output", kustomization: helmChart + "configMapGenerator:\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n", wantErr: `does not exist; cannot merge or replace`},
		{name: "replace into helm output", kustomization: helmChart + "configMapGenerator:\n  - name: shared\n    behavior: replace\n    literals: [a=b]\n", wantErr: `id resid.ResId{Gvk:resid.Gvk{Group:"", Version:"v1", Kind:"ConfigMap", isClusterScoped:false}, Name:"shared", Namespace:""} does not exist; cannot merge or replace`},
		{name: "merge into a resource that helm output repeats", kustomization: "resources: [res.yaml]\n" + helmChart + "configMapGenerator:\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n", wantErr: `exists; can not use behavior: 'unspecified', behavior must be merge or replace`},
		{name: "merge into a resource", kustomization: "resources: [res.yaml]\nhelmCharts:\n  - name: other\n    releaseName: other-release\nconfigMapGenerator:\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n", wantData: "a,fromResource"},
		{name: "secret merge into KSOPS output", kustomization: ksopsGenerator + "secretGenerator:\n  - name: demo-secret\n    behavior: merge\n    literals: [a=b]\n", wantErr: `Kind:"Secret", isClusterScoped:false}, Name:"demo-secret", Namespace:""} does not exist; cannot merge or replace`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			app := filepath.Join(root, "app")
			writeFile(t, filepath.Join(app, "kustomization.yaml"), tc.kustomization)
			writeFile(t, filepath.Join(app, "res.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: shared\ndata:\n  fromResource: \"true\"\n")
			for chart, name := range map[string]string{"shared": "shared", "other": "other"} {
				writeFile(t, filepath.Join(app, "charts", chart, "Chart.yaml"), "apiVersion: v2\nname: "+chart+"\nversion: 0.1.0\n")
				writeFile(t, filepath.Join(app, "charts", chart, "values.yaml"), "{}\n")
				writeFile(t, filepath.Join(app, "charts", chart, "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"\ndata:\n  fromHelm: \"true\"\n")
			}
			writeFile(t, filepath.Join(app, "secret-generator.yaml"), ksopsTestGeneratorManifest)
			writeFile(t, filepath.Join(app, "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "NYI8Q9o3original"))
			manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{EnableKSOPSCompat: true})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			for _, manifest := range manifests {
				if manifest.Object.GetName() != "shared" {
					continue
				}
				data, _ := manifest.Object.Object["data"].(map[string]any)
				keys := make([]string, 0, len(data))
				for key := range data {
					keys = append(keys, key)
				}
				if got := strings.Join(slices.Sorted(slices.Values(keys)), ","); got != tc.wantData {
					t.Fatalf("shared data keys = %s, want %s", got, tc.wantData)
				}
				return
			}
			t.Fatal("no ConfigMap shared")
		})
	}
}
