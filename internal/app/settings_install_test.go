package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	sigsyaml "sigs.k8s.io/yaml"
)

// The fake argo-cd chart copies the argocd-cm, argocd-cmd-params-cm, and
// argocd-cmp-cm templates and the "argo-cd.namespace", "argo-cd.config.cm",
// and "argo-cd.config.params" helpers of the argo-helm argo-cd chart 10.14.0. The
// presets and the default values are shorter: they keep only what the tests
// read, and the eight ignoreResourceUpdates defaults of the real chart.
// Vendoring the real chart (3 MB) is too heavy for a unit test.
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
    resource.customizations.ignoreResourceUpdates.all: |
      jsonPointers:
        - /status
    resource.customizations.ignoreResourceUpdates.argoproj.io_Application: |
      jqPathExpressions:
        - '.metadata.annotations."notified.notifications.argoproj.io"'
        - '.metadata.annotations."argocd.argoproj.io/refresh"'
        - '.metadata.annotations."argocd.argoproj.io/refresh-timestamp"'
        - '.metadata.annotations."argocd.argoproj.io/hydrate"'
        - '.metadata.annotations."argocd.argoproj.io/hydrate-timestamp"'
        - '.operation'
    resource.customizations.ignoreResourceUpdates.argoproj.io_Rollout: |
      jqPathExpressions:
        - '.metadata.annotations."notified.notifications.argoproj.io"'
    resource.customizations.ignoreResourceUpdates.autoscaling_HorizontalPodAutoscaler: |
      jqPathExpressions:
        - '.metadata.annotations."autoscaling.alpha.kubernetes.io/behavior"'
        - '.metadata.annotations."autoscaling.alpha.kubernetes.io/conditions"'
        - '.metadata.annotations."autoscaling.alpha.kubernetes.io/metrics"'
        - '.metadata.annotations."autoscaling.alpha.kubernetes.io/current-metrics"'
    resource.customizations.ignoreResourceUpdates.ConfigMap: |
      jqPathExpressions:
        - '.metadata.annotations."cluster-autoscaler.kubernetes.io/last-updated"'
        - '.metadata.annotations."control-plane.alpha.kubernetes.io/leader"'
    resource.customizations.ignoreResourceUpdates.apps_ReplicaSet: |
      jqPathExpressions:
        - '.metadata.annotations."deployment.kubernetes.io/desired-replicas"'
        - '.metadata.annotations."deployment.kubernetes.io/max-replicas"'
        - '.metadata.annotations."rollout.argoproj.io/desired-replicas"'
    resource.customizations.ignoreResourceUpdates.discovery.k8s.io_EndpointSlice: |
      jsonPointers:
        - /metadata
        - /endpoints
        - /ports
    resource.customizations.ignoreResourceUpdates.Endpoints: |
      jsonPointers:
        - /metadata
        - /subsets
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

const fakeArgoCDChartHelpers = `{{- define "argo-cd.namespace" -}}
{{- default .Release.Namespace .Values.namespaceOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "argo-cd.config.cm.presets" -}}
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
  namespace: {{ include "argo-cd.namespace" . }}
data:
  {{- include "argo-cd.config.cm" . | trim | nindent 2 }}
{{- end }}
`

const fakeArgoCDChartParams = `{{- if .Values.configs.params.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: {{ include "argo-cd.namespace" . }}
data:
  {{- include "argo-cd.config.params" . | trim | nindent 2 }}
{{- end }}
`

const fakeArgoCDChartCMP = `{{- if .Values.configs.cmp.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmp-cm
  namespace: {{ include "argo-cd.namespace" . }}
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
	// localChart reports that the chart is in the repository, so the chart
	// acquirer cannot make the render fail.
	localChart bool
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
		{
			name:  "kustomize helmCharts in a component of the Application",
			write: writeKustomizeComponentArgoCDInstall,
		},
		{
			name:       "local chart that the frontier renders",
			write:      writeLocalChartArgoCDInstall,
			localChart: true,
		},
	}
}

// writeLocalChartArgoCDInstall writes the fake chart into the repository.
// Its templates name argocd-cm, so the discovery frontier renders the
// Application itself (applicationMayRenderDiscoveryObjects), not the
// settings-only pass. The values.yaml of the chart is also a committed
// values file that sets Argo CD settings.
func writeLocalChartArgoCDInstall(t *testing.T, root string, valuesFiles []string) {
	t.Helper()
	writeFakeArgoCDChart(t, filepath.Join(root, "argocd", "chart"))
	var files strings.Builder
	for _, file := range valuesFiles {
		files.WriteString("        - ../" + file + "\n")
	}
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
    path: argocd/chart
    helm:
      releaseName: argocd
      valueFiles:
`+files.String()+`  destination:
    name: in-cluster
    namespace: argocd
`)
	writeDiffApplication(t, root, "other", "other", "same")
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

// writeKustomizeComponentArgoCDInstall puts the argo-cd helmCharts entry in
// a Kustomize component that spec.source.kustomize.components adds. The
// kustomization of the source path holds no helmCharts entry of its own.
func writeKustomizeComponentArgoCDInstall(t *testing.T, root string, valuesFiles []string) {
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
    path: install
    kustomize:
      components:
        - ../argocd
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "install", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: argocd\n")
	component := strings.Replace(kustomizeArgoCDInstallYAML("", valuesFiles),
		"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: argocd\n",
		"apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n", 1)
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), component)
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

