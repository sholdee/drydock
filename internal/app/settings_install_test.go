package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// The fake argo-cd chart copies the argocd-cm, argocd-cmd-params-cm, and
// argocd-cmp-cm templates and the "argo-cd.config.cm" and
// "argo-cd.config.params" helpers of the argo-helm argo-cd chart 10.14.0. The
// presets and the default values are shorter: they keep only what the tests
// read. Vendoring the real chart (3 MB) is too heavy for a unit test.
const (
	fakeArgoCDChartRepo    = "https://argoproj.github.io/argo-helm"
	fakeArgoCDChartVersion = "10.14.0"
	fakeArgoCDChartLease   = "coordination.k8s.io"
)

const fakeArgoCDChartValues = `global:
  domain: argocd.example.com
repoServer:
  service:
    port: 8081
configs:
  cm:
    create: true
    annotations: {}
    application.instanceLabelKey: argocd.argoproj.io/instance
    exec.enabled: false
    timeout.reconciliation: 120s
    resource.exclusions: |
      - apiGroups:
        - coordination.k8s.io
        kinds:
        - Lease
    resourceExclusionsAdditional: []
  params:
    create: true
    annotations: {}
  cmp:
    create: false
    annotations: {}
    plugins: {}
`

const fakeArgoCDChartHelpers = `{{- define "argo-cd.config.cm.presets" -}}
{{- $presets := dict -}}
{{- $_ := set $presets "url" (printf "https://%s" .Values.global.domain) -}}
{{- toYaml $presets }}
{{- end -}}

{{- define "argo-cd.config.cm" -}}
{{- $config := omit .Values.configs.cm "create" "annotations" "resourceExclusionsAdditional" -}}
{{- $preset := include "argo-cd.config.cm.presets" . | fromYaml | default dict -}}
{{- $merged := mergeOverwrite $preset $config -}}
{{- if .Values.configs.cm.resourceExclusionsAdditional }}
{{- $existing := get $merged "resource.exclusions" | default "" | trimSuffix "\n" }}
{{- $additional := .Values.configs.cm.resourceExclusionsAdditional | toYaml }}
{{- $_ := set $merged "resource.exclusions" (list $existing $additional | compact | join "\n") -}}
{{- end }}
{{- range $key, $value := $merged }}
{{- $fmted := $value | toString }}
{{- if or (eq $key "url") (eq $key "statusbadge.url") }}
{{- $fmted = tpl $fmted $ }}
{{- end }}
{{- if not (eq $fmted "") }}
{{ $key }}: {{ $fmted | toYaml }}
{{- end }}
{{- end }}
{{- end -}}

{{- define "argo-cd.config.params.presets" -}}
{{- $presets := dict -}}
{{- $_ := set $presets "repo.server" (printf "%s-repo-server:%s" .Release.Name (.Values.repoServer.service.port | toString)) -}}
{{- toYaml $presets }}
{{- end -}}

{{- define "argo-cd.config.params" -}}
{{- $config := omit .Values.configs.params "create" "annotations" }}
{{- $preset := include "argo-cd.config.params.presets" . | fromYaml | default dict -}}
{{- range $key, $value := mergeOverwrite $preset $config }}
{{ $key }}: {{ toString $value | toYaml }}
{{- end }}
{{- end -}}
`

const fakeArgoCDChartCM = `{{- if .Values.configs.cm.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: {{ .Release.Namespace }}
data:
  {{- include "argo-cd.config.cm" . | trim | nindent 2 }}
{{- end }}
`

const fakeArgoCDChartParams = `{{- if .Values.configs.params.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: {{ .Release.Namespace }}
data:
  {{- include "argo-cd.config.params" . | trim | nindent 2 }}
{{- end }}
`

const fakeArgoCDChartCMP = `{{- if .Values.configs.cmp.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmp-cm
  namespace: {{ .Release.Namespace }}
data:
  {{- range $cmp_plugin, $cmp_plugin_config := .Values.configs.cmp.plugins }}
  {{ $cmp_plugin }}.yaml: |
    apiVersion: argoproj.io/v1alpha1
    kind: ConfigManagementPlugin
    metadata:
      name: {{ $cmp_plugin }}
    spec:
      {{- toYaml $cmp_plugin_config | nindent 6 }}
  {{- end }}
{{- end }}
`

