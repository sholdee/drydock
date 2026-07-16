package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestListApplicationsUsesExplicitRenderedKustomizeAheadOfStaticDuplicates(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "base", "application.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: HEAD
    path: apps/raw
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  application.instanceLabelKey: app.kubernetes.io/raw
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "project.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: demo
  namespace: argocd
spec:
  sourceRepos:
    - https://github.com/example/raw
  destinations:
    - server: https://kubernetes.default.svc
      namespace: raw
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "kustomization.yaml"), `resources:
  - application.yaml
  - argocd-cm.yaml
  - project.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "overlays", "prod", "kustomization.yaml"), `resources:
  - ../../base
patches:
  - target:
      group: argoproj.io
      version: v1alpha1
      kind: Application
      name: demo
    patch: |-
      - op: replace
        path: /spec/source/path
        value: apps/rendered
  - target:
      version: v1
      kind: ConfigMap
      name: argocd-cm
    patch: |-
      - op: replace
        path: /data/application.instanceLabelKey
        value: app.kubernetes.io/rendered
  - target:
      group: argoproj.io
      version: v1alpha1
      kind: AppProject
      name: demo
    patch: |-
      - op: replace
        path: /spec/sourceRepos/0
        value: https://github.com/example/rendered
      - op: replace
        path: /spec/destinations/0/namespace
        value: rendered
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			DiscoverKustomizePaths: []string{filepath.Join("argocd", "overlays", "prod")},
		},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %#v, want one rendered Application", result.Applications)
	}
	if got := result.Applications[0].Spec.Source.Path; got != "apps/rendered" {
		t.Fatalf("Application source path = %q, want rendered path", got)
	}
	if got := result.Settings.InstanceLabelKey.Value; got != "app.kubernetes.io/rendered" {
		t.Fatalf("InstanceLabelKey = %q, want rendered settings", got)
	}
	if len(result.Projects) != 1 || len(result.Projects[0].Spec.SourceRepos) != 1 || result.Projects[0].Spec.SourceRepos[0] != "https://github.com/example/rendered" {
		t.Fatalf("Projects = %#v, want explicit rendered project", result.Projects)
	}
	if len(result.ApplicationInputs) != 1 || len(result.ApplicationInputs[0].Paths) != 1 || result.ApplicationInputs[0].Paths[0] != "argocd/overlays/prod" {
		t.Fatalf("ApplicationInputs = %#v, want explicit rendered discovery path", result.ApplicationInputs)
	}
	if diag, ok := diagnosticByCategory(result.Diagnostics, "discovery"); !ok || !strings.Contains(diag.Message, "duplicate Application") {
		t.Fatalf("Diagnostics = %#v, want duplicate discovery warning", result.Diagnostics)
	}
}

func TestExplicitRenderedKustomizeApplicationSetWinsOverStaticBase(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "base", "application-set.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: argocd
  namespace: argocd
spec:
  generators:
    - git:
        files:
          - path: "*/configurations/{{ cluster.name }}/cluster-config.yaml"
  template:
    metadata:
      name: "{{ cluster.name }}-{{ app.name }}"
      namespace: argocd
    spec:
      project: "{{ project.name }}"
      source:
        repoURL: "{{ source.repoURL }}"
        targetRevision: "{{ source.targetRevision }}"
        path: "{{ source.path }}"
      destination:
        server: "{{ destination.server }}"
        namespace: "{{ destination.namespace }}"
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "kustomization.yaml"), `resources:
  - application-set.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "overlays", "nuc10-cluster", "kustomization.yaml"), `resources:
  - ../../base
patches:
  - target:
      group: argoproj.io
      version: v1alpha1
      kind: ApplicationSet
      name: argocd
    patch: |-
      - op: replace
        path: /spec/generators/0/git/files/0/path
        value: "*/configurations/nuc10-cluster/cluster-config.yaml"
`)
	writeTestFile(t, filepath.Join(root, "demo", "configurations", "nuc10-cluster", "cluster-config.yaml"), `cluster:
  name: nuc10
app:
  name: demo
project:
  name: homelab
source:
  repoURL: https://github.com/example/repo.git
  targetRevision: main
  path: demo/overlays/nuc10-cluster
