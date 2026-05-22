package appset

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

func TestGenerateGitDirectoriesApplications(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data, err := os.ReadFile(filepath.Join(root, "app-set.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 2 {
		t.Fatalf("len(apps) = %d, want 2", len(apps))
	}

	if apps[0].Application.Name != "adguard" || apps[1].Application.Name != "powerdns-conf" {
		t.Fatalf("generated order = %q, %q", apps[0].Application.Name, apps[1].Application.Name)
	}

	byName := map[string]GeneratedApplication{}
	for _, app := range apps {
		byName[app.Application.Name] = app
	}
	if byName["adguard"].Application.Namespace != "argocd" {
		t.Fatalf("generated namespace = %s", byName["adguard"].Application.Namespace)
	}
	if byName["adguard"].Application.APIVersion != "argoproj.io/v1alpha1" || byName["adguard"].Application.Kind != "Application" {
		t.Fatalf("generated TypeMeta = %s/%s", byName["adguard"].Application.APIVersion, byName["adguard"].Application.Kind)
	}
	adguard := byName["adguard"].Application
	if adguard.Spec.GetSource().Path != "apps/adguard" {
		t.Fatalf("adguard path = %s", adguard.Spec.GetSource().Path)
	}
	if byName["powerdns-conf"].Application.Spec.Destination.Namespace != "powerdns" {
		t.Fatalf("regexReplaceAll namespace = %s", byName["powerdns-conf"].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateRejectsUnsafeTemplateFunctions(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	t.Setenv("APPSET_SECRET", "should-not-render")

	for _, fn := range []struct {
		name     string
		template string
	}{
		{name: "env", template: `{{ env "APPSET_SECRET" }}`},
		{name: "expandenv", template: `{{ expandenv "$APPSET_SECRET" }}`},
		{name: "getHostByName", template: `{{ getHostByName "localhost" }}`},
	} {
		t.Run(fn.name, func(t *testing.T) {
			data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: apps/*
  template:
    metadata:
      name: '` + fn.template + `'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

			_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
			if err == nil {
				t.Fatalf("expected %s to be unavailable", fn.name)
			}
			if !strings.Contains(err.Error(), `function "`+fn.name+`" not defined`) {
				t.Fatalf("error = %v, want unavailable %s function", err, fn.name)
			}
		})
	}
}

func TestGenerateSupportsArgoTemplateFunctions(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        directories:
          - path: apps/adguard
  template:
    metadata:
      name: '{{ normalize "Team_App..Name!!" }}'
      annotations:
        array: '{{ fromYamlArray "- first\n- second" | last }}'
        slug: '{{ slugify 20 false "Feature Branch" }}'
        yaml: '{{ toYaml (fromYaml "hello: world") }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}
	app := apps[0].Application
	if app.Name != "team-app..name" {
		t.Fatalf("normalize name = %q", app.Name)
	}
	if app.Annotations["array"] != "second" {
		t.Fatalf("fromYamlArray annotation = %q", app.Annotations["array"])
	}
	if app.Annotations["slug"] != "feature-branch" {
		t.Fatalf("slugify annotation = %q", app.Annotations["slug"])
	}
	if app.Annotations["yaml"] != "hello: world" {
		t.Fatalf("toYaml/fromYaml annotation = %q", app.Annotations["yaml"])
	}
}

func TestGenerateGitDirectoriesHonorsExclude(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: apps/*
          - path: apps/powerdns-conf
            exclude: true
  template:
    metadata:
      name: "{{.path.basenameNormalized}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 || apps[0].Application.Name != "adguard" {
		t.Fatalf("generated apps = %#v", apps)
	}
}

func TestGenerateGitDirectoriesExcludeDoesNotPruneGrandchildren(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "p1", "p2", "app3"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: nested
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: p1/*/*
          - path: p1/*
            exclude: true
  template:
    metadata:
      name: "{{.path.basename}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1: %#v", len(apps), apps)
	}
	if apps[0].SourcePath != "p1/p2/app3" {
		t.Fatalf("SourcePath = %q", apps[0].SourcePath)
	}
}

func TestGenerateAddsDefaultResourcesFinalizer(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data, err := os.ReadFile(filepath.Join(root, "app-set.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) == 0 {
		t.Fatalf("expected generated applications")
	}
	if !slices.Equal(apps[0].Application.Finalizers, []string{argoappv1.ResourcesFinalizerName}) {
		t.Fatalf("finalizers = %#v", apps[0].Application.Finalizers)
	}
}

func TestGeneratePreserveResourcesOnDeletionSkipsDefaultFinalizer(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
spec:
  goTemplate: true
  syncPolicy:
    preserveResourcesOnDeletion: true
  generators:
    - git:
        directories:
          - path: apps/adguard
  template:
    metadata:
      name: "{{.path.basename}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}
	if len(apps[0].Application.Finalizers) != 0 {
		t.Fatalf("finalizers = %#v, want none", apps[0].Application.Finalizers)
	}
}

func TestGenerateBasenameNormalizedMatchesArgoCD(t *testing.T) {
	root := t.TempDir()
	longBase := strings.Repeat("a", 255)
	for _, dir := range []string{
		filepath.Join(root, "apps", "Team__App..Name"),
		filepath.Join(root, "apps", longBase),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: normalized
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: apps/*
  template:
    metadata:
      name: "{{.path.basenameNormalized}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	byName := map[string]GeneratedApplication{}
	for _, app := range apps {
		byName[app.Application.Name] = app
	}
	if _, ok := byName["team--app..name"]; !ok {
		t.Fatalf("missing Argo CD normalized dotted name, got %#v", byName)
	}
	if _, ok := byName[strings.Repeat("a", 253)]; !ok {
		t.Fatalf("missing Argo CD truncated name, got %#v", byName)
	}
}

func TestGenerateMissingKeyOptionReturnsTemplateError(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        directories:
          - path: apps/*
  template:
    metadata:
      name: "{{.missing.value}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil {
		t.Fatalf("expected missing key template error")
	}
}

func TestGenerateRejectsUnsupportedGenerator(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  generators:
    - list:
        elements:
          - name: dev
  template:
    metadata:
      name: dev
`)

	_, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil {
		t.Fatalf("expected unsupported generator error")
	}
	if len(diags) == 0 {
		t.Fatalf("expected diagnostics")
	}
	if diags[0].Category != "appset" {
		t.Fatalf("diagnostic category = %q", diags[0].Category)
	}
}
