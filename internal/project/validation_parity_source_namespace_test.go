package project

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace},
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
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
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
