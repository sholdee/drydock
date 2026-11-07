package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/argoproj/argo-cd/v3/util/app/discovery"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

const reviewRepoURL = "https://github.com/example/repo"

// buildConfigMapData builds root offline and returns the data of the
// ConfigMap name that Application app renders, and the build error.
func buildConfigMapData(t *testing.T, root, app, name string) (map[string]any, error) {
	t.Helper()
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:     root,
		RepoMaps: []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
		Offline:  true,
	})
	for _, item := range result.ApplicationManifests {
		if item.Application.Name == app && item.Manifest.Object.GetName() == name {
			data, _ := item.Manifest.Object.Object["data"].(map[string]any)
			return data, err
		}
	}
	return nil, err
}

func writeReviewApplication(t *testing.T, root, name, path, block, namespace string) {
	t.Helper()
	destination := "    name: in-cluster\n"
	if namespace != "" {
		destination += "    namespace: " + namespace + "\n"
	}
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+reviewRepoURL+`
    targetRevision: HEAD
    path: `+path+`
`+block+`  destination:
`+destination)
}

func writeReviewArgoCDConfigMap(t *testing.T, root, data string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\n  namespace: argocd\ndata:\n"+data)
}

// A helm or kustomize block that does not parse fails only the tool that
// reads it. With the tool off in argocd-cm, Argo CD renders the source as a
// plain directory and never reads the block (repository.go:1806-1811 at
// v3.6.1).
func TestGatedSourceIgnoresItsToolBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block string
		key   string
		want  string
	}{
		{name: "helm values not a map", block: "    helm:\n      values: \"- not a map\"\n", key: "helm.enable", want: "helm values must be a YAML mapping"},
		{name: "helm kubeVersion", block: "    helm:\n      kubeVersion: not-a-version\n", key: "helm.enable", want: "not-a-version"},
		{name: "kustomize kubeVersion", block: "    kustomize:\n      kubeVersion: not-a-version\n", key: "kustomize.enable", want: "not-a-version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "work", "configmap.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\ndata:\n  a: b\n")
			writeReviewApplication(t, root, "gated", "work", tc.block, "demo")
			if _, err := buildConfigMapData(t, root, "gated", "plain"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Build() with the tool on error = %v, want %q", err, tc.want)
			}
			writeReviewArgoCDConfigMap(t, root, "  "+tc.key+": \"false\"\n")
			data, err := buildConfigMapData(t, root, "gated", "plain")
			if err != nil {
				t.Fatalf("Build() with the tool off error = %v", err)
			}
			if data["a"] != "b" {
				t.Fatalf("data = %v, want the plain directory ConfigMap", data)
			}
		})
	}
}

// The chart kubeVersion constraint is checked only against an explicit kube
// version. Argo CD checks the destination cluster version, which drydock
// does not know without one. A per-app kubeVersion wins over
// --kube-version, as GetKubeVersionOrDefault picks it.
func TestHelmChartKubeVersionCheckNeedsExplicitVersion(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: chart\nversion: 0.1.0\nkubeVersion: '<1.20.0-0'\n")
	writeTestFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: kube\ndata:\n  kube: {{ .Capabilities.KubeVersion.Version | quote }}\n")
	writeReviewApplication(t, root, "kube", "chart", "", "demo")
	if _, err := buildConfigMapData(t, root, "kube", "kube"); err != nil {
		t.Fatalf("Build() without an explicit kube version error = %v, want the check skipped", err)
	}
	build := func(capabilities CapabilityOptions) error {
		_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
			Path:              root,
			RepoMaps:          []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
			Offline:           true,
			CapabilityOptions: capabilities,
		})
		return err
	}
	if err := build(CapabilityOptions{KubeVersion: "1.30.0"}); err == nil || !strings.Contains(err.Error(), "chart requires kubeVersion: <1.20.0-0 which is incompatible with Kubernetes v1.30.0") {
		t.Fatalf("Build() with --kube-version error = %v, want the kubeVersion error", err)
	}
	writeReviewApplication(t, root, "kube", "chart", "    helm:\n      kubeVersion: 1.19.0\n", "demo")
	if err := build(CapabilityOptions{KubeVersion: "1.30.0"}); err != nil {
		t.Fatalf("Build() with a per-app kubeVersion 1.19.0 and --kube-version 1.30.0 error = %v, want the per-app version to win", err)
	}
}

const repoServerInstallChart = `apiVersion: v2
name: argo-cd
version: 10.14.0
`

const repoServerInstallTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  application.instanceLabelKey: argocd.argoproj.io/instance
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: argocd-repo-server
  namespace: argocd
{{- if ne .Values.serviceAccountAutomount "" }}
automountServiceAccountToken: {{ .Values.serviceAccountAutomount }}
{{- end }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-repo-server
  namespace: argocd
  labels:
    app.kubernetes.io/component: repo-server
    app.kubernetes.io/part-of: argocd
spec:
  selector:
    matchLabels:
      app: repo-server
  template:
    metadata:
      labels:
        app: repo-server
    spec:
      serviceAccountName: argocd-repo-server
{{- if ne .Values.podAutomount "" }}
      automountServiceAccountToken: {{ .Values.podAutomount }}
{{- end }}
      containers:
        - name: repo-server
          image: quay.io/argoproj/argocd:v3.6.1
          env:
            - name: SECRET_LOOKING
              value: never-kept
{{- if .Values.podNamespaceEnv }}
            - name: POD_NAMESPACE
              value: {{ .Values.podNamespaceEnv }}
{{- end }}
`

// The namespace that helm uses without --namespace comes from the rendered
// repo-server: with a service account token, client-go reads the in-cluster
// namespace, which is the pod namespace or POD_NAMESPACE; without one it is
// "default".
func TestHelmNamespaceFollowsRenderedRepoServer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values string
		want   string
	}{
		{name: "upstream install.yaml shape", values: "podAutomount: \"false\"\nserviceAccountAutomount: \"\"\npodNamespaceEnv: \"\"\n", want: "default"},
		{name: "argo-helm default shape", values: "podAutomount: \"true\"\nserviceAccountAutomount: \"\"\npodNamespaceEnv: \"\"\n", want: "argocd"},
		{name: "unset pod, ServiceAccount off", values: "podAutomount: \"\"\nserviceAccountAutomount: \"false\"\npodNamespaceEnv: \"\"\n", want: "default"},
		{name: "unset pod and ServiceAccount", values: "podAutomount: \"\"\nserviceAccountAutomount: \"\"\npodNamespaceEnv: \"\"\n", want: "argocd"},
		{name: "POD_NAMESPACE", values: "podAutomount: \"true\"\nserviceAccountAutomount: \"\"\npodNamespaceEnv: elsewhere\n", want: "elsewhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "argocd", "chart", "Chart.yaml"), repoServerInstallChart)
			writeTestFile(t, filepath.Join(root, "argocd", "chart", "values.yaml"), tc.values)
			writeTestFile(t, filepath.Join(root, "argocd", "chart", "templates", "install.yaml"), repoServerInstallTemplate)
			writeReviewApplication(t, root, "argocd", "argocd/chart", "    helm:\n      releaseName: argocd\n", "argocd")
			writeTestFile(t, filepath.Join(root, "probe", "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 0.1.0\n")
			writeTestFile(t, filepath.Join(root, "probe", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: probe\n  namespace: probe\ndata:\n  namespace: {{ .Release.Namespace | quote }}\n")
			writeReviewApplication(t, root, "probe", "probe", "", "")
			writeTestFile(t, filepath.Join(root, "kprobe", "kustomization.yaml"), "helmCharts:\n  - name: probe\n    releaseName: kprobe\n")
			writeTestFile(t, filepath.Join(root, "kprobe", "charts", "probe", "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 0.1.0\n")
			writeTestFile(t, filepath.Join(root, "kprobe", "charts", "probe", "values.yaml"), "{}\n")
			writeTestFile(t, filepath.Join(root, "kprobe", "charts", "probe", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: kprobe\n  namespace: probe\ndata:\n  namespace: {{ .Release.Namespace | quote }}\n")
			writeReviewApplication(t, root, "kprobe", "kprobe", "", "")

			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
				Path:     root,
				RepoMaps: []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
				Offline:  true,
			})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if got := result.Settings.HelmNamespace(); got != tc.want {
				t.Fatalf("Settings.HelmNamespace() = %q, want %q", got, tc.want)
			}
			for _, item := range result.ApplicationManifests {
				if item.Application.Name != "probe" && item.Application.Name != "kprobe" {
					continue
				}
				data, _ := item.Manifest.Object.Object["data"].(map[string]any)
				if data["namespace"] != tc.want {
					t.Errorf("%s .Release.Namespace = %v, want %q", item.Application.Name, data["namespace"], tc.want)
				}
			}
			if result.Settings.RepoServerHelmNamespace == nil {
				t.Fatal("RepoServerHelmNamespace = nil, want the rendered install")
			}
		})
	}
}

// A chart from a chart repository renders as a plain directory when
// argocd-cm turns Helm off: the repo-server extracts the chart and picks
// the source type in the chart directory (repository.go:451-470 and
// 1958-1980 at v3.6.1), where discovery skips Chart.yaml.
func TestChartOnlySourceFollowsHelmEnable(t *testing.T) {
	t.Setenv("ARGOCD_PLUGINSOCKFILEPATH", t.TempDir())
	chartDir := t.TempDir()
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 1.0.0\n")
	writeTestFile(t, filepath.Join(chartDir, "values.yaml"), "{}\n")
	writeTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: from-template\n")
	writeTestFile(t, filepath.Join(chartDir, "gate-probe.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: from-chart-root\n")
	for _, tc := range []struct {
		name, settings string
		enabled        map[string]bool
		want           string
	}{
		{name: "helm on", want: "from-template", enabled: nil},
		{name: "helm off", settings: "  helm.enable: \"false\"\n", want: "from-chart-root", enabled: map[string]bool{"Helm": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, err := discovery.AppType(context.Background(), chartDir, chartDir, tc.enabled, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (upstream == "Helm") != (tc.want == "from-template") {
				t.Fatalf("upstream AppType = %q, the test case is wrong", upstream)
			}
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "apps", "probe.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: probe
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://charts.example.test
    chart: probe
    targetRevision: 1.0.0
  destination:
    name: in-cluster
    namespace: probe
`)
			if tc.settings != "" {
				writeReviewArgoCDConfigMap(t, root, tc.settings)
			}
			result, err := Orchestrator{ChartAcquirer: &recordingChartAcquirer{chartDir: chartDir}}.Build(context.Background(), BuildRequest{Path: root})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			names := make([]string, 0, len(result.ApplicationManifests))
			for _, item := range result.ApplicationManifests {
				names = append(names, item.Manifest.Object.GetName())
			}
			if len(names) != 1 || names[0] != tc.want {
				t.Fatalf("rendered %v, want only %s", names, tc.want)
			}
		})
	}
}

