package project

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestValidateApplicationsSourceNamespaceParityWithArgoCD(t *testing.T) {
	tests := []struct {
		name             string
		appNamespace     string
		sourceNamespaces []string
	}{
		{
			name:             "controller namespace default allow",
			appNamespace:     "argocd",
			sourceNamespaces: []string{"team-a"},
		},
		{
			name:             "explicit project source namespace allow",
			appNamespace:     "team-a",
			sourceNamespaces: []string{"team-a"},
		},
		{
			name:             "wildcard source namespace allow",
			appNamespace:     "team-prod",
			sourceNamespaces: []string{"team-*"},
		},
		{
			name:             "empty source namespaces deny non-controller namespace",
			appNamespace:     "team-a",
			sourceNamespaces: []string{},
		},
		{
			name:             "empty source namespaces allow controller namespace",
			appNamespace:     "argocd",
			sourceNamespaces: []string{},
		},
		{
			name:             "denied source namespace warning",
			appNamespace:     "team-b",
			sourceNamespaces: []string{"team-a"},
		},
		{
			name:             "single slash entry is not a regexp",
			appNamespace:     "team-a",
			sourceNamespaces: []string{"/"},
		},
		{
			name:             "empty regexp entry is not a regexp",
			appNamespace:     "team-a",
			sourceNamespaces: []string{"//"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := sourceNamespaceParityApplication(tt.appNamespace)
			proj := sourceNamespaceParityProject(tt.sourceNamespaces)
			expectedPermitted := proj.IsAppNamespacePermitted(&app, "argocd")

			diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{proj}, config.DefaultSettings())

			assertSourceNamespaceDiagnosticsMatchPermit(t, diags, expectedPermitted)
		})
	}
}

func sourceNamespaceParityApplication(namespace string) argoappv1.Application {
	return argoappv1.Application{
		Name: "demo", Namespace: namespace,
		Spec: argoappv1.ApplicationSpec{
			Project: "platform",
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://github.com/example/repo",
				Path:    "apps/demo",
			},
			Destination: argoappv1.ApplicationDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			},
		},
	}
}

func sourceNamespaceParityProject(sourceNamespaces []string) argoappv1.AppProject {
	return argoappv1.AppProject{
		Name: "platform",
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Server:    "*",
				Namespace: "*",
			}},
			SourceNamespaces: sourceNamespaces,
		},
	}
}

func assertSourceNamespaceDiagnosticsMatchPermit(t *testing.T, diags []diagnostic.Diagnostic, permitted bool) {
	t.Helper()

	sourceNamespaceDiagnostics := 0
	unexpectedProjectDiagnostics := make([]diagnostic.Diagnostic, 0)
	for _, diag := range diags {
		if diag.Category != projectDiagnosticCategory {
			continue
		}
		if strings.Contains(diag.Message, "source namespace") {
			sourceNamespaceDiagnostics++
			continue
		}
		unexpectedProjectDiagnostics = append(unexpectedProjectDiagnostics, diag)
	}
	if len(unexpectedProjectDiagnostics) > 0 {
		t.Fatalf("Diagnostics = %#v, want no non-source-namespace project diagnostics", diags)
	}

	if permitted && sourceNamespaceDiagnostics != 0 {
		t.Fatalf("Diagnostics = %#v, want no source namespace diagnostics for Argo CD-permitted namespace", diags)
	}
	if !permitted && sourceNamespaceDiagnostics != 1 {
		t.Fatalf("Diagnostics = %#v, want one source namespace diagnostic for Argo CD-denied namespace", diags)
	}
}

// Argo CD v3.6.1 util/glob.MatchStringInList treats an entry as a regexp only
// when it is longer than two characters (c49f8b712, #30572): `/` used to
// panic with a slice bounds error and `//` compiled an empty regexp that
// matched every namespace. Both are now literal globs that deny any other
// namespace.
func TestValidateApplicationsSourceNamespaceSlashEntriesDeny(t *testing.T) {
	for _, entry := range []string{"/", "//"} {
		t.Run(entry, func(t *testing.T) {
			app := sourceNamespaceParityApplication("team-a")
			proj := sourceNamespaceParityProject([]string{entry})

			diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{proj}, config.DefaultSettings())

			assertSourceNamespaceDiagnosticsMatchPermit(t, diags, false)
		})
	}
}
