package project

import (
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
)

// A repository entry reaches every Application validation applies it to:
// as repository metadata for a source URL in its plain, Git, or (with
// enableOCI) OCI form, and, in its project, as a sourceRepos entry Argo CD
// matches as a normalized glob, a deny pattern reaching every source there.
func TestRepositoryMatchReaches(t *testing.T) {
	const (
		chartsX = "https://charts.example/x"
		chartsY = "https://charts.example/y"
	)
	app := func(project, repoURL string) argoappv1.Application {
		return argoappv1.Application{Spec: argoappv1.ApplicationSpec{
			Project: project,
			Sources: argoappv1.ApplicationSources{
				{RepoURL: "https://github.com/example/values", Ref: "values"},
				{RepoURL: repoURL, Chart: "app", TargetRevision: "1.0.0"},
			},
		}}
	}
	// single has only the one source: a deny pattern alone permits any
	// other, such as app's $ref source.
	single := func(project, repoURL string) argoappv1.Application {
		return argoappv1.Application{Spec: argoappv1.ApplicationSpec{
			Project: project,
			Source:  &argoappv1.ApplicationSource{RepoURL: repoURL, Chart: "app", TargetRevision: "1.0.0"},
		}}
	}
	for _, tc := range []struct {
		name string
		key  string
		repo config.RepositorySettings
		app  argoappv1.Application
		want bool
	}{
		{name: "same URL", key: chartsX, repo: config.RepositorySettings{URL: chartsX}, app: app("", chartsX), want: true},
		{name: "key only", key: chartsX, repo: config.RepositorySettings{}, app: app("", chartsX), want: true},
		{name: "Git form", key: "https://github.com/example/repo/", repo: config.RepositorySettings{URL: "https://user@github.com/example/repo.git"}, app: app("", "https://github.com/example/repo"), want: true},
		{name: "OCI form with enableOCI", key: "ghcr.io/example/charts", repo: config.RepositorySettings{URL: "ghcr.io/example/charts", EnableOCI: true}, app: app("", "oci://ghcr.io/example/charts"), want: true},
		{name: "OCI form without enableOCI", key: "ghcr.io/example/charts", repo: config.RepositorySettings{URL: "ghcr.io/example/charts"}, app: app("", "oci://ghcr.io/example/charts")},
		{name: "$ref source", key: "https://github.com/example/values", repo: config.RepositorySettings{URL: "https://github.com/example/values"}, app: app("", chartsY), want: true},
		{name: "other URL", key: chartsX, repo: config.RepositorySettings{URL: chartsX, Project: "team-a"}, app: app("team-a", chartsY)},
		{name: "project glob, Argo CD normalization", key: "glob", repo: config.RepositorySettings{URL: "HTTPS://Charts.Example/*", Project: "team-a"}, app: app("team-a", chartsY), want: true},
		{name: "project glob, other project", key: "glob", repo: config.RepositorySettings{URL: "https://charts.example/*", Project: "team-a"}, app: app("team-b", chartsY)},
		{name: "SSH forms in its project", key: "ssh", repo: config.RepositorySettings{URL: "ssh://git@github.com/example/repo", Project: "team-a"}, app: app("team-a", "git@github.com:example/repo.git"), want: true},
		{name: "deny pattern, its project", key: "!" + chartsX, repo: config.RepositorySettings{URL: "!" + chartsX, Project: "team-a"}, app: app("team-a", chartsY), want: true},
		{name: "deny pattern, its own URL", key: "!" + chartsX, repo: config.RepositorySettings{URL: "!" + chartsX, Project: "team-a"}, app: single("team-a", chartsX), want: true},
		{name: "deny pattern, other project", key: "!" + chartsX, repo: config.RepositorySettings{URL: "!" + chartsX, Project: "team-a"}, app: app("team-b", chartsY)},
		{name: "default-project deny pattern, any project", key: "!" + chartsX, repo: config.RepositorySettings{URL: "!" + chartsX, Project: argoappv1.DefaultAppProjectName}, app: single("team-x", chartsX), want: true},
		{name: "default project", key: "glob", repo: config.RepositorySettings{URL: "https://charts.example/*", Project: argoappv1.DefaultAppProjectName}, app: app("", chartsY), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewRepositoryMatch(tc.key, tc.repo).Reaches(tc.app); got != tc.want {
				t.Fatalf("Reaches() = %t, want %t", got, tc.want)
			}
		})
	}
}
