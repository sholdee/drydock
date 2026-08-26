package project

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateApplicationsCurrentBehaviorReportsDeniedMultiSourceRepository(t *testing.T) {
	apps := []argoappv1.Application{applicationWithSources("multi-source", "platform", argoappv1.ApplicationSources{
		{
			RepoURL: "https://github.com/example/allowed",
			Path:    "apps/allowed",
		},
		{
			RepoURL: "https://github.com/example/denied",
			Path:    "apps/denied",
		},
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "workloads",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"https://github.com/example/allowed"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnosticCategoryAndMessage(t, diags, projectDiagnosticCategory, "source repository")
	assertDiagnostic(t, diags, "https://github.com/example/denied")
	assertNoDiagnostic(t, diags, "https://github.com/example/allowed")
	assertNoDiagnostic(t, diags, "destination")
}

func TestValidateApplicationsCurrentBehaviorPhase3SourceNamespacesEmptyListDeniesNonControllerNamespace(t *testing.T) {
	apps := []argoappv1.Application{applicationInNamespace("demo", "team-a", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "workloads",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:      []string{"*"},
			Destinations:     []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
			SourceNamespaces: []string{},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnosticCategoryAndMessage(t, diags, projectDiagnosticCategory, "source namespace")
	assertDiagnostic(t, diags, `"team-a"`)
}

func TestValidateApplicationsCurrentBehaviorPhase4ResourcePolicyFieldsEmitNoProjectDiagnostics(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "workloads",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Name:      "*",
				Namespace: "*",
			}},
			ClusterResourceWhitelist: []argoappv1.ClusterResourceRestrictionItem{{
				Group: "",
				Kind:  "Namespace",
			}},
			ClusterResourceBlacklist: []argoappv1.ClusterResourceRestrictionItem{{
				Group: "",
				Kind:  "Node",
			}},
			NamespaceResourceWhitelist: []metav1.GroupKind{{
				Group: "apps",
				Kind:  "Deployment",
			}},
			NamespaceResourceBlacklist: []metav1.GroupKind{{
				Group: "",
				Kind:  "Secret",
			}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertNoDiagnosticCategory(t, diags, projectDiagnosticCategory)
}

func TestValidateApplicationsRuntimeBoundFieldsDoNotSimulateLivePolicy(t *testing.T) {
	warn := true
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{
		Server:    "https://kubernetes.default.svc",
		Namespace: "workloads",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Server:    "*",
				Namespace: "*",
			}},
			SyncWindows: argoappv1.SyncWindows{{
				Kind:         "deny",
				Schedule:     "* * * * *",
				Duration:     "1h",
				Applications: []string{"demo"},
			}},
			OrphanedResources: &argoappv1.OrphanedResourcesMonitorSettings{
				Warn: &warn,
				Ignore: []argoappv1.OrphanedResourceKey{{
					Group: "apps",
					Kind:  "Deployment",
					Name:  "ignored",
				}},
			},
			SignatureKeys: []argoappv1.SignatureKey{{ //nolint:staticcheck // deprecated in Argo CD v3.5, but still appears on real AppProjects this test documents
				KeyID: "0123456789ABCDEF",
			}},
			DestinationServiceAccounts: []argoappv1.ApplicationDestinationServiceAccount{{
				Server:                "https://kubernetes.default.svc",
				Namespace:             "workloads",
				DefaultServiceAccount: "deployer",
			}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertNoDiagnosticCategory(t, diags, projectDiagnosticCategory)
}

func TestValidateApplicationsCurrentBehaviorAllowsImplicitDefaultProjectWhenOtherLocalProjectsExist(t *testing.T) {
	apps := []argoappv1.Application{application("demo", argoappv1.DefaultAppProjectName, argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "workloads",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"https://github.com/example/other"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertNoDiagnosticCategory(t, diags, projectDiagnosticCategory)
}

func TestValidateApplicationsCurrentBehaviorReportsMissingNonDefaultProject(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "missing", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "workloads",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnosticCategoryAndMessage(t, diags, projectDiagnosticCategory, "references missing AppProject")
	assertDiagnostic(t, diags, `"missing"`)
}

func applicationWithSources(name, project string, sources argoappv1.ApplicationSources, destination argoappv1.ApplicationDestination) argoappv1.Application {
	return argoappv1.Application{
		Name: name, Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Project:     project,
			Sources:     sources,
			Destination: destination,
		},
	}
}

func assertDiagnosticCategoryAndMessage(t *testing.T, diags []diagnostic.Diagnostic, category, fragment string) {
	t.Helper()
	for _, diag := range diags {
		if diag.Category == category && strings.Contains(diag.Message, fragment) {
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want category %q with message containing %q", diags, category, fragment)
}

func assertNoDiagnosticCategory(t *testing.T, diags []diagnostic.Diagnostic, category string) {
	t.Helper()
	for _, diag := range diags {
		if diag.Category == category {
			t.Fatalf("Diagnostics = %#v, want no diagnostics in category %q", diags, category)
		}
	}
}