// settingsDiagnostics returns the settings diagnostics.
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
// directory size limit and the out-of-bounds symlink check.
func TestSettingsFromRenderedArgoCDInstallCommandParameters(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    reposerver.max.combined.directory.manifests.size: 50M
    reposerver.allow.oob.symlinks: "true"
`)

			result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
			if got := result.Settings.MaxCombinedDirectoryManifestsSize.Value; got != "50M" {
				t.Fatalf("MaxCombinedDirectoryManifestsSize = %q, want configs.params value", got)
			}
			if got := result.Settings.AllowOutOfBoundsSymlinks.Value; got != "true" {
				t.Fatalf("AllowOutOfBoundsSymlinks = %q, want configs.params value", got)
			}
		})
	}
}

// configs.params can set controller.diff.server.side. The chart renders the
// Helm boolean as "true" in argocd-cmd-params-cm, and the controller reads
// it from there through ARGOCD_APPLICATION_CONTROLLER_SERVER_SIDE_DIFF.
// drydock applies it and gives no runtime-only warning for it.
func TestSettingsFromRenderedArgoCDInstallControllerServerSideDiff(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    controller.diff.server.side: true
`)

			result := listArgoCDInstallSettings(t, root, shape, fakeArgoCDChartAcquirer(t))
			if got := result.Settings.ControllerServerSideDiff.Value; got != "true" {
				t.Fatalf("ControllerServerSideDiff = %q, want configs.params value", got)
			}
			for _, diag := range result.Diagnostics {
				if diag.Code == "settings.metadata-only" {
					t.Fatalf("diagnostic %#v, want no runtime-only warning", diag)
				}
			}
		})
	}
}

// When drydock cannot render the install, it reads each values file on its
// own as before, and it says so with a warning.
func TestSettingsFromArgoCDInstallFallsBackToValuesFilesWhenRenderFails(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		if shape.localChart {
			continue
		}
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
		if strings.Contains(diag.Message, "duplicate") {
			t.Fatalf("diagnostic = %#v, want no duplicate", diag)
		}
	}
	if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("settings diagnostics = %#v, want none", diags)
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
  cm:
    kustomize.buildOptions: --enable-helm
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

// perRepoChartAcquirer serves the fake argo-cd chart, but fails for the
// chart repositories in failures. Run with Parallelism 1.
type perRepoChartAcquirer struct {
	*recordingChartAcquirer
	failures map[string]error
}

func (acquirer perRepoChartAcquirer) Acquire(ctx context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	if err, ok := acquirer.failures[request.Repository]; ok {
		return chart.Result{}, err
	}
	return acquirer.recordingChartAcquirer.Acquire(ctx, request, opts)
}

// The default chart sets eight ignoreResourceUpdates customizations. They
// never change desired output, so they give no diagnostic and --strict
// passes.
func TestBuildStrictPassesForDefaultArgoCDChartInstall(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	// The install renders its chart with helmCharts, which kustomize builds
	// only with --enable-helm.
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    kustomize.buildOptions: --enable-helm
    timeout.reconciliation: 300s
`)

	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.Build(context.Background(), BuildRequest{
		Path:        root,
		Parallelism: 1,
		Strict:      true,
	})
	if err != nil {
		t.Fatalf("Build(--strict) error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if !hasArgoCDChartIgnoreResourceUpdates(result.Settings) {
		t.Fatalf("ResourceCustomizations = %#v, want the chart ignoreResourceUpdates defaults", result.Settings.ResourceCustomizations)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "settings" || strings.Contains(diag.Message, "ignoreResourceUpdates") {
			t.Fatalf("diagnostic = %#v, want no settings diagnostic for the chart defaults", diag)
		}
	}
}

// hasArgoCDChartIgnoreResourceUpdates reports whether the settings keep the
// eight ignoreResourceUpdates defaults of the fake argo-cd chart, so a test
// that wants no diagnostic for them proves they were read.
func hasArgoCDChartIgnoreResourceUpdates(settings config.ArgoSettings) bool {
	count := 0
	for _, customization := range settings.ResourceCustomizations {
		ignore := customization.IgnoreResourceUpdates
		if len(ignore.JSONPointers) > 0 || len(ignore.JQPathExpressions) > 0 || len(ignore.ManagedFieldsManagers) > 0 {
			count++
		}
	}
	return count == 8
}

// The install reads ../shared as a Kustomize resource. A values file in that
// directory that the install does not read is not an input of the rendered
// argocd-cm, so drydock still reads it on its own. A committed argocd-cm
// there that the install does not read is a second argocd-cm: drydock keeps
// the committed one and reports the rendered one as a duplicate.
func TestSettingsFromRenderedArgoCDInstallKeepsFilesInReadDirectories(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, shared string)
		check func(t *testing.T, result BuildResult)
	}{
		{
			name: "values file the install does not read",
			write: func(t *testing.T, shared string) {
				writeTestFile(t, filepath.Join(shared, "legacy-values.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: label
`)
			},
			check: func(t *testing.T, result BuildResult) {
				if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
					t.Fatalf("settings diagnostics = %#v, want none", diags)
				}
				if got := result.Settings.TrackingMethod.Value; got != "label" {
					t.Fatalf("TrackingMethod = %q, want label from the unread values file", got)
				}
			},
		},
		{
			name: "argocd-cm the install does not read",
			write: func(t *testing.T, shared string) {
				writeTestFile(t, filepath.Join(shared, "argocd-cm.yaml"), argocdCMYAML("  application.resourceTrackingMethod: label\n"))
			},
			check: func(t *testing.T, result BuildResult) {
				if got := result.Settings.TrackingMethod.Value; got != "label" {
					t.Fatalf("TrackingMethod = %q, want label from the committed argocd-cm", got)
				}
				for _, diag := range result.Diagnostics {
					if strings.Contains(diag.Message, "duplicate ConfigMap argocd/argocd-cm from rendered/argocd/argocd/") {
						return
					}
				}
				t.Fatalf("diagnostics = %#v, want the rendered argocd-cm reported as a duplicate", result.Diagnostics)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), kustomizeArgoCDInstallYAML("resources:\n  - ../shared\n", []string{"values.yaml"}))
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    timeout.reconciliation: 300s
`)
			shared := filepath.Join(root, "shared")
			writeTestFile(t, filepath.Join(shared, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
`)
			writeTestFile(t, filepath.Join(shared, "namespace.yaml"), `apiVersion: v1
kind: Namespace
metadata:
  name: argocd
`)
			tc.write(t, shared)

			tc.check(t, listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t)))
		})
	}
}

