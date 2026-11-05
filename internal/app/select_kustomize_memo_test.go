package app

import (
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// TestKustomizeSelectionMemoKeySeparatesIgnoreMissingComponents pins that
// two sources differing only in ignoreMissingComponents never share one
// changed-only selection walk: with the flag, a component directory without
// a kustomization file is skipped instead of walked, so the owned inputs
// differ.
func TestKustomizeSelectionMemoKeySeparatesIgnoreMissingComponents(t *testing.T) {
	source := func(ignoreMissing bool) argoappv1.ApplicationSource {
		return argoappv1.ApplicationSource{
			Path: "apps/demo",
			Kustomize: &argoappv1.ApplicationSourceKustomize{
				Components:              []string{"../components/extra"},
				IgnoreMissingComponents: ignoreMissing,
			},
		}
	}
	if kustomizeSelectionMemoKey(source(true)) == kustomizeSelectionMemoKey(source(false)) {
		t.Fatal("kustomizeSelectionMemoKey() is equal for ignoreMissingComponents true and false, want distinct keys")
	}
}