// A config management plugin sidecar runs its own tools, so the native
// argocd-vault-plugin path ignores jsonnet.enable.
func TestAVPCompatNativeOptionsIgnoreSourceTypeGates(t *testing.T) {
	native := avpCompatNativeOptions(render.RenderOptions{DirectoryJsonnetDisabled: true})
	if native.DirectoryJsonnetDisabled {
		t.Fatal("avpCompatNativeOptions() keeps DirectoryJsonnetDisabled, want it off")
	}
}

// A --kube-version that does not parse fails the run, also when every
// Application sets its own kubeVersion, which wins over the flag.
func TestInvalidKubeVersionFlagFailsEveryRun(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: chart\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: kube\n")
	writeReviewApplication(t, root, "kube", "chart", "    helm:\n      kubeVersion: 1.30.0\n", "demo")
	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:        root,
		RepoMaps:    []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
		Offline:     true,
		KubeVersion: "not-a-version",
	})
	if err == nil || !strings.Contains(err.Error(), "--kube-version: parse kube version \"not-a-version\"") {
		t.Fatalf("Build() error = %v, want the --kube-version error", err)
	}
}

// With argocd-cm helm.enable "false", the repo-server renders a chart
// directory as a plain directory and never resolves the Helm inputs
// (GenerateManifests, repository.go:1806-1811 at v3.6.1): no value file
// or file parameter URL fetch, no value file glob, and no dependency
// build. With Helm on, the same source reads them.
func TestGatedHelmSourceSkipsHelmInputs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 1.0.0\ndependencies:\n- name: dep\n  version: '>=1.0.0'\n  repository: https://charts.example.test\n")
	writeTestFile(t, filepath.Join(root, "chart", "values.yaml"), "{}\n")
	writeTestFile(t, filepath.Join(root, "chart", "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: from-template\n")
	writeTestFile(t, filepath.Join(root, "chart", "gate-probe.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\ndata:\n  a: b\n")
	writeReviewApplication(t, root, "gated", "chart", `    helm:
      valueFiles:
      - https://values.example.test/values.yaml
      - "*.missing.yaml"
      ignoreMissingValueFiles: true
      fileParameters:
      - name: probe
        path: https://values.example.test/probe.txt
`, "demo")
	remoteFile := filepath.Join(t.TempDir(), "remote.yaml")
	writeTestFile(t, remoteFile, "{}\n")
	build := func() (BuildResult, *recordingChartAcquirer, *recordingRemoteAcquirer, error) {
		charts := &recordingChartAcquirer{acquireErr: errors.New("chart acquire probe")}
		remotes := &recordingRemoteAcquirer{path: remoteFile}
		result, err := Orchestrator{ChartAcquirer: charts, RemoteResourceAcquirer: remotes}.Build(context.Background(), BuildRequest{
			Path:     root,
			RepoMaps: []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
			Offline:  true,
		})
		return result, charts, remotes, err
	}
	if _, charts, remotes, err := build(); err == nil || !strings.Contains(err.Error(), "chart acquire probe") || len(charts.requests) == 0 || len(remotes.requests) == 0 {
		t.Fatalf("Build() with Helm on: error = %v, chart requests = %d, remote requests = %d, want URL fetches and the dependency acquire error", err, len(charts.requests), len(remotes.requests))
	}
	writeReviewArgoCDConfigMap(t, root, "  helm.enable: \"false\"\n")
	result, charts, remotes, err := build()
	if err != nil {
		t.Fatalf("Build() with Helm off error = %v", err)
	}
	if len(charts.requests) != 0 || len(remotes.requests) != 0 {
		t.Fatalf("Build() with Helm off: chart requests = %v, remote requests = %v, want none", charts.requests, remotes.requests)
	}
	names := make([]string, 0, len(result.ApplicationManifests))
	for _, item := range result.ApplicationManifests {
		names = append(names, item.Manifest.Object.GetName())
	}
	if len(names) != 1 || names[0] != "plain" {
		t.Fatalf("rendered %v, want only the plain directory ConfigMap", names)
	}
}
