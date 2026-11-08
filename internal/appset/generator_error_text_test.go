package appset

import (
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// generatorFailedMessage returns the Argo CD message that an
// appset.generator-failed warning carries.
func generatorFailedMessage(t *testing.T, diags []diagnostic.Diagnostic) string {
	t.Helper()
	const prefix = "ApplicationSet generator failed: "
	const suffix = "; this ApplicationSet contributes no Applications"
	for _, diag := range diags {
		if diagnostic.StableCode(diag) != diagnostic.CodeAppSetGeneratorFailed {
			continue
		}
		message, ok := strings.CutPrefix(diag.Message, prefix)
		if !ok {
			t.Fatalf("diagnostic message = %q, want prefix %q", diag.Message, prefix)
		}
		before, _, found := strings.CutLast(message, suffix)
		if !found {
			t.Fatalf("diagnostic message = %q, want suffix %q", diag.Message, suffix)
		}
		return before
	}
	t.Fatalf("diagnostics = %#v, want an appset.generator-failed warning", diags)
	return ""
}

// TestGeneratorErrorTextMatchesArgoCD pins the exact text of the Argo CD
// v3.6.1 errors, with the wraps that the matrix and merge generators and
// their getParams add (applicationset/generators/matrix.go:23-24, :55-58,
// :80-82, :117-139; merge.go:19-21, :39-76, :137-177; list.go:44-86;
// generator_spec_processor.go:34-37), including the upstream typos.
func TestGeneratorErrorTextMatchesArgoCD(t *testing.T) {
	for _, tc := range []struct {
		name       string
		goTemplate bool
		generators string
		want       string
	}{
		{
			name: "matrix with one child",
			generators: `
    - matrix:
        generators:
          - list: {elements: [{name: a}]}`,
			want: "found less than two generators, Matrix support only two",
		},
		{
			name: "matrix with three children",
			generators: `
    - matrix:
        generators:
          - list: {elements: [{name: a}]}
          - list: {elements: [{env: b}]}
          - list: {elements: [{zone: c}]}`,
			want: "found more than two generators, Matrix support only two",
		},
		{
			name: "matrix first child empty",
			generators: `
    - matrix:
        generators:
          - {}
          - list: {elements: [{name: a}]}`,
			want: "error failed to get params for first generator in matrix generator: child generator generated no parameters",
		},
		{
			name: "matrix second child empty",
			generators: `
    - matrix:
        generators:
          - list: {elements: [{name: a}]}
          - {}`,
			want: "failed to get params for second generator in the matrix generator: child generator generated no parameters",
		},
		{
			name: "matrix empty child with a bad selector reports the selector",
			generators: `
    - matrix:
        generators:
          - selector: {matchExpressions: [{key: a, operator: Sometimes}]}
          - list: {elements: [{name: a}]}`,
			want: `error failed to get params for first generator in matrix generator: child generator returned an error on parameter generation: error parsing label selector: "Sometimes" is not a valid pod selector operator`,
		},
		{
			name: "matrix child with two generators",
			generators: `
    - matrix:
        generators:
          - list: {elements: [{name: a}]}
            clusters: {}
          - list: {elements: [{env: b}]}`,
			want: "error failed to get params for first generator in matrix generator: found more than one generator in matrix.Generators",
		},
		{
			name: "matrix nested too deep",
			generators: `
    - matrix:
        generators:
          - list: {elements: [{env: dev}]}
          - matrix:
              generators:
                - list: {elements: [{name: a}]}
                - matrix:
                    generators:
                      - list: {elements: [{region: east}]}
                      - list: {elements: [{zone: a}]}`,
			want: "failed to get params for second generator in the matrix generator: child generator returned an error on parameter generation: failed to get params for second generator in the matrix generator: child generator generated no parameters",
		},
		{
			name: "fasttemplate matrix key clash",
			generators: `
    - matrix:
        generators:
          - list: {elements: [{name: a, env: dev}]}
          - list: {elements: [{env: prod}]}`,
			want: "failed to combine string maps with merging params for the matrix generator: found duplicate key env with different value, a: dev ,b: prod",
		},
		{
			name: "merge with one child",
			generators: `
    - merge:
        mergeKeys: [name]
        generators:
          - list: {elements: [{name: a}]}`,
			want: "found less than two generators, Merge requires two or more",
		},
		{
			name: "merge second child empty",
			generators: `
    - merge:
        mergeKeys: [name]
        generators:
          - list: {elements: [{name: a}]}
          - {}`,
			want: "error getting param sets from generators: error getting params from generator 2 of 2: child generator generated no parameters",
		},
		{
			name: "merge without merge keys",
			generators: `
    - merge:
        generators:
          - list: {elements: [{name: a}]}
          - list: {elements: [{name: a}]}`,
			want: "error getting param sets by merge key: no merge keys were specified, Merge requires at least one",
		},
		{
			name: "merge child error before missing merge keys",
			generators: `
    - merge:
        generators:
          - list: {elements: [{name: a}]}
          - {}`,
			want: "error getting param sets from generators: error getting params from generator 2 of 2: child generator generated no parameters",
		},
		{
			name: "merge duplicate key in a later child",
			generators: `
    - merge:
        mergeKeys: [name]
        generators:
          - list: {elements: [{name: a}]}
          - list: {elements: [{name: a}, {name: a}]}`,
			want: `error getting param sets by merge key: the parameters from a generator were not unique by the given mergeKeys, Merge requires all param sets to be unique. Duplicate key was {"name":"a"}`,
		},
		{
			name: "list value that is not a string",
			generators: `
    - list: {elements: [{name: a, replicas: 2}]}`,
			want: "error parsing value as string %!w(<nil>)",
		},
		{
			name: "list values that are not a map",
			generators: `
    - list: {elements: [{name: a, values: text}]}`,
			want: "error parsing values map",
		},
		{
			name: "list element null",
			generators: `
    - list: {elements: [null]}`,
			want: "error unmarshling list element unexpected end of JSON input",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-text
spec:
  generators:` + tc.generators + `
  template:
    metadata:
      name: 'app-{{name}}'
    spec:
      project: default
      source: {repoURL: https://github.com/example/repo, path: apps, targetRevision: main}
      destination: {name: in-cluster, namespace: default}
`)
			apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
			if err != nil || len(apps) != 0 {
				t.Fatalf("GenerateFromYAML() = %#v, %v; want no Applications and no error", generatedNames(apps), err)
			}
			if got := generatorFailedMessage(t, diags); got != tc.want {
				t.Fatalf("message = %q\nwant      %q", got, tc.want)
			}
		})
	}
}

const noFixtureAppSetHead = `
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: no-fixture
spec:
  goTemplate: true
  template:
    metadata:
      name: "app-{{.name}}"
    spec:
      project: default
      source: {repoURL: https://github.com/example/repo, path: apps, targetRevision: HEAD}
      destination: {server: https://kubernetes.default.svc, namespace: d}
  generators:
    - list:
        elements:
          - name: sibling
`

// Argo CD runs the spec checks of a generator whatever the cluster or SCM
// provider returns, so they fail the ApplicationSet also when drydock has no
// provider fixture for the generator.
func TestGeneratorSpecChecksRunWithoutProviderFixture(t *testing.T) {
	for _, tc := range []struct {
		name      string
		generator string
		want      string
	}{
		{
			name: "cluster selector value",
			generator: `
    - clusters:
        selector:
          matchLabels:
            role: https://bad`,
			want: "error getting cluster secrets: error converting label selector: ",
		},
		{
			name: "generator selector on a cluster generator",
			generator: `
    - clusters: {}
      selector:
        matchExpressions:
          - {key: a, operator: Sometimes}`,
			want: `error parsing label selector: "Sometimes" is not a valid pod selector operator`,
		},
		{
			name: "pull request filter",
			generator: `
    - pullRequest:
        github: {owner: o, repo: r}
        filters:
          - branchMatch: "("`,
			want: `error listing repos: error compiling BranchMatch regexp "(": `,
		},
		{
			name: "scm provider filter",
			generator: `
    - scmProvider:
        github: {organization: o}
        filters:
          - labelMatch: "["`,
			want: `error listing repos: error compiling LabelMatch regexp "[": `,
		},
		{
			name: "clusterDecisionResource definition",
			generator: `
    - clusterDecisionResource:
        configMapRef: cm`,
			want: "there is a problem with the definition of the ClusterDecisionResource generator",
		},
		{
			name: "plugin generator selector",
			generator: `
    - plugin:
        configMapRef: {name: plugin}
      selector:
        matchExpressions:
          - {key: a, operator: In}`,
			want: "error parsing label selector: ",
		},
		{
			name: "matrix child selector behind a provider child",
			generator: `
    - matrix:
        generators:
          - clusters: {}
            selector:
              matchExpressions:
                - {key: a, operator: Sometimes}
          - list: {elements: [{env: dev}]}`,
			want: `error failed to get params for first generator in matrix generator: child generator returned an error on parameter generation: error parsing label selector: "Sometimes" is not a valid pod selector operator`,
		},
		{
			name: "merge child error after a provider child",
			generator: `
    - merge:
        mergeKeys: [name]
        generators:
          - clusters: {}
          - {}`,
			want: "error getting param sets from generators: error getting params from generator 2 of 2: child generator generated no parameters",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", []byte(noFixtureAppSetHead+tc.generator))
			if err != nil || len(apps) != 0 {
				t.Fatalf("GenerateFromYAML() = %#v, %v; want no Applications and no error", generatedNames(apps), err)
			}
			if got := generatorFailedMessage(t, diags); !strings.HasPrefix(got, tc.want) {
				t.Fatalf("message = %q, want prefix %q", got, tc.want)
			}
		})
	}
}

// Argo CD v3.6.1 lists the decision resources with
// metav1.FormatLabelSelector of the generator's labelSelector
// (applicationset/generators/duck_type.go:113-127). An empty selector
// formats as "<none>", which the API server rejects.
func TestClusterDecisionResourceEmptyLabelSelectorFailsApplicationSet(t *testing.T) {
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", []byte(noFixtureAppSetHead+`
    - clusterDecisionResource:
        configMapRef: cm
        labelSelector:
          matchLabels: {}`), Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters:         []ClusterInput{{Name: "c1", Server: "https://c1"}},
		ClusterDecisions: []ClusterDecisionInput{{ConfigMapRef: "cm", ResourceName: "r1", MatchKey: "clusterName", Decisions: []map[string]any{{"clusterName": "c1"}}}},
	}}})
	if err != nil || len(apps) != 0 {
		t.Fatalf("GenerateFromYAMLWithOptions() = %#v, %v; want no Applications and no error", generatedNames(apps), err)
	}
	if got := generatorFailedMessage(t, diags); !strings.HasPrefix(got, "failed to get dynamic resources: ") {
		t.Fatalf("message = %q, want the dynamic resource list failure", got)
	}
}

// When the fixture has no decision resource for the configMapRef, it does
// not describe that resource kind, so drydock reports no provider match and
// keeps the other Applications, as before.
func TestClusterDecisionResourceUnknownConfigMapRefIsNoMatch(t *testing.T) {
	apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "app-set.yaml", []byte(noFixtureAppSetHead+`
    - clusterDecisionResource:
        configMapRef: other-cm
        name: r9`), Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters:         []ClusterInput{{Name: "c1", Server: "https://c1"}},
		ClusterDecisions: []ClusterDecisionInput{{ConfigMapRef: "cm", ResourceName: "r1", MatchKey: "clusterName", Decisions: []map[string]any{{"clusterName": "c1"}}}},
	}}})
	if err != nil {
		t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
	}
	if got := generatedNames(apps); len(got) != 1 || got[0] != "app-sibling" {
		t.Fatalf("generated names = %#v, want app-sibling", got)
	}
	if len(diags) != 1 || diags[0].Code != "appset.provider-no-match" {
		t.Fatalf("diagnostics = %#v, want one provider no-match diagnostic", diags)
	}
}
