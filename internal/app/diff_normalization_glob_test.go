package app

import (
	"slices"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/manifest"
)

// Argo CD v3.6.1 matches ignoreDifferences groups and kinds and
// resource.customizations keys with util/glob on gobwas/glob v1.0.0
// (821e090d6, #29482). v1.0.0 matches an empty brace alternative
// (`{*.,}example.com` matches example.com) and no longer lets a prefix and a
// suffix overlap (`a*ant` does not match ant), where v0.2.3 did the opposite.
func TestNormalizationForGlobSemanticsMatchGobwasV1(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		group   string
		kind    string
		want    bool
	}{
		{name: "empty brace alternative matches the bare group", pattern: "{*.,}example.com", group: "example.com", kind: "Widget", want: true},
		{name: "brace alternative matches a subdomain group", pattern: "{*.,}example.com", group: "monitoring.example.com", kind: "Widget", want: true},
		{name: "brace alternative keeps the separator", pattern: "{*.,}example.com", group: "xexample.com", kind: "Widget", want: false},
		{name: "overlapping prefix and suffix do not match", pattern: "a*ant", group: "ant", kind: "Widget", want: false},
		{name: "prefix and suffix with a gap match", pattern: "a*ant", group: "aant", kind: "Widget", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := manifest.Identity{Group: tt.group, Kind: tt.kind, Name: "demo", Namespace: "default"}

			app := argoappv1.Application{Spec: argoappv1.ApplicationSpec{IgnoreDifferences: []argoappv1.ResourceIgnoreDifferences{{
				Group:        tt.pattern,
				Kind:         tt.kind,
				JSONPointers: []string{"/spec/replicas"},
			}}}}
			got := slices.Contains(normalizationFor(app, id, config.DefaultSettings()).JSONPointers, "/spec/replicas")
			if got != tt.want {
				t.Fatalf("ignoreDifferences group %q on %q applied = %t, want %t", tt.pattern, tt.group, got, tt.want)
			}

			settings := config.DefaultSettings()
			settings.ResourceCustomizations[tt.pattern+"/"+tt.kind] = config.ResourceCustomization{
				IgnoreDifferences: config.OverrideIgnoreDifferences{JSONPointers: []string{"/spec/template"}},
			}
			got = slices.Contains(normalizationFor(argoappv1.Application{}, id, settings).JSONPointers, "/spec/template")
			if got != tt.want {
				t.Fatalf("resource.customizations key %q on group %q applied = %t, want %t", tt.pattern+"/"+tt.kind, tt.group, got, tt.want)
			}
		})
	}

	// Kind patterns go through the same matcher.
	app := argoappv1.Application{Spec: argoappv1.ApplicationSpec{IgnoreDifferences: []argoappv1.ResourceIgnoreDifferences{{
		Group:        "apps",
		Kind:         "D*Deployment",
		JSONPointers: []string{"/spec/replicas"},
	}}}}
	if got := normalizationFor(app, manifest.Identity{Group: "apps", Kind: "Deployment"}, config.DefaultSettings()).JSONPointers; len(got) != 0 {
		t.Fatalf("ignoreDifferences kind D*Deployment on Deployment JSONPointers = %v, want none", got)
	}
}