destination:
  server: https://kubernetes.default.svc
  namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "demo", "overlays", "nuc10-cluster", "kustomization.yaml"), `resources: []
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			DiscoverKustomizePaths: []string{"argocd/overlays/nuc10-cluster"},
		},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	names := applicationNames(result.Applications)
	if strings.Join(names, ",") != "nuc10-demo" {
		t.Fatalf("Applications = %#v, want generated rendered overlay app", names)
	}
	if got := result.Applications[0].Spec.Source.Path; got != "demo/overlays/nuc10-cluster" {
		t.Fatalf("Application source path = %q, want patched overlay path", got)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "appset" && strings.Contains(diag.Message, "generated zero Applications") {
			t.Fatalf("Diagnostics = %#v, explicit overlay should replace zero-app static base", result.Diagnostics)
		}
	}
}

func TestListApplicationsKeepsStaticObjectsAheadOfRenderedFleetDuplicates(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "bootstrap.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: bootstrap
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: bootstrap-chart
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: apps/raw
  destination:
    name: in-cluster
    namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "Chart.yaml"), `apiVersion: v2
name: bootstrap-chart
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: apps/rendered
  destination:
    name: in-cluster
    namespace: demo
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "bootstrap,demo" {
		t.Fatalf("Applications = %#v, want bootstrap and demo", names)
	}
	var demo argoappv1.Application
	for _, application := range result.Applications {
		if application.Name == "demo" {
			demo = application
		}
	}
	if got := demo.Spec.GetSource().Path; got != "apps/raw" {
		t.Fatalf("demo source path = %q, want static committed path", got)
	}
	if diag, ok := diagnosticByCategory(result.Diagnostics, "discovery"); !ok || !strings.Contains(diag.Message, "duplicate Application") {
		t.Fatalf("Diagnostics = %#v, want duplicate discovery warning", result.Diagnostics)
	}
}

func TestListApplicationsDiscoversRenderedFleetApplicationsByDefault(t *testing.T) {
	root := t.TempDir()
	writeRenderedFleetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	names := applicationNames(result.Applications)
	if strings.Join(names, ",") != "child,root" {
		t.Fatalf("Applications = %#v, want child and root", names)
	}
	inputs, ok := applicationInputPathsForName(result.ApplicationInputs, "child")
	if !ok {
		t.Fatalf("ApplicationInputs = %#v, missing child", result.ApplicationInputs)
	}
	for _, want := range []string{"apps/root.yaml", "bootstrap-chart/templates/child.yaml"} {
		if !containsPath(inputs, want) {
			t.Fatalf("child inputs = %#v, missing %q", inputs, want)
		}
	}
}

func TestListApplicationsStaticDiscoveryModeDisablesRenderedFleetExpansion(t *testing.T) {
	root := t.TempDir()
	writeRenderedFleetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			DiscoveryMode: DiscoveryModeStatic,
		},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	names := applicationNames(result.Applications)
	if strings.Join(names, ",") != "root" {
		t.Fatalf("Applications = %#v, want only static root", names)
	}
}

func TestListApplicationsPrefersRenderedNamespaceOverNamespaceLessApplicationSet(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "overlays", "argocd", "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: apps
spec:
  generators:
    - list:
        elements:
          - name: demo
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: workloads/{{name}}
      destination:
        name: in-cluster
        namespace: '{{name}}'
`)
	writeTestFile(t, filepath.Join(root, "overlays", "argocd", "kustomization.yaml"), `namespace: argocd
resources:
  - appset.yaml
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			DiscoverKustomizePaths: []string{"overlays/argocd"},
		},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "demo" {
		t.Fatalf("Applications = %#v, want demo", names)
	}
	if got := result.Applications[0].Namespace; got != "argocd" {
		t.Fatalf("Application namespace = %q, want rendered namespace", got)
	}
	if diag, ok := diagnosticByCategory(result.Diagnostics, "discovery"); ok && strings.Contains(diag.Message, "duplicate") {
		t.Fatalf("Diagnostics = %#v, want namespace-defaulted duplicate to stay quiet", result.Diagnostics)
	}
}

func TestListApplicationsErrorsWhenRenderedFleetDiscoveryDoesNotConverge(t *testing.T) {
	root := t.TempDir()
	writeRenderedFleetFixture(t, root)

	_, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			MaxDiscoveryDepth: 1,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "maximum discovery depth 1") {
		t.Fatalf("ListApplications() error = %v, want max discovery depth error", err)
	}
}

func TestListApplicationsDiscoversRenderedProjectsAndSettings(t *testing.T) {
	root := t.TempDir()
	writeRenderedFleetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	if got := result.Settings.InstanceLabelKey.Value; got != "app.kubernetes.io/rendered" {
		t.Fatalf("InstanceLabelKey = %q, want rendered settings", got)
	}
	projectNames := make([]string, 0, len(result.Projects))
	for _, project := range result.Projects {
		projectNames = append(projectNames, project.Name)
	}
	if strings.Join(projectNames, ",") != "rendered" {
		t.Fatalf("Projects = %#v, want rendered project", projectNames)
	}
}

