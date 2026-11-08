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
// provider fixture for the generator. The pull request and SCM provider
// texts assume that Argo CD selects the provider without error first, and
// the clusterDecisionResource text assumes that it reads the configMapRef
// ConfigMap (duck_type.go:80-97). If those cluster lookups fail, Argo CD
// reports their error instead, and the ApplicationSet still fails.
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

// TestGeneratorErrorTextPrefixMatchesArgoCD pins errors whose end comes from
// encoding/json, so only the Argo CD part of the text is fixed.
func TestGeneratorErrorTextPrefixMatchesArgoCD(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generators string
		want       string
	}{
		{
			// getMatrixGenerator returns the decode error as it is
			// (matrix.go:308-317).
			name: "matrix child matrix that does not decode",
			generators: `
    - matrix:
        generators:
          - matrix: "str"
          - list: {elements: [{name: a}]}`,
			want: "error failed to get params for first generator in matrix generator: json: cannot unmarshal",
		},
		{
			// nestedGeneratorSpec wraps a merge decode error
			// (matrix.go:146-149).
			name: "matrix child merge that does not decode",
			generators: `
    - matrix:
        generators:
          - merge: "str"
          - list: {elements: [{name: a}]}`,
			want: "error failed to get params for first generator in matrix generator: error retrieving merge generator: error converting to nested merge generator: json: cannot unmarshal",
		},
		{
			// The merge generator's getParams does not wrap either decode
			// error (merge.go:137-145).
			name: "merge child merge that does not decode",
			generators: `
    - merge:
        mergeKeys: [name]
        generators:
          - merge: "str"
          - list: {elements: [{name: a}]}`,
			want: "error getting param sets from generators: error getting params from generator 1 of 2: error converting to nested merge generator: json: cannot unmarshal",
		},
		{
			name: "merge child matrix that does not decode",
			generators: `
    - merge:
        mergeKeys: [name]
        generators:
          - matrix: "str"
          - list: {elements: [{name: a}]}`,
			want: "error getting param sets from generators: error getting params from generator 1 of 2: json: cannot unmarshal",
		},
		{
			// Transform runs both generators of a two-generator child, and
			// getParams reports the first error before the count
			// (generator_spec_processor.go:43-101, matrix.go:125-135).
			name: "two-generator child whose list fails",
			generators: `
    - matrix:
        generators:
          - list:
              elementsYaml: "notalist"
            clusters: {}
          - list: {elements: [{name: a}]}`,
			want: "error failed to get params for first generator in matrix generator: child generator returned an error on parameter generation: error unmarshling decoded ElementsYaml ",
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
			if got := generatorFailedMessage(t, diags); !strings.HasPrefix(got, tc.want) {
				t.Fatalf("message = %q\nwant prefix %q", got, tc.want)
			}
		})
	}
}

// Argo CD v3.6.1 checks the merge keys after every child ran
// (merge.go:62-76 and :106-109). A missing merge key list, or a repeated
// merge key value in a child that drydock can run, fails the ApplicationSet
// whatever a child that needs provider data returns.
func TestMergeKeyChecksRunBeforeUnsupportedChild(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mergeKeys string
		elements  string
		want      string
	}{
		{
			name:     "no merge keys",
			elements: "[{name: a}]",
			want:     "error getting param sets by merge key: no merge keys were specified, Merge requires at least one",
		},
		{
			name:      "repeated merge key value",
			mergeKeys: "mergeKeys: [name]",
			elements:  "[{name: a}, {name: a}]",
			want:      `error getting param sets by merge key: the parameters from a generator were not unique by the given mergeKeys, Merge requires all param sets to be unique. Duplicate key was {"name":"a"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", []byte(noFixtureAppSetHead+`
    - merge:
        `+tc.mergeKeys+`
        generators:
          - clusters: {}
          - list: {elements: `+tc.elements+`}`))
			if err != nil || len(apps) != 0 {
				t.Fatalf("GenerateFromYAML() = %#v, %v; want no Applications and no error", generatedNames(apps), err)
			}
			if got := generatorFailedMessage(t, diags); got != tc.want {
				t.Fatalf("message = %q\nwant      %q", got, tc.want)
			}
		})
	}
	// Control: valid merge keys leave the ApplicationSet unsupported.
	apps, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", []byte(noFixtureAppSetHead+`
    - merge:
        mergeKeys: [name]
        generators:
          - clusters: {}
          - list: {elements: [{name: a}]}`))
	if err != nil || len(apps) != 1 || len(diags) != 1 || diagnostic.StableCode(diags[0]) != "appset.unsupported-generator" {
		t.Fatalf("GenerateFromYAML() = %#v, %#v, %v; want the sibling and one unsupported generator warning", generatedNames(apps), diags, err)
	}
}

// Argo CD walks a list element in Go map order, so which error it reports
// for an element with two bad fields varies. drydock reports the first in
// key order every time.
func TestListElementErrorIsStable(t *testing.T) {
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: error-text
spec:
  generators:
    - list: {elements: [{name: a, a: 1, b: 2, c: 3, d: 4, e: 5, values: text}]}
  template:
    metadata:
      name: 'app-{{name}}'
`)
	for range 40 {
		_, diags, err := GenerateFromYAML(t.TempDir(), "app-set.yaml", data)
		if err != nil {
			t.Fatal(err)
		}
		if got := generatorFailedMessage(t, diags); got != "error parsing value as string %!w(<nil>)" {
			t.Fatalf("message = %q, want the first key in order (a)", got)
		}
	}
}
