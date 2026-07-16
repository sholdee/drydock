package appset

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
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

func TestMatchGitFilesSkipsDotGitDirectory(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, ".git", "config.json"), `{}`)
	writeAppsetTestFile(t, filepath.Join(root, ".github", "config.json"), `{}`)
	writeAppsetTestFile(t, filepath.Join(root, "apps", "config.json"), `{}`)

	matches, _, err := matchGitFiles(root, []string{"**/config.json"}, nil, "appset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)
	want := []string{".github/config.json", "apps/config.json"}
	if !slices.Equal(matches, want) {
		t.Fatalf("matches = %v, want %v", matches, want)
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
func missingKeyGitFilesAppSet(paramTemplate string) []byte {
	return []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: kargo-pipelines
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        files:
          - path: pipelines/*.yaml
  template:
    metadata:
      name: '` + paramTemplate + `'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: 'apps/{{ .path.filename }}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

func assertTemplateRenderFailedDiagnostic(t *testing.T, diag diagnostic.Diagnostic, sourcePath string) {
	t.Helper()
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
	}
	if diag.Category != "appset" {
		t.Fatalf("diagnostic category = %q, want appset", diag.Category)
	}
	if diag.Provenance.Path != "app-set.yaml" {
		t.Fatalf("diagnostic provenance path = %q, want app-set.yaml", diag.Provenance.Path)
	}
	if diag.Provenance.Pointer != "spec.template" {
		t.Fatalf("diagnostic pointer = %q, want spec.template", diag.Provenance.Pointer)
	}
	if !strings.Contains(diag.Message, sourcePath) {
		t.Fatalf("diagnostic message = %q, want source path %q", diag.Message, sourcePath)
	}
	if !strings.Contains(diag.Message, "contributes no Applications") {
		t.Fatalf("diagnostic message = %q, want contributes no Applications consequence", diag.Message)
	}
	if code := diagnostic.StableCode(diag); code != "appset.template-render-failed" {
		t.Fatalf("stable code = %q, want appset.template-render-failed", code)
	}
}

func TestGenerateGitFilesCommentOnlyFileScopesTemplateErrorToAppSet(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "pipelines.yaml"), "# none yet\n")

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", missingKeyGitFilesAppSet("kargo-{{ .name }}"))
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v, want appset-scoped warning", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	assertTemplateRenderFailedDiagnostic(t, diags[0], "pipelines/pipelines.yaml")
}

func TestGenerateGitFilesEmptyContentVariantsScopeTemplateErrorToAppSet(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
	}{
		{name: "blank", content: ""},
		{name: "document separator", content: "---\n"},
		{name: "document terminator", content: "...\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeAppsetTestFile(t, filepath.Join(root, "pipelines", "pipelines.yaml"), tt.content)

			apps, diags, err := GenerateFromYAML(root, "app-set.yaml", missingKeyGitFilesAppSet("kargo-{{ .name }}"))
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v, want appset-scoped warning", err)
			}
			if len(apps) != 0 {
				t.Fatalf("generated apps = %#v, want none", apps)
			}
			if len(diags) != 1 {
				t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
			}
			assertTemplateRenderFailedDiagnostic(t, diags[0], "pipelines/pipelines.yaml")
		})
	}
}

func TestGenerateGitFilesEmptyFileWithPathOnlyTemplateRendersApplication(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
	}{
		{name: "blank", content: ""},
		{name: "comment only", content: "# none yet\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeAppsetTestFile(t, filepath.Join(root, "pipelines", "pipelines.yaml"), tt.content)

			apps, diags, err := GenerateFromYAML(root, "app-set.yaml", missingKeyGitFilesAppSet("kargo-{{ .path.filename }}"))
			if err != nil {
				t.Fatalf("GenerateFromYAML() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v, want none", diags)
			}
			if got := generatedNames(apps); !slices.Equal(got, []string{"kargo-pipelines.yaml"}) {
				t.Fatalf("generated names = %#v, want Argo CD parity single Application", got)
			}
		})
	}
}

func TestGenerateGitFilesTemplateRenderFailureDiscardsWholeAppSet(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "healthy.yaml"), "name: alpha\n")
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "pending.yaml"), "# none yet\n")

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", missingKeyGitFilesAppSet("kargo-{{ .name }}"))
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v, want appset-scoped warning", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want whole ApplicationSet discarded", apps)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	assertTemplateRenderFailedDiagnostic(t, diags[0], "pipelines/pending.yaml")
}

func TestGenerateGitFilesEmptyListFileYieldsZeroApplicationsWithoutDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "pipelines.yaml"), "[]\n")

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", missingKeyGitFilesAppSet("kargo-{{ .name }}"))
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestGenerateTemplateRenderFailureDoesNotTripUnsupportedGenerator(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "pipelines.yaml"), "# none yet\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed-failure
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - scmProvider: {}
    - git:
        files:
          - path: pipelines/*.yaml
  template:
    metadata:
      name: 'kargo-{{ .name }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: 'apps/{{ .path.filename }}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v, want nil despite unsupported generator", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	var sawUnsupported, sawTemplateFailure bool
	for _, diag := range diags {
		sawUnsupported = sawUnsupported || strings.Contains(diag.Message, "unsupported ApplicationSet generator")
		sawTemplateFailure = sawTemplateFailure || strings.Contains(diag.Message, "template render failed")
	}
	if !sawUnsupported || !sawTemplateFailure {
		t.Fatalf("diagnostics = %#v, want unsupported-generator and template render failed warnings", diags)
	}
}

func TestGenerateGitFilesCollectsAllTemplateRenderFailures(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "first.yaml"), "# none yet\n")
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "second.yaml"), "# none yet\n")

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", missingKeyGitFilesAppSet("kargo-{{ .name }}"))
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v, want appset-scoped warnings", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", apps)
	}
	if len(diags) != 2 {
		t.Fatalf("len(diags) = %d, want one per failing param file: %#v", len(diags), diags)
	}
	assertTemplateRenderFailedDiagnostic(t, diags[0], "pipelines/first.yaml")
	assertTemplateRenderFailedDiagnostic(t, diags[1], "pipelines/second.yaml")
}

func TestGenerateGitFilesValuesTemplatingFailureStaysFatal(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "pipelines", "pipelines.yaml"), "name: alpha\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: fatal-values
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        files:
          - path: pipelines/*.yaml
        values:
          bad: '{{ .missing.value }}'
  template:
    metadata:
      name: 'kargo-{{ .name }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: 'apps/{{ .path.filename }}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil {
		t.Fatal("GenerateFromYAML() error = nil, want fatal values-templating error")
	}
	if !strings.Contains(err.Error(), `render value "bad"`) {
		t.Fatalf("error = %v, want values-templating render failure", err)
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
