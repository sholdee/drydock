package appset

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
func TestGenerateMatrixGeneratorRejectsInvalidNestedChildWhenFirstChildEmpty(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: invalid-nested-matrix
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements: []
          - matrix:
              generators:
                - list:
                    elements:
                      - name: alpha
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

	_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil || !strings.Contains(err.Error(), "matrix support only two child generators") {
		t.Fatalf("GenerateFromYAML() error = %v, want nested matrix child count error", err)
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