// An install of the argo-cd chart whose values set nothing under configs is
// still an install: its argocd-cm holds the chart defaults.
func TestSettingsFromRenderedArgoCDInstallFoundByChartName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, root string)
	}{
		{
			name: "helm chart",
			write: func(t *testing.T, root string) {
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
  destination:
    name: in-cluster
    namespace: argocd
`)
			},
		},
		{
			name: "kustomize helmCharts",
			write: func(t *testing.T, root string) {
				writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
				writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "global:\n  domain: argocd.example.org\n")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.write(t, root)

			result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
			if got := result.Settings.InstanceLabelKey.Value; got != "argocd.argoproj.io/instance" {
				t.Fatalf("InstanceLabelKey = %q, want the chart default", got)
			}
			if got := resourceExclusionGroups(result.Settings); !slices.Equal(got, []string{fakeArgoCDChartLease}) {
				t.Fatalf("resource exclusion groups = %v, want the chart default", got)
			}
		})
	}
}

// The settings that the install renders can change how the install renders.
// Here the chart default kustomize.buildOptions passes an API version to
// Helm, and the chart sets the tracking method only when Helm has that API
// version. drydock renders the install again until its settings stop
// changing how it renders.
func TestSettingsFromRenderedArgoCDInstallSettlesOverRounds(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    timeout.reconciliation: 300s
`)
	acquirer := fakeArgoCDChartAcquirer(t)
	writeTestFile(t, filepath.Join(acquirer.chartDir, "values.yaml"), strings.Replace(fakeArgoCDChartValues,
		"    timeout.reconciliation: 120s\n",
		"    timeout.reconciliation: 120s\n    kustomize.buildOptions: --helm-api-versions example.com/v1/Probe\n", 1))
	writeTestFile(t, filepath.Join(acquirer.chartDir, "templates", "_helpers.tpl"), strings.Replace(fakeArgoCDChartHelpers,
		`{{- $_ := set $presets "url" (printf "https://%s" .Values.global.domain) -}}`,
		`{{- $_ := set $presets "url" (printf "https://%s" .Values.global.domain) -}}
{{- if .Capabilities.APIVersions.Has "example.com/v1/Probe" -}}
{{- $_ := set $presets "application.resourceTrackingMethod" "annotation+label" -}}
{{- end -}}`, 1))

	// The first render has no API version, so only a second render, with the
	// rendered kustomize.buildOptions, sets the tracking method.
	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, acquirer)
	if got := result.Settings.TrackingMethod.Value; got != "annotation+label" {
		t.Fatalf("TrackingMethod = %q, want the value of the second render", got)
	}
}