// writeFakeArgoCDChart writes the fake argo-cd chart to dir.
func writeFakeArgoCDChart(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: argo-cd\nversion: "+fakeArgoCDChartVersion+"\nappVersion: v3.6.1\n")
	writeTestFile(t, filepath.Join(dir, "values.yaml"), fakeArgoCDChartValues)
	writeTestFile(t, filepath.Join(dir, "templates", "_helpers.tpl"), fakeArgoCDChartHelpers)
	writeTestFile(t, filepath.Join(dir, "templates", "argocd-configs", "argocd-cm.yaml"), fakeArgoCDChartCM)
	writeTestFile(t, filepath.Join(dir, "templates", "argocd-configs", "argocd-cmd-params-cm.yaml"), fakeArgoCDChartParams)
	writeTestFile(t, filepath.Join(dir, "templates", "argocd-configs", "argocd-cmp-cm.yaml"), fakeArgoCDChartCMP)
}

// fakeArgoCDChartAcquirer serves the fake argo-cd chart from outside the
// repository, as a chart repository would. Run with Parallelism 1: it records
// requests without a lock.
func fakeArgoCDChartAcquirer(t *testing.T) *recordingChartAcquirer {
	t.Helper()
	dir := t.TempDir()
	writeFakeArgoCDChart(t, dir)
	return &recordingChartAcquirer{chartDir: dir}
}

// Argo CD install shapes. Each writes an install of the fake chart that reads
// the values files in valuesFiles, in order, and also the Application other.
type argoCDInstallShape struct {
	name  string
	write func(t *testing.T, root string, valuesFiles []string)
	// repoMaps maps the $values ref source to the repository root.
	repoMaps func(root string) []sourcepkg.RepoMap
}

const argoCDValuesRepoURL = "https://github.com/example/argocd-values"

func argoCDInstallShapes() []argoCDInstallShape {
	return []argoCDInstallShape{
		{
			name:  "helm chart with $values ref",
			write: writeHelmRefArgoCDInstall,
			repoMaps: func(root string) []sourcepkg.RepoMap {
				return []sourcepkg.RepoMap{{URL: argoCDValuesRepoURL, Path: root}}
			},
		},
		{
			name:  "kustomize helmCharts",
			write: writeKustomizeArgoCDInstall,
		},
		{
			name:  "ApplicationSet-generated kustomize helmCharts",
			write: writeApplicationSetKustomizeArgoCDInstall,
		},
	}
}

func writeHelmRefArgoCDInstall(t *testing.T, root string, valuesFiles []string) {
	t.Helper()
	var refs strings.Builder
	for _, file := range valuesFiles {
		refs.WriteString("          - $values/argocd/" + file + "\n")
	}
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: `+fakeArgoCDChartRepo+`
      chart: argo-cd
      targetRevision: `+fakeArgoCDChartVersion+`
      helm:
        releaseName: argocd
        valueFiles:
`+refs.String()+`    - repoURL: `+argoCDValuesRepoURL+`
      targetRevision: main
      ref: values
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeDiffApplication(t, root, "other", "other", "same")
}

func writeKustomizeArgoCDInstall(t *testing.T, root string, valuesFiles []string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: argocd
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), kustomizeArgoCDInstallYAML("", valuesFiles))
	writeDiffApplication(t, root, "other", "other", "same")
}

func kustomizeArgoCDInstallYAML(resources string, valuesFiles []string) string {
	var additional strings.Builder
	if len(valuesFiles) > 1 {
		additional.WriteString("    additionalValuesFiles:\n")
		for _, file := range valuesFiles[1:] {
			additional.WriteString("      - " + file + "\n")
		}
	}
	return `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: argocd
` + resources + `helmCharts:
  - name: argo-cd
    repo: ` + fakeArgoCDChartRepo + `
    version: ` + fakeArgoCDChartVersion + `
    releaseName: argocd
    namespace: argocd
    valuesFile: ` + valuesFiles[0] + `
` + additional.String()
}