func TestRenderedFleetApplicationInputsIncludeParentAndRenderedPaths(t *testing.T) {
	root := t.TempDir()
	writeRenderedFleetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	selected, unowned := SelectChangedApplicationInputs(result.ApplicationInputs, []string{"workloads/child/cm.yaml"})
	if len(unowned) != 0 {
		t.Fatalf("unowned = %#v, want none", unowned)
	}
	if names := applicationNames(selected); strings.Join(names, ",") != "child" {
		t.Fatalf("selected for child source change = %#v, want child", names)
	}

	selected, unowned = SelectChangedApplicationInputs(result.ApplicationInputs, []string{"bootstrap-chart/templates/child.yaml"})
	if len(unowned) != 0 {
		t.Fatalf("unowned = %#v, want none", unowned)
	}
	if names := applicationNames(selected); strings.Join(names, ",") != "child,root" {
		t.Fatalf("selected for rendered child definition change = %#v, want child and root", names)
	}

	selected, unowned = SelectChangedApplicationInputs(result.ApplicationInputs, []string{"apps/root.yaml"})
	if len(unowned) != 0 {
		t.Fatalf("unowned = %#v, want none", unowned)
	}
	if names := applicationNames(selected); strings.Join(names, ",") != "child,root" {
		t.Fatalf("selected for parent change = %#v, want child and root", names)
	}
}

