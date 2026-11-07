package project

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestValidateApplicationsSourceRepositoryParityWithArgoCD(t *testing.T) {
	tests := []struct {
		name                    string
		sourceRepos             []string
		sources                 argoappv1.ApplicationSources
		wantArgoCDDeniedSources int
	}{
		{
			name:        "exact repository allow",
			sourceRepos: []string{"https://github.com/example/gitops"},
			sources:     sourceParitySources("https://github.com/example/gitops"),
		},
		{
			name:        "wildcard repository allow",
			sourceRepos: []string{"https://github.com/example/*"},
			sources:     sourceParitySources("https://github.com/example/service"),
		},
		{
			name:                    "deny pattern takes precedence over allow pattern",
			sourceRepos:             []string{"https://github.com/example/*", "!https://github.com/example/private"},
			sources:                 sourceParitySources("https://github.com/example/private"),
			wantArgoCDDeniedSources: 1,
		},
		{
			name: "normalized Git URL variants upstream allows",
			sourceRepos: []string{
				"https://GITHUB.com/example/gitops.git",
				"git@GITHUB.com:example/platform.git",
			},
			sources: sourceParitySources(
				"https://github.com/example/gitops",
				"ssh://git@github.com/example/platform",
			),
		},
		{
			name:        "multi-source application with one denied source",
			sourceRepos: []string{"https://github.com/example/allowed"},
			sources: sourceParitySources(
				"https://github.com/example/allowed",
				"https://github.com/example/denied",
			),
			wantArgoCDDeniedSources: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := sourceParityProject(tt.sourceRepos)
			app := sourceParityApplication(tt.sources)

			diags := ValidateApplications(
				[]argoappv1.Application{app},
				[]argoappv1.AppProject{proj},
				config.DefaultSettings(),
			)

			assertSourceRepositoryParityWithArgoCD(t, app, proj, diags, tt.wantArgoCDDeniedSources)
		})
	}
}

func sourceParityProject(sourceRepos []string) argoappv1.AppProject {
	return argoappv1.AppProject{
		Name: "platform", Namespace: "argocd",
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: append([]string(nil), sourceRepos...),
			Destinations: []argoappv1.ApplicationDestination{{
				Name:      "*",
				Namespace: "*",
			}},
		},
	}
}

func sourceParityApplication(sources argoappv1.ApplicationSources) argoappv1.Application {
	return argoappv1.Application{
		Name: "source-parity", Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Project: "platform",
			Sources: sources,
			Destination: argoappv1.ApplicationDestination{
				Name:      "in-cluster",
				Namespace: "workloads",
			},
		},
	}
}

func sourceParitySources(repoURLs ...string) argoappv1.ApplicationSources {
	sources := make(argoappv1.ApplicationSources, 0, len(repoURLs))
	for _, repoURL := range repoURLs {
		sources = append(sources, argoappv1.ApplicationSource{
			RepoURL: repoURL,
			Path:    "apps/source-parity",
		})
	}
	return sources
}

func assertSourceRepositoryParityWithArgoCD(t *testing.T, app argoappv1.Application, proj argoappv1.AppProject, diags []diagnostic.Diagnostic, wantArgoCDDeniedSources int) {
	t.Helper()

	sourceDiags := sourceRepositoryProjectDiagnostics(diags)
	deniedSources := 0
	for _, source := range app.Spec.GetSources() {
		permittedByArgoCD := proj.IsSourcePermitted(source)
		hasDrydockDiagnostic := hasSourceRepositoryDiagnosticForRepo(sourceDiags, source.RepoURL)
		if permittedByArgoCD && hasDrydockDiagnostic {
			t.Fatalf("Argo CD permits source %q, but drydock emitted source repository diagnostics: %#v", source.RepoURL, sourceDiags)
		}
		if !permittedByArgoCD {
			deniedSources++
			if !hasDrydockDiagnostic {
				t.Fatalf("Argo CD denies source %q, but drydock diagnostics = %#v", source.RepoURL, diags)
			}
		}
	}

	if deniedSources != wantArgoCDDeniedSources {
		t.Fatalf("Argo CD denied %d sources for fixture, want %d", deniedSources, wantArgoCDDeniedSources)
	}
	if len(sourceDiags) != deniedSources {
		t.Fatalf("source repository diagnostics = %#v, want %d diagnostics derived from Argo CD", sourceDiags, deniedSources)
	}
	for _, diag := range diags {
		if diag.Category == projectDiagnosticCategory && !isSourceRepositoryProjectDiagnostic(diag) {
			t.Fatalf("unexpected non-source project diagnostic in source parity fixture: %#v", diag)
		}
	}
}

