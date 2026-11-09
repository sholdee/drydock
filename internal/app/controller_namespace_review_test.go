package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/discovery"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// kustomizeArgoCDInstallIn writes, at dir, a kustomization that installs the
// fake argo-cd chart in namespace with the values file values.yaml.
func kustomizeArgoCDInstallIn(t *testing.T, root, dir, namespace, values string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, dir, "kustomization.yaml"), strings.ReplaceAll(kustomizeArgoCDInstallYAML("", []string{"values.yaml"}), "namespace: argocd\n", "namespace: "+namespace+"\n"))
	writeTestFile(t, filepath.Join(root, dir, "values.yaml"), values)
}

func kustomizeApplicationYAML(name, namespace, path, destination string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + name + `
  namespace: ` + namespace + `
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: ` + path + `
  destination:
    name: in-cluster
    namespace: ` + destination + `
`
}

// An Application in argocd installs a tenant Argo CD in team-a whose own
// application.namespaces does not enable argocd, and nothing renders the
// hub. The tenant never names the controller namespace: drydock keeps the
// default argocd, keeps every Application, and names team-a as another
// install in a warning that --strict accepts.
func TestTenantInstallThatDoesNotReconcileItsApplicationNeverDecides(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "tenant.yaml"), kustomizeApplicationYAML("tenant", "argocd", "tenant", "team-a"))
	kustomizeArgoCDInstallIn(t, root, "tenant", "team-a", "configs:\n  params:\n    application.namespaces: \"\"\n")
	writeDiffApplication(t, root, "demo", "demo", "same")
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: argocd\nresources: [argocd-cm.yaml]\n")
	writeTestFile(t, filepath.Join(root, "argocd", "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\ndata:\n  kustomize.buildOptions: --enable-helm\n")

	result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t)}.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true})
	if err != nil {
		t.Fatalf("ListApplications(--strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := applicationNames(result.Applications); !slices.Contains(got, "tenant") || !slices.Contains(got, "demo") {
		t.Fatalf("Applications = %v, want tenant and demo", got)
	}
	if result.Settings.ControllerNamespaceKnown() || result.Settings.ControllerNamespace("") != defaultArgoCDControllerNamespace {
		t.Fatalf("ControllerNamespace = %q (known %t), want the unknown default", result.Settings.ControllerNamespace(""), result.Settings.ControllerNamespaceKnown())
	}
	if gated := diagnosticsWithCode(result.Diagnostics, namespaceNotEnabledCode); len(gated) != 0 {
		t.Fatalf("gated = %#v, want none", gated)
	}
	warnings := diagnosticsWithCode(result.Diagnostics, controllerNamespaceAmbiguousCode)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "other Argo CD installs in team-a") {
		t.Fatalf("namespace warnings = %#v, want one that names team-a as another install", controllerNamespaceWarnings(result.Diagnostics))
	}
}

// writeLateInstallRepo writes the Application root in gitops. Its local chart
// renders the Application child-ca, or child-cb with the
// .argocd-source-root.yaml that only the instance name root reads, and the
// Application argocd, which installs Argo CD in gitops. So the namespace
// is known only one level below root.
func writeLateInstallRepo(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), kustomizeApplicationYAML("root", controllerNamespaceTestNamespace, "root", controllerNamespaceTestNamespace))
	writeTestFile(t, filepath.Join(root, "root", "Chart.yaml"), "apiVersion: v2\nname: root\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "root", "values.yaml"), "child: ca\n")
	writeTestFile(t, filepath.Join(root, "root", ".argocd-source-root.yaml"), "helm:\n  parameters:\n    - name: child\n      value: cb\n")
	writeTestFile(t, filepath.Join(root, "root", "templates", "apps.yaml"), kustomizeApplicationYAML("child-{{ .Values.child }}", controllerNamespaceTestNamespace, "{{ .Values.child }}", "{{ .Values.child }}")+"---\n"+kustomizeApplicationYAML("argocd", controllerNamespaceTestNamespace, "install", controllerNamespaceTestNamespace))
	kustomizeArgoCDInstallIn(t, root, "install", controllerNamespaceTestNamespace, "configs:\n  params:\n    application.namespaces: \"\"\n")
	for _, child := range []string{"ca", "cb"} {
		writeTestFile(t, filepath.Join(root, child, "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+child+"\n")
	}
}

// An install that discovery finds one level deep names the namespace after
// the first level rendered with argocd. drydock then runs discovery once
// more with the final namespace, so root reads its .argocd-source-root.yaml
// and child-ca does not stay. --argocd-namespace needs no second run.
func TestLateControllerNamespaceRestartsDiscoveryOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
		runs int
	}{
		{name: "found late", runs: 2},
		{name: "--argocd-namespace", flag: controllerNamespaceTestNamespace, runs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeLateInstallRepo(t, root)
			runs := 0
			orchestrator := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t), discoveryRunObserver: func() { runs++ }}
			result, err := orchestrator.ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, Strict: true, ArgoCDNamespace: tc.flag})
			if err != nil {
				t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			names := applicationNames(result.Applications)
			if !slices.Contains(names, "child-cb") || slices.Contains(names, "child-ca") {
				t.Fatalf("Applications = %v, want child-cb and no child-ca", names)
			}
			if runs != tc.runs {
				t.Fatalf("discovery runs = %d, want %d", runs, tc.runs)
			}
			if got := diagnosticsWithCode(result.Diagnostics, controllerNamespaceUnsettledCode); len(got) != 0 {
				t.Fatalf("unsettled = %#v, want none", got)
			}
		})
	}
}

