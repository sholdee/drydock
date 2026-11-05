package manifest

import (
	"testing"

	argosettings "github.com/argoproj/argo-cd/v3/util/settings"
	"github.com/sholdee/drydock/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var selectorParityRules = []argosettings.FilteredResource{
	{APIGroups: []string{""}, Kinds: []string{"Pod"}, Clusters: []string{"https://192.168.0.20"}, Selector: "!argocd.argoproj.io/instance"},
	{Selector: "  tier in (backend,frontend)  "},
	{APIGroups: []string{"apps"}, Kinds: []string{"*"}, Selector: "team=platform"},
	{APIGroups: []string{"*.example.com"}, Selector: "managed"},
	{Kinds: []string{"ConfigMap"}, Clusters: []string{"https://prod-*"}, Selector: "env!=dev"},
	{Kinds: []string{"Secret"}},
	{Kinds: []string{"Service"}, Selector: "   "},
}

func drydockSelectorRules(rules []argosettings.FilteredResource) []config.ResourceFilterRule {
	out := make([]config.ResourceFilterRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, config.ResourceFilterRule{
			APIGroups: rule.APIGroups,
			Kinds:     rule.Kinds,
			Clusters:  rule.Clusters,
			Selector:  rule.Selector,
		})
	}
	return out
}

// TestSettingsResourceFilterLabelSelectorParity checks LabelSelector against
// Argo CD's ResourcesFilter.GetLabelSelector over the same rules.
func TestSettingsResourceFilterLabelSelectorParity(t *testing.T) {
	upstream := &argosettings.ResourcesFilter{ResourceSelectors: selectorParityRules}
	filter := SettingsResourceFilter{Selectors: drydockSelectorRules(selectorParityRules)}

	for _, group := range []string{"", "apps", "batch", "foo.example.com"} {
		for _, kind := range []string{"Pod", "Deployment", "ConfigMap", "Secret", "Service", "Widget"} {
			for _, cluster := range []string{"", "https://192.168.0.20", "https://prod-east", "https://kubernetes.default.svc", "in-cluster"} {
				want := upstream.GetLabelSelector(group, kind, cluster)
				got := filter.LabelSelector(Identity{Group: group, Kind: kind}, cluster)
				if got != want {
					t.Fatalf("LabelSelector(%q, %q, %q) = %q, want %q", group, kind, cluster, got, want)
				}
			}
		}
	}
}

func selectorTestObject(group, kind, name string, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	apiVersion := "v1"
	if group != "" {
		apiVersion = group + "/v1"
	}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	if labels != nil {
		obj.SetLabels(labels)
	}
	return obj
}

func TestSettingsResourceFilterSelectorMismatch(t *testing.T) {
	filter := SettingsResourceFilter{Selectors: []config.ResourceFilterRule{
		{Kinds: []string{"ConfigMap"}, Selector: "team=platform"},
		{Selector: "!skip"},
		{Clusters: []string{"https://prod-*"}, Selector: "env=prod"},
	}}

	for _, tc := range []struct {
		name     string
		obj      *unstructured.Unstructured
		cluster  string
		selector string
		mismatch bool
	}{
		{name: "matching labels", obj: selectorTestObject("", "ConfigMap", "a", map[string]string{"team": "platform"}), cluster: "https://dev", mismatch: false},
		{name: "missing label", obj: selectorTestObject("", "ConfigMap", "b", nil), cluster: "https://dev", selector: "team=platform,!skip", mismatch: true},
		{name: "ANDed selectors", obj: selectorTestObject("", "ConfigMap", "c", map[string]string{"team": "platform", "skip": "yes"}), cluster: "https://dev", selector: "team=platform,!skip", mismatch: true},
		{name: "cluster rule applies", obj: selectorTestObject("apps", "Deployment", "d", nil), cluster: "https://prod-east", selector: "!skip,env=prod", mismatch: true},
		{name: "cluster rule skipped", obj: selectorTestObject("apps", "Deployment", "e", nil), cluster: "https://dev", mismatch: false},
		{name: "nil object", obj: nil, mismatch: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, mismatch := filter.SelectorMismatch(tc.obj, tc.cluster)
			if mismatch != tc.mismatch || selector != tc.selector {
				t.Fatalf("SelectorMismatch() = %q, %v; want %q, %v", selector, mismatch, tc.selector, tc.mismatch)
			}
		})
	}
}

func TestSettingsResourceFilterSelectorsNeverDrop(t *testing.T) {
	filter := SettingsResourceFilter{Selectors: []config.ResourceFilterRule{{Selector: "team=platform"}}}
	if filter.Drop(Identity{Group: "apps", Kind: "Deployment"}, "https://dev") {
		t.Fatal("Drop() = true for a resource.selectors-only filter, want false")
	}
}

func TestSettingsResourceFilterSelectorMismatchIgnoresUnparsableSelector(t *testing.T) {
	filter := SettingsResourceFilter{Selectors: []config.ResourceFilterRule{{Selector: "tier in (backend"}}}
	if selector, mismatch := filter.SelectorMismatch(selectorTestObject("", "Pod", "p", nil), ""); mismatch {
		t.Fatalf("SelectorMismatch() = %q, true; want no mismatch for an unparsable selector", selector)
	}
}
