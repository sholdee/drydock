package appset

import (
	"slices"
	"testing"
)

// These tests pin which generator errors fail the whole ApplicationSet in
// Argo CD v3.6.1, and the selector and generator-field rules of
// generators.Transform (applicationset/generators/generator_spec_processor.go).
// The expected results follow the upstream code paths named on each test.

const errorScopeTemplate = `
  template:
    metadata:
      name: '{{.name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`

func generateErrorScopeAppSet(t *testing.T, generators string) ([]GeneratedApplication, error) {
	t.Helper()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-scope
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:` + generators + errorScopeTemplate)
	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	if err != nil {
		return apps, err
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
	return apps, nil
}

// Transform filters a matrix child's params with the selector of the child
// it was given, which is not interpolated with the first child's params
// (generator_spec_processor.go:34 and :89). The literal "{{.env}}" matches no
// param, so Argo CD generates nothing.
func TestGenerateMatrixChildSelectorIsNotInterpolated(t *testing.T) {
	apps, err := generateErrorScopeAppSet(t, `
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
                - env: prod
          - list:
              elements:
                - name: api-dev
                  env: dev
                - name: api-prod
                  env: prod
            selector:
              matchLabels:
                env: '{{.env}}'`)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated names = %#v, want none: the child selector stays literal", generatedNames(apps))
	}
}

// A literal selector value still filters the child as usual.
func TestGenerateMatrixChildLiteralSelectorFilters(t *testing.T) {
	apps, err := generateErrorScopeAppSet(t, `
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
          - list:
              elements:
                - name: api
                  tier: web
                - name: worker
                  tier: batch
            selector:
              matchLabels:
                tier: web`)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"api"}) {
		t.Fatalf("generated names = %#v, want api", got)
	}
}

// The child's own selector stays literal, but a merge child evaluates its
// own children from the interpolated spec (matrix.go:117-139, merge.go:137-160),
// so the selectors inside it are interpolated.
func TestGenerateMatrixMergeChildInnerSelectorIsInterpolated(t *testing.T) {
	apps, err := generateErrorScopeAppSet(t, `
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
          - merge:
              mergeKeys: [name]
              generators:
                - list:
                    elements:
                      - name: api-dev
                        stage: dev
                      - name: api-prod
                        stage: prod
                  selector:
                    matchLabels:
                      stage: '{{.env}}'
                - list:
                    elements:
                      - name: api-dev`)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"api-dev"}) {
		t.Fatalf("generated names = %#v, want api-dev", got)
	}
}

// A matrix or merge child that names no generator, including a matrix nested
// one level too deep, makes getParams fail with "child generator generated
// no parameters" (matrix.go:129-131, merge.go:169-171). Argo CD then fails the
// whole ApplicationSet, so the sibling list generator generates nothing too.
func TestGenerateEmptyCombinatorChildFailsApplicationSet(t *testing.T) {
	sibling := `
    - list:
        elements:
          - name: sibling`
	for _, tc := range []struct {
		name       string
		generators string
		want       string
	}{
		{
			name: "matrix empty child",
			generators: `
    - matrix:
        generators:
          - list:
              elements:
                - name: alpha
          - {}`,
			want: "child generator generated no parameters",
		},
		{
			name: "matrix selector-only child",
			generators: `
    - matrix:
        generators:
          - {selector: {matchLabels: {name: alpha}}}
          - list:
              elements:
                - env: dev`,
			want: "child generator generated no parameters",
		},
		{
			name: "merge empty child",
			generators: `
    - merge:
        mergeKeys: [name]
        generators:
          - list:
              elements:
                - name: alpha
          - {}`,
			want: "child generator generated no parameters",
		},
		{
			name: "matrix nested too deep",
			generators: `
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
          - matrix:
              generators:
                - list:
                    elements:
                      - name: alpha
                - matrix:
                    generators:
                      - list:
                          elements:
                            - region: east
                      - list:
                          elements:
                            - zone: a`,
			want: "child generator generated no parameters",
		},
		{
			name: "child with two generators",
			generators: `
    - matrix:
        generators:
          - list:
              elements:
                - env: dev
            clusters: {}
          - list:
              elements:
                - name: alpha`,
			want: "found more than one generator in matrix.Generators",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-scope
spec:
  goTemplate: true
  generators:` + sibling + tc.generators + errorScopeTemplate)
			apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
			requireGeneratorFailed(t, apps, diags, err, tc.want)
		})
	}
}

