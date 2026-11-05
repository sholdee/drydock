package manifest

import (
	"testing"

	argosettings "github.com/argoproj/argo-cd/v3/util/settings"
	"github.com/sholdee/drydock/internal/config"
)

func drydockFilterRules(rules []argosettings.FilteredResource) []config.ResourceFilterRule {
	out := make([]config.ResourceFilterRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, config.ResourceFilterRule{
			APIGroups: rule.APIGroups,
			Kinds:     rule.Kinds,
			Clusters:  rule.Clusters,
		})
	}
	return out
}

// filterParityCases are exclusion and inclusion rule sets. Each one is
// checked against Argo CD's ResourcesFilter.IsExcludedResource.
var filterParityCases = []struct {
	name       string
	exclusions []argosettings.FilteredResource
	inclusions []argosettings.FilteredResource
}{
	{name: "none"},
	{
		name:       "cluster independent exclusion",
		exclusions: []argosettings.FilteredResource{{APIGroups: []string{"apps"}, Kinds: []string{"Deployment"}}},
	},
	{
		name:       "server glob exclusion",
		exclusions: []argosettings.FilteredResource{{APIGroups: []string{"apps"}, Clusters: []string{"https://prod-*"}}},
	},
	{
		name:       "star cluster exclusion",
		exclusions: []argosettings.FilteredResource{{Kinds: []string{"ConfigMap"}, Clusters: []string{"*"}}},
	},
	{
		name:       "double star and empty cluster exclusion",
		exclusions: []argosettings.FilteredResource{{Kinds: []string{"Secret"}, Clusters: []string{"", "**"}}},
	},
	{
		name:       "invalid and empty cluster globs",
		exclusions: []argosettings.FilteredResource{{Kinds: []string{"Secret"}, Clusters: []string{"[", ""}}},
		inclusions: []argosettings.FilteredResource{{Kinds: []string{"Service"}, Clusters: []string{"{"}}},
	},
	{
		name:       "cluster independent inclusion",
		inclusions: []argosettings.FilteredResource{{APIGroups: []string{"apps"}}},
	},
	{
		name:       "server glob inclusion",
		inclusions: []argosettings.FilteredResource{{APIGroups: []string{"apps"}, Kinds: []string{"Deployment"}, Clusters: []string{"https://prod-*"}}},
	},
	{
		name:       "star inclusion with server glob exclusion",
		exclusions: []argosettings.FilteredResource{{APIGroups: []string{"apps"}, Kinds: []string{"Deployment"}, Clusters: []string{"https://prod-*"}}},
		inclusions: []argosettings.FilteredResource{{APIGroups: []string{"apps"}, Clusters: []string{"*"}}},
	},
	{
		name: "server glob inclusion with cluster independent inclusion",
		inclusions: []argosettings.FilteredResource{
			{Kinds: []string{"ConfigMap"}, Clusters: []string{"https://prod-*"}},
			{APIGroups: []string{"apps"}},
		},
	},
	{
		name:       "name glob that never matches a server",
		exclusions: []argosettings.FilteredResource{{Kinds: []string{"ConfigMap"}, Clusters: []string{"prod-*"}}},
	},
}

var filterParityIdentities = []Identity{
	{Group: "", Kind: "ConfigMap"},
	{Group: "", Kind: "Secret"},
	{Group: "", Kind: "Service"},
	{Group: "", Kind: "Event"},
	{Group: "apps", Kind: "Deployment"},
	{Group: "apps", Kind: "StatefulSet"},
	{Group: "coordination.k8s.io", Kind: "Lease"},
	{Group: "example.com", Kind: "Widget"},
}

// filterParityServers stands for "every possible server": it covers each
// pattern in filterParityCases, both ways.
var filterParityServers = []string{
	"https://kubernetes.default.svc",
	"https://prod-east.example.com",
	"https://dev.example.com",
	"prod-east",
	"[",
	"{",
	"https://192.168.0.20",
}

// TestSettingsResourceFilterDropParity checks Drop against Argo CD's
// ResourcesFilter.IsExcludedResource for resolved servers.
func TestSettingsResourceFilterDropParity(t *testing.T) {
	for _, tc := range filterParityCases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &argosettings.ResourcesFilter{ResourceExclusions: tc.exclusions, ResourceInclusions: tc.inclusions}
			filter := SettingsResourceFilter{Exclusions: drydockFilterRules(tc.exclusions), Inclusions: drydockFilterRules(tc.inclusions)}
			for _, id := range filterParityIdentities {
				for _, server := range filterParityServers {
					want := upstream.IsExcludedResource(id.Group, id.Kind, server)
					if got := filter.Drop(id, server); got != want {
						t.Fatalf("Drop(%#v, %q) = %t, want %t", id, server, got, want)
					}
				}
			}
		})
	}
}

// TestSettingsResourceFilterDropUnresolvedParity checks DropUnresolved
// against Argo CD's ResourcesFilter.IsExcludedResource over every sample
// server. When Argo CD gives the same answer for every server, drydock gives
// that answer. When the answer depends on the server, drydock keeps the
// resource.
func TestSettingsResourceFilterDropUnresolvedParity(t *testing.T) {
	for _, tc := range filterParityCases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &argosettings.ResourcesFilter{ResourceExclusions: tc.exclusions, ResourceInclusions: tc.inclusions}
			filter := SettingsResourceFilter{Exclusions: drydockFilterRules(tc.exclusions), Inclusions: drydockFilterRules(tc.inclusions)}
			for _, id := range filterParityIdentities {
				dropped, kept := 0, 0
				for _, server := range filterParityServers {
					if upstream.IsExcludedResource(id.Group, id.Kind, server) {
						dropped++
					} else {
						kept++
					}
				}
				want := kept == 0
				if got := filter.DropUnresolved(id); got != want {
					t.Fatalf("DropUnresolved(%#v) = %t, want %t (upstream drops for %d of %d servers)", id, got, want, dropped, len(filterParityServers))
				}
			}
		})
	}
}
