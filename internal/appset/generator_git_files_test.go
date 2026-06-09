package appset

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

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

func TestGenerateGitGeneratorPrefersDirectoriesOverFiles(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "apps", "alpha", "kustomization.yaml"), `resources: []`)
	writeAppsetTestFile(t, filepath.Join(root, "clusters", "prod.yaml"), `cluster:
  name: prod
`)
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git-directories-precedence
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: apps/*
        files:
          - path: clusters/*.yaml
  template:
    metadata:
      name: '{{.path.basename}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("generated names = %#v, want only directory result", got)
	}
}

func TestGenerateGitFilesGeneratorAcceptsMappingArrayAndEmptyDocuments(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "root.yaml"), `name: root`)
	writeAppsetTestFile(t, filepath.Join(root, "configs", "array.yaml"), `- name: alpha
- name: beta
`)
	writeAppsetTestFile(t, filepath.Join(root, "configs", "empty.yaml"), ``)
	writeAppsetTestFile(t, filepath.Join(root, "configs", "empty-document.yaml"), "---\n")
	writeAppsetTestFile(t, filepath.Join(root, "configs", "empty-object.yaml"), `{}`)
	writeAppsetTestFile(t, filepath.Join(root, "configs", "whitespace.yaml"), " \n\t\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: accepted-files
spec:
  goTemplate: true
  generators:
    - git:
        files:
          - path: '*.yaml'
          - path: configs/*.yaml
  template:
    metadata:
      name: '{{if .name}}{{.name}}{{else}}empty{{end}}-{{.path.filenameNormalized}}'
      annotations:
        source-path: '{{.path.path}}'
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-array.yaml", "beta-array.yaml", "empty-empty-document.yaml", "empty-empty-object.yaml", "empty-empty.yaml", "empty-whitespace.yaml", "root-root.yaml"}) {
		t.Fatalf("generated names = %#v, want mapping, array, empty object, empty document, whitespace, and empty file results", got)
	}
	rootApp := apps[len(apps)-1].Application
	if rootApp.Name != "root-root.yaml" || rootApp.Annotations["source-path"] != "." {
		t.Fatalf("root app = %q annotations %#v, want dot source-path", rootApp.Name, rootApp.Annotations)
	}
}

func TestGenerateGitFilesGeneratorUsesDotForRootFilePathParams(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "root.yaml"), `name: root`)
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: root-file-path
spec:
  generators:
    - git:
        files:
          - path: '*.yaml'
  template:
    metadata:
      name: '{{name}}'
      annotations:
        path: '{{path}}'
        basename: '{{path.basename}}'
        seg0: '{{path[0]}}'
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
	annotations := apps[0].Application.Annotations
	if annotations["path"] != "." || annotations["basename"] != "." || annotations["seg0"] != "." {
		t.Fatalf("root path annotations = %#v, want dot path metadata", annotations)
	}
}
func TestGenerateGitFilesGeneratorReportsInvalidFiles(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "files", "bad-array.yaml"), `- name: ok
- item
`)
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
	if len(diags) != 3 {
		t.Fatalf("len(diags) = %d, want 3: %#v", len(diags), diags)
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
func TestCleanGitFilePatternTrimsAndNormalizesSlashPath(t *testing.T) {
	got, err := cleanGitFilePattern(" configs/../apps/app.yaml ")
	if err != nil {
		t.Fatalf("cleanGitFilePattern() error = %v", err)
	}
	if got != "apps/app.yaml" {
		t.Fatalf("cleanGitFilePattern() = %q, want apps/app.yaml", got)
	}
}
