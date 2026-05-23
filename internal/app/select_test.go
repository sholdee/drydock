package app

import (
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSelectChangedApplicationsKeepsIntersectingApps(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication("renovate", &argoappv1.ApplicationSource{Path: "./apps//renovate"}, nil),
		testApplication("adguard", &argoappv1.ApplicationSource{Path: "apps/adguard"}, nil),
	}

	selected, unowned := SelectChangedApplications(apps, []string{"./apps//renovate/kustomization.yaml"})

	assertApplicationNames(t, selected, []string{"renovate"})
	assertStrings(t, unowned, nil)
}

func TestSelectChangedApplicationsReportsUnowned(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication("renovate", &argoappv1.ApplicationSource{Path: "apps/renovate"}, nil),
	}

	selected, unowned := SelectChangedApplications(apps, []string{"README.md"})

	assertApplicationNames(t, selected, nil)
	assertStrings(t, unowned, []string{"README.md"})
}

func TestSelectChangedApplicationsKeepsOverlappingApplications(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication("shared", &argoappv1.ApplicationSource{Path: "apps/shared"}, nil),
		testApplication("shared-config", &argoappv1.ApplicationSource{Path: "apps/shared/config"}, nil),
	}

	selected, unowned := SelectChangedApplications(apps, []string{"apps/shared/config/cm.yaml"})

	assertApplicationNames(t, selected, []string{"shared", "shared-config"})
	assertStrings(t, unowned, nil)
}

func TestSelectChangedApplicationsUsesSourcesOverSource(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication(
			"multi",
			&argoappv1.ApplicationSource{Path: "apps/ignored"},
			argoappv1.ApplicationSources{
				{Path: "apps/current"},
			},
		),
	}

	selected, unowned := SelectChangedApplications(apps, []string{
		"apps/ignored/cm.yaml",
		"apps/current/cm.yaml",
	})

	assertApplicationNames(t, selected, []string{"multi"})
	assertStrings(t, unowned, []string{"apps/ignored/cm.yaml"})
}

func TestSelectChangedApplicationsMatchesWholePathSegments(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication("a", &argoappv1.ApplicationSource{Path: "apps/a"}, nil),
	}

	selected, unowned := SelectChangedApplications(apps, []string{"apps/ab/cm.yaml"})

	assertApplicationNames(t, selected, nil)
	assertStrings(t, unowned, []string{"apps/ab/cm.yaml"})
}

func TestSelectChangedApplicationsSkipsSourcesWithoutLocalPath(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication(
			"remote",
			nil,
			argoappv1.ApplicationSources{
				{Chart: "nginx", RepoURL: "https://charts.example.com"},
				{Ref: "values", RepoURL: "https://git.example.com/values.git"},
			},
		),
	}

	selected, unowned := SelectChangedApplications(apps, []string{"charts/nginx/values.yaml"})

	assertApplicationNames(t, selected, nil)
	assertStrings(t, unowned, []string{"charts/nginx/values.yaml"})
}

func TestSelectChangedApplicationsRootPathOwnsAllChangedPaths(t *testing.T) {
	tests := []struct {
		name   string
		source argoappv1.ApplicationSource
	}{
		{name: "dot", source: argoappv1.ApplicationSource{Path: "."}},
		{name: "empty", source: argoappv1.ApplicationSource{RepoURL: "https://git.example.com/root.git"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apps := []argoappv1.Application{
				testApplication("root", &tt.source, nil),
			}

			selected, unowned := SelectChangedApplications(apps, []string{"apps/a/cm.yaml"})

			assertApplicationNames(t, selected, []string{"root"})
			assertStrings(t, unowned, nil)
		})
	}
}

func TestSelectChangedApplicationsUsesSameRepoRefHelmValueFiles(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication(
			"helm",
			nil,
			argoappv1.ApplicationSources{
				{RepoURL: " https://example.com/repo.git/ ", Ref: "values"},
				{
					RepoURL: "https://example.com/repo",
					Path:    "charts/demo",
					Helm: &argoappv1.ApplicationSourceHelm{
						ValueFiles: []string{"$values/values/demo.yaml"},
					},
				},
			},
		),
	}

	selected, unowned := SelectChangedApplications(apps, []string{"values/demo.yaml"})

	assertApplicationNames(t, selected, []string{"helm"})
	assertStrings(t, unowned, nil)
}

func TestSelectChangedApplicationsSkipsRemoteChartOnlyOrdinaryValueFiles(t *testing.T) {
	apps := []argoappv1.Application{
		testApplication(
			"remote-chart",
			&argoappv1.ApplicationSource{
				RepoURL: "https://charts.example.com",
				Chart:   "demo",
				Helm: &argoappv1.ApplicationSourceHelm{
					ValueFiles: []string{"values/demo.yaml"},
				},
			},
			nil,
		),
	}

	selected, unowned := SelectChangedApplications(apps, []string{"values/demo.yaml"})

	assertApplicationNames(t, selected, nil)
	assertStrings(t, unowned, []string{"values/demo.yaml"})
}

func testApplication(name string, source *argoappv1.ApplicationSource, sources argoappv1.ApplicationSources) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: argoappv1.ApplicationSpec{
			Source:  source,
			Sources: sources,
		},
	}
}

func assertApplicationNames(t *testing.T, apps []argoappv1.Application, want []string) {
	t.Helper()
	got := make([]string, 0, len(apps))
	for _, app := range apps {
		got = append(got, app.Name)
	}
	assertStrings(t, got, want)
}

func assertStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
