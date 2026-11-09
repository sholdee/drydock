package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// The workload templates copy, from the argo-helm argo-cd chart 10.14.0,
// the parts of the repo-server Deployment, the application controller
// StatefulSet, and the ApplicationSet controller Deployment that decide the
// settings that drydock reads: the command line with extraArgs after the
// chart arguments, the env list with the chart values in the same place as
// the chart (before the argocd-cmd-params-cm references in the repo-server
// and the ApplicationSet controller, after them in the controller
// StatefulSet), the references themselves, and envFrom.
const fakeArgoCDChartWorkloadValues = `
global:
  domain: argocd.example.com
  env: []
controller:
  extraArgs: []
  env: []
  envFrom: []
repoServer:
  service:
    port: 8081
  extraArgs: []
  env: []
  envFrom: []
applicationSet:
  extraArgs: []
  extraEnv: []
  envFrom: []
`

const fakeArgoCDChartRepoServer = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-repo-server
  namespace: {{ include "argo-cd.namespace" . }}
  labels:
    app.kubernetes.io/component: repo-server
    app.kubernetes.io/part-of: argocd
spec:
  template:
    spec:
      automountServiceAccountToken: true
      containers:
      - name: repo-server
        args:
        - /usr/local/bin/argocd-repo-server
        - --port=8081
        - --metrics-port=8084
        {{- with .Values.repoServer.extraArgs }}
          {{- toYaml . | nindent 8 }}
        {{- end }}
        env:
          {{- with (concat .Values.global.env .Values.repoServer.env) }}
            {{- tpl (toYaml .) $ | nindent 10 }}
          {{- end }}
          - name: ARGOCD_REPO_SERVER_MAX_COMBINED_DIRECTORY_MANIFESTS_SIZE
            valueFrom:
              configMapKeyRef:
                name: argocd-cmd-params-cm
                key: reposerver.max.combined.directory.manifests.size
                optional: true
          - name: ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS
            valueFrom:
              configMapKeyRef:
                key: reposerver.allow.oob.symlinks
                name: argocd-cmd-params-cm
                optional: true
          - name: ARGOCD_GIT_MODULES_ENABLED
            valueFrom:
              configMapKeyRef:
                key: reposerver.enable.git.submodule
                name: argocd-cmd-params-cm
                optional: true
          - name: ARGOCD_REPO_SERVER_INCLUDE_HIDDEN_DIRECTORIES
            valueFrom:
              configMapKeyRef:
                key: reposerver.include.hidden.directories
                name: argocd-cmd-params-cm
                optional: true
        {{- with .Values.repoServer.envFrom }}
        envFrom:
          {{- toYaml . | nindent 10 }}
        {{- end }}
`

const fakeArgoCDChartController = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ .Release.Name }}-application-controller
  namespace: {{ include "argo-cd.namespace" . }}
  labels:
    app.kubernetes.io/component: application-controller
    app.kubernetes.io/part-of: argocd
spec:
  template:
    spec:
      containers:
      - args:
        - /usr/local/bin/argocd-application-controller
        - --metrics-port=8082
        {{- with .Values.controller.extraArgs }}
          {{- toYaml . | nindent 8 }}
        {{- end }}
        name: application-controller
        env:
          - name: ARGOCD_APPLICATION_NAMESPACES
            valueFrom:
              configMapKeyRef:
                name: argocd-cmd-params-cm
                key: application.namespaces
                optional: true
          - name: ARGOCD_IGNORE_NORMALIZER_JQ_TIMEOUT
            valueFrom:
              configMapKeyRef:
                name: argocd-cmd-params-cm
                key: controller.ignore.normalizer.jq.timeout
                optional: true
          {{- with (concat .Values.global.env .Values.controller.env) }}
            {{- tpl (toYaml .) $ | nindent 10 }}
          {{- end }}
        {{- with .Values.controller.envFrom }}
        envFrom:
          {{- toYaml . | nindent 10 }}
        {{- end }}
`

const fakeArgoCDChartApplicationSet = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-applicationset-controller
  namespace: {{ include "argo-cd.namespace" . }}
  labels:
    app.kubernetes.io/component: applicationset-controller
    app.kubernetes.io/part-of: argocd