func TestBuildReusesRenderedDiscoveryCacheForFinalRender(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: root
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: manifests/root
    plugin:
      name: app-of-apps
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "manifests", "root", "marker.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
`)
	writeTestFile(t, filepath.Join(root, "workloads", "child", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: child
`)

	var calls atomic.Int32
	renderer := internalPluginRendererFunc(func(_ context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		calls.Add(1)
		return []render.Manifest{{
			Path: "templates/child.yaml",
			Object: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "Application",
				"metadata": map[string]any{
					"name":      "child",
					"namespace": "argocd",
				},
				"spec": map[string]any{
					"project": "default",
					"source": map[string]any{
						"repoURL":        "https://github.com/example/repo",
						"targetRevision": "main",
						"path":           "workloads/child",
					},
					"destination": map[string]any{
						"name":      "in-cluster",
						"namespace": "child",
					},
				},
			}},
		}}, nil, nil
	})

	result, err := (Orchestrator{PluginRenderer: renderer}).Build(context.Background(), BuildRequest{
		Path: root,
		ExecutionOptions: ExecutionOptions{
			Parallelism: 1,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("plugin render calls = %d, want discovery result reused for final render", got)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "child,root" {
		t.Fatalf("Applications = %#v, want child and root", names)
	}
}

func TestApplicationMayRenderDiscoveryObjectsUsesLocalSourcePrefilter(t *testing.T) {
	root := t.TempDir()
	leaf := argoappv1.Application{}
	leaf.Name = "leaf"
	leaf.Spec.Source = &argoappv1.ApplicationSource{
		RepoURL:        "https://github.com/example/repo",
		TargetRevision: "main",
		Path:           "leaf",
	}
	writeTestFile(t, filepath.Join(root, "leaf", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: leaf
`)
	if applicationMayRenderDiscoveryObjects(root, BuildRequest{}, discovery.Result{}, leaf) {
		t.Fatal("leaf Application prefilter = true, want false")
	}

	appOfApps := argoappv1.Application{}
	appOfApps.Name = "app-of-apps"
	appOfApps.Spec.Source = &argoappv1.ApplicationSource{
		RepoURL:        "https://github.com/example/repo",
		TargetRevision: "main",
		Path:           "chart",
	}
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), `apiVersion: v2
name: app-of-apps
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "chart", "templates", "app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
`)
	if !applicationMayRenderDiscoveryObjects(root, BuildRequest{}, discovery.Result{}, appOfApps) {
		t.Fatal("app-of-apps prefilter = false, want true")
	}
}

func TestListApplicationsRejectsUnsafeDiscoverKustomizePath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "overlay", "kustomization.yaml"), "resources: []\n")

	_, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			DiscoverKustomizePaths: []string{filepath.Join(root, "overlay")},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("ListApplications() error = %v, want relative path error", err)
	}

	if err := os.Symlink(filepath.Join(root, "overlay"), filepath.Join(root, "linked-overlay")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	_, err = Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			DiscoverKustomizePaths: []string{"linked-overlay"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink component") {
		t.Fatalf("ListApplications() error = %v, want symlink path error", err)
	}
}

func TestListApplicationsDiscoverIgnoreDoesNotFilterGitFilesGeneratorParams(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "params", "demo", "config.yaml"), `team: platform
`)
	writeTestFile(t, filepath.Join(root, "appsets", "generated.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: generated
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://github.com/example/repo.git
        revision: HEAD
        files:
          - path: params/*/config.yaml
  template:
    metadata:
      name: '{{.path.basename}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo.git
        path: '{{.path.path}}'
        targetRevision: HEAD
      destination:
        server: https://kubernetes.default.svc
        namespace: '{{.path.basename}}'
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			// The generator param file matches the ignore glob; discover-ignore
			// scopes to repository discovery only and must not filter appset git
			// generator file matching.
			DiscoverIgnoreGlobs: []string{"params/**"},
		},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	for _, app := range result.Applications {
		if app.Name == "demo" {
			return
		}
	}
	t.Fatalf("Applications = %#v, want generated demo from ignored param file", result.Applications)
}

func TestListApplicationsDiscoverIgnoreDoesNotFilterExplicitRenderedKustomize(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "base", "application.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: HEAD
    path: apps/raw
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "kustomization.yaml"), `resources:
  - application.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "overlays", "prod", "kustomization.yaml"), `resources:
  - ../../base
patches:
  - target:
      group: argoproj.io
      version: v1alpha1
      kind: Application
      name: demo
    patch: |-
      - op: replace
        path: /spec/source/path
        value: apps/rendered
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		DiscoveryOptions: DiscoveryOptions{
			DiscoverKustomizePaths: []string{filepath.Join("argocd", "overlays", "prod")},
			// Every static file matches the ignore glob; rendered-tier discovery
			// (ScanObjects with synthetic display paths under argocd/) must be
			// unaffected.
			DiscoverIgnoreGlobs: []string{"argocd/**"},
		},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %#v, want one rendered Application", result.Applications)
	}
	if got := result.Applications[0].Spec.Source.Path; got != "apps/rendered" {
		t.Fatalf("Application source path = %q, want rendered path", got)
	}
	// The static duplicate was ignored, so the rendered Application must not
	// produce a duplicate discovery warning.
	if diag, ok := diagnosticByCategory(result.Diagnostics, "discovery"); ok && strings.Contains(diag.Message, "duplicate Application") {
		t.Fatalf("Diagnostics = %#v, want no duplicate discovery warning", result.Diagnostics)
	}
}

func TestListApplicationsWarnsWhenApplicationSetGeneratesZeroApplications(t *testing.T) {
	root := t.TempDir()
	writeZeroApplicationSet(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.Applications) != 0 {
		t.Fatalf("Applications = %#v, want none", result.Applications)
	}
	diag, ok := diagnosticByCategory(result.Diagnostics, "appset")
	if !ok {
		t.Fatalf("Diagnostics = %#v, want appset warning", result.Diagnostics)
	}
	if diag.Severity != diagnostic.SeverityWarning || !strings.Contains(diag.Message, "ApplicationSet argocd/empty generated zero Applications") {
		t.Fatalf("Diagnostic = %#v, want zero-generated warning", diag)
	}

	_, err = Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil || !strings.Contains(err.Error(), "generated zero Applications") {
		t.Fatalf("strict ListApplications() error = %v, want zero-generated error", err)
	}
}

func writeZeroApplicationSet(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: empty
  namespace: argocd
spec:
  generators:
    - list:
        elements: []
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      destination:
        server: https://kubernetes.default.svc
        namespace: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: apps/{{name}}
`)
}

func writeRenderedFleetFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "root.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: root
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: bootstrap-chart
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "Chart.yaml"), `apiVersion: v2
name: bootstrap-chart
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "child.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
  namespace: argocd
spec:
  project: rendered
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/child
  destination:
    name: in-cluster
    namespace: child
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "project.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: rendered
  namespace: argocd
spec:
  sourceRepos:
    - '*'
  destinations:
    - namespace: '*'
      server: '*'
      name: '*'
`)
	writeTestFile(t, filepath.Join(root, "bootstrap-chart", "templates", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  application.instanceLabelKey: app.kubernetes.io/rendered
`)
	writeTestFile(t, filepath.Join(root, "workloads", "child", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: child
  namespace: child
data:
  source: rendered-fleet
`)
}

func applicationNames(applications []argoappv1.Application) []string {
	names := make([]string, 0, len(applications))
	for _, application := range applications {
		names = append(names, application.Name)
	}
	sort.Strings(names)
	return names
}

func applicationInputPathsForName(inputs []ApplicationSelectionInput, name string) ([]string, bool) {
	for _, input := range inputs {
		if input.Application.Name == name {
			return input.Paths, true
		}
	}
	return nil, false
}

func containsPath(values []string, want string) bool {
	return slices.Contains(values, want)
}
