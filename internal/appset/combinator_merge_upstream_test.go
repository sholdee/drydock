package appset

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dario.cat/mergo"
	"github.com/sholdee/drydock/internal/diagnostic"
)

// These tests compare the Go-template param merge of the drydock matrix and
// merge generators with the merge that Argo CD v3.6.1 runs. Both use
// dario.cat/mergo v1.0.2 with mergo.WithOverride. mergo writes map entries
// through reflect, so when a map[string]any entry goes into a
// map[string]string under the same key, reflect panics. Argo CD panics in
// that case. Its ApplicationSet controller recovers the panic in Reconcile
// (applicationset/controllers/applicationset_controller.go:123-133), so the
// ApplicationSet creates no Applications. drydock must report a
// generatorTemplateError in the same cases and give the same params in all
// other cases.

// upstreamMatrixGoTemplateMerge copies the Go-template branch of the Argo CD
// v3.6.1 matrix generator (applicationset/generators/matrix.go:91-98). a is
// a param set of the first child generator, b one of the second.
func upstreamMatrixGoTemplateMerge(a, b map[string]any) (map[string]any, error) {
	tmp := map[string]any{}
	if err := mergo.Merge(&tmp, b, mergo.WithOverride); err != nil {
		return nil, fmt.Errorf("failed to merge params from the second generator in the matrix generator with temp map: %w", err)
	}
	if err := mergo.Merge(&tmp, a, mergo.WithOverride); err != nil {
		return nil, fmt.Errorf("failed to merge params from the second generator in the matrix generator with the first: %w", err)
	}
	return tmp, nil
}

// upstreamMergeGoTemplateMerge copies the Go-template branch of the Argo CD
// v3.6.1 merge generator (applicationset/generators/merge.go:80-84).
// baseParamSet comes from the first child generator, overrideParamSet from
// a later one with the same merge key.
func upstreamMergeGoTemplateMerge(baseParamSet, overrideParamSet map[string]any) (map[string]any, error) {
	if err := mergo.Merge(&baseParamSet, overrideParamSet, mergo.WithOverride); err != nil {
		return nil, fmt.Errorf("error merging base param set with override param set: %w", err)
	}
	return baseParamSet, nil
}

// callUpstreamMerge runs one copied upstream merge and returns its panic
// value, if any.
func callUpstreamMerge(merge func() (map[string]any, error)) (out map[string]any, panicked any, err error) {
	defer func() {
		panicked = recover()
	}()
	out, err = merge()
	return out, nil, err
}

// Param shapes as Argo CD v3.6.1 generators build them in Go-template mode:
//
//   - list: each element is the JSON element decoded into map[string]any
//     (applicationset/generators/list.go:44-52), so a "values" field is a
//     map[string]any.
//   - cluster: .metadata is a map[string]any that holds the
//     map[string]string labels (applicationset/generators/cluster.go:167-178),
//     and generator values are a map[string]string
//     (applicationset/generators/value_interpolation.go:20-24).
//   - git directory: .path is a map[string]any whose segments are a
//     []string (applicationset/generators/repo_path_utils.go:132-136).
func mergeOracleClusterParams(values map[string]string) map[string]any {
	params := map[string]any{
		"name":           "alpha",
		"nameNormalized": "alpha",
		"server":         "https://alpha.example.invalid",
		"project":        "",
		"metadata":       map[string]any{"labels": map[string]string{"env": "prod"}},
	}
	if values != nil {
		params["values"] = values
	}
	return params
}

func mergeOracleListParams(fields map[string]any) map[string]any {
	params := map[string]any{"name": "alpha", "env": "prod"}
	maps.Copy(params, fields)
	return params
}

type mergeOracleCase struct {
	name string
	// receiving builds the param set that the merge writes into: the
	// second matrix child (b) or the merge base. incoming builds the param
	// set that the merge copies from: the first matrix child (a) or the
	// merge override. Each call returns new maps, because the upstream merge
	// writes into nested maps of its inputs.
	receiving func() map[string]any
	incoming  func() map[string]any
	wantPanic bool
}