spec:
  template:
    spec:
      containers:
        - name: applicationset-controller
          args:
            - /usr/local/bin/argocd-applicationset-controller
            - --metrics-addr=:8080
            - --probe-addr=:8081
            - --webhook-addr=:7000
            {{- with .Values.applicationSet.extraArgs }}
              {{- toYaml . | nindent 12 }}
            {{- end }}
          env:
            {{- with (concat .Values.global.env .Values.applicationSet.extraEnv) }}
              {{- tpl (toYaml .) $ | nindent 12 }}
            {{- end }}
            - name: ARGOCD_GIT_MODULES_ENABLED
              valueFrom:
                configMapKeyRef:
                  key: applicationsetcontroller.enable.git.submodule
                  name: argocd-cmd-params-cm
                  optional: true
            - name: ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_PROGRESSIVE_SYNCS
              valueFrom:
                configMapKeyRef:
                  key: applicationsetcontroller.enable.progressive.syncs
                  name: argocd-cmd-params-cm
                  optional: true
            - name: ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_NEW_GIT_FILE_GLOBBING
              valueFrom:
                configMapKeyRef:
                  key: applicationsetcontroller.enable.new.git.file.globbing
                  name: argocd-cmd-params-cm
                  optional: true
            - name: ARGOCD_APPLICATIONSET_CONTROLLER_NAMESPACES
              valueFrom:
                configMapKeyRef:
                  key: applicationsetcontroller.namespaces
                  name: argocd-cmd-params-cm
                  optional: true
          {{- with .Values.applicationSet.envFrom }}
          envFrom:
            {{- toYaml . | nindent 12 }}
          {{- end }}
`

// fakeArgoCDChartWithWorkloadsAcquirer serves the fake argo-cd chart with
// the three workload templates.
func fakeArgoCDChartWithWorkloadsAcquirer(t *testing.T) *recordingChartAcquirer {
	t.Helper()
	acquirer := fakeArgoCDChartAcquirer(t)
	values := strings.Replace(fakeArgoCDChartValues, "global:\n  domain: argocd.example.com\nrepoServer:\n  service:\n    port: 8081\n", fakeArgoCDChartWorkloadValues, 1)
	writeTestFile(t, filepath.Join(acquirer.chartDir, "values.yaml"), values)
	writeTestFile(t, filepath.Join(acquirer.chartDir, "templates", "argocd-repo-server", "deployment.yaml"), fakeArgoCDChartRepoServer)
	writeTestFile(t, filepath.Join(acquirer.chartDir, "templates", "argocd-application-controller", "statefulset.yaml"), fakeArgoCDChartController)
	writeTestFile(t, filepath.Join(acquirer.chartDir, "templates", "argocd-applicationset", "deployment.yaml"), fakeArgoCDChartApplicationSet)
	return acquirer
}

func componentSettingsResult(t *testing.T, values string, extra func(root string)) BuildResult {
	t.Helper()
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), values)
	if extra != nil {
		extra(root)
	}
	return listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartWithWorkloadsAcquirer(t))
}

func workloadWarnings(diags []diagnostic.Diagnostic, text string) []diagnostic.Diagnostic {
	var out []diagnostic.Diagnostic
	for _, diag := range diags {
		if diag.Severity == diagnostic.SeverityWarning && strings.Contains(diag.Message, text) {
			out = append(out, diag)
		}
	}
	return out
}

// A flag of the program wins over the environment variable, which is only
// the default value of the flag. extraArgs reach the command line of each
// workload.
func TestComponentSettingsFromRenderedInstallFlags(t *testing.T) {
	result := componentSettingsResult(t, `configs:
  params:
    reposerver.allow.oob.symlinks: "false"
    application.namespaces: team-a
repoServer:
  extraArgs:
    - --allow-oob-symlinks
    - --max-combined-directory-manifests-size
    - 20M
controller:
  extraArgs:
    - --application-namespaces=team-b,team-c
    - --application-namespaces
    - team-d
applicationSet:
  extraArgs:
    - --enable-leader-election=true
    - --enable-progressive-syncs=1