// writeApplicationSetKustomizeArgoCDInstall writes the home-ops layout: the
// install directory holds the ApplicationSet that generates the install
// Application and the other Applications, as Kustomize resources, so static
// discovery has already found them there.
func writeApplicationSetKustomizeArgoCDInstall(t *testing.T, root string, valuesFiles []string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "argocd", "manifests", "app-set.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: apps
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: argocd
  template:
    metadata:
      name: '{{.name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: '{{.name}}'
      destination:
        name: in-cluster
        namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "argocd", "manifests", "apps.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: other
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/other
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "other", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: other
data:
  value: same
`)
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), kustomizeArgoCDInstallYAML("resources:\n  - manifests/app-set.yaml\n  - manifests/apps.yaml\n", valuesFiles))
}

func listArgoCDInstallSettings(t *testing.T, root string, shape argoCDInstallShape, acquirer *recordingChartAcquirer) BuildResult {
	t.Helper()
	request := BuildRequest{Path: root, Parallelism: 1}
	if shape.repoMaps != nil {
		request.RepoMaps = shape.repoMaps(root)
	}
	result, err := Orchestrator{ChartAcquirer: acquirer}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	return result
}

func settingsDiagnostics(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var out []diagnostic.Diagnostic
	for _, diag := range diags {
		if diag.Category == "settings" || strings.Contains(diag.Message, "argocd-cm") {
			out = append(out, diag)
		}
	}
	return out
}

func resourceExclusionGroups(settings config.ArgoSettings) []string {
	var groups []string
	for _, exclusion := range settings.ResourceExclusions {
		groups = append(groups, exclusion.APIGroups...)
	}
	return groups
}

// Helm merges every values file of one release into one configs.cm map, and
// a later file wins for each key. So two layered values files that set the
// same key are not a conflict, as Argo CD sees one argocd-cm.
func TestSettingsFromRenderedArgoCDInstallLayeredValuesFiles(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values-base.yaml", "values-prod.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values-base.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: label
`)
			writeTestFile(t, filepath.Join(root, "argocd", "values-prod.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: annotation+label
`)

			result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
			if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
				t.Fatalf("settings diagnostics = %#v, want none", diags)
			}
			// drydock renders the install only for its settings, so the
			// objects static discovery found there are not duplicated.
			for _, diag := range result.Diagnostics {
				if strings.Contains(diag.Message, "duplicate") {
					t.Fatalf("diagnostic = %#v, want no duplicate", diag)
				}
			}
			if got := strings.Join(applicationNames(result.Applications), ","); got != "argocd,other" {
				t.Fatalf("Applications = %s, want argocd,other", got)
			}
			if got := result.Settings.TrackingMethod.Value; got != "annotation+label" {
				t.Fatalf("TrackingMethod = %q, want the later values file", got)
			}
			if got := result.Settings.TrackingMethod.Provenance.Path; !strings.HasPrefix(got, "rendered/argocd/argocd/") {
				t.Fatalf("TrackingMethod provenance = %q, want the rendered argocd-cm", got)
			}
		})
	}
}

// The chart defaults reach argocd-cm: the default resource.exclusions list,
// to which resourceExclusionsAdditional is added, and the default instance
// label key. A null value removes a chart default, also when an earlier
// values file sets the key.
func TestSettingsFromRenderedArgoCDInstallChartDefaultsAndNull(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values-base.yaml", "values-prod.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values-base.yaml"), `configs:
  cm:
    timeout.reconciliation: 300s
    resourceExclusionsAdditional:
      - apiGroups:
          - cilium.io
        kinds:
          - CiliumIdentity
`)
			writeTestFile(t, filepath.Join(root, "argocd", "values-prod.yaml"), `configs:
  cm:
    application.instanceLabelKey: null
`)

			result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
			if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
				t.Fatalf("settings diagnostics = %#v, want none", diags)
			}
			if got, want := resourceExclusionGroups(result.Settings), []string{fakeArgoCDChartLease, "cilium.io"}; !slices.Equal(got, want) {
				t.Fatalf("resource exclusion groups = %v, want chart default then additional %v", got, want)
			}
			if got, want := result.Settings.InstanceLabelKey.Value, config.DefaultSettings().InstanceLabelKey.Value; got != want {
				t.Fatalf("InstanceLabelKey = %q, want the Argo CD default %q after null removes the chart default", got, want)
			}
		})
	}
}

// With no null, the chart default instance label key reaches argocd-cm.
func TestSettingsFromRenderedArgoCDInstallChartDefaultInstanceLabelKey(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: annotation
`)

			result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
			if got := result.Settings.InstanceLabelKey.Value; got != "argocd.argoproj.io/instance" {
				t.Fatalf("InstanceLabelKey = %q, want the chart default", got)
			}
		})
	}
}

// configs.params becomes argocd-cmd-params-cm, which sets the repo-server
// directory size limit.
func TestSettingsFromRenderedArgoCDInstallCommandParameters(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    reposerver.max.combined.directory.manifests.size: 50M
`)

			result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
			if got := result.Settings.MaxCombinedDirectoryManifestsSize.Value; got != "50M" {
				t.Fatalf("MaxCombinedDirectoryManifestsSize = %q, want configs.params value", got)
			}
		})
	}
}

// When drydock cannot render the install, it reads each values file on its
// own as before, and it says so with a warning.
func TestSettingsFromArgoCDInstallFallsBackToValuesFilesWhenRenderFails(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: label
`)

			acquirer := &recordingChartAcquirer{acquireErr: errors.New("chart is not in the cache")}
			result := listArgoCDInstallSettings(t, root, shape, acquirer)
			if got := result.Settings.TrackingMethod.Value; got != "label" {
				t.Fatalf("TrackingMethod = %q, want the values file value", got)
			}
			if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); !strings.HasSuffix(got, "/argocd/values.yaml") {
				t.Fatalf("TrackingMethod provenance = %q, want the values file", got)
			}
			var warnings []diagnostic.Diagnostic
			for _, diag := range result.Diagnostics {
				if diag.Category == "settings" && strings.Contains(diag.Message, "cannot render Application argocd/argocd") {
					warnings = append(warnings, diag)
				}
			}
			if len(warnings) != 1 || warnings[0].Severity != diagnostic.SeverityWarning || warnings[0].Provenance.Path != "argocd/values.yaml" {
				t.Fatalf("fallback diagnostics = %#v, want one warning at argocd/values.yaml", warnings)
			}
			if !strings.Contains(warnings[0].Message, "chart is not in the cache") {
				t.Fatalf("fallback message = %q, want the render error", warnings[0].Message)
			}
		})
	}
}