var mergeOracleCases = []mergeOracleCase{
	{
		name:      "list values map[string]any into cluster values map[string]string",
		receiving: func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "west"}) },
		incoming: func() map[string]any {
			return mergeOracleListParams(map[string]any{"values": map[string]any{"region": "east"}})
		},
		wantPanic: true,
	},
	{
		name:      "list values with a new key into cluster values",
		receiving: func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "west"}) },
		incoming: func() map[string]any {
			return mergeOracleListParams(map[string]any{"values": map[string]any{"zone": "a"}})
		},
		wantPanic: true,
	},
	{
		name:      "list metadata.labels map[string]any into cluster labels map[string]string",
		receiving: func() map[string]any { return mergeOracleClusterParams(nil) },
		incoming: func() map[string]any {
			return mergeOracleListParams(map[string]any{"metadata": map[string]any{"labels": map[string]any{"env": "dev"}}})
		},
		wantPanic: true,
	},
	{
		name: "cluster values map[string]string into list values map[string]any",
		receiving: func() map[string]any {
			return mergeOracleListParams(map[string]any{"values": map[string]any{"region": "east", "zone": "a"}})
		},
		incoming:  func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "west"}) },
		wantPanic: false,
	},
	{
		name:      "empty list values map into cluster values",
		receiving: func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "west"}) },
		incoming: func() map[string]any {
			return mergeOracleListParams(map[string]any{"values": map[string]any{}})
		},
		wantPanic: false,
	},
	{
		name: "cluster values into cluster values",
		receiving: func() map[string]any {
			return mergeOracleClusterParams(map[string]string{"region": "west", "tier": "gold"})
		},
		incoming:  func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "east"}) },
		wantPanic: false,
	},
	{
		name: "list path.segments []any into git path.segments []string",
		receiving: func() map[string]any {
			return map[string]any{"name": "alpha", "path": map[string]any{"path": "apps/alpha", "segments": []string{"apps", "alpha"}}}
		},
		incoming: func() map[string]any {
			return mergeOracleListParams(map[string]any{"path": map[string]any{"segments": []any{"other"}}})
		},
		wantPanic: false,
	},
	{
		name:      "string values into cluster values map[string]string",
		receiving: func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "west"}) },
		incoming:  func() map[string]any { return mergeOracleListParams(map[string]any{"values": "plain"}) },
		wantPanic: false,
	},
	{
		name:      "cluster values map[string]string into string values",
		receiving: func() map[string]any { return mergeOracleListParams(map[string]any{"values": "plain"}) },
		incoming:  func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "west"}) },
		wantPanic: false,
	},
	{
		name:      "nil []any into cluster params",
		receiving: func() map[string]any { return mergeOracleClusterParams(map[string]string{"region": "west"}) },
		incoming:  func() map[string]any { return mergeOracleListParams(map[string]any{"items": []any(nil)}) },
		wantPanic: false,
	},
}

// TestCombinatorParamMergeMatchesUpstream runs every case through the copied
// upstream matrix and merge code and through drydock. Where upstream panics,
// drydock must return a generatorTemplateError marked upstreamPanic. Where
// upstream does not panic, drydock must give the same params, Go types
// included.
func TestCombinatorParamMergeMatchesUpstream(t *testing.T) {
	for _, tc := range mergeOracleCases {
		t.Run("matrix/"+tc.name, func(t *testing.T) {
			upstream, panicked, upstreamErr := callUpstreamMerge(func() (map[string]any, error) {
				return upstreamMatrixGoTemplateMerge(tc.incoming(), tc.receiving())
			})
			got, err := combineMatrixParams("oracle", tc.incoming(), tc.receiving(), true)
			assertMergeMatchesUpstream(t, tc, "applicationset/generators/matrix.go:95", upstream, panicked, upstreamErr, got, err)
		})
		t.Run("merge/"+tc.name, func(t *testing.T) {
			upstream, panicked, upstreamErr := callUpstreamMerge(func() (map[string]any, error) {
				return upstreamMergeGoTemplateMerge(tc.receiving(), tc.incoming())
			})
			got, err := mergeParams("oracle", tc.receiving(), tc.incoming(), true)
			assertMergeMatchesUpstream(t, tc, "applicationset/generators/merge.go:81", upstream, panicked, upstreamErr, got, err)
		})
	}
}

func assertMergeMatchesUpstream(t *testing.T, tc mergeOracleCase, cite string, upstream map[string]any, panicked any, upstreamErr error, got map[string]any, err error) {
	t.Helper()
	if upstreamErr != nil {
		t.Fatalf("upstream merge returned error %v; the oracle cases expect a panic or a result", upstreamErr)
	}
	if tc.wantPanic {
		// The oracle must panic, or this case no longer proves the
		// recover is needed.
		if panicked == nil || !strings.Contains(fmt.Sprint(panicked), "SetMapIndex") {
			t.Fatalf("upstream panic = %v, want a reflect SetMapIndex panic", panicked)
		}
		templateErr, ok := errors.AsType[*generatorTemplateError](err)
		if !ok || !templateErr.upstreamPanic {
			t.Fatalf("drydock error = %#v, want a generatorTemplateError marked upstreamPanic", err)
		}
		for _, want := range []string{"ApplicationSet oracle:", "SetMapIndex", cite} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("drydock error = %q, want it to contain %q", err, want)
			}
		}
		return
	}
	if panicked != nil {
		t.Fatalf("upstream panicked with %v; the case expects no panic", panicked)
	}
	if err != nil {
		t.Fatalf("drydock merge error = %v, upstream merged without error", err)
	}
	if !reflect.DeepEqual(got, upstream) {
		t.Fatalf("drydock params differ from upstream:\ndrydock  = %#v\nupstream = %#v", got, upstream)
	}
}