`, nil)
	settings := result.Settings
	for _, tc := range []struct{ key, got, want string }{
		{"reposerver.allow.oob.symlinks", settings.AllowOutOfBoundsSymlinks.Value, "true"},
		{"reposerver.max.combined.directory.manifests.size", settings.MaxCombinedDirectoryManifestsSize.Value, "20M"},
		{"application.namespaces", settings.ApplicationNamespaces.Value, "team-b,team-c,team-d"},
		{"applicationsetcontroller.enable.progressive.syncs", settings.EnableProgressiveSyncs.Value, "true"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q from the flag", tc.key, tc.got, tc.want)
		}
	}
	if got := settings.AllowOutOfBoundsSymlinks.Provenance.Pointer; !strings.Contains(got, "--allow-oob-symlinks") {
		t.Errorf("AllowOutOfBoundsSymlinks pointer = %q, want the flag", got)
	}
	if diags := workloadWarnings(result.Diagnostics, "Argo CD argocd-"); len(diags) != 0 {
		t.Fatalf("workload warnings = %#v, want none", diags)
	}
}

// The kubelet keeps the last env entry with a name, and an optional
// configMapKeyRef whose key is missing sets nothing. The repo-server and
// ApplicationSet controller templates put repoServer.env and
// applicationSet.extraEnv before the argocd-cmd-params-cm references, so a
// literal there wins only when argocd-cmd-params-cm does not have the key.
// The controller StatefulSet puts controller.env after them, so it wins.
func TestComponentSettingsFromRenderedInstallEnvOrder(t *testing.T) {
	result := componentSettingsResult(t, `configs:
  params:
    reposerver.include.hidden.directories: "false"
    application.namespaces: team-a
repoServer:
  env:
    - name: ARGOCD_REPO_SERVER_INCLUDE_HIDDEN_DIRECTORIES
      value: "true"
    - name: ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS
      value: "true"
controller:
  env:
    - name: ARGOCD_APPLICATION_NAMESPACES
      value: team-z
applicationSet:
  extraEnv:
    - name: ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_NEW_GIT_FILE_GLOBBING
      value: "true"
`, nil)
	settings := result.Settings
	for _, tc := range []struct{ key, got, want, why string }{
		{"reposerver.include.hidden.directories", settings.IncludeHiddenDirectories.Value, "false", "argocd-cmd-params-cm has the key and its reference comes later"},
		{"reposerver.allow.oob.symlinks", settings.AllowOutOfBoundsSymlinks.Value, "true", "argocd-cmd-params-cm does not have the key"},
		{"application.namespaces", settings.ApplicationNamespaces.Value, "team-z", "controller.env comes after the reference"},
		{"applicationsetcontroller.enable.new.git.file.globbing", settings.EnableNewGitFileGlobbing.Value, "true", "argocd-cmd-params-cm does not have the key"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q: %s", tc.key, tc.got, tc.want, tc.why)
		}
	}
	if got := settings.IncludeHiddenDirectories.Provenance.Path; !strings.Contains(got, "argocd-cmd-params-cm") && !strings.HasPrefix(got, "rendered/") {
		t.Errorf("IncludeHiddenDirectories provenance = %q, want the rendered argocd-cmd-params-cm", got)
	}
	if diags := workloadWarnings(result.Diagnostics, "Argo CD argocd-"); len(diags) != 0 {
		t.Fatalf("workload warnings = %#v, want none", diags)
	}
}

// A configMapKeyRef to a ConfigMap of the same render reads its key. A
// secretKeyRef is never read: drydock warns and reads the variable without
// it. So does a reference to a ConfigMap that drydock does not find, and
// envFrom.
func TestComponentSettingsFromRenderedInstallValueFrom(t *testing.T) {
	result := componentSettingsResult(t, `repoServer:
  env:
    - name: ARGOCD_REPO_SERVER_MAX_COMBINED_DIRECTORY_MANIFESTS_SIZE
      valueFrom:
        configMapKeyRef:
          name: repo-limits
          key: size
    - name: ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS
      valueFrom:
        secretKeyRef:
          name: repo-secret
          key: symlinks
    - name: ARGOCD_REPO_SERVER_INCLUDE_HIDDEN_DIRECTORIES
      valueFrom:
        configMapKeyRef:
          name: elsewhere
          key: hidden
applicationSet:
  envFrom:
    - configMapRef:
        name: appset-env