// Two installs on this cluster put argocd-cm in the same namespace. drydock
// keeps one, reports the other as a duplicate, and keeps the values files of
// both out of the settings sources: the losing install's values files do not
// come back.
func TestSettingsFromTwoRenderedArgoCDInstallsInOneNamespace(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    application.instanceLabelKey: example.com/a
`)
	writeTestFile(t, filepath.Join(root, "apps", "argocd-b.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd-b
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: `+fakeArgoCDChartRepo+`
      chart: argo-cd
      targetRevision: `+fakeArgoCDChartVersion+`
      helm:
        valueFiles:
          - $values/argocd-b/values.yaml
    - repoURL: `+argoCDValuesRepoURL+`
      targetRevision: main
      ref: values
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "argocd-b", "values.yaml"), `configs:
  cm:
    application.instanceLabelKey: example.com/b
`)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{repoMaps: func(root string) []sourcepkg.RepoMap {
		return []sourcepkg.RepoMap{{URL: argoCDValuesRepoURL, Path: root}}
	}}, fakeArgoCDChartAcquirer(t))
	for _, diag := range result.Diagnostics {
		if diag.Category == "settings" {
			t.Fatalf("settings diagnostic = %#v, want no conflict", diag)
		}
	}
	duplicate := false
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "duplicate ConfigMap argocd/argocd-cm from rendered/argocd/") {
			duplicate = true
		}
	}
	if !duplicate {
		t.Fatalf("diagnostics = %#v, want the second argocd-cm reported as a duplicate", result.Diagnostics)
	}
	if got := filepath.ToSlash(result.Settings.InstanceLabelKey.Provenance.Path); !strings.HasPrefix(got, "rendered/") {
		t.Fatalf("InstanceLabelKey %q from %q, want a rendered argocd-cm", result.Settings.InstanceLabelKey.Value, got)
	}
}

// Scenario A of the review: a hub Application installs Argo CD on a spoke
// cluster. That Argo CD runs other Applications, so its argocd-cm is not a
// setting here: drydock does not render it, and the committed argocd-cm has
// no duplicate.
func TestSettingsIgnoreArgoCDInstallOnAnotherCluster(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), argocdCMYAML("  application.resourceTrackingMethod: label\n"))
	writeTestFile(t, filepath.Join(root, "apps", "spoke-argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: spoke-argocd
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+fakeArgoCDChartRepo+`
    chart: argo-cd
    targetRevision: `+fakeArgoCDChartVersion+`
  destination:
    server: https://spoke.example.com
    namespace: argocd
`)

	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.Build(context.Background(), BuildRequest{
		Path:        root,
		Parallelism: 1,
		Strict:      true,
	})
	if err != nil {
		t.Fatalf("Build(--strict) error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if got := result.Settings.TrackingMethod.Value; got != "label" {
		t.Fatalf("TrackingMethod = %q, want label from the committed argocd-cm", got)
	}
	if got := result.Settings.InstanceLabelKey.Value; got == "argocd.argoproj.io/instance" {
		t.Fatalf("InstanceLabelKey = %q, want no chart default from the spoke install", got)
	}
}

// Scenario B of the review: the hub install on this cluster sets the
// instance label key, and a second install on a spoke cluster uses the chart
// default. The spoke install is not an install of this Argo CD, so it does
// not conflict, and its values file keeps the behavior of a values file that
// no rendered install reads.
func TestSettingsFromHubInstallWithSpokeInstall(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    application.instanceLabelKey: app.kubernetes.io/instance
`)
	writeTestFile(t, filepath.Join(root, "apps", "spoke-argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: spoke-argocd
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: `+fakeArgoCDChartRepo+`
      chart: argo-cd
      targetRevision: `+fakeArgoCDChartVersion+`
      helm:
        valueFiles:
          - $values/spoke/values.yaml
    - repoURL: `+argoCDValuesRepoURL+`
      targetRevision: main
      ref: values
  destination:
    server: https://spoke.example.com
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "spoke", "values.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: label
`)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{repoMaps: func(root string) []sourcepkg.RepoMap {
		return []sourcepkg.RepoMap{{URL: argoCDValuesRepoURL, Path: root}}
	}}, fakeArgoCDChartAcquirer(t))
	if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("settings diagnostics = %#v, want none", diags)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "duplicate") {
			t.Fatalf("diagnostic = %#v, want no duplicate", diag)
		}
	}
	if got := filepath.ToSlash(result.Settings.InstanceLabelKey.Provenance.Path); result.Settings.InstanceLabelKey.Value != "app.kubernetes.io/instance" || !strings.HasPrefix(got, "rendered/argocd/argocd/") {
		t.Fatalf("InstanceLabelKey = %q from %q, want the hub install", result.Settings.InstanceLabelKey.Value, got)
	}
	if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "label" || !strings.HasSuffix(got, "/spoke/values.yaml") {
		t.Fatalf("TrackingMethod = %q from %q, want the spoke values file read on its own", result.Settings.TrackingMethod.Value, got)
	}
}

// Two installs on this cluster read one values file and one does not
// render. The values file still gives way to the install that rendered, and
// drydock warns about the other.
func TestSettingsFromSharedValuesFileWhenOneInstallFails(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: label
`)
	const failingRepo = "https://charts.example.com/unreachable"
	writeTestFile(t, filepath.Join(root, "apps", "argocd-b.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd-b
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: `+failingRepo+`
      chart: argo-cd
      targetRevision: `+fakeArgoCDChartVersion+`
      helm:
        valueFiles:
          - $values/argocd/values.yaml
    - repoURL: `+argoCDValuesRepoURL+`
      targetRevision: main
      ref: values
  destination:
    name: in-cluster
    namespace: argocd-b
`)
	acquirer := perRepoChartAcquirer{
		recordingChartAcquirer: fakeArgoCDChartAcquirer(t),
		failures:               map[string]error{failingRepo: errors.New("chart is not in the cache")},
	}

	request := BuildRequest{Path: root, Parallelism: 1, RepoMaps: []sourcepkg.RepoMap{{URL: argoCDValuesRepoURL, Path: root}}}
	result, err := Orchestrator{ChartAcquirer: acquirer}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if got := result.Settings.TrackingMethod.Provenance.Path; result.Settings.TrackingMethod.Value != "label" || !strings.HasPrefix(got, "rendered/argocd/argocd/") {
		t.Fatalf("TrackingMethod = %q from %q, want label from the install that rendered", result.Settings.TrackingMethod.Value, got)
	}
	var warnings []diagnostic.Diagnostic
	for _, diag := range settingsDiagnostics(result.Diagnostics) {
		if strings.Contains(diag.Message, "cannot render Application argocd/argocd-b") {
			warnings = append(warnings, diag)
			continue
		}
		t.Fatalf("settings diagnostic = %#v, want only the warning for argocd-b", diag)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "unless another rendered install reads the file") {
		t.Fatalf("fallback diagnostics = %#v, want one warning for argocd-b", warnings)
	}
}

// drydock picks the source type as Argo CD does: an explicit directory or
// plugin source with a local chart is not a Helm install, and a directory
// with both a kustomization file and a Chart.yaml is Kustomize unless the
// source sets Helm.
func TestApplicationSettingsInstallFollowsExplicitSourceType(t *testing.T) {
	root := t.TempDir()
	writeFakeArgoCDChart(t, filepath.Join(root, "argocd", "chart"))
	writeFakeArgoCDChart(t, filepath.Join(root, "argocd", "both"))
	writeTestFile(t, filepath.Join(root, "argocd", "both", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
	for _, tc := range []struct {
		name   string
		path   string
		source string
		want   bool
	}{
		{name: "detected Helm", path: "argocd/chart", want: true},
		{name: "explicit directory", path: "argocd/chart", source: "    directory:\n      recurse: false\n", want: false},
		{name: "explicit plugin", path: "argocd/chart", source: "    plugin:\n      name: example\n", want: false},
		{name: "kustomization and chart detected as Kustomize", path: "argocd/both", want: false},
		{name: "kustomization and chart with explicit Helm", path: "argocd/both", source: "    helm:\n      releaseName: argocd\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			application := decodeTestApplication(t, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: `+tc.path+`
`+tc.source+`  destination:
    name: in-cluster
    namespace: argocd
`)
			install := applicationSettingsInstall(context.Background(), BuildRequest{}, settingsInstallProbe(root, BuildRequest{}, ""), application)
			if got := install.installsArgoCD(); got != tc.want {
				t.Fatalf("installsArgoCD() = %t, want %t (install %#v)", got, tc.want, install)
			}
		})
	}
}

func decodeTestApplication(t *testing.T, data string) argoappv1.Application {
	t.Helper()
	var application argoappv1.Application
	if err := sigsyaml.Unmarshal([]byte(data), &application); err != nil {
		t.Fatalf("decode Application: %v", err)
	}
	return application
}

