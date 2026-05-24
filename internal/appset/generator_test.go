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

func TestGenerateListGeneratorsConcatenateInOrder(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: list-apps
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: alpha
            namespace: apps
    - list:
        elements:
          - name: beta
            namespace: infra
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.namespace}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("generated names = %#v, want alpha beta", got)
	}
	if apps[0].Application.Spec.Destination.Namespace != "apps" || apps[1].Application.Spec.Destination.Namespace != "infra" {
		t.Fatalf("destination namespaces = %q, %q", apps[0].Application.Spec.Destination.Namespace, apps[1].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateListGeneratorAppliesSelector(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: selected
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: alpha
            env: dev
          - name: beta
            env: prod
      selector:
        matchLabels:
          env: prod
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
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
	if got := generatedNames(apps); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("generated names = %#v, want beta", got)
	}
}

func TestGenerateGitFilesGeneratorSelectorMatchesFlattenedNestedParams(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "dev.yaml"), `cluster:
  name: dev
  tier: test
app:
  path: apps/dev
`)
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "prod.yaml"), `cluster:
  name: prod
  tier: prod
app:
  path: apps/prod
`)
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: selected-files
spec:
  goTemplate: true
  generators:
    - git:
        files:
          - path: clusters/*.yaml
      selector:
        matchExpressions:
          - key: cluster.tier
            operator: In
            values: ["prod"]
  template:
    metadata:
      name: '{{.cluster.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{.app.path}}'
        targetRevision: main
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
	if got := generatedNames(apps); !slices.Equal(got, []string{"prod"}) {
		t.Fatalf("generated names = %#v, want prod", got)
	}
}

func TestGenerateListGeneratorTemplateOverridesBaseTemplate(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: template-override
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: alpha
        template:
          spec:
            destination:
              namespace: override
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: base
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if apps[0].Application.Spec.Destination.Namespace != "override" {
		t.Fatalf("namespace = %q, want override", apps[0].Application.Spec.Destination.Namespace)
	}
	if apps[0].Application.Spec.Project != "default" {
		t.Fatalf("project = %q, want inherited default", apps[0].Application.Spec.Project)
	}
}

func TestGenerateGitDirectoriesGeneratorTemplateOverridesBaseTemplate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "alpha"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git-template-override
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: apps/*
        template:
          spec:
            destination:
              namespace: git-override
  template:
    metadata:
      name: '{{.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{.path.path}}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: base
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if apps[0].Application.Spec.Destination.Namespace != "git-override" {
		t.Fatalf("namespace = %q, want git-override", apps[0].Application.Spec.Destination.Namespace)
	}
	if apps[0].Application.Spec.Project != "default" {
		t.Fatalf("project = %q, want inherited default", apps[0].Application.Spec.Project)
	}
}

func TestGenerateListGeneratorElementsYaml(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: list-yaml
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: alpha
            namespace: base
        elementsYaml: |
          - name: beta
            namespace: extra
          - name: gamma
            namespace: extra
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.namespace}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("generated names = %#v, want alpha beta gamma", got)
	}
	if apps[2].Application.Spec.Destination.Namespace != "extra" {
		t.Fatalf("gamma namespace = %q, want extra", apps[2].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateListGeneratorElementsYamlNonMappingReturnsDiagnostic(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: bad-list-yaml
spec:
  generators:
    - list:
        elementsYaml: |
          - alpha
  template:
    metadata:
      name: '{{name}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "elementsYaml entries must be mappings") {
		t.Fatalf("diagnostics = %#v, want elementsYaml mapping diagnostic", diags)
	}
}

func TestGenerateListGeneratorElementsYamlAllowsEmptyMapping(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: empty-list-yaml
spec:
  generators:
    - list:
        elementsYaml: |
          - {}
  template:
    metadata:
      name: static
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/static
        targetRevision: main
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
	if got := generatedNames(apps); !slices.Equal(got, []string{"static"}) {
		t.Fatalf("generated names = %#v, want static", got)
	}
}

func TestGenerateSupportedAndUnsupportedGeneratorsKeepsSupportedOutput(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: supported
    - scmProvider: {}
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"supported"}) {
		t.Fatalf("generated names = %#v, want supported", got)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("diagnostic message = %q, want unsupported ApplicationSet generator", diags[0].Message)
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
    - scmProvider: {}
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

func TestGenerateMatrixGeneratorCombinesListChildrenInOrder(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-apps
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - app: alpha
                - app: beta
          - list:
              elements:
                - env: dev
                - env: prod
  template:
    metadata:
      name: '{{.app}}-{{.env}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.app}}/overlays/{{.env}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.env}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-dev", "alpha-prod", "beta-dev", "beta-prod"}) {
		t.Fatalf("generated names = %#v", got)
	}
}

func TestGenerateMatrixGeneratorInterpolatesSecondChildGenerator(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "apps", "dev", "alpha"),
		filepath.Join(root, "apps", "prod", "beta"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-interpolate
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
          - git:
              directories:
                - path: apps/{{.env}}/*
  template:
    metadata:
      name: '{{.env}}-{{.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{.path.path}}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.env}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"dev-alpha"}) {
		t.Fatalf("generated names = %#v, want only dev-alpha", got)
	}
}

func TestGenerateMatrixGeneratorRejectsNonGoTemplateDuplicateKeyConflict(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-conflict
spec:
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
          - list:
              elements:
                - env: prod
  template:
    metadata:
      name: '{{env}}'
`)

	_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil {
		t.Fatalf("expected duplicate key error")
	}
	if !strings.Contains(err.Error(), "duplicate key env") {
		t.Fatalf("error = %v, want duplicate key env", err)
	}
}

func TestGenerateMatrixGeneratorRejectsWrongChildCount(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name       string
		generators string
	}{
		{
			name: "one",
			generators: `
          - list:
              elements:
                - name: alpha`,
		},
		{
			name: "three",
			generators: `
          - list:
              elements:
                - name: alpha
          - list:
              elements:
                - env: dev
          - list:
              elements:
                - region: us`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-count
spec:
  generators:
    - matrix:
        generators:` + tc.generators + `
  template:
    metadata:
      name: '{{name}}'
`)

			_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
			if err == nil {
				t.Fatalf("expected matrix child count error")
			}
			if !strings.Contains(err.Error(), "matrix support only two") {
				t.Fatalf("error = %v, want matrix support only two", err)
			}
		})
	}
}

func TestGenerateMatrixGeneratorKeepsSupportedTopLevelOutputWhenChildUnsupported(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed-matrix
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: supported
    - matrix:
        generators:
          - list:
              elements:
                - name: unsupported
          - scmProvider: {}
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"supported"}) {
		t.Fatalf("generated names = %#v, want supported", got)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("diagnostics = %#v, want unsupported generator diagnostic", diags)
	}
}

func TestGenerateMatrixGeneratorKeepsSupportedOutputWhenTemplatedChildUnsupported(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed-matrix-templated
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - list:
        elements:
          - name: supported
    - matrix:
        generators:
          - list:
              elements:
                - name: unsupported
          - scmProvider:
              github:
                organization: '{{.missing.value}}'
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"supported"}) {
		t.Fatalf("generated names = %#v, want supported", got)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("diagnostics = %#v, want unsupported generator diagnostic", diags)
	}
}

func TestGenerateMatrixGeneratorReportsUnsupportedSecondChildWhenFirstChildEmpty(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed-matrix-empty
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: supported
    - matrix:
        generators:
          - list:
              elements: []
          - scmProvider: {}
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"supported"}) {
		t.Fatalf("generated names = %#v, want supported", got)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("diagnostics = %#v, want unsupported generator diagnostic", diags)
	}
}

func TestGenerateMatrixGeneratorReportsUnsupportedNestedChildWhenFirstChildEmpty(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed-matrix-empty-nested
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: supported
    - matrix:
        generators:
          - list:
              elements: []
          - merge:
              mergeKeys: ["name"]
              generators:
                - list:
                    elements:
                      - name: unsupported
                - scmProvider: {}
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"supported"}) {
		t.Fatalf("generated names = %#v, want supported", got)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("diagnostics = %#v, want unsupported generator diagnostic", diags)
	}
}

func TestGenerateMatrixGeneratorPreservesAllChildSourcePaths(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "apps", "alpha"),
		filepath.Join(root, "clusters", "dev"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-paths
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - git:
              pathParamPrefix: app
              directories:
                - path: apps/*
          - git:
              pathParamPrefix: cluster
              directories:
                - path: clusters/*
  template:
    metadata:
      name: '{{.app.path.basename}}-{{.cluster.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{.app.path.path}}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.cluster.path.basename}}'
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
	if apps[0].SourcePath != "apps/alpha" {
		t.Fatalf("SourcePath = %q, want primary apps/alpha", apps[0].SourcePath)
	}
	if !slices.Equal(apps[0].SourcePaths, []string{"apps/alpha", "clusters/dev"}) {
		t.Fatalf("SourcePaths = %#v, want apps and clusters", apps[0].SourcePaths)
	}
}

func TestGenerateMatrixGeneratorAppliesSelectorAfterCombination(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-selected
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - app: alpha
                - app: beta
          - list:
              elements:
                - env: dev
                - env: prod
      selector:
        matchLabels:
          env: prod
  template:
    metadata:
      name: '{{.app}}-{{.env}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.app}}/{{.env}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.env}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-prod", "beta-prod"}) {
		t.Fatalf("generated names = %#v, want prod-only matrix output", got)
	}
}

func TestGenerateMatrixGeneratorTemplateOverridesBaseTemplate(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-template
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - app: alpha
          - list:
              elements:
                - env: prod
        template:
          spec:
            destination:
              namespace: matrix
  template:
    metadata:
      name: '{{.app}}-{{.env}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.app}}/{{.env}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: base
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if apps[0].Application.Spec.Destination.Namespace != "matrix" {
		t.Fatalf("namespace = %q, want matrix", apps[0].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateMatrixGeneratorSupportsNestedMergeChild(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-nested-merge
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - app: alpha
          - merge:
              mergeKeys: ["env"]
              generators:
                - list:
                    elements:
                      - env: dev
                        namespace: base
                      - env: prod
                        namespace: base
                - list:
                    elements:
                      - env: prod
                        namespace: override
  template:
    metadata:
      name: '{{.app}}-{{.env}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.app}}/{{.env}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.namespace}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-dev", "alpha-prod"}) {
		t.Fatalf("generated names = %#v, want alpha-dev alpha-prod", got)
	}
	if apps[1].Application.Spec.Destination.Namespace != "override" {
		t.Fatalf("prod namespace = %q, want override", apps[1].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateMatrixGeneratorInterpolatesListElementsYaml(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-elements-yaml
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - app: alpha
                  overlays:
                    - env: dev
                    - env: prod
          - list:
              elementsYaml: '{{.overlays | toJson}}'
  template:
    metadata:
      name: '{{.app}}-{{.env}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.app}}/{{.env}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.env}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-dev", "alpha-prod"}) {
		t.Fatalf("generated names = %#v, want alpha-dev alpha-prod", got)
	}
}

func TestGenerateMergeGeneratorOverlaysByMergeKeyInBaseOrder(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merge-apps
spec:
  goTemplate: true
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha
                  namespace: base
                  enabled: "true"
                - name: beta
                  namespace: base
                  enabled: "true"
          - list:
              elements:
                - name: beta
                  namespace: override
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.namespace}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("generated names = %#v, want base order alpha beta", got)
	}
	if apps[0].Application.Spec.Destination.Namespace != "base" {
		t.Fatalf("alpha namespace = %q, want base", apps[0].Application.Spec.Destination.Namespace)
	}
	if apps[1].Application.Spec.Destination.Namespace != "override" {
		t.Fatalf("beta namespace = %q, want override", apps[1].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateMergeGeneratorRejectsInvalidConfiguration(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "no merge keys",
			yaml: `
        generators:
          - list:
              elements:
                - name: alpha
          - list:
              elements:
                - name: alpha`,
			want: "merge requires at least one merge key",
		},
		{
			name: "one child",
			yaml: `
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha`,
			want: "merge requires two or more",
		},
		{
			name: "duplicate base key",
			yaml: `
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha
                - name: alpha
          - list:
              elements:
                - name: alpha`,
			want: "parameters from a generator were not unique",
		},
		{
			name: "duplicate override key",
			yaml: `
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha
          - list:
              elements:
                - name: alpha
                - name: alpha`,
			want: "parameters from a generator were not unique",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merge-invalid
spec:
  goTemplate: true
  generators:
    - merge:` + tc.yaml + `
  template:
    metadata:
      name: '{{.name}}'
`)

			_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
			if err == nil {
				t.Fatalf("expected merge error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGenerateMergeGeneratorIgnoresOverrideKeysMissingFromBase(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merge-missing
spec:
  goTemplate: true
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha
                  namespace: base
          - list:
              elements:
                - name: beta
                  namespace: ignored
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.namespace}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("generated names = %#v, want alpha", got)
	}
	if apps[0].Application.Spec.Destination.Namespace != "base" {
		t.Fatalf("namespace = %q, want base", apps[0].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateMergeGeneratorMergesNestedMapsInGoTemplateMode(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merge-nested
spec:
  goTemplate: true
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha
                  cluster:
                    name: base
                    region: us
          - list:
              elements:
                - name: alpha
                  cluster:
                    name: override
  template:
    metadata:
      name: '{{.name}}'
      labels:
        cluster: '{{.cluster.name}}'
        region: '{{.cluster.region}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
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
	if apps[0].Application.Labels["cluster"] != "override" || apps[0].Application.Labels["region"] != "us" {
		t.Fatalf("labels = %#v, want nested merge with override cluster and inherited region", apps[0].Application.Labels)
	}
}

func TestGenerateMergeGeneratorKeepsSupportedTopLevelOutputWhenChildUnsupported(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed-merge
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: supported
    - merge:
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: unsupported
          - scmProvider: {}
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"supported"}) {
		t.Fatalf("generated names = %#v, want supported", got)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("diagnostics = %#v, want unsupported generator diagnostic", diags)
	}
}

func TestGenerateMergeGeneratorAppliesSelectorAfterMerge(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merge-selected
spec:
  goTemplate: true
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha
                  enabled: "false"
                - name: beta
                  enabled: "false"
          - list:
              elements:
                - name: beta
                  enabled: "true"
      selector:
        matchLabels:
          enabled: "true"
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
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
	if got := generatedNames(apps); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("generated names = %#v, want beta", got)
	}
}

func TestGenerateMergeGeneratorTemplateOverridesBaseTemplate(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merge-template
spec:
  goTemplate: true
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - list:
              elements:
                - name: alpha
                  namespace: base
          - list:
              elements:
                - name: alpha
                  namespace: override
        template:
          spec:
            destination:
              namespace: merge
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: base
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if apps[0].Application.Spec.Destination.Namespace != "merge" {
		t.Fatalf("namespace = %q, want merge", apps[0].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateMergeGeneratorSupportsNestedMatrixChild(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: merge-nested-matrix
spec:
  goTemplate: true
  generators:
    - merge:
        mergeKeys: ["name"]
        generators:
          - matrix:
              generators:
                - list:
                    elements:
                      - app: alpha
                - list:
                    elements:
                      - name: alpha-dev
                        env: dev
                        namespace: base
                      - name: alpha-prod
                        env: prod
                        namespace: base
          - list:
              elements:
                - name: alpha-prod
                  namespace: override
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{.app}}/{{.env}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.namespace}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-dev", "alpha-prod"}) {
		t.Fatalf("generated names = %#v, want alpha-dev alpha-prod", got)
	}
	if apps[1].Application.Spec.Destination.Namespace != "override" {
		t.Fatalf("prod namespace = %q, want override", apps[1].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateGitFilesGeneratorOrdersExcludesAndSetsGoTemplateParams(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "configs", "b", "app.yaml"), `cluster:
  name: beta
  env: prod
app:
  path: apps/beta
`)
	writeAppsetTestFile(t, filepath.Join(root, "configs", "a", "app.yaml"), `cluster:
  name: alpha
  env: dev
app:
  path: apps/alpha
`)
	writeAppsetTestFile(t, filepath.Join(root, "configs", "skip.yaml"), `cluster:
  name: skip
  env: test
app:
  path: apps/skip
`)
	writeAppsetTestFile(t, filepath.Join(root, "extras", "c", "app.yaml"), `cluster:
  name: gamma
  env: qa
app:
  path: apps/gamma
`)
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git-files
spec:
  goTemplate: true
  generators:
    - git:
        pathParamPrefix: repo
        files:
          - path: configs/**/*.yaml
          - path: configs/skip.yaml
            exclude: true
        values:
          envBase: '{{.cluster.env}}-{{.repo.path.basename}}'
    - git:
        pathParamPrefix: repo
        files:
          - path: extras/**/*.yaml
        values:
          envBase: '{{.cluster.env}}-{{.repo.path.basename}}'
  template:
    metadata:
      name: '{{.repo.path.filenameNormalized}}-{{.cluster.name}}'
      labels:
        path: '{{.repo.path.path}}'
        base: '{{.repo.path.basename}}'
        file: '{{.repo.path.filename}}'
        seg0: '{{index .repo.path.segments 0}}'
      annotations:
        values: '{{.values.envBase}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{.app.path}}'
        targetRevision: main
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
	if got := generatedNames(apps); !slices.Equal(got, []string{"app.yaml-alpha", "app.yaml-beta", "app.yaml-gamma"}) {
		t.Fatalf("generated names = %#v, want deterministic alpha beta gamma", got)
	}
	first := apps[0].Application
	if first.Labels["path"] != "configs/a" || first.Labels["base"] != "a" || first.Labels["file"] != "app.yaml" || first.Labels["seg0"] != "configs" {
		t.Fatalf("path labels = %#v", first.Labels)
	}
	if first.Annotations["values"] != "dev-a" {
		t.Fatalf("values annotation = %q, want dev-a", first.Annotations["values"])
	}
	if first.Spec.GetSource().Path != "apps/alpha" {
		t.Fatalf("source path = %q, want apps/alpha", first.Spec.GetSource().Path)
	}
}

