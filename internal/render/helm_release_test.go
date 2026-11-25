package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	argohelm "github.com/argoproj/argo-cd/v3/util/helm"
	chartv2util "helm.sh/helm/v4/pkg/chart/v2/util"
	sigsyaml "sigs.k8s.io/yaml"
)

// releaseProbeTemplate renders what helm template sets for the release and
// the capabilities, so a test can read them from one ConfigMap.
const releaseProbeTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: probe
data:
  release: {{ .Release.Name | quote }}
  namespace: {{ .Release.Namespace | quote }}
  kube: {{ .Capabilities.KubeVersion.Version | quote }}
  chartOnly: {{ .Capabilities.APIVersions.Has "chartonly.example/v1" | quote }}
  flag: {{ .Capabilities.APIVersions.Has "flag.example/v1" | quote }}
`

// writeReleaseProbeChart writes a local chart that renders
// releaseProbeTemplate. chartYAML holds extra Chart.yaml fields.
func writeReleaseProbeChart(t *testing.T, chartDir, chartYAML string) {
	t.Helper()
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 0.1.0\n"+chartYAML)
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(chartDir, "templates", "probe.yaml"), releaseProbeTemplate)
}

func renderReleaseProbeHelm(t *testing.T, chartYAML string, opts RenderOptions) ([]Manifest, error) {
	t.Helper()
	root := t.TempDir()
	writeReleaseProbeChart(t, filepath.Join(root, "chart"), chartYAML)
	manifests, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "chart"}, opts)
	return manifests, err
}

func renderReleaseProbeKustomize(t *testing.T, kustomization string, opts RenderOptions, files map[string]string) ([]Manifest, error) {
	t.Helper()
	root := t.TempDir()
	writeReleaseProbeChart(t, filepath.Join(root, "app", "charts", "probe"), "")
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), kustomization)
	for name, content := range files {
		writeFile(t, filepath.Join(root, "app", filepath.FromSlash(name)), content)
	}
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, opts)
	return manifests, err
}

// A helmCharts entry without releaseName and namespace renders the release
// "release-name" in the namespace "default", as kustomize 5.8.3 with helm
// 4.3.1 renders it: kustomize passes --generate-name, helm template presets
// "release-name", and helm falls back to "default" without --namespace.
func TestKustomizeHelmChartsDefaultReleaseNameAndNamespace(t *testing.T) {
	manifests, err := renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n", RenderOptions{}, nil)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "release", helmTemplateDefaultReleaseName)
	assertConfigMapData(t, manifests, "namespace", "default")
}

// A Helm source without a destination namespace and without
// helm.namespace renders in the namespace "default".
func TestHelmRendererDefaultsReleaseNamespace(t *testing.T) {
	manifests, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: "demo"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "release", "demo")
	assertConfigMapData(t, manifests, "namespace", "default")
}

// kustomize replaces the --helm-api-versions list with the apiVersions of
// the entry when the entry sets any, and keeps the list otherwise. The kube
// version follows the same rule.
func TestKustomizeHelmChartsAPIVersionsReplaceFlagValues(t *testing.T) {
	opts := RenderOptions{BuildOptions: []string{"--helm-api-versions", "flag.example/v1"}, KubeVersion: "1.30.0"}
	manifests, err := renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n    releaseName: probe\n    apiVersions:\n      - chartonly.example/v1\n", opts, nil)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "chartOnly", "true")
	assertConfigMapData(t, manifests, "flag", "false")
	assertConfigMapData(t, manifests, "kube", "v1.30.0")

	manifests, err = renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n    releaseName: probe\n", opts, nil)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "chartOnly", "false")
	assertConfigMapData(t, manifests, "flag", "true")
}

// kustomize loads the values.yaml of a chart unless valuesFile names another
// file or valuesInline replaces it, so a chart without one fails.
func TestKustomizeHelmChartsRequireChartValuesFile(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		wantErr       bool
	}{
		{name: "no values", kustomization: "helmCharts:\n  - name: probe\n    releaseName: probe\n", wantErr: true},
		{name: "valuesInline override", kustomization: "helmCharts:\n  - name: probe\n    releaseName: probe\n    valuesInline:\n      a: b\n", wantErr: true},
		{name: "valuesInline merge", kustomization: "helmCharts:\n  - name: probe\n    releaseName: probe\n    valuesInline:\n      a: b\n    valuesMerge: merge\n", wantErr: true},
		{name: "valuesInline replace", kustomization: "helmCharts:\n  - name: probe\n    releaseName: probe\n    valuesInline:\n      a: b\n    valuesMerge: replace\n"},
		{name: "valuesFile", kustomization: "helmCharts:\n  - name: probe\n    releaseName: probe\n    valuesFile: values.yaml\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			chartDir := filepath.Join(root, "app", "charts", "probe")
			writeReleaseProbeChart(t, chartDir, "")
			if err := os.Remove(filepath.Join(chartDir, "values.yaml")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, "app", "values.yaml"), "{}\n")
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), tt.kustomization)
			_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{})
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Render() error = %v", err)
				}
				return
			}
			want := `kustomize helm chart "probe": values file "charts/probe/values.yaml": file does not exist`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Render() error = %v, want %q", err, want)
			}
		})
	}
}

// kustomize loads valuesFile and additionalValuesFiles as file names and
// expands no glob.
func TestKustomizeHelmChartsReadValuesFilesWithoutGlobs(t *testing.T) {
	files := map[string]string{"values-a.yaml": "x: 1\n"}
	for _, kustomization := range []string{
		"helmCharts:\n  - name: probe\n    releaseName: probe\n    valuesFile: values-*.yaml\n",
		"helmCharts:\n  - name: probe\n    releaseName: probe\n    additionalValuesFiles:\n      - values-*.yaml\n",
		"helmCharts:\n  - name: probe\n    releaseName: probe\n    valuesInline:\n      a: b\n    valuesFile: values-*.yaml\n",
	} {
		_, err := renderReleaseProbeKustomize(t, kustomization, RenderOptions{}, files)
		want := `read helm value file "values-*.yaml": file does not exist`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Render(%q) error = %v, want %q", kustomization, err, want)
		}
	}

	// A file whose name holds glob characters is read as written, and
	// only that file.
	files["values-*.yaml"] = "x: 1\n"
	files["values-b.yaml"] = "release: wrong\n"
	manifests, err := renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n    releaseName: probe\n    additionalValuesFiles:\n      - values-*.yaml\n", RenderOptions{}, files)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "release", "probe")
}

// kustomize rejects a release name or a chart name that helm would read as
// a flag (HelmChartInflationGeneratorPlugin.validateArgs).
func TestKustomizeHelmChartsRejectFlagLikeNames(t *testing.T) {
	_, err := renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n    releaseName: --post-renderer\n", RenderOptions{}, nil)
	want := `releaseName must not start with '-', got "--post-renderer"`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Render() error = %v, want %q", err, want)
	}
}

// Argo CD v3.6.3 fails a helm.releaseName that helm would reject, before it
// runs helm. kustomize leaves the check of a helmCharts entry to helm.
func TestHelmRendererValidatesReleaseNameLikeArgoCD(t *testing.T) {
	for _, name := range []string{strings.Repeat("a", 54), "Upper", "under_score"} {
		argoErr := argohelm.ValidateReleaseName(name)
		helmErr := chartv2util.ValidateReleaseName(name)
		if argoErr == nil || helmErr == nil {
			t.Fatalf("ValidateReleaseName(%q) = %v, %v, want two errors", name, argoErr, helmErr)
		}
		want := "invalid spec.source.helm.releaseName: " + argoErr.Error()
		_, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: "demo", ReleaseName: name})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Helm Render(%q) error = %v, want %q", name, err, want)
		}
		want = `release name check failed: release name "` + name + `": ` + helmErr.Error()
		_, err = renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n    releaseName: "+name+"\n", RenderOptions{}, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Kustomize Render(%q) error = %v, want %q", name, err, want)
		}
	}
	// The Application name is the release name when helm.releaseName is
	// empty. Argo CD checks it right before it runs helm, and the error
	// does not name the field.
	long := strings.Repeat("a", 54)
	want := "failed to execute helm template command: " + argohelm.ValidateReleaseName(long).Error()
	_, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: long})
	if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "spec.source.helm.releaseName") {
		t.Fatalf("Render() error = %v, want %q", err, want)
	}
	// 53 characters is the longest name Argo CD and helm accept.
	if _, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: strings.Repeat("a", 53)}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	// With no release name and no Application name, Argo CD passes no name,
	// and helm template uses its default name.
	manifests, err := renderReleaseProbeHelm(t, "", RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertConfigMapData(t, manifests, "release", helmTemplateDefaultReleaseName)
}

// Argo CD v3.6.3 passes the release name to helm template as an argument,
// which helm reads as written. A name with template actions fails the
// release name check and is never evaluated (GHSA-v2hj-322q-6cg4). Argo CD
// v3.6.2 and earlier passed it as --name-template, which helm evaluated
// with the sprig functions, env among them.
func TestHelmRendererPassesReleaseNameAsWritten(t *testing.T) {
	t.Setenv("DRYDOCK_RELEASE_NAME_PROBE", "leaked")
	for _, name := range []string{
		`{{ env "DRYDOCK_RELEASE_NAME_PROBE" }}`,
		`{{ printf "%s-%s" "tmpl" ("NAME" | lower) }}`,
		"{{ .Missing.Field }",
	} {
		argoErr := argohelm.ValidateReleaseName(name)
		if argoErr == nil {
			t.Fatalf("ValidateReleaseName(%q) = nil, want an error", name)
		}
		want := "invalid spec.source.helm.releaseName: " + argoErr.Error()
		manifests, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: "demo", ReleaseName: name})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Render(%q) error = %v, want %q", name, err, want)
		}
		if len(manifests) != 0 || strings.Contains(err.Error(), "leaked") {
			t.Fatalf("Render(%q) = %d manifests, error %v, want no output and no evaluated name", name, len(manifests), err)
		}
	}

	_, err := renderReleaseProbeKustomize(t, "helmCharts:\n  - name: probe\n    releaseName: '{{ \"x\" }}'\n", RenderOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), `release name "{{ \"x\" }}"`) {
		t.Fatalf("Kustomize Render() error = %v, want the release name check of the literal name", err)
	}
}

// Argo CD v3.6.3 checks helm.releaseName before it resolves the value files
// and before helm reads its flags. It checks the Application name as the
// release name after it resolves the value file paths.
func TestHelmRendererReleaseNameCheckOrder(t *testing.T) {
	outside := RenderOptions{AppName: "demo", ValueFiles: []string{"../../../outside.yaml"}}
	badFlag := RenderOptions{AppName: "demo", APIVersions: []string{`a"b`}}
	for _, opts := range []RenderOptions{outside, badFlag} {
		opts.ReleaseName = "Bad_Name"
		_, err := renderReleaseProbeHelm(t, "", opts)
		if err == nil || !strings.Contains(err.Error(), "invalid spec.source.helm.releaseName") {
			t.Fatalf("Render(%+v) error = %v, want the release name error first", opts, err)
		}
	}
	outside.AppName = strings.Repeat("a", 54)
	_, err := renderReleaseProbeHelm(t, "", outside)
	if err == nil || !strings.Contains(err.Error(), "resolved to outside repository root") {
		t.Fatalf("Render() error = %v, want the value file path error before the Application name check", err)
	}
}