// A committed argocd-cm that the install patches is an input of the rendered
// argocd-cm, not a second argocd-cm: drydock reads the rendered one, which
// holds the patch and the chart defaults, and reports no duplicate.
func TestSettingsFromRenderedArgoCDInstallReplacesCommittedPatch(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: argocd
helmCharts:
  - name: argo-cd
    repo: `+fakeArgoCDChartRepo+`
    version: `+fakeArgoCDChartVersion+`
    releaseName: argocd
    namespace: argocd
    valuesFile: values.yaml
patches:
  - path: argocd-cm-patch.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    timeout.reconciliation: 300s
`)
	writeTestFile(t, filepath.Join(root, "argocd", "argocd-cm-patch.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  application.resourceTrackingMethod: label
`)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "duplicate") || diag.Category == "settings" {
			t.Fatalf("diagnostic = %#v, want none", diag)
		}
	}
	if got := result.Settings.TrackingMethod.Value; got != "label" {
		t.Fatalf("TrackingMethod = %q, want the patched value", got)
	}
	if got := result.Settings.InstanceLabelKey.Value; got != "argocd.argoproj.io/instance" {
		t.Fatalf("InstanceLabelKey = %q, want the chart default from the rendered argocd-cm", got)
	}
}

// A values file that belongs to no rendered install is still read on its
// own, next to the rendered argocd-cm of the install.
func TestSettingsFromRenderedArgoCDInstallKeepsUnrenderedValuesFile(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    timeout.reconciliation: 300s
`)
			writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: label
`)

			result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
			if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
				t.Fatalf("settings diagnostics = %#v, want none", diags)
			}
			if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "label" || !strings.HasSuffix(got, "/settings/values.yaml") {
				t.Fatalf("TrackingMethod = %q from %q, want label from the unrendered values file", result.Settings.TrackingMethod.Value, got)
			}
			if got := result.Settings.InstanceLabelKey.Value; got != "argocd.argoproj.io/instance" {
				t.Fatalf("InstanceLabelKey = %q, want the chart default from the rendered argocd-cm", got)
			}
		})
	}
}

// A values change that only the rendered install shows still changes the
// settings of the side, so changed-only renders every Application. Before,
// drydock did not read configs.params, so it rendered only the install.
func TestOrchestratorDiffAppsChangedOnlyRendersAllOnRenderedInstallSettingsChange(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for side, size := range map[string]string{left: "10M", right: "20M"} {
		writeKustomizeArgoCDInstall(t, side, []string{"values.yaml"})
		writeTestFile(t, filepath.Join(side, "argocd", "values.yaml"), `configs:
  params:
    reposerver.max.combined.directory.manifests.size: `+size+`
`)
	}
	request := DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		Unified:     3,
		ChangedOnly: true,
		Parallelism: 1,
	}
	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.DiffApps(context.Background(), request)
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	var changedOnly []diagnostic.Diagnostic
	for _, diag := range result.Diagnostics {
		if diag.Category == "changed-only" {
			changedOnly = append(changedOnly, diag)
		}
	}
	if want := []diagnostic.Diagnostic{changedOnlySettingsDiagnostic()}; !slices.Equal(changedOnly, want) {
		t.Fatalf("changed-only diagnostics = %#v, want %#v", changedOnly, want)
	}
}

// Helm values written in the Application, with no values file, also make
// drydock render the install for its settings.
func TestSettingsFromRenderedArgoCDInstallInlineValues(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+fakeArgoCDChartRepo+`
    chart: argo-cd
    targetRevision: `+fakeArgoCDChartVersion+`
    helm:
      releaseName: argocd
      valuesObject:
        configs:
          cm:
            application.resourceTrackingMethod: label
  destination:
    name: in-cluster
    namespace: argocd
`)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if got := result.Settings.TrackingMethod.Value; got != "label" {
		t.Fatalf("TrackingMethod = %q, want the inline value", got)
	}
}
