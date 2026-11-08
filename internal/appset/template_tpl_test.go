package appset

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// Argo CD v3.6 adds a helm-style `tpl` function to ApplicationSet Go
// templates (applicationset/utils/template_functions.go, commit 182883123).
// drydock renders through the upstream appsetutils.Render, so these tests pin
// that it reaches every templating path: template, templatePatch, generator
// values, and matrix child interpolation.

func tplListAppSet(goTemplateOptions, elements, values, templateName, templatePatch string) []byte {
	return []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: tpl
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ` + goTemplateOptions + `
  generators:
    - list:
        elements:
` + elements + values + `
  template:
    metadata:
      name: ` + templateName + `
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
` + templatePatch)
}

func TestGenerateSupportsTplInTemplate(t *testing.T) {
	data := tplListAppSet(`["missingkey=error"]`, `          - name: alpha
            nameTemplate: '{{ .name }}-{{ .env }}'
            env: dev`, "", `'{{ tpl .nameTemplate . }}'`, "")

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-dev"}) {
		t.Fatalf("generated names = %#v, want tpl-rendered name", got)
	}
}

func TestGenerateSupportsTplInTemplatePatch(t *testing.T) {
	data := tplListAppSet(`[]`, `          - name: alpha
            pathTemplate: 'apps/{{ .name | upper }}'`, "", `'{{ .name }}'`, `  templatePatch: |
    spec:
      source:
        path: '{{ tpl .pathTemplate . }}'
`)

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 || len(apps) != 1 {
		t.Fatalf("apps = %#v diagnostics = %#v", generatedNames(apps), diags)
	}
	if got := apps[0].Application.Spec.GetSource().Path; got != "apps/ALPHA" {
		t.Fatalf("templatePatch tpl path = %q, want apps/ALPHA", got)
	}
}

func TestGenerateSupportsTplInGeneratorValues(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "configs", "alpha.yaml"), "name: alpha\nsuffix: '{{ .name }}-svc'\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: tpl-values
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        files:
          - path: configs/*.yaml
        values:
          service: '{{ tpl .suffix . }}'
  template:
    metadata:
      name: '{{ .values.service }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
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
	if got := generatedNames(apps); !slices.Equal(got, []string{"alpha-svc"}) {
		t.Fatalf("generated names = %#v, want tpl-rendered generator value", got)
	}
}

func tplMatrixAppSet(interpolatedPath string) []byte {
	return []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: tpl-matrix
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
                  dirTemplate: 'apps/{{ .env }}/*'
          - git:
              directories:
                - path: '` + interpolatedPath + `'
  template:
    metadata:
      name: '{{ .env }}-{{ .path.basename }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{ .path.path }}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

func TestGenerateSupportsTplInMatrixInterpolation(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "apps", "dev", "alpha", "kustomization.yaml"), "resources: []\n")
	writeAppsetTestFile(t, filepath.Join(root, "apps", "prod", "beta", "kustomization.yaml"), "resources: []\n")

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", tplMatrixAppSet(`{{ tpl .dirTemplate . }}`))
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"dev-alpha"}) {
		t.Fatalf("generated names = %#v, want matrix child interpolated through tpl", got)
	}
}

func assertTplTemplateRenderFailure(t *testing.T, apps []GeneratedApplication, diags []diagnostic.Diagnostic, err error, wantSubstring string) {
	t.Helper()
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v, want appset-scoped warning", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated apps = %#v, want none", generatedNames(apps))
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one template render failure", diags)
	}
	diag := diags[0]
	if diag.Severity != diagnostic.SeverityWarning || diagnostic.StableCode(diag) != "appset.template-render-failed" {
		t.Fatalf("diagnostic = %#v, want appset.template-render-failed warning", diag)
	}
	if !strings.Contains(diag.Message, wantSubstring) {
		t.Fatalf("diagnostic message = %q, want %q", diag.Message, wantSubstring)
	}
}

func TestGenerateTplCarriesMissingKeyErrorIntoNestedTemplate(t *testing.T) {
	data := tplListAppSet(`["missingkey=error"]`, `          - name: alpha
            nameTemplate: '{{ .name }}-{{ .missing }}'`, "", `'{{ tpl .nameTemplate . }}'`, "")

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	assertTplTemplateRenderFailure(t, apps, diags, err, `map has no entry for key "missing"`)
}

func TestGenerateTplRecursionLimitIsAppSetScoped(t *testing.T) {
	data := tplListAppSet(`[]`, `          - name: alpha
            loop: '{{ tpl .loop . }}'`, "", `'{{ tpl .loop . }}'`, "")

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	assertTplTemplateRenderFailure(t, apps, diags, err, "maximum recursion depth 3 exceeded in tpl function")
}

func TestGenerateTplKeepsRestrictedFunctionMap(t *testing.T) {
	t.Setenv("APPSET_SECRET", "should-not-render")
	data := tplListAppSet(`[]`, `          - name: alpha
            secretTemplate: '{{ env "APPSET_SECRET" }}'`, "", `'{{ tpl .secretTemplate . }}'`, "")

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	assertTplTemplateRenderFailure(t, apps, diags, err, `function "env" not defined`)
	if strings.Contains(diags[0].Message, "should-not-render") {
		t.Fatalf("diagnostic leaked APPSET_SECRET value: %#v", diags[0])
	}
}

func TestGenerateTplTemplatePatchFailureIsAppSetScoped(t *testing.T) {
	data := tplListAppSet(`["missingkey=error"]`, `          - name: alpha
            pathTemplate: 'apps/{{ .missing }}'`, "", `'{{ .name }}'`, `  templatePatch: |
    spec:
      source:
        path: '{{ tpl .pathTemplate . }}'
`)

	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	assertTplTemplateRenderFailure(t, apps, diags, err, "error replacing values in templatePatch")
}

func TestGenerateTplGeneratorValuesFailureIsAppSetScoped(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "configs", "alpha.yaml"), "name: alpha\nsuffix: '{{ .missing }}'\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: tpl-values
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        files:
          - path: configs/*.yaml
        values:
          service: '{{ tpl .suffix . }}'
  template:
    metadata:
      name: '{{ .name }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	assertTplTemplateRenderFailure(t, apps, diags, err, `render value "service"`)
	if diags[0].Provenance.Pointer != "spec.generators" {
		t.Fatalf("diagnostic provenance = %#v, want spec.generators", diags[0].Provenance)
	}
}

func TestGenerateTplMatrixInterpolationFailureIsAppSetScoped(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "apps", "dev", "alpha", "kustomization.yaml"), "resources: []\n")

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", tplMatrixAppSet(`{{ tpl "apps/{{ .missing }}" . }}`))
	assertTplTemplateRenderFailure(t, apps, diags, err, "child generator returned an error on parameter generation: failed to replace parameters in generator")
}

func TestGenerateGeneratorTemplateFailureDropsOtherGeneratorsApplications(t *testing.T) {
	root := t.TempDir()
	writeAppsetTestFile(t, filepath.Join(root, "configs", "alpha.yaml"), "name: alpha\n")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: mixed
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - list:
        elements:
          - name: healthy
    - git:
        files:
          - path: configs/*.yaml
        values:
          bad: '{{ .missing }}'
  template:
    metadata:
      name: '{{ .name }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	assertTplTemplateRenderFailure(t, apps, diags, err, `render value "bad"`)
}