// localChartIsArgoCD finds the argo-cd chart by its name or as a dependency
// of an umbrella chart.
func TestLocalChartIsArgoCD(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name  string
		chart string
		want  bool
	}{
		{name: "argo-cd chart", chart: "apiVersion: v2\nname: argo-cd\nversion: 1.0.0\n", want: true},
		{name: "umbrella dependency", chart: "apiVersion: v2\nname: platform\nversion: 1.0.0\ndependencies:\n  - name: argo-cd\n    version: 10.14.0\n    repository: https://argoproj.github.io/argo-helm\n", want: true},
		{name: "other chart", chart: "apiVersion: v2\nname: platform\nversion: 1.0.0\ndependencies:\n  - name: redis\n    version: 1.0.0\n", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, strings.ReplaceAll(tc.name, " ", "-"))
			writeTestFile(t, filepath.Join(dir, "Chart.yaml"), tc.chart)
			if got := localChartIsArgoCD(dir); got != tc.want {
				t.Fatalf("localChartIsArgoCD() = %t, want %t", got, tc.want)
			}
		})
	}
}

// An actions customization still gives a metadata-only warning that
// --strict fails on.
func TestBuildStrictStillFailsOnActionsCustomization(t *testing.T) {
	root := t.TempDir()
	writeDiffApplication(t, root, "other", "other", "same")
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), argocdCMYAML(`  resource.customizations.actions.apps_Deployment: |
    definitions:
      - name: restart
        action.lua: |
          return obj
`))

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
	if err == nil || !strings.Contains(err.Error(), "actions are parsed as metadata only") {
		t.Fatalf("Build(--strict) error = %v, want the actions metadata-only warning to fail", err)
	}
}

// Files that the render reads through the same resolution as the renderer
// give way to the rendered argocd-cm: a glob value file, a value file path
// with $ARGOCD_APP_NAME, and a value file that an .argocd-source.yaml
// override adds. In each case two files set the tracking method, the later
// one wins without a conflict, and the file that only the resolution finds
// is the earlier one.
func TestSettingsFromRenderedArgoCDInstallResolvesValueFilesLikeTheRenderer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, root string)
		want  string
	}{
		{
			name: "glob",
			write: func(t *testing.T, root string) {
				writeHelmRefArgoCDInstall(t, root, []string{"values-*.yaml"})
				writeTestFile(t, filepath.Join(root, "argocd", "values-b.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation+label\n")
			},
			want: "annotation+label",
		},
		{
			name: "env substitution",
			write: func(t *testing.T, root string) {
				writeHelmRefArgoCDInstall(t, root, []string{"values-$ARGOCD_APP_NAME.yaml", "values-a.yaml"})
				writeTestFile(t, filepath.Join(root, "argocd", "values-argocd.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation\n")
			},
			want: "label",
		},
		{
			// A local source renders with the HEAD commit as
			// ARGOCD_APP_REVISION, so the install reads the file that the
			// short SHA names, also in a dirty worktree.
			name: "revision env substitution",
			write: func(t *testing.T, root string) {
				writeLocalChartArgoCDInstall(t, root, []string{"values-$ARGOCD_APP_REVISION_SHORT.yaml", "values-a.yaml"})
				head := gitCommitAll(t, root, "install")
				writeTestFile(t, filepath.Join(root, "argocd", "values-"+head[:7]+".yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation\n")
			},
			want: "label",
		},
		{
			// A multi-source Application pinned to an abbreviated SHA
			// renders with the abbreviation as ARGOCD_APP_REVISION, so
			// the install reads the file that the abbreviation names.
			name: "multi-source abbreviated SHA env substitution",
			write: func(t *testing.T, root string) {
				writeFakeArgoCDChart(t, filepath.Join(root, "argocd", "chart"))
				head := gitCommitAll(t, root, "chart")
				writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: "`+head[:9]+`"
      path: argocd/chart
      helm:
        releaseName: argocd
        valueFiles:
          - ../values-$ARGOCD_APP_REVISION.yaml
          - ../values-a.yaml
  destination:
    name: in-cluster
    namespace: argocd
`)
				writeDiffApplication(t, root, "other", "other", "same")
				writeTestFile(t, filepath.Join(root, "argocd", "values-"+head[:9]+".yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation\n")
			},
			want: "label",
		},
		{
			name: "argocd-source override",
			write: func(t *testing.T, root string) {
				writeLocalChartArgoCDInstall(t, root, []string{"values-a.yaml"})
				writeTestFile(t, filepath.Join(root, "argocd", "chart", ".argocd-source.yaml"), "helm:\n  valueFiles:\n    - ../values-b.yaml\n    - ../values-a.yaml\n")
				writeTestFile(t, filepath.Join(root, "argocd", "values-b.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation+label\n")
			},
			want: "label",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.write(t, root)
			writeTestFile(t, filepath.Join(root, "argocd", "values-a.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: label\n")

			result := listArgoCDInstallSettings(t, root, argoCDInstallShape{repoMaps: func(root string) []sourcepkg.RepoMap {
				return []sourcepkg.RepoMap{{URL: argoCDValuesRepoURL, Path: root}}
			}}, fakeArgoCDChartAcquirer(t))
			if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
				t.Fatalf("settings diagnostics = %#v, want none", diags)
			}
			if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != tc.want || !strings.HasPrefix(got, "rendered/") {
				t.Fatalf("TrackingMethod = %q from %q, want %q from the rendered argocd-cm", result.Settings.TrackingMethod.Value, got, tc.want)
			}
		})
	}
}

// An umbrella chart in the repository depends on the argo-cd chart, which is
// unpacked under charts/. Helm reads the values.yaml of the subchart, so the
// static discovery copy of it gives way to the rendered argocd-cm.
func TestSettingsFromRenderedUmbrellaChartReadsSubchartValues(t *testing.T) {
	root := t.TempDir()
	umbrella := filepath.Join(root, "argocd", "umbrella")
	writeTestFile(t, filepath.Join(umbrella, "Chart.yaml"), "apiVersion: v2\nname: platform\nversion: 1.0.0\ndependencies:\n  - name: argo-cd\n    version: "+fakeArgoCDChartVersion+"\n")
	writeTestFile(t, filepath.Join(umbrella, "values.yaml"), "argo-cd:\n  configs:\n    cm:\n      application.resourceTrackingMethod: label\n")
	writeFakeArgoCDChart(t, filepath.Join(umbrella, "charts", "argo-cd"))
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
    path: argocd/umbrella
  destination:
    name: in-cluster
    namespace: argocd
`)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("settings diagnostics = %#v, want none", diags)
	}
	if got := result.Settings.TrackingMethod.Value; got != "label" {
		t.Fatalf("TrackingMethod = %q, want the umbrella value", got)
	}
	if got := resourceExclusionGroups(result.Settings); !slices.Equal(got, []string{fakeArgoCDChartLease}) {
		t.Fatalf("resource exclusion groups = %v, want the chart default once, from the rendered argocd-cm only", got)
	}
}

// Files in a local chart directory that the render does not read stay
// settings sources: an extra values file, and an argocd-cm that is not a
// template.
func TestSettingsFromRenderedLocalChartKeepsUnreadChartFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, chart string)
		check func(t *testing.T, result BuildResult)
	}{
		{
			name: "extra values file",
			write: func(t *testing.T, chart string) {
				writeTestFile(t, filepath.Join(chart, "values-unused.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: label\n")
			},
			check: func(t *testing.T, result BuildResult) {
				if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "label" || !strings.HasSuffix(got, "/values-unused.yaml") {
					t.Fatalf("TrackingMethod = %q from %q, want label from the unread values file", result.Settings.TrackingMethod.Value, got)
				}
			},
		},
		{
			name: "argocd-cm that is not a template",
			write: func(t *testing.T, chart string) {
				writeTestFile(t, filepath.Join(chart, "extra", "argocd-cm.yaml"), argocdCMYAML("  application.resourceTrackingMethod: label\n"))
			},
			check: func(t *testing.T, result BuildResult) {
				if got := result.Settings.TrackingMethod.Value; got != "label" {
					t.Fatalf("TrackingMethod = %q, want label from the committed argocd-cm", got)
				}
				for _, diag := range result.Diagnostics {
					if strings.Contains(diag.Message, "duplicate ConfigMap argocd/argocd-cm from rendered/argocd/argocd/") {
						return
					}
				}
				t.Fatalf("diagnostics = %#v, want the rendered argocd-cm reported as a duplicate", result.Diagnostics)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeLocalChartArgoCDInstall(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  cm:\n    timeout.reconciliation: 300s\n")
			tc.write(t, filepath.Join(root, "argocd", "chart"))
			tc.check(t, listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t)))
		})
	}
}

