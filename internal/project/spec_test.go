package project

import (
	"context"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoutil "github.com/argoproj/argo-cd/v3/util/argo"
	"github.com/sholdee/drydock/internal/config"
)

// TestValidateApplicationsStopsAfterFailedSpecChecks pins the order of
// ValidatePermissions (util/argo/argo.go:698-737 at v3.6.1): a failed
// source or hydrator check stops the AppProject checks. The sources of a
// multi-source Application before the failing one are still checked, and
// the destination is not. The upstream function runs on the same spec, with
// a destination that it rejects before it reads the cluster database, to
// count its source conditions and to show whether it reached the
// destination.
func TestValidateApplicationsStopsAfterFailedSpecChecks(t *testing.T) {
	const allowed = "https://github.com/example/allowed"
	const denied = "https://github.com/example/denied"
	testCases := []struct {
		name            string
		spec            argoappv1.ApplicationSpec
		wantSources     int
		wantDestination bool
	}{
		{name: "valid single source", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: denied, Path: "app"}}, wantSources: 1, wantDestination: true},
		{name: "invalid single source", spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: denied}}},
		{name: "invalid second source", spec: argoappv1.ApplicationSpec{Sources: argoappv1.ApplicationSources{
			{RepoURL: denied, Path: "app"},
			{RepoURL: denied},
			{RepoURL: denied, Path: "other"},
		}}, wantSources: 1},
		{name: "invalid hydrator", spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: denied, Path: "dry"},
			SyncSource: argoappv1.SyncSource{Path: "hydrated"},
		}}},
		{name: "valid hydrator", spec: argoappv1.ApplicationSpec{SourceHydrator: &argoappv1.SourceHydrator{
			DrySource:  argoappv1.DrySource{RepoURL: denied, Path: "dry"},
			SyncSource: argoappv1.SyncSource{RepoURL: allowed, TargetBranch: "env/prod", Path: "hydrated"},
		}}, wantSources: 1, wantDestination: true},
	}
	proj := argoappv1.AppProject{ObjectMeta: objectMeta("platform"), Spec: argoappv1.AppProjectSpec{
		SourceRepos:  []string{allowed},
		Destinations: []argoappv1.ApplicationDestination{{Server: "https://kubernetes.default.svc", Namespace: "allowed"}},
	}}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			upstreamSpec := testCase.spec
			upstreamSpec.Destination = argoappv1.ApplicationDestination{Name: "stop", Server: "https://stop.invalid"}
			conditions, err := argoutil.ValidatePermissions(context.Background(), &upstreamSpec, &proj, nil)
			if err != nil {
				t.Fatal(err)
			}
			upstreamSources, upstreamDestination := 0, false
			for _, condition := range conditions {
				switch {
				case strings.Contains(condition.Message, "is not permitted in project"):
					upstreamSources++
				case strings.Contains(condition.Message, "can't have both name and server"):
					upstreamDestination = true
				}
			}
			if upstreamSources != testCase.wantSources || upstreamDestination != testCase.wantDestination {
				t.Fatalf("upstream source conditions = %d, destination reached = %t; the test case is wrong", upstreamSources, upstreamDestination)
			}

			app := argoappv1.Application{Name: "demo", Namespace: "argocd", Spec: testCase.spec}
			app.Spec.Project = "platform"
			app.Spec.Destination = argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "denied"}
			sources, destination := 0, false
			for _, diag := range ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{proj}, config.DefaultSettings()) {
				switch {
				case strings.Contains(diag.Message, "source repository"):
					sources++
				case strings.Contains(diag.Message, "destination"):
					destination = true
				}
			}
			if sources != testCase.wantSources || destination != testCase.wantDestination {
				t.Fatalf("drydock source diagnostics = %d, destination checked = %t, want %d and %t", sources, destination, testCase.wantSources, testCase.wantDestination)
			}
		})
	}
}
