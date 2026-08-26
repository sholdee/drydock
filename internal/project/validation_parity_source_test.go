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