// Two installs on this cluster have the same sources, and their value files
// name $ARGOCD_APP_NAME. Each install reads its own file, so the result of
// one must not stand in for the other: every values file gives way, and the
// two argocd-cm only collide.
func TestSettingsInstallKeysOnTheApplication(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"argocd", "argocd-dev"} {
		writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: `+fakeArgoCDChartRepo+`
      chart: argo-cd
      targetRevision: `+fakeArgoCDChartVersion+`
      helm:
        valueFiles:
          - $values/argocd/values-$ARGOCD_APP_NAME.yaml
          - $values/argocd/values-final.yaml
    - repoURL: `+argoCDValuesRepoURL+`
      targetRevision: main
      ref: values
  destination:
    name: in-cluster
    namespace: argocd
`)
	}
	writeTestFile(t, filepath.Join(root, "argocd", "values-argocd.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: label\n")
	writeTestFile(t, filepath.Join(root, "argocd", "values-argocd-dev.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation\n")
	writeTestFile(t, filepath.Join(root, "argocd", "values-final.yaml"), "configs:\n  cm:\n    timeout.reconciliation: 300s\n")

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{repoMaps: func(root string) []sourcepkg.RepoMap {
		return []sourcepkg.RepoMap{{URL: argoCDValuesRepoURL, Path: root}}
	}}, fakeArgoCDChartAcquirer(t))
	for _, diag := range result.Diagnostics {
		if diag.Category == "settings" {
			t.Fatalf("settings diagnostic = %#v, want no conflict", diag)
		}
	}
	if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); !strings.HasPrefix(got, "rendered/") {
		t.Fatalf("TrackingMethod %q from %q, want a rendered argocd-cm", result.Settings.TrackingMethod.Value, got)
	}
}

// A $ref source with the repository and revision of the chart source is the
// chart source's root, as in the render, even when drydock cannot tell that
// the repository is the local checkout.
func TestSettingsFromRenderedArgoCDInstallSameRepositoryRef(t *testing.T) {
	root := t.TempDir()
	writeFakeArgoCDChart(t, filepath.Join(root, "argocd", "chart"))
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: argocd/chart
      helm:
        valueFiles:
          - $values/argocd/values-prod.yaml
          - ../values-override.yaml
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "argocd", "values-prod.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: label\n")
	writeTestFile(t, filepath.Join(root, "argocd", "values-override.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation+label\n")

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if diags := settingsDiagnostics(result.Diagnostics); len(diags) != 0 {
		t.Fatalf("settings diagnostics = %#v, want none", diags)
	}
	if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "annotation+label" || !strings.HasPrefix(got, "rendered/") {
		t.Fatalf("TrackingMethod = %q from %q, want annotation+label from the rendered argocd-cm", result.Settings.TrackingMethod.Value, got)
	}
}

// A --discover-kustomize path names the Argo CD install explicitly, so its
// rendered argocd-cm replaces the values files that its render reads, also
// with no Application that installs Argo CD.
func TestSettingsFromDiscoverKustomizeArgoCDInstall(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), kustomizeArgoCDInstallYAML("", []string{"values-base.yaml", "values-prod.yaml"}))
	writeTestFile(t, filepath.Join(root, "argocd", "values-base.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: label\n")
	writeTestFile(t, filepath.Join(root, "argocd", "values-prod.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation+label\n")

	request := BuildRequest{Path: root, Parallelism: 1, DiscoverKustomizePaths: []string{"argocd"}}
	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "annotation+label" || !strings.HasPrefix(got, "argocd") {
		t.Fatalf("TrackingMethod = %q from %q, want annotation+label from the rendered argocd-cm", result.Settings.TrackingMethod.Value, got)
	}
	if got := result.Settings.InstanceLabelKey.Value; got != "argocd.argoproj.io/instance" {
		t.Fatalf("InstanceLabelKey = %q, want the chart default", got)
	}
}

// A diff of two sides that both install the default chart reports no
// ignoreResourceUpdates diagnostic on either side.
func TestDiffAppsReportsNoIgnoreResourceUpdatesDiagnostic(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	for _, side := range []string{left, right} {
		writeKustomizeArgoCDInstall(t, side, []string{"values.yaml"})
		writeTestFile(t, filepath.Join(side, "argocd", "values.yaml"), "configs:\n  cm:\n    kustomize.buildOptions: --enable-helm\n    timeout.reconciliation: 300s\n")
	}
	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.DiffApps(context.Background(), DiffRequest{
		LeftPath:    left,
		RightPath:   right,
		Unified:     3,
		Parallelism: 1,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "settings" || strings.Contains(diag.Message, "ignoreResourceUpdates") {
			t.Fatalf("diagnostic = %#v, want no settings diagnostic for the chart defaults", diag)
		}
	}
}

// An install counts only when its destination resolves to the in-cluster
// server, as project validation resolves it.
func TestSettingsInstallInCluster(t *testing.T) {
	inCluster := argoappv1.KubernetesInternalAPIServerAddr
	withLocalSecret := config.DefaultSettings()
	withLocalSecret.Clusters = map[string]config.ClusterSettings{inCluster: {Name: "local", Server: inCluster}}
	disabled := config.DefaultSettings()
	disabled.InClusterEnabled = config.Value[bool]{Value: false, Provenance: config.Provenance{Path: "argocd-cm.yaml"}}
	for _, tc := range []struct {
		name        string
		destination argoappv1.ApplicationDestination
		settings    config.ArgoSettings
		want        bool
	}{
		{name: "in-cluster server", destination: argoappv1.ApplicationDestination{Server: inCluster}, settings: config.DefaultSettings(), want: true},
		{name: "in-cluster name", destination: argoappv1.ApplicationDestination{Name: "in-cluster"}, settings: config.DefaultSettings(), want: true},
		{name: "cluster Secret name for in-cluster", destination: argoappv1.ApplicationDestination{Name: "local"}, settings: withLocalSecret, want: true},
		{name: "in-cluster name while disabled", destination: argoappv1.ApplicationDestination{Name: "in-cluster"}, settings: disabled, want: false},
		{name: "unknown name", destination: argoappv1.ApplicationDestination{Name: "unknown"}, settings: config.DefaultSettings(), want: false},
		{name: "remote server", destination: argoappv1.ApplicationDestination{Server: "https://spoke.example.com"}, settings: config.DefaultSettings(), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{Destination: tc.destination}}
			if got := settingsInstallInCluster(application, tc.settings); got != tc.want {
				t.Fatalf("settingsInstallInCluster() = %t, want %t", got, tc.want)
			}
		})
	}
}

// With cluster.inClusterEnabled "false", Argo CD does not deploy to the
// in-cluster destination, so the install there does not count, and its
// values file is read on its own.
func TestSettingsFromInstallWhileInClusterDisabledFallsBack(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: label\n")
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: settings
data:
  cluster.inClusterEnabled: "false"
`)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "label" || !strings.HasSuffix(got, "/argocd/values.yaml") {
		t.Fatalf("TrackingMethod = %q from %q, want label from the values file read on its own", result.Settings.TrackingMethod.Value, got)
	}
}