func sourceRepositoryProjectDiagnostics(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	out := make([]diagnostic.Diagnostic, 0)
	for _, diag := range diags {
		if isSourceRepositoryProjectDiagnostic(diag) {
			out = append(out, diag)
		}
	}
	return out
}

func isSourceRepositoryProjectDiagnostic(diag diagnostic.Diagnostic) bool {
	return diag.Category == projectDiagnosticCategory && strings.Contains(diag.Message, "source repository")
}

func hasSourceRepositoryDiagnosticForRepo(diags []diagnostic.Diagnostic, repoURL string) bool {
	repoDisplay := displayRepoURL(repoURL)
	for _, diag := range diags {
		if strings.Contains(diag.Message, repoDisplay) {
			return true
		}
	}
	return false
}

// TestValidateApplicationsHydratorSourceRepositories pins the repositories
// that the AppProject must permit for an Application with
// spec.sourceHydrator: the dry source's, and the sync source's when it names
// another repository (ValidatePermissions, util/argo/argo.go:701-719 at
// v3.6.1). spec.source is ignored.
func TestValidateApplicationsHydratorSourceRepositories(t *testing.T) {
	const allowed = "https://github.com/example/allowed"
	const denied = "https://github.com/example/denied"
	tests := []struct {
		name     string
		dryRepo  string
		syncRepo string
		want     []string
	}{
		{name: "dry repository allowed, sync inherits it", dryRepo: allowed},
		{name: "dry repository denied, sync inherits it", dryRepo: denied, want: []string{denied}},
		{name: "dry repository denied, sync repository allowed", dryRepo: denied, syncRepo: allowed, want: []string{denied}},
		{name: "dry repository allowed, sync repository denied", dryRepo: allowed, syncRepo: denied, want: []string{denied}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := sourceParityApplication(nil)
			app.Spec.Source = &argoappv1.ApplicationSource{RepoURL: denied + "-ignored", Path: "ignored"}
			app.Spec.SourceHydrator = &argoappv1.SourceHydrator{
				DrySource:  argoappv1.DrySource{RepoURL: tt.dryRepo, Path: "dry", TargetRevision: "main"},
				SyncSource: argoappv1.SyncSource{RepoURL: tt.syncRepo, TargetBranch: "env/prod", Path: "hydrated"},
			}
			diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{sourceParityProject([]string{allowed})}, config.DefaultSettings())
			var got []string
			for _, diag := range diags {
				if diag.Code == diagnostic.CodeProjectSourceRepositoryDenied || strings.Contains(diag.Message, "source repository") {
					got = append(got, diag.Message)
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("source repository diagnostics = %q, want %d naming %q", got, len(tt.want), tt.want)
			}
			for i, repo := range tt.want {
				if !strings.Contains(got[i], repo) {
					t.Fatalf("diagnostic %q does not name %q", got[i], repo)
				}
			}
		})
	}
}

// TestRepositoryMetadataChecksHydratorDrySource pins that the repository
// metadata check covers the dry source repository of a source hydrator.
func TestRepositoryMetadataChecksHydratorDrySource(t *testing.T) {
	const dry = "https://github.com/example/config"
	app := sourceParityApplication(nil)
	app.Spec.SourceHydrator = &argoappv1.SourceHydrator{
		DrySource:  argoappv1.DrySource{RepoURL: dry, Path: "app", TargetRevision: "main"},
		SyncSource: argoappv1.SyncSource{RepoURL: "https://github.com/example/deployments", TargetBranch: "env/prod", Path: "app"},
	}
	settings := config.DefaultSettings()
	settings.HelmRepositories[dry] = config.RepositorySettings{URL: dry, Project: "other"}
	diags := repositoryMetadataDiagnostics(app, settings)
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "github.com/example/config") {
		t.Fatalf("repository metadata diagnostics = %#v, want one for the dry source repository", diags)
	}
}
