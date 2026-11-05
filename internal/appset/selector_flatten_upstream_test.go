package appset

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"testing"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// These tests compare drydock's generator selector labels with the labels
// Argo CD v3.6.1 builds. drydock cannot import
// applicationset/generators (see upstream_param_types_test.go), and
// github.com/jeremywohl/flatten is not in drydock's module graph. Adding it
// would change go.mod and go.sum for a test, so this file copies both.
// Upstream writes interface{}; the copies write any, which is the same type.

// upstreamFlattenParameters copies
// applicationset/generators/generator_spec_processor.go:126-138.
func upstreamFlattenParameters(in map[string]any) (map[string]string, error) {
	flat, err := upstreamFlatten(upstreamNormalizeMapForFlatten(in), "")
	if err != nil {
		return nil, fmt.Errorf("error flatenning parameters: %w", err)
	}

	out := make(map[string]string, len(flat))
	for k, v := range flat {
		out[k] = fmt.Sprintf("%v", v)
	}

	return out, nil
}

// upstreamNormalizeMapForFlatten copies
// applicationset/generators/generator_spec_processor.go:146-163.
func upstreamNormalizeMapForFlatten(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch cast := v.(type) {
		case map[string]string:
			inner := make(map[string]any, len(cast))
			for ik, iv := range cast {
				inner[ik] = iv
			}
			out[k] = inner
		case map[string]any:
			out[k] = upstreamNormalizeMapForFlatten(cast)
		default:
			out[k] = v
		}
	}
	return out
}

var errUpstreamFlattenNotValidInput = errors.New("Not a valid input: map or slice") //nolint:staticcheck // Copied upstream error text.

// upstreamFlatten copies Flatten from github.com/jeremywohl/flatten
// v1.0.2-0.20211013061545-07e4a09fb8e4 (the version in the Argo CD v3.6.1
// go.mod), flatten.go:45-54, with the DotStyle separator that
// flattenParameters passes.
func upstreamFlatten(nested map[string]any, prefix string) (map[string]any, error) {
	flatmap := make(map[string]any)

	err := upstreamFlattenRecursive(true, flatmap, nested, prefix)
	if err != nil {
		return nil, err
	}

	return flatmap, nil
}

// upstreamFlattenRecursive copies flatten, flatten.go:88-118. Upstream
// ignores the error that assign returns, so the copy does too.
func upstreamFlattenRecursive(top bool, flatMap map[string]any, nested any, prefix string) error {
	assign := func(newKey string, v any) error {
		switch v.(type) {
		case map[string]any, []any:
			if err := upstreamFlattenRecursive(false, flatMap, v, newKey); err != nil {
				return err
			}
		default:
			flatMap[newKey] = v
		}

		return nil
	}

	switch nested := nested.(type) {
	case map[string]any:
		for k, v := range nested {
			newKey := upstreamEnkey(top, prefix, k)
			_ = assign(newKey, v)
		}
	case []any:
		for i, v := range nested {
			newKey := upstreamEnkey(top, prefix, strconv.Itoa(i))
			_ = assign(newKey, v)
		}
	default:
		return errUpstreamFlattenNotValidInput
	}

	return nil
}

// upstreamEnkey copies enkey, flatten.go:120-130, with DotStyle (Before and
// After empty, Middle ".").
func upstreamEnkey(top bool, prefix, subkey string) string {
	key := prefix

	if top {
		key += subkey
	} else {
		key += "." + subkey
	}

	return key
}

// selectorFlattenCases holds param shapes that drydock generators produce.
func selectorFlattenCases() map[string]map[string]any {
	repositoryID := int64(123456789)
	return map[string]map[string]any{
		"git directory": {
			"path": map[string]any{
				"path":               "apps/alpha",
				"basename":           "alpha",
				"basenameNormalized": "alpha",
				"segments":           []string{"apps", "alpha"},
			},
			"values": map[string]string{"region": "east"},
		},
		"pull request": {
			"number": "7",
			"branch": "feature",
			"labels": []string{"preview", "team-a"},
		},
		"cluster": {
			"name":     "alpha",
			"metadata": map[string]any{"labels": map[string]string{"env": "prod"}, "annotations": map[string]string{}},
		},
		"cluster flatList": {
			"clusters": []map[string]any{{"name": "alpha"}, {"name": "beta"}},
		},
		"plugin output": {
			"items": []any{
				map[string]any{"name": "alpha", "tags": []any{"a", "b"}},
				map[string]string{"name": "beta"},
				[]string{"c", "d"},
				nil,
				float64(3),
			},
			"nested": map[string]any{"inner": map[string]any{"labels": map[string]string{"tier": "web"}, "empty": map[string]any{}}},
			"empty":  []any{},
			"nil":    nil,
		},
		"typed nils": {
			"strings": []string(nil),
			"anys":    []any(nil),
			"map":     map[string]any(nil),
			"labels":  map[string]string(nil),
			"maps":    []map[string]any(nil),
		},
		"scalars": {
			"bool":          true,
			"int":           42,
			"float":         1.5,
			"uuid":          uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
			"repository_id": &repositoryID,
			"empty":         "",
		},
		"empty keys": {
			"":       "top",
			"parent": map[string]any{"": "child"},
			"blank":  map[string]any{"": map[string]any{"x": "y"}},
		},
	}
}

