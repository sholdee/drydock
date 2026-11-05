package app

import (
	"slices"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argodiff "github.com/argoproj/argo-cd/v3/util/argo/diff"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/manifest"
)

// TestManagedFieldsManagersKeyParityWithArgoCD checks which
// managedFieldsManagers drydock picks for a resource against Argo CD v3.6.1's
// IgnoreDiffConfig.HasIgnoreDifference, which preDiffNormalize uses
// (util/argo/diff/diff.go:424-429). Argo CD looks resource.customizations up
// by the exact "group/kind" key and by "*/*", with no glob. Application rules
// still use glob matching.
func TestManagedFieldsManagersKeyParityWithArgoCD(t *testing.T) {
	identities := []manifest.Identity{
		{Group: "", Kind: "Service", Name: "demo", Namespace: "default"},
		{Group: "example.com", Kind: "Service", Name: "demo", Namespace: "default"},
		{Group: "apps", Kind: "Deployment", Name: "demo", Namespace: "default"},
		{Group: "apps", Kind: "StatefulSet", Name: "demo", Namespace: "default"},
	}
	keys := []string{
		"Service",
		"/Service",
		"example.com/Service",
		"apps/Deployment",
		"apps/*",
		"*/Deployment",
		"*/*",
		"*",
		"apps/Deployment/extra",
	}
	appRules := []argoappv1.ResourceIgnoreDifferences{
		{Group: "apps", Kind: "*", ManagedFieldsManagers: []string{"app-glob"}},
		{Group: "*", Kind: "Service", Name: "demo", ManagedFieldsManagers: []string{"app-service"}},
	}

	for _, key := range keys {
		for _, withAppRules := range []bool{false, true} {
			customizations := map[string]config.ResourceCustomization{
				key: {IgnoreDifferences: config.OverrideIgnoreDifferences{
					ManagedFieldsManagers: []string{"manager-" + key},
				}},
			}
			var ignores []argoappv1.ResourceIgnoreDifferences
			if withAppRules {
				ignores = appRules
			}
			application := argoappv1.Application{Spec: argoappv1.ApplicationSpec{IgnoreDifferences: ignores}}
			settings := config.ArgoSettings{ResourceCustomizations: customizations}
			oracle := argodiff.NewIgnoreDiffConfig(ignores, parityOverrides(customizations))

			for _, id := range identities {
				got := slices.Clone(normalizationFor(application, id, settings).ManagedFieldsManagers)
				var want []string
				if ok, ignore := oracle.HasIgnoreDifference(id.Group, id.Kind, id.Name, id.Namespace); ok {
					want = slices.Clone(ignore.ManagedFieldsManagers)
				}
				// Manager order and repeats do not change the result, because
				// managedfields.Normalize only checks membership
				// (util/argo/managedfields/managed_fields.go:42-43).
				slices.Sort(got)
				got = slices.Compact(got)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Errorf("key %q, app rules %v, resource %s/%s: managers = %v, want %v",
						key, withAppRules, id.Group, id.Kind, got, want)
				}
			}
		}
	}
}