// TestCombinatorTypeClashFailsOnlyTheApplicationSet runs the type clash
// end to end. The clash direction gives a template-render-failed warning
// and no Applications. The reverse child order merges without error and
// gives the value that Argo CD gives.
func TestCombinatorTypeClashFailsOnlyTheApplicationSet(t *testing.T) {
	const clusterChild = `
          - clusters:
              values:
                region: west`
	const listChild = `
          - list:
              elements:
                - name: alpha
                  values:
                    region: east`
	options := Options{Provider: ProviderOptions{Data: ProviderData{
		Clusters: []ClusterInput{{Name: "alpha", Server: "https://alpha.example.invalid"}},
	}}}
	cases := []struct {
		name       string
		combinator string
		children   []string
		cite       string
		wantRegion string
	}{
		// The matrix merges the first child into the second
		// (matrix.go:92-95), so a list first writes map[string]any values
		// into the cluster values.
		{name: "matrix list then clusters", combinator: "matrix", children: []string{listChild, clusterChild}, cite: "matrix.go:95"},
		{name: "matrix clusters then list", combinator: "matrix", children: []string{clusterChild, listChild}, wantRegion: "west"},
		// The merge generator merges later children into the first
		// (merge.go:81), so a cluster base receives the list values.
		{name: "merge clusters then list", combinator: "merge", children: []string{clusterChild, listChild}, cite: "merge.go:81"},
		{name: "merge list then clusters", combinator: "merge", children: []string{listChild, clusterChild}, wantRegion: "west"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mergeKeys := ""
			if tc.combinator == "merge" {
				mergeKeys = "\n        mergeKeys: [name]"
			}
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
    - ` + tc.combinator + `:` + mergeKeys + `
        generators:` + strings.Join(tc.children, "") + `
  template:
    metadata:
      name: 'x-{{ .name }}'
      annotations:
        region: '{{ .values.region }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps
        targetRevision: main
      destination:
        server: https://alpha.example.invalid
        namespace: default
`)
			apps, diags, err := GenerateFromYAMLWithOptions(t.TempDir(), "appset.yaml", data, options)
			if err != nil {
				t.Fatalf("GenerateFromYAMLWithOptions() error = %v", err)
			}
			if tc.cite == "" {
				if len(diags) != 0 {
					t.Fatalf("diagnostics = %#v, want none", diags)
				}
				if got := generatedNames(apps); !slices.Equal(got, []string{"x-alpha"}) {
					t.Fatalf("generated names = %#v, want [x-alpha]", got)
				}
				if got := apps[0].Application.Annotations["region"]; got != tc.wantRegion {
					t.Fatalf("region annotation = %q, want %q", got, tc.wantRegion)
				}
				return
			}
			if len(apps) != 0 {
				t.Fatalf("generated names = %#v, want none", generatedNames(apps))
			}
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %#v, want one template-render-failed warning", diags)
			}
			diag := diags[0]
			if diag.Severity != diagnostic.SeverityWarning || diagnostic.StableCode(diag) != "appset.template-render-failed" {
				t.Fatalf("diagnostic = %#v, want an appset.template-render-failed warning", diag)
			}
			for _, want := range []string{"ApplicationSet mixed:", tc.combinator + " generator cannot merge", tc.cite, "without setting an ErrorOccurred condition"} {
				if !strings.Contains(diag.Message, want) {
					t.Fatalf("diagnostic message = %q, want it to contain %q", diag.Message, want)
				}
			}
		})
	}
}

// TestCloneParamValueKeepsNil checks that the param copy keeps a nil list
// and a nil map nil, as Argo CD v3.6.1 deepCopyParamValue does
// (applicationset/generators/matrix.go:188-212). toJson prints a nil list
// as null and an empty one as [].
func TestCloneParamValueKeepsNil(t *testing.T) {
	for _, value := range []any{[]any(nil), map[string]any(nil), map[string]string(nil), []string(nil), []map[string]any(nil)} {
		got := cloneParamValue(value)
		if reflect.TypeOf(got) != reflect.TypeOf(value) || !reflect.ValueOf(got).IsNil() {
			t.Fatalf("cloneParamValue(%#v) = %#v, want a nil %T", value, got, value)
		}
	}
	got, err := combineMatrixParams("nil-list", map[string]any{"items": []any(nil)}, map[string]any{"name": "alpha"}, true)
	if err != nil {
		t.Fatalf("combineMatrixParams() error = %v", err)
	}
	upstream, err := upstreamMatrixGoTemplateMerge(map[string]any{"items": []any(nil)}, map[string]any{"name": "alpha"})
	if err != nil {
		t.Fatalf("upstream matrix merge error = %v", err)
	}
	if !reflect.DeepEqual(got, upstream) {
		t.Fatalf("drydock params = %#v, upstream = %#v", got, upstream)
	}
	// Empty lists stay empty, not nil.
	if got := cloneParamValue([]any{}); got == nil || reflect.ValueOf(got).IsNil() {
		t.Fatalf("cloneParamValue([]any{}) = %#v, want an empty non-nil list", got)
	}
}