func TestSelectorLabelsMatchUpstreamFlattenParameters(t *testing.T) {
	for name, params := range selectorFlattenCases() {
		t.Run(name, func(t *testing.T) {
			want, err := upstreamFlattenParameters(params)
			if err != nil {
				t.Fatalf("upstream flattenParameters() error = %v", err)
			}
			got := selectorLabels(params)
			if !maps.Equal(got, want) {
				t.Fatalf("selector labels differ from upstream:\ndrydock  = %#v\nupstream = %#v", got, want)
			}
		})
	}
}

// TestSelectorLabelsOracleKeepsListsAsLeaves pins the upstream behavior that
// the user-visible change depends on, so a change in the oracle shows up.
func TestSelectorLabelsOracleKeepsListsAsLeaves(t *testing.T) {
	cases := selectorFlattenCases()
	want := map[string]map[string]string{
		"git directory": {
			"path.path":               "apps/alpha",
			"path.basename":           "alpha",
			"path.basenameNormalized": "alpha",
			"path.segments":           "[apps alpha]",
			"values.region":           "east",
		},
		"pull request": {
			"number": "7",
			"branch": "feature",
			"labels": "[preview team-a]",
		},
		"cluster flatList": {
			"clusters": "[map[name:alpha] map[name:beta]]",
		},
		"typed nils": {
			"strings": "[]",
			"maps":    "[]",
		},
	}
	for name, wantLabels := range want {
		got, err := upstreamFlattenParameters(cases[name])
		if err != nil {
			t.Fatalf("%s: upstream flattenParameters() error = %v", name, err)
		}
		if !maps.Equal(got, wantLabels) {
			t.Fatalf("%s: upstream labels = %#v, want %#v", name, got, wantLabels)
		}
	}
	plugin, err := upstreamFlattenParameters(cases["plugin output"])
	if err != nil {
		t.Fatalf("plugin output: upstream flattenParameters() error = %v", err)
	}
	for key, value := range map[string]string{
		"items.0.name":             "alpha",
		"items.0.tags.1":           "b",
		"items.1":                  "map[name:beta]",
		"items.2":                  "[c d]",
		"items.3":                  "<nil>",
		"items.4":                  "3",
		"nested.inner.labels.tier": "web",
		"nil":                      "<nil>",
	} {
		if plugin[key] != value {
			t.Fatalf("plugin output: upstream %s = %q, want %q (labels %#v)", key, plugin[key], value, plugin)
		}
	}
}

// TestGeneratorSelectorMatchesListParamsLikeArgoCD runs upstream selectors
// on both label sets for the param shapes whose handling changed.
func TestGeneratorSelectorMatchesListParamsLikeArgoCD(t *testing.T) {
	cases := selectorFlattenCases()
	selectors := []struct {
		params   string
		selector metav1.LabelSelector
		matches  bool
	}{
		{"git directory", metav1.LabelSelector{MatchLabels: map[string]string{"path.segments": "[apps alpha]"}}, true},
		{"git directory", metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "path.segments.1", Operator: metav1.LabelSelectorOpExists}}}, false},
		{"git directory", metav1.LabelSelector{MatchLabels: map[string]string{"path.segments.1": "alpha"}}, false},
		{"pull request", metav1.LabelSelector{MatchLabels: map[string]string{"labels": "[preview team-a]"}}, true},
		{"pull request", metav1.LabelSelector{MatchLabels: map[string]string{"labels.0": "preview"}}, false},
		{"cluster", metav1.LabelSelector{MatchLabels: map[string]string{"metadata.labels.env": "prod"}}, true},
		{"cluster flatList", metav1.LabelSelector{MatchLabels: map[string]string{"clusters.0.name": "alpha"}}, false},
		{"plugin output", metav1.LabelSelector{MatchLabels: map[string]string{"items.1.name": "beta"}}, false},
		{"plugin output", metav1.LabelSelector{MatchLabels: map[string]string{"items.0.tags.1": "b"}}, true},
	}
	for _, tc := range selectors {
		selector, err := appsetutils.LabelSelectorAsSelector(&tc.selector)
		if err != nil {
			t.Fatalf("LabelSelectorAsSelector(%#v) error = %v", tc.selector, err)
		}
		upstream, err := upstreamFlattenParameters(cases[tc.params])
		if err != nil {
			t.Fatalf("upstream flattenParameters() error = %v", err)
		}
		if got := selector.Matches(labels.Set(upstream)); got != tc.matches {
			t.Fatalf("%s selector %v: upstream match = %v, want %v", tc.params, tc.selector.String(), got, tc.matches)
		}
		if got := selector.Matches(labels.Set(selectorLabels(cases[tc.params]))); got != tc.matches {
			t.Fatalf("%s selector %v: drydock match = %v, want %v", tc.params, tc.selector.String(), got, tc.matches)
		}
	}
}

// TestSelectorLabelsDoNotChangeParams checks that the map[string]string
// conversion copies and never writes into the generator params.
func TestSelectorLabelsDoNotChangeParams(t *testing.T) {
	params := selectorFlattenCases()["git directory"]
	before := cloneParams(params)
	_ = selectorLabels(params)
	if !reflect.DeepEqual(params, before) {
		t.Fatalf("selectorLabels changed params:\nafter  = %#v\nbefore = %#v", params, before)
	}
}