// A repository whose Argo CD runs in argocd never runs discovery twice for
// the namespace.
func TestArgoCDInArgoCDNeverRestartsDiscovery(t *testing.T) {
	for _, shape := range argoCDInstallShapes() {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			shape.write(t, root, []string{"values.yaml"})
			writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  params:\n    application.namespaces: team-*\n")
			runs := 0
			request := BuildRequest{Path: root, Parallelism: 1}
			if shape.repoMaps != nil {
				request.RepoMaps = shape.repoMaps(root)
			}
			result, err := Orchestrator{ChartAcquirer: fakeArgoCDChartAcquirer(t), discoveryRunObserver: func() { runs++ }}.ListApplications(context.Background(), request)
			if err != nil {
				t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
			}
			if runs != 1 {
				t.Fatalf("discovery runs = %d, want 1", runs)
			}
		})
	}
}

// The plugin policy bootstrap renders before fleet discovery. When a
// rendered install names the controller namespace later, the second run
// renders the bootstrap entrypoint in that namespace.
func TestPluginPolicyBootstrapFollowsALateControllerNamespace(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	writeTestFile(t, filepath.Join(root, "apps", "install.yaml"), kustomizeApplicationYAML("argocd", controllerNamespaceTestNamespace, "install", controllerNamespaceTestNamespace))
	kustomizeArgoCDInstallIn(t, root, "install", controllerNamespaceTestNamespace, "configs:\n  params:\n    application.namespaces: \"\"\n")
	runner := &recordingExecRunner{}

	result, err := (Orchestrator{PluginExecRunner: runner, ChartAcquirer: fakeArgoCDChartAcquirer(t)}).ListApplications(context.Background(), BuildRequest{Path: root, Parallelism: 1, PluginOptions: trustedBootstrapPluginOptions(t, root)})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace {
		t.Fatalf("ControllerNamespace = %q, want gitops", got)
	}
	if env := strings.Join(runner.lastRequest.ExtraEnv, "\n"); !strings.Contains(env, "ARGOCD_APP_NAMESPACE="+controllerNamespaceTestNamespace) {
		t.Fatalf("plugin env = %q, want the bootstrap in gitops", runner.lastRequest.ExtraEnv)
	}
}

// A namespace-less settings ConfigMap of an install lands in the
// destination namespace. When that is not the controller namespace, it is
// not a setting, as in earlier releases. The render pipeline gives most
// rendered objects their namespace, so this checks the marks directly.
func TestNamespaceLessInstallConfigMapLandsInTheDestination(t *testing.T) {
	cm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "argocd-cm"},
		"data":       map[string]any{"application.resourceTrackingMethod": "label"},
	}}
	result := discovery.Result{SettingsCandidates: []discovery.SettingsCandidate{{Kind: "argocd-cm", Path: "rendered/edge/cm.yaml", Object: cm}}}
	application := argoappv1.Application{Name: "edge", Namespace: "argocd", Spec: argoappv1.ApplicationSpec{Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "edge"}}}
	markArgoCDInstallSettings(&result, application)
	if got := result.SettingsCandidates[0].InstallNamespace; got != "edge" {
		t.Fatalf("InstallNamespace = %q, want the destination namespace edge", got)
	}
	if other := otherInstallSettings(result.SettingsCandidates, defaultArgoCDControllerNamespace); len(other) != 1 {
		t.Fatalf("otherInstallSettings() = %v, want the ConfigMap that lands in edge", other)
	}
	if other := otherInstallSettings(result.SettingsCandidates, "edge"); len(other) != 0 {
		t.Fatalf("otherInstallSettings() = %v, want none in the controller namespace edge", other)
	}
}