// Argo CD v3.6.1 supports only helm.version "", v3, and v4.
func TestHelmRendererRejectsUnsupportedHelmVersion(t *testing.T) {
	for _, version := range []string{"", "v3", "v4"} {
		if _, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: "demo", HelmVersion: version}); err != nil {
			t.Fatalf("Render(version %q) error = %v", version, err)
		}
	}
	_, err := renderReleaseProbeHelm(t, "", RenderOptions{AppName: "demo", HelmVersion: "v2"})
	want := "failed to create new helm command: helm version 'v2' is not supported"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Render() error = %v, want %q", err, want)
	}
}

// helm template renders only application charts of apiVersion v1 or v2
// whose kubeVersion constraint the kube version meets.
func TestHelmRendererRejectsChartsHelmCannotInstall(t *testing.T) {
	for _, tt := range []struct {
		name      string
		chartYAML string
		opts      RenderOptions
		want      string
	}{
		{name: "library chart", chartYAML: "type: library\n", want: "library charts are not installable"},
		{name: "kube version", chartYAML: "kubeVersion: '>=99.0.0'\n", opts: RenderOptions{KubeVersion: "1.30.0"}, want: "chart requires kubeVersion: >=99.0.0 which is incompatible with Kubernetes v1.30.0"},
		{name: "invalid kube version constraint", chartYAML: "kubeVersion: 'not a range'\n", opts: RenderOptions{KubeVersion: "1.30.0"}, want: "chart requires kubeVersion: not a range which is incompatible with Kubernetes v1.30.0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			opts.AppName = "demo"
			_, err := renderReleaseProbeHelm(t, tt.chartYAML, opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Render() error = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := renderReleaseProbeHelm(t, "kubeVersion: '>=1.20.0-0'\n", RenderOptions{AppName: "demo", KubeVersion: "1.30.0"}); err != nil {
		t.Fatalf("Render() with a met kubeVersion error = %v", err)
	}

	root := t.TempDir()
	chartDir := filepath.Join(root, "chart")
	writeReleaseProbeChart(t, chartDir, "")
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v3\nname: probe\nversion: 0.1.0\n")
	_, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: root, Path: "chart"}, RenderOptions{AppName: "demo"})
	if err == nil || !strings.Contains(err.Error(), "invalid chart apiVersion") {
		t.Fatalf("Render() of an apiVersion v3 chart error = %v, want invalid chart apiVersion", err)
	}
}

// TestHelmRenderParityDecodeFixtures renders the render cases and the
// Kustomize helmCharts cases of the Argo CD render parity smoke
// (testdata/argocd-parity) offline. drydock must fail with the
// drydockMessage that the smoke expects. helm and kustomize produce the
// Argo CD side, so the smoke checks that side against Argo CD itself.
func TestHelmRenderParityDecodeFixtures(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "testdata", "argocd-parity", "repo")
	var checked int
	for _, tc := range readParityDecodeCasesForSource(t) {
		if tc.Source != "render" && !parityCaseHasHelmCharts(t, tc) {
			continue
		}
		checked++
		t.Run(tc.Name, func(t *testing.T) {
			caseDir := strings.TrimPrefix(tc.Name, "parity-decode-")
			source := readParityApplicationSource(t, filepath.Join(repoRoot, "directory-decode-applications", caseDir+".yaml"))
			if source.Chart != "" {
				// A chart from a chart repository fails in its version
				// resolution, which the tests of internal/chart cover.
				t.Skip("chart repository source")
			}
			opts := RenderOptions{AppName: tc.Name, Namespace: "parity-directory-decode-workloads"}
			if source.Helm != nil {
				opts.ReleaseName = source.Helm.ReleaseName
				opts.HelmVersion = source.Helm.Version
				opts.KubeVersion = source.Helm.KubeVersion
				opts.ValueFiles = source.Helm.ValueFiles
				// The $ref sources of the fixture are the fixture
				// repository itself.
				opts.RefRoots = map[string]string{"$values": repoRoot}
			}
			// The spec.source.kustomize options of the Kustomize source
			// option cases.
			opts.Kustomize = source.Kustomize
			resolved := ResolvedSource{RepoRoot: repoRoot, Path: source.Path}
			var renderer Renderer = HelmRenderer{}
			if _, err := os.Stat(filepath.Join(repoRoot, source.Path, "kustomization.yaml")); err == nil {
				renderer = KustomizeRenderer{}
			}
			_, _, err := renderer.Render(context.Background(), resolved, opts)
			if err == nil || !strings.Contains(err.Error(), tc.DrydockMessage) {
				t.Fatalf("drydock error = %v, want %q", err, tc.DrydockMessage)
			}
		})
	}
	if checked == 0 {
		t.Fatal("the expected file has no render or Kustomize helmCharts case")
	}
}

// readParityDecodeCasesForSource reads every case of the expected file that
// renders a source in the main fixture repository.
func readParityDecodeCasesForSource(t *testing.T) []parityDecodeCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "argocd-parity", "directory-decode-expected.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var expected struct {
		Cases []parityDecodeCase `json:"cases"`
	}
	if err := sigsyaml.Unmarshal(data, &expected); err != nil {
		t.Fatalf("parse expected file: %v", err)
	}
	var out []parityDecodeCase
	for _, tc := range expected.Cases {
		if tc.Repository == "" && tc.Source != "spec" {
			out = append(out, tc)
		}
	}
	return out
}