func TestGenerateGitFilesGeneratorSetsNonGoTemplateParams(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "team-one", "config.json"), `{"cluster":{"name":"dev"},"app":{"path":"apps/dev"}}`)
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git-files
spec:
  generators:
    - git:
        pathParamPrefix: repo
        files:
          - path: clusters/*/*.json
        values:
          summary: '{{cluster.name}}-{{repo.path.basename}}'
  template:
    metadata:
      name: '{{cluster.name}}-{{repo.path.filenameNormalized}}'
      labels:
        path: '{{repo.path}}'
        base: '{{repo.path.basename}}'
        file: '{{repo.path.filename}}'
        seg1: '{{repo.path[1]}}'
      annotations:
        values: '{{values.summary}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{app.path}}'
        targetRevision: main
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
	if app.Name != "dev-config.json" {
		t.Fatalf("name = %q, want dev-config.json", app.Name)
	}
	if app.Labels["path"] != "clusters/team-one" || app.Labels["base"] != "team-one" || app.Labels["file"] != "config.json" || app.Labels["seg1"] != "team-one" {
		t.Fatalf("labels = %#v", app.Labels)
	}
	if app.Annotations["values"] != "dev-team-one" {
		t.Fatalf("values annotation = %q, want dev-team-one", app.Annotations["values"])
	}
}

func TestGenerateGitFilesGeneratorReportsInvalidFiles(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "files", "array.yaml"), `- item`)
	writeAppsetTestFile(t, filepath.Join(root, "files", "empty.yaml"), ``)
	writeAppsetTestFile(t, filepath.Join(root, "files", "scalar.yaml"), `value`)
	writeAppsetTestFile(t, filepath.Join(root, "files", "invalid.json"), `{"broken":`)
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: invalid-files
spec:
  goTemplate: true
  generators:
    - git:
        files:
          - path: files/*
  template:
    metadata:
      name: '{{.name}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 4 {
		t.Fatalf("len(diags) = %d, want 4: %#v", len(diags), diags)
	}
}

func TestGenerateGitFilesGeneratorReportsSymlinksAndRootEscapes(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "files", "valid.yaml"), `name: valid`)
	writeAppsetTestFile(t, filepath.Join(root, "outside.yaml"), `name: outside`)
	if err := os.Symlink(filepath.Join(root, "outside.yaml"), filepath.Join(root, "files", "linked.yaml")); err != nil {
		t.Fatalf("Symlink(file) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "external"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "external"), filepath.Join(root, "files", "linked-dir")); err != nil {
		t.Fatalf("Symlink(dir) error = %v", err)
	}
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: safe-files
spec:
  goTemplate: true
  generators:
    - git:
        files:
          - path: files/**/*.yaml
          - path: ../outside.yaml
  template:
    metadata:
      name: '{{.name}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"valid"}) {
		t.Fatalf("generated names = %#v, want only valid", got)
	}
	if len(diags) < 2 {
		t.Fatalf("len(diags) = %d, want symlink/root escape diagnostics: %#v", len(diags), diags)
	}
}

func generatedNames(apps []GeneratedApplication) []string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		names = append(names, app.Application.Name)
	}
	return names
}

func writeAppsetTestFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