// A $ref with the repository and revision of a repo-mapped source reads the
// mapped copy, as the render anchors it at that source's root. So the local
// file at the same path is not an input of the install and stays a settings
// source.
func TestSettingsInstallRefOfMappedSourceIsNotLocal(t *testing.T) {
	root := t.TempDir()
	mapped := t.TempDir()
	const mappedURL = "https://github.com/example/platform"
	writeFakeArgoCDChart(t, filepath.Join(mapped, "argocd", "chart"))
	writeTestFile(t, filepath.Join(mapped, "argocd", "values.yaml"), "configs:\n  cm:\n    timeout.reconciliation: 300s\n")
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: label\n")
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: `+mappedURL+`
      targetRevision: main
      path: argocd/chart
      helm:
        valueFiles:
          - $v/argocd/values.yaml
    - repoURL: `+mappedURL+`
      targetRevision: main
      ref: v
  destination:
    name: in-cluster
    namespace: argocd
`)

	request := BuildRequest{Path: root, Parallelism: 1, RepoMaps: []sourcepkg.RepoMap{{URL: mappedURL, Path: mapped}}}
	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.ListApplications(context.Background(), request)
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "label" || !strings.HasSuffix(got, root+"/argocd/values.yaml") {
		t.Fatalf("TrackingMethod = %q from %q, want label from the local values file", result.Settings.TrackingMethod.Value, got)
	}
}

// A failed install render gives a settings.install-render-failed warning,
// once for each side, and --strict fails on it.
func TestSettingsInstallRenderFailedDiagnostic(t *testing.T) {
	failing := func() *recordingChartAcquirer {
		return &recordingChartAcquirer{acquireErr: errors.New("chart is not in the cache")}
	}
	countCode := func(diags []diagnostic.Diagnostic) int {
		count := 0
		for _, diag := range diags {
			if diag.Code == diagnostic.CodeSettingsInstallRenderFailed {
				count++
			}
		}
		return count
	}
	write := func(t *testing.T, root string) {
		writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
		writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  cm:\n    timeout.reconciliation: 300s\n")
	}

	t.Run("list", func(t *testing.T) {
		root := t.TempDir()
		write(t, root)
		result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, failing())
		if got := countCode(result.Diagnostics); got != 1 {
			t.Fatalf("install-render-failed diagnostics = %d, want 1: %#v", got, result.Diagnostics)
		}
	})
	t.Run("list strict", func(t *testing.T) {
		root := t.TempDir()
		write(t, root)
		_, err := Orchestrator{ChartAcquirer: failing()}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
		if err == nil || !strings.Contains(err.Error(), "cannot render Application argocd/argocd") {
			t.Fatalf("ListApplications(--strict) error = %v, want the install render warning to fail", err)
		}
	})
	t.Run("diff", func(t *testing.T) {
		root := t.TempDir()
		left := filepath.Join(root, "left")
		right := filepath.Join(root, "right")
		write(t, left)
		write(t, right)
		result, _ := Orchestrator{ChartAcquirer: failing()}.DiffApps(context.Background(), DiffRequest{LeftPath: left, RightPath: right, Unified: 3, Parallelism: 1})
		if got := countCode(result.Diagnostics); got != 2 {
			t.Fatalf("install-render-failed diagnostics = %d, want one for each side: %#v", got, result.Diagnostics)
		}
	})
}

// When the frontier renders an install for discovery, its settings
// ConfigMaps name the namespace where they land, as for a settings-only
// render. Here namespaceOverride moves them to another namespace. When that
// Argo CD would reconcile the Application in argocd that installs it, it is
// the Argo CD of the repository, and the rendered argocd-cm replaces the
// values file that the render read. Otherwise it is another install, and
// drydock reads none of its settings.
func TestSettingsFromFrontierRenderedInstallInAnotherNamespace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		want   string
	}{
		{name: "reconciles its Application", params: "  params:\n    application.namespaces: argocd\n", want: "label"},
		{name: "another install", want: "annotation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeLocalChartArgoCDInstall(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "namespaceOverride: other\nconfigs:\n  cm:\n    application.resourceTrackingMethod: label\n"+tc.params)

			result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
			if got := result.Settings.TrackingMethod.Value; got != tc.want {
				t.Fatalf("TrackingMethod = %q from %q, want %q", got, result.Settings.TrackingMethod.Provenance.Path, tc.want)
			}
			if tc.want == "label" && !strings.HasSuffix(filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path), "/argocd-cm.yaml") {
				t.Fatalf("TrackingMethod from %q, want the rendered argocd-cm", result.Settings.TrackingMethod.Provenance.Path)
			}
		})
	}
}

// Helm merges every YAML document of a values file, so detection reads them
// all.
func TestHelmValuesFileSetsArgoCDSettingsReadsEveryDocument(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "values.yaml"), "global:\n  domain: argocd.example.com\n---\nconfigs:\n  cm:\n    timeout.reconciliation: 300s\n")
	if !helmValuesFileSetsArgoCDSettings(nil, root, "values.yaml", valuesParserHelm) {
		t.Fatalf("helmValuesFileSetsArgoCDSettings() = false, want true for configs.cm in the second document")
	}
}

// kustomize reads a helmCharts valuesFile that it merges with valuesInline
// with its own parser, which reads only the first YAML document. Helm reads
// the same file, without valuesInline, with every document. Detection reads
// the file with the parser that the render uses, so the two cannot judge the
// file differently.
func TestApplicationSettingsInstallUsesTheKustomizeParser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inline string
		want   bool
	}{
		{name: "valuesFile read by Helm", want: true},
		{name: "valuesFile merged with valuesInline by kustomize", inline: "    valuesInline:\n      extra: true\n", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "platform", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nhelmCharts:\n  - name: platform\n    releaseName: platform\n    valuesFile: values.yaml\n"+tc.inline)
			writeTestFile(t, filepath.Join(root, "platform", "values.yaml"), "global:\n  domain: example.com\n---\nconfigs:\n  cm:\n    timeout.reconciliation: 300s\n")
			application := decodeTestApplication(t, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: platform
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: platform
  destination:
    name: in-cluster
    namespace: argocd
`)
			install := applicationSettingsInstall(context.Background(), BuildRequest{}, settingsInstallProbe(root, BuildRequest{}, ""), application)
			if got := install.installsArgoCD(); got != tc.want {
				t.Fatalf("installsArgoCD() = %t, want %t (install %#v)", got, tc.want, install)
			}
			if !slices.Contains(install.valuesFilesRead, "platform/values.yaml") {
				t.Fatalf("valuesFilesRead = %v, want the valuesFile in either case", install.valuesFilesRead)
			}
		})
	}
}

