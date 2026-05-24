package manifest

import (
	"testing"

	"github.com/sholdee/drydock/internal/config"
)

func TestSettingsResourceFilterDropsCoreExcludedResources(t *testing.T) {
	filter := SettingsResourceFilter{}

	for _, tc := range []struct {
		name    string
		id      Identity
		cluster string
	}{
		{name: "core event", id: Identity{Group: "", Kind: "Event"}},
		{name: "events group", id: Identity{Group: "events.k8s.io", Kind: "Event"}},
		{name: "metrics group", id: Identity{Group: "metrics.k8s.io", Kind: "PodMetrics"}},
		{name: "lease", id: Identity{Group: "coordination.k8s.io", Kind: "Lease"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !filter.Drop(tc.id, tc.cluster) {
				t.Fatalf("Drop(%#v, %q) = false, want true", tc.id, tc.cluster)
			}
		})
	}

	if filter.Drop(Identity{Group: "apps", Kind: "Deployment"}, "") {
		t.Fatal("Drop(apps/Deployment) = true, want false")
	}
}

func TestSettingsResourceFilterDropsUserExclusions(t *testing.T) {
	filter := SettingsResourceFilter{
		Exclusions: []config.ResourceFilterRule{{
			APIGroups: []string{"cert-manager.io"},
			Kinds:     []string{"Certificate"},
			Clusters:  []string{"prod-*"},
		}},
	}

	if !filter.Drop(Identity{Group: "cert-manager.io", Kind: "Certificate"}, "prod-east") {
		t.Fatal("Drop(cert-manager.io/Certificate, prod-east) = false, want true")
	}
	if filter.Drop(Identity{Group: "cert-manager.io", Kind: "Certificate"}, "dev") {
		t.Fatal("Drop(cert-manager.io/Certificate, dev) = true, want false")
	}
}

func TestSettingsResourceFilterInclusionsRestrictMatchingCluster(t *testing.T) {
	filter := SettingsResourceFilter{
		Inclusions: []config.ResourceFilterRule{{
			APIGroups: []string{"apps"},
			Kinds:     []string{"Deployment"},
			Clusters:  []string{"prod-*"},
		}},
	}

	if filter.Drop(Identity{Group: "apps", Kind: "Deployment"}, "prod-west") {
		t.Fatal("Drop(apps/Deployment, prod-west) = true, want false")
	}
	if !filter.Drop(Identity{Group: "", Kind: "ConfigMap"}, "prod-west") {
		t.Fatal("Drop(ConfigMap, prod-west) = false, want true")
	}
	if filter.Drop(Identity{Group: "", Kind: "ConfigMap"}, "dev") {
		t.Fatal("Drop(ConfigMap, dev) = true, want false")
	}
}

func TestSettingsResourceFilterExclusionsWinOverInclusions(t *testing.T) {
	filter := SettingsResourceFilter{
		Exclusions: []config.ResourceFilterRule{{
			APIGroups: []string{"apps"},
			Kinds:     []string{"Deployment"},
		}},
		Inclusions: []config.ResourceFilterRule{{
			APIGroups: []string{"apps"},
			Kinds:     []string{"Deployment"},
		}},
	}

	if !filter.Drop(Identity{Group: "apps", Kind: "Deployment"}, "") {
		t.Fatal("Drop(apps/Deployment) = false, want true")
	}
}

func TestSettingsResourceFilterEmptyFieldsAndKindWildcardMatchAll(t *testing.T) {
	filter := SettingsResourceFilter{
		Exclusions: []config.ResourceFilterRule{{
			Kinds: []string{"*"},
		}},
	}

	if !filter.Drop(Identity{Group: "batch", Kind: "Job"}, "any") {
		t.Fatal("Drop(batch/Job) = false, want true")
	}
}
