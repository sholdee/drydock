package render

import (
	"context"
	"maps"
	"os"
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
		// wantSecretKeys are the data keys of the Secret demo-secret.
		wantSecretKeys string
	}{
		{name: "merge into helm output", kustomization: helmChart + "configMapGenerator:\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n", wantErr: `does not exist; cannot merge or replace`},
		{name: "replace into helm output", kustomization: helmChart + "configMapGenerator:\n  - name: shared\n    behavior: replace\n    literals: [a=b]\n", wantErr: `id resid.ResId{Gvk:resid.Gvk{Group:"", Version:"v1", Kind:"ConfigMap", isClusterScoped:false}, Name:"shared", Namespace:""} does not exist; cannot merge or replace`},
		// kustomize fails this with "exists; can not use behavior" on the
		// helm output. drydock gives the merge text instead: telling the two
		// apart needs a build of the resources before the build (recorded
		// divergence).
		{name: "merge into a resource that helm output repeats", kustomization: "resources: [res.yaml]\n" + helmChart + "configMapGenerator:\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n", wantErr: `does not exist; cannot merge or replace`},
		// A create step before the merge makes the ID, so the merge works and
		// the helm output collides, as in kustomize.
		{name: "merge into generator output that helm output repeats", kustomization: helmChart + "configMapGenerator:\n  - name: shared\n    literals: [c=d]\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n", wantErr: `exists; can not use behavior: 'unspecified', behavior must be merge or replace`},
		{name: "merge into a resource", kustomization: "resources: [res.yaml]\nhelmCharts:\n  - name: other\n    releaseName: other-release\nconfigMapGenerator:\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n", wantData: "a,fromResource"},
		{name: "secret merge into KSOPS output", kustomization: ksopsGenerator + "secretGenerator:\n  - name: demo-secret\n    behavior: merge\n    literals: [a=b]\n", wantErr: `Kind:"Secret", isClusterScoped:false}, Name:"demo-secret", Namespace:""} does not exist; cannot merge or replace`},
		// The entries of generators: run in list order.
		{name: "builtin merge before KSOPS in generators", kustomization: "generators:\n  - ./merge.yaml\n  - ./secret-generator.yaml\n", wantErr: `Name:"demo-secret", Namespace:""} does not exist; cannot merge or replace`},
		{name: "builtin merge after KSOPS in generators", kustomization: "generators:\n  - ./secret-generator.yaml\n  - ./merge.yaml\n", wantSecretKeys: "TOKEN,a"},
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
			writeFile(t, filepath.Join(app, "merge.yaml"), "apiVersion: builtin\nkind: SecretGenerator\nmetadata:\n  name: demo-secret\nbehavior: merge\nliterals:\n  - a=b\n")
			writeFile(t, filepath.Join(app, "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "NYI8Q9o3original"))
			manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{EnableKSOPSCompat: true, KustomizeCaller: KustomizeCallerArgoCD, BuildOptions: []string{"--enable-helm", "--enable-alpha-plugins"}})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Render() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if tc.wantSecretKeys != "" {
				for _, manifest := range manifests {
					if manifest.Object.GetKind() != "Secret" {
						continue
					}
					data, _ := manifest.Object.Object["data"].(map[string]any)
					if got := strings.Join(slices.Sorted(maps.Keys(data)), ","); got != tc.wantSecretKeys {
						t.Fatalf("Secret %s data keys = %s, want %s", manifest.Object.GetName(), got, tc.wantSecretKeys)
					}
					return
				}
				t.Fatal("no Secret")
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

// The merge order check builds nothing on its own. In a cycle through a
// component, the parent copy is not prepared yet, so such a build would
// fetch its remote generator under --offline.
func TestKustomizeGeneratorOrderCheckFetchesNothing(t *testing.T) {
	connections := listenForNoConnections(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), "generators:\n  - http://"+connections.addr+"/x.yaml\ncomponents: [../comp]\n")
	writeFile(t, filepath.Join(root, "comp", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources: [../app]\nhelmCharts:\n  - name: merged\n    releaseName: merged\nconfigMapGenerator:\n  - name: merged\n    behavior: merge\n    literals: [a=b]\n")
	writeFile(t, filepath.Join(root, "comp", "charts", "merged", "Chart.yaml"), "apiVersion: v2\nname: merged\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(root, "comp", "charts", "merged", "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(root, "comp", "charts", "merged", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: merged\n")
	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{
		OfflineRemoteResources: true,
		RemoteResourceCacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want an error")
	}
	if got := connections.count.Load(); got != 0 {
		t.Fatalf("listener got %d connections, want none; Render() error = %v", got, err)
	}
}

// The merge order check writes and removes nothing in the workspace, so a
// symlinked .drydock directory cannot point it outside, and it does not
// skip the check.
func TestKustomizeGeneratorOrderCheckTouchesNoSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "probes", "000", "keep.txt"), "keep")
	app := filepath.Join(root, "app")
	writeFile(t, filepath.Join(app, "kustomization.yaml"), "resources: [res.yaml]\nhelmCharts:\n  - name: shared\n    releaseName: x\nconfigMapGenerator:\n  - name: shared\n    behavior: merge\n    literals: [a=b]\n")
	writeFile(t, filepath.Join(app, "res.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n")
	writeFile(t, filepath.Join(app, "charts", "shared", "Chart.yaml"), "apiVersion: v2\nname: shared\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(app, "charts", "shared", "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(app, "charts", "shared", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: shared\n")
	if err := os.MkdirAll(filepath.Join(app, ".drydock"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "probes"), filepath.Join(app, ".drydock", "probes")); err != nil {
		t.Fatal(err)
	}
	_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want a failure, not a skipped check")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "probes", "000", "keep.txt")); statErr != nil {
		t.Fatalf("file outside the repository is gone: %v; Render() error = %v", statErr, err)
	}
}