`, func(root string) {
		writeTestFile(t, filepath.Join(root, "argocd", "limits.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: repo-limits\ndata:\n  size: 30M\n")
		writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), kustomizeArgoCDInstallYAML("resources:\n  - limits.yaml\n", []string{"values.yaml"}))
	})
	settings := result.Settings
	if got := settings.MaxCombinedDirectoryManifestsSize.Value; got != "30M" {
		t.Errorf("MaxCombinedDirectoryManifestsSize = %q, want 30M from the ConfigMap in the render", got)
	}
	if got := settings.AllowOutOfBoundsSymlinks.Value; got != "" {
		t.Errorf("AllowOutOfBoundsSymlinks = %q, want the default without the Secret", got)
	}
	if got := workloadWarnings(result.Diagnostics, "ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS comes from valueFrom secretKeyRef"); len(got) != 1 {
		t.Errorf("secretKeyRef warnings = %#v, want one", got)
	}
	if got := workloadWarnings(result.Diagnostics, "comes from ConfigMap elsewhere"); len(got) != 1 {
		t.Errorf("unknown ConfigMap warnings = %#v, want one", got)
	}
	if got := workloadWarnings(result.Diagnostics, "can come from envFrom"); len(got) != 4 {
		t.Errorf("envFrom warnings = %#v, want one for each ApplicationSet controller setting", got)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "symlinks") && strings.Contains(diag.Message, "repo-secret") {
			t.Errorf("diagnostic %q names the Secret, want only the kind of the source", diag.Message)
		}
	}
}

// The home-ops shape: an argo-cd helmCharts install in apps/argocd whose
// ApplicationSet controller has an extraArgs flag that sets no setting
// that drydock reads, and configs.params values. drydock reads the
// configs.params values as before, with no warning.
func TestComponentSettingsKeepHomeOpsShape(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "kustomization.yaml"), kustomizeArgoCDInstallYAML("resources:\n  - manifests/app-set.yaml\n", []string{"manifests/values.yaml"}))
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "manifests", "app-set.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
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
        targetRevision: master
        path: 'apps/{{.name}}'
      destination:
        name: in-cluster
        namespace: '{{.name}}'
`)
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "manifests", "values.yaml"), `applicationSet:
  replicas: 2
  extraArgs:
    - --enable-leader-election=true
configs:
  params:
    reposerver.include.hidden.directories: "true"
    applicationsetcontroller.enable.progressive.syncs: "true"
`)
	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartWithWorkloadsAcquirer(t))
	if got := result.Settings.IncludeHiddenDirectories; got.Value != "true" || !strings.HasPrefix(got.Provenance.Path, "rendered/argocd/") {
		t.Errorf("IncludeHiddenDirectories = %q from %q, want true from the rendered argocd-cmd-params-cm", got.Value, got.Provenance.Path)
	}
	if got := result.Settings.EnableProgressiveSyncs.Value; got != "true" {
		t.Errorf("EnableProgressiveSyncs = %q, want true", got)
	}
	if diags := workloadWarnings(result.Diagnostics, "Argo CD argocd-"); len(diags) != 0 {
		t.Fatalf("workload warnings = %#v, want none", diags)
	}
}

// A workload that does not pass an argocd-cmd-params-cm key to its program
// makes Argo CD ignore the key. drydock uses the default and says so. A
// committed repo-server counts with the rules of the Helm namespace: here
// it is in the file of the argocd-cmd-params-cm.
func TestComponentSettingsWarnWhenWorkloadDropsAKey(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "argocd-cmd-params-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  reposerver.include.hidden.directories: "true"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-repo-server
  namespace: argocd
spec:
  template:
    spec:
      containers:
        - name: argocd-repo-server
          args:
            - /usr/local/bin/argocd-repo-server
`)
	writeDiffApplication(t, root, "other", "other", "same")
	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if got := result.Settings.IncludeHiddenDirectories.Value; got != "" {
		t.Errorf("IncludeHiddenDirectories = %q, want the default", got)
	}
	if got := workloadWarnings(result.Diagnostics, "does not pass it to the program as ARGOCD_REPO_SERVER_INCLUDE_HIDDEN_DIRECTORIES"); len(got) != 1 {
		t.Fatalf("warnings = %#v, want one", got)
	}
}