// drydock asks which Applications render a committed argocd-cmd-params-cm
// once for each Application and file in a build side, however many
// settings loads, checks, and landing lookups ask it.
func TestCommittedCommandParametersRenderCheckIsMemoized(t *testing.T) {
	var calls atomic.Int64
	original := committedFileReader
	committedFileReader = func(provider localProvider, source argoappv1.ApplicationSource, file string, gates sourceTypeGates) (bool, string) {
		calls.Add(1)
		return original(provider, source, file, gates)
	}
	t.Cleanup(func() { committedFileReader = original })

	write := func(t *testing.T, remote bool) string {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), commandParametersConfigMapYAML(controllerNamespaceTestNamespace, "  server.insecure: \"true\"\n"))
		if !remote {
			writeTestFile(t, filepath.Join(root, "apps", "settings.yaml"), kustomizeApplicationYAML("settings", controllerNamespaceTestNamespace, "settings", controllerNamespaceTestNamespace))
		}
		for _, name := range []string{"a", "b", "c"} {
			app := kustomizeApplicationYAML(name, controllerNamespaceTestNamespace, "workloads/"+name, name)
			if remote {
				app = strings.Replace(app, "    name: in-cluster\n", "    server: https://spoke.example.com\n", 1)
			}
			writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), app)
			writeTestFile(t, filepath.Join(root, "workloads", name, "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"\n")
		}
		return root
	}

	for _, tc := range []struct {
		name   string
		remote bool
		calls  int64
	}{
		{name: "rendered by an Application in its namespace", calls: 4},
		{name: "Applications on another cluster only", remote: true, calls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls.Store(0)
			result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: write(t, tc.remote), Parallelism: 1})
			if err != nil {
				t.Fatalf("ListApplications() error = %v", err)
			}
			if got := result.Settings.ControllerNamespace(""); got != controllerNamespaceTestNamespace {
				t.Fatalf("ControllerNamespace = %q, want gitops", got)
			}
			if got := calls.Load(); got != tc.calls {
				t.Fatalf("render checks = %d, want %d for the whole run", got, tc.calls)
			}
		})
	}
}

// The persistent render cache keys a render by the controller namespace of
// its tracking options. The Application renders a chart from a chart
// repository, so no .argocd-source file name reaches the key: only the
// tracking options tell the two runs apart.
func TestRenderCacheKeyFollowsTheTrackingNamespace(t *testing.T) {
	root := t.TempDir()
	cacheDir := t.TempDir()
	chartDir := t.TempDir()
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: env\nversion: 1.0.0\n")
	writeTestFile(t, filepath.Join(chartDir, "values.yaml"), "appName: unset\n")
	writeTestFile(t, filepath.Join(chartDir, "templates", "env.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: root-env\ndata:\n  appName: {{ .Values.appName | quote }}\n")
	writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: root
  namespace: `+controllerNamespaceTestNamespace+`
spec:
  project: default
  source:
    repoURL: https://charts.example.com
    chart: env
    targetRevision: 1.0.0
    helm:
      parameters:
        - name: appName
          value: $ARGOCD_APP_NAME
  destination:
    name: in-cluster
    namespace: workloads
`)
	gitCommitAll(t, root, "initial")

	build := func(flag string) BuildResult {
		request := persistentBuildRequest(root, cacheDir)
		request.RecordCacheEvents = true
		request.ArgoCDNamespace = flag
		result, err := Orchestrator{ChartAcquirer: &recordingChartAcquirer{chartDir: chartDir}}.Build(context.Background(), request)
		if err != nil {
			t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
		}
		return result
	}
	first := build("")
	if !hasRenderCacheEvent(first.CacheEvents, cacheevent.ActionStore, controllerNamespaceTestNamespace+"/root", "") {
		t.Fatalf("cache events = %#v, want a stored render", first.CacheEvents)
	}
	second := build(controllerNamespaceTestNamespace)
	if hasRenderCacheEvent(second.CacheEvents, cacheevent.ActionHit, controllerNamespaceTestNamespace+"/root", "") {
		t.Fatalf("cache events = %#v, want no hit on the render of another controller namespace", second.CacheEvents)
	}
	env := controllerNamespaceManifest(t, second.Manifests, "root-env")
	if got, _, _ := unstructured.NestedString(env.Object, "data", "appName"); got != "root" {
		t.Fatalf("ARGOCD_APP_NAME = %v, want root", got)
	}
}