// Transform runs every generator field that one spec.generators entry sets,
// in field order (GetRelevantGenerators, generator_spec_processor.go:104-124),
// so an entry with both list and matrix generates from both.
func TestGenerateTopLevelEntryWithTwoGeneratorsGeneratesFromBoth(t *testing.T) {
	apps, err := generateErrorScopeAppSet(t, `
    - matrix:
        generators:
          - list:
              elements:
                - name: matrix-a
          - list:
              elements:
                - env: dev
      list:
        elements:
          - name: list-a`)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if got := generatedNames(apps); !slices.Equal(got, []string{"list-a", "matrix-a"}) {
		t.Fatalf("generated names = %#v, want list-a then matrix-a", got)
	}
}

// The Argo CD v3.6.1 list generator fails the ApplicationSet when
// elementsYaml does not decode to a list of mappings, even when its elements
// generate (applicationset/generators/list.go:80-86).
func TestGenerateListElementsYamlErrorFailsApplicationSet(t *testing.T) {
	for _, tc := range []struct {
		name         string
		elementsYaml string
	}{
		{name: "invalid YAML", elementsYaml: "[unclosed"},
		{name: "scalar entry", elementsYaml: "- alpha"},
		{name: "mapping instead of list", elementsYaml: "name: alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-scope
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: sibling
    - list:
        elements:
          - name: good
        elementsYaml: '` + tc.elementsYaml + `'` + errorScopeTemplate)
			apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
			requireGeneratorFailed(t, apps, diags, err, "error unmarshling decoded ElementsYaml")
		})
	}
}

// An element written as null decodes to no JSON, which the Argo CD list
// generator cannot unmarshal (list.go:44-47).
func TestGenerateListNullElementFailsApplicationSet(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-scope
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: alpha
          - null` + errorScopeTemplate)
	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	requireGeneratorFailed(t, apps, diags, err, "error unmarshling list element 1")
}

// A selector that does not parse fails the ApplicationSet in Transform before
// the generator runs (generator_spec_processor.go:34-37).
func TestGenerateListInvalidSelectorFailsApplicationSet(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-scope
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: sibling
    - list:
        elements:
          - name: alpha
      selector:
        matchExpressions:
          - key: name
            operator: Sometimes` + errorScopeTemplate)
	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
	requireGeneratorFailed(t, apps, diags, err, `error parsing label selector: "Sometimes" is not a valid pod selector operator`)
}

// The Argo CD v3.6.1 pull request and SCM provider services compile every
// filter regexp before they list anything, and a compile error fails the
// ApplicationSet (applicationset/services/pull_request/utils.go:53-57 and
// scm_provider/utils.go:96-100). It does not matter whether any pull
// request or repository exists.
func TestGenerateProviderInvalidFilterRegexpFailsApplicationSet(t *testing.T) {
	for _, tc := range []struct {
		name      string
		generator string
		want      string
	}{
		{
			name: "pull request branchMatch",
			generator: `
    - pullRequest:
        github:
          owner: example-org
          repo: platform-api
        filters:
          - branchMatch: "feature/("`,
			want: `error listing repos: error compiling BranchMatch regexp "feature/("`,
		},
		{
			name: "pull request titleMatch",
			generator: `
    - pullRequest:
        github:
          owner: example-org
          repo: platform-api
        filters:
          - titleMatch: "[a-"`,
			want: `error compiling TitleMatch regexp "[a-"`,
		},
		{
			name: "scm repositoryMatch",
			generator: `
    - scmProvider:
        github:
          organization: example-org
        filters:
          - repositoryMatch: "api-("`,
			want: `error listing repos: error compiling RepositoryMatch regexp "api-("`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-scope
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: sibling` + tc.generator + errorScopeTemplate)
			apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
				Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
			}}})
			requireGeneratorFailed(t, apps, diags, err, tc.want)
		})
	}
}

// The Argo CD v3.6.1 cluster generator lists cluster Secrets with
// metav1.LabelSelectorAsSelector, which rejects a label value that is not
// valid, and fails the ApplicationSet (applicationset/generators/cluster.go:194-198).
func TestGenerateClusterInvalidSelectorValueFailsApplicationSet(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-scope
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: sibling
    - clusters:
        selector:
          matchLabels:
            server: https://prod-a.example.invalid` + errorScopeTemplate)
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", data, Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "prod-a", Server: "https://prod-a.example.invalid"}},
	}}})
	requireGeneratorFailed(t, apps, diags, err, "error converting label selector")
}
