package render

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/chart/common"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// fakeHelmEnv makes the test binary act as helm (fakeHelmMain). kustomize
// runs helm with the environment of the test.
const fakeHelmEnv = "DRYDOCK_TEST_FAKE_HELM"

func TestMain(m *testing.M) {
	if os.Getenv(fakeHelmEnv) == "1" {
		os.Exit(fakeHelmMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

const helmCapabilitiesProbePrefix = "probe.example.com/"

// fakeHelmMain answers the two helm commands that kustomize runs for a
// local chart: "version --short" and "template". For a template it prints
// one ConfigMap named like the release, with the kube version and the probe
// API versions that helm would give the chart, as
// helmCapabilitiesProbeTemplate prints them.
func fakeHelmMain(args []string) int {
	if len(args) > 0 && args[0] == "version" {
		fmt.Println("v4.3.1")
		return 0
	}
	if len(args) < 2 || args[0] != "template" {
		fmt.Fprintf(os.Stderr, "fake helm: unexpected command %q\n", args)
		return 1
	}
	release := args[1]
	if release == "--generate-name" {
		release = "release-name"
	}
	kubeVersion := common.DefaultCapabilities.KubeVersion.Version
	var probes []string
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--kube-version":
			i++
			parsed, err := common.ParseKubeVersion(args[i])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			kubeVersion = parsed.Version
		case "--api-versions":
			i++
			values, err := csv.NewReader(strings.NewReader(args[i])).Read()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			for _, value := range values {
				if strings.HasPrefix(value, helmCapabilitiesProbePrefix) {
					probes = append(probes, value)
				}
			}
		}
	}
	fmt.Printf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  kubeVersion: %q\n  apiVersions: %q\n", release, kubeVersion, strings.Join(probes, ","))
	return 0
}

// helmCapabilitiesProbeTemplate prints what fakeHelmMain prints, from the
// capabilities that drydock gives the chart.
const helmCapabilitiesProbeTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  kubeVersion: {{ .Capabilities.KubeVersion.Version | quote }}
  {{- $probes := list }}
  {{- range .Capabilities.APIVersions }}
  {{- if hasPrefix "` + helmCapabilitiesProbePrefix + `" . }}{{ $probes = append $probes . }}{{ end }}
  {{- end }}
  apiVersions: {{ join "," $probes | quote }}
`

func writeHelmCapabilitiesProbeChart(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "charts", "probe", "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(dir, "charts", "probe", "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(dir, "charts", "probe", "templates", "configmap.yaml"), helmCapabilitiesProbeTemplate)
}

// helmCapabilitiesByRelease reads the probe ConfigMaps of a build.
func helmCapabilitiesByRelease(t *testing.T, manifests []map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, object := range manifests {
		metadata, _ := object["metadata"].(map[string]any)
		data, _ := object["data"].(map[string]any)
		name, _ := metadata["name"].(string)
		out[name] = data
	}
	return out
}

// kustomize v5.8.3 shares the --helm-api-versions values with each
// helmCharts entry without a copy, so the apiVersions of one entry
// overwrite the values that the other entries see, across bases, overlays,
// and components. drydock gives each entry the capabilities that kustomize
// gives it. The oracle is krusty with the flags of the kustomize build
// command and a fake helm that reports its --kube-version and
// --api-versions.
func TestKustomizeHelmChartCapabilitiesMatchKustomize(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		options []string
		app     RenderOptions
	}{
		{
			name: "entries of one kustomization",
			files: map[string]string{
				"app/kustomization.yaml": `helmCharts:
  - name: probe
    releaseName: a
    apiVersions: [probe.example.com/a]
  - name: probe
    releaseName: b
  - name: probe
    releaseName: c
    apiVersions: [probe.example.com/c1, probe.example.com/c2, probe.example.com/c3]
  - name: probe
    releaseName: d
`,
			},
			options: []string{"--enable-helm", "--helm-api-versions", "probe.example.com/f1", "--helm-api-versions", "probe.example.com/f2"},
		},
		{
			name: "base, overlay, and component",
			files: map[string]string{
				"app/kustomization.yaml": `resources: [../base]
components: [../component]
helmCharts:
  - name: probe
    releaseName: overlay
  - name: probe
    releaseName: overlay-own
    apiVersions: [probe.example.com/overlay]
`,
				"base/kustomization.yaml": `helmCharts:
  - name: probe
    releaseName: base
    apiVersions: [probe.example.com/base1, probe.example.com/base2]
`,
				"component/kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
helmCharts:
  - name: probe
    releaseName: component
`,
			},
			options: []string{"--enable-helm", "--helm-api-versions=probe.example.com/f1", "--helm-api-versions=probe.example.com/f2", "--helm-api-versions=probe.example.com/f3"},
		},
		{
			name: "kube versions and comma separated values",
			files: map[string]string{
				"app/kustomization.yaml": `helmCharts:
  - name: probe
    releaseName: flag
  - name: probe
    releaseName: entry
    kubeVersion: 1.30.0
    apiVersions: ["probe.example.com/x,probe.example.com/y"]
`,
			},
			options: []string{"--enable-helm", "--helm-kube-version", "1.29.0", "--helm-api-versions", "probe.example.com/f1,probe.example.com/f2"},
		},
		{
			name: "Argo CD flags after kustomize.buildOptions",
			files: map[string]string{
				"app/kustomization.yaml": `helmCharts:
  - name: probe
    releaseName: first
    apiVersions: [probe.example.com/entry]
  - name: probe
    releaseName: second
`,
			},
			options: []string{"--enable-helm", "--helm-kube-version", "1.29.0", "--helm-api-versions", "probe.example.com/options"},
			app:     RenderOptions{KubeVersion: "v1.31.0", APIVersions: []string{"probe.example.com/app1", "probe.example.com/app2"}},
		},
		{
			name: "alpha plugins without enable helm",
			files: map[string]string{
				"app/kustomization.yaml": `helmCharts:
  - name: probe
    releaseName: plugins
`,
			},
			options: []string{"--enable-alpha-plugins", "--helm-api-versions", "probe.example.com/options"},
			app:     RenderOptions{KubeVersion: "v1.31.0", APIVersions: []string{"probe.example.com/app"}},
		},
		{
			name: "no flag values",
			files: map[string]string{
				"app/kustomization.yaml": `helmCharts:
  - name: probe
    releaseName: own
    apiVersions: [probe.example.com/own]
  - name: probe
    releaseName: none
`,
			},
			options: []string{"--enable-helm"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range tc.files {
				writeFile(t, filepath.Join(root, path), content)
			}
			for _, dir := range []string{"app", "base", "component"} {
				if _, err := os.Stat(filepath.Join(root, dir)); err == nil {
					writeHelmCapabilitiesProbeChart(t, filepath.Join(root, dir))
				}
			}
			opts := tc.app
			opts.KustomizeCaller = KustomizeCallerArgoCD
			opts.BuildOptions = tc.options

			want := kustomizeHelmCapabilitiesOracle(t, root, kustomizeBuildArgs(opts))
			manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			objects := make([]map[string]any, 0, len(manifests))
			for _, manifest := range manifests {
				objects = append(objects, manifest.Object.Object)
			}
			if got := helmCapabilitiesByRelease(t, objects); !reflect.DeepEqual(got, want) {
				t.Fatalf("drydock capabilities = %#v\nkustomize = %#v", got, want)
			}
		})
	}
}

// kustomizeHelmCapabilitiesOracle builds root/app with krusty, the flags of
// the kustomize v5.8.3 build command, and fakeHelmMain as helm.
func kustomizeHelmCapabilitiesOracle(t *testing.T, root string, args []string) map[string]map[string]any {
	t.Helper()
	options, _, _, err := upstreamKustomizeBuild(t, append([]string{"app"}, args...))
	if err != nil {
		t.Fatalf("kustomize build flags: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	options.PluginConfig.HelmConfig.Command = self
	t.Setenv(fakeHelmEnv, "1")
	resMap, err := krusty.MakeKustomizer(options).Run(filesys.MakeFsOnDisk(), filepath.Join(root, "app"))
	if err != nil {
		t.Fatalf("kustomize build: %v", err)
	}
	objects := make([]map[string]any, 0, resMap.Size())
	for _, resource := range resMap.Resources() {
		object, err := resource.Map()
		if err != nil {
			t.Fatalf("resource.Map() error = %v", err)
		}
		objects = append(objects, object)
	}
	return helmCapabilitiesByRelease(t, objects)
}
