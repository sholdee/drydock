package config

import (
	"reflect"
	"testing"

	argosettings "github.com/argoproj/argo-cd/v3/util/settings"
	"github.com/sholdee/drydock/internal/diagnostic"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// upstreamResourcesFilter loads argocd-cm data through Argo CD's own
// SettingsManager.GetResourcesFilter (util/settings/settings.go at v3.6.1),
// backed by a fake clientset.
func upstreamResourcesFilter(t *testing.T, data map[string]string) (*argosettings.ResourcesFilter, error) {
	t.Helper()
	cm := &corev1.ConfigMap{Data: data}
	cm.Name = "argocd-cm"
	cm.Namespace = "argocd"
	cm.Labels = map[string]string{"app.kubernetes.io/part-of": "argocd"}
	manager := argosettings.NewSettingsManager(t.Context(), fake.NewClientset(cm), "argocd")
	return manager.GetResourcesFilter()
}

func sameFilteredResources(upstream []argosettings.FilteredResource, got []ResourceFilterRule) bool {
	if len(upstream) != len(got) {
		return false
	}
	for i, rule := range upstream {
		if !reflect.DeepEqual(rule.APIGroups, got[i].APIGroups) ||
			!reflect.DeepEqual(rule.Kinds, got[i].Kinds) ||
			!reflect.DeepEqual(rule.Clusters, got[i].Clusters) {
			return false
		}
	}
	return true
}

// TestResourceFiltersParseParityWithArgoCD decodes the same argocd-cm
// resource.exclusions and resource.inclusions values through Argo CD's
// SettingsManager and through drydock's provider. A value Argo CD rejects is
// an error diagnostic in drydock, and an accepted value yields the same
// rules. Argo CD decodes with sigs.k8s.io/yaml, so keys match
// case-insensitively and a repeated key keeps its last value.
func TestResourceFiltersParseParityWithArgoCD(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		// wantKinds is the decoded kinds of the first rule. It is checked
		// so a regression to a case-sensitive decoder cannot pass by
		// accident.
		wantKinds []string
	}{
		{name: "lowercase keys", value: "- apiGroups: [apps]\n  kinds: [Deployment]\n  clusters: [\"https://prod-*\"]\n", wantKinds: []string{"Deployment"}},
		{name: "capitalized keys", value: "- APIGroups: [apps]\n  Kinds: [Deployment]\n  Clusters: [\"https://prod-*\"]\n", wantKinds: []string{"Deployment"}},
		{name: "upper case keys", value: "- APIGROUPS: [apps]\n  KINDS: [Deployment]\n", wantKinds: []string{"Deployment"}},
		{name: "repeated key keeps last", value: "- kinds: [Deployment]\n  kinds: [StatefulSet]\n", wantKinds: []string{"StatefulSet"}},
		{name: "repeated key in another case", value: "- kinds: [Deployment]\n  Kinds: [StatefulSet]\n"},
		{name: "unknown key ignored", value: "- kinds: [Deployment]\n  namespaces: [x]\n", wantKinds: []string{"Deployment"}},
		{name: "selector key ignored by matching", value: "- kinds: [Deployment]\n  selector: a=b\n", wantKinds: []string{"Deployment"}},
		{name: "wildcard rule", value: "- apiGroups: [\"*\"]\n"},
		{name: "null list", value: "null\n"},
		{name: "not a list", value: "kinds: [Deployment]\n"},
		{name: "scalar kinds", value: "- kinds: Deployment\n"},
		{name: "numeric kind", value: "- kinds: [1]\n"},
		{name: "invalid yaml", value: "- kinds: [Deployment\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, upstreamErr := upstreamResourcesFilter(t, map[string]string{
				"resource.exclusions": tc.value,
				"resource.inclusions": tc.value,
			})

			settings := DefaultSettings()
			diags := applyCMMap(&settings, map[string]string{
				"resource.exclusions": tc.value,
				"resource.inclusions": tc.value,
			}, "argocd-cm.yaml", "data")
			errors := 0
			for _, diag := range diags {
				if diag.Severity == diagnostic.SeverityError {
					errors++
				}
			}
			if upstreamErr != nil {
				if errors != 2 || len(settings.ResourceExclusions) != 0 || len(settings.ResourceInclusions) != 0 {
					t.Fatalf("upstream error = %v, drydock diagnostics = %#v, exclusions = %#v, inclusions = %#v", upstreamErr, diags, settings.ResourceExclusions, settings.ResourceInclusions)
				}
				return
			}
			if errors != 0 {
				t.Fatalf("upstream accepted the value, drydock diagnostics = %#v", diags)
			}
			if !sameFilteredResources(upstream.ResourceExclusions, settings.ResourceExclusions) {
				t.Fatalf("exclusions: upstream %#v, drydock %#v", upstream.ResourceExclusions, settings.ResourceExclusions)
			}
			if !sameFilteredResources(upstream.ResourceInclusions, settings.ResourceInclusions) {
				t.Fatalf("inclusions: upstream %#v, drydock %#v", upstream.ResourceInclusions, settings.ResourceInclusions)
			}
			if tc.wantKinds != nil && !reflect.DeepEqual(settings.ResourceExclusions[0].Kinds, tc.wantKinds) {
				t.Fatalf("exclusion kinds = %#v, want %#v", settings.ResourceExclusions[0].Kinds, tc.wantKinds)
			}
		})
	}
}

// TestResourceFiltersCapitalizedKindsDoNotWidenToWildcard pins the B2 bug:
// a case-sensitive decoder dropped "Kinds:" and turned a Deployment-only
// exclusion into one that matched every kind in the group.
func TestResourceFiltersCapitalizedKindsDoNotWidenToWildcard(t *testing.T) {
	settings := DefaultSettings()
	diags := applyCMMap(&settings, map[string]string{
		"resource.exclusions": "- apiGroups: [apps]\n  Kinds: [Deployment]\n",
	}, "argocd-cm.yaml", "data")
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	want := []ResourceFilterRule{{
		APIGroups:  []string{"apps"},
		Kinds:      []string{"Deployment"},
		Provenance: Provenance{Path: "argocd-cm.yaml", Pointer: "data.resource.exclusions"},
	}}
	if !reflect.DeepEqual(settings.ResourceExclusions, want) {
		t.Fatalf("ResourceExclusions = %#v, want %#v", settings.ResourceExclusions, want)
	}
}