// When --discover-kustomize names the install path, that render is the
// install. drydock does not render the Application at the same path again
// for its settings, so no duplicate settings ConfigMap appears, --strict
// passes, and the values files stay superseded.
func TestSettingsFromDiscoverKustomizeInstallPathWithApplication(t *testing.T) {
	for _, shape := range []argoCDInstallShape{
		{name: "Application", write: writeKustomizeArgoCDInstall},
		{name: "ApplicationSet-generated Application", write: writeApplicationSetKustomizeArgoCDInstall},
	} {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values-base.yaml", "values-prod.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values-base.yaml"), "configs:\n  cm:\n    kustomize.buildOptions: --enable-helm\n    application.resourceTrackingMethod: label\n")
			writeTestFile(t, filepath.Join(root, "argocd", "values-prod.yaml"), "configs:\n  cm:\n    application.resourceTrackingMethod: annotation+label\n")

			result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.Build(context.Background(), BuildRequest{
				Path:                   root,
				Parallelism:            1,
				Strict:                 true,
				DiscoverKustomizePaths: []string{"argocd"},
			})
			if err != nil {
				t.Fatalf("Build(--strict) error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			for _, diag := range result.Diagnostics {
				if strings.Contains(diag.Message, "duplicate") {
					t.Fatalf("diagnostic = %#v, want no duplicate", diag)
				}
			}
			if got := filepath.ToSlash(result.Settings.TrackingMethod.Provenance.Path); result.Settings.TrackingMethod.Value != "annotation+label" || !strings.HasPrefix(got, "argocd") {
				t.Fatalf("TrackingMethod = %q from %q, want annotation+label from the --discover-kustomize render", result.Settings.TrackingMethod.Value, got)
			}
		})
	}
}
