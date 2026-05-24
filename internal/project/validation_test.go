package project

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/config"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateApplicationsReportsProjectPolicyViolations(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL:        "https://github.com/example/denied",
		Path:           "apps/demo",
		TargetRevision: "main",
	}, argoappv1.ApplicationDestination{
		Server:    "https://kubernetes.default.svc",
		Namespace: "forbidden",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"https://github.com/example/allowed"},
			Destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "source repository")
	assertDiagnostic(t, diags, "destination")
}

func TestValidateApplicationsAllowsImplicitDefaultProject(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "default",
	})}

	diags := ValidateApplications(apps, nil, config.DefaultSettings())
	if len(diags) != 0 {
		t.Fatalf("Diagnostics = %#v, want none for implicit default project", diags)
	}
}

func TestValidateApplicationsReportsSourceNamespaceAndRBACMetadata(t *testing.T) {
	apps := []argoappv1.Application{applicationInNamespace("demo", "team-b", "platform", argoappv1.ApplicationSource{
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
			SourceNamespaces: []string{"team-a"},
			Roles:            []argoappv1.ProjectRole{{Name: "developer", Policies: []string{"p, proj:platform:developer, applications, sync, platform/*, allow"}}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "source namespace")
	assertDiagnostic(t, diags, "RBAC roles")
}

func TestValidateApplicationsAllowsControllerNamespace(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:      []string{"*"},
			Destinations:     []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
			SourceNamespaces: []string{"team-a"},
		},
	}}
	settings := settingsWithRepository("https://github.com/example/repo", "platform")

	diags := ValidateApplications(apps, projects, settings)
	if len(diags) != 0 {
		t.Fatalf("Diagnostics = %#v, want none for controller namespace app", diags)
	}
}

func TestValidateApplicationsAllowsDiscoveredControllerNamespace(t *testing.T) {
	apps := []argoappv1.Application{applicationInNamespace("demo", "gitops", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMetaInNamespace("platform", "gitops"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:      []string{"*"},
			Destinations:     []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
			SourceNamespaces: []string{"team-a"},
		},
	}}
	settings := settingsWithRepository("https://github.com/example/repo", "platform")

	diags := ValidateApplications(apps, projects, settings)
	if len(diags) != 0 {
		t.Fatalf("Diagnostics = %#v, want none for custom controller namespace app", diags)
	}
}

func TestValidateApplicationsReportsProjectScopedClusterPolicyAsDeferred(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:                     []string{"*"},
			Destinations:                    []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
			PermitOnlyProjectScopedClusters: true,
		},
	}}
	settings := settingsWithRepository("https://github.com/example/repo", "platform")

	diags := ValidateApplications(apps, projects, settings)
	assertDiagnostic(t, diags, "project-scoped cluster Secrets")
	assertNoDiagnostic(t, diags, "destination is not permitted")
}

func TestValidateApplicationsReportsRepositoryMetadataIssues(t *testing.T) {
	settings := config.DefaultSettings()
	settings.HelmRepositories["https://github.com/example/repo"] = config.RepositorySettings{
		URL:     "https://github.com/example/repo",
		Project: "other",
	}
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, settings)
	assertDiagnostic(t, diags, "repository metadata")
}

func TestValidateApplicationsReportsMissingRepositoryMetadataForRemoteGitPathSource(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "missing repository metadata")
}

func TestValidateApplicationsRedactsCredentialBearingRepoURLsInDiagnostics(t *testing.T) {
	secretURL := "https://user:password@example.test/org/repo.git?token=query-secret#frag-secret"
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: secretURL,
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"https://github.com/example/allowed"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "https://example.test/org/repo.git")
	assertNoDiagnostic(t, diags, "user")
	assertNoDiagnostic(t, diags, "password")
	assertNoDiagnostic(t, diags, "query-secret")
	assertNoDiagnostic(t, diags, "frag-secret")
	assertNoDiagnostic(t, diags, "?token")
	assertNoDiagnostic(t, diags, "#frag")
}

func assertDiagnostic(t *testing.T, diags []diagnostic.Diagnostic, fragment string) {
	t.Helper()
	for _, diag := range diags {
		if strings.Contains(diag.Message, fragment) {
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want message containing %q", diags, fragment)
}

func assertNoDiagnostic(t *testing.T, diags []diagnostic.Diagnostic, fragment string) {
	t.Helper()
	for _, diag := range diags {
		if strings.Contains(diag.Message, fragment) {
			t.Fatalf("Diagnostics = %#v, want no message containing %q", diags, fragment)
		}
	}
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: "argocd"}
}

func objectMetaInNamespace(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace}
}

func application(name, project string, source argoappv1.ApplicationSource, destination argoappv1.ApplicationDestination) argoappv1.Application {
	return applicationInNamespace(name, "argocd", project, source, destination)
}

func applicationInNamespace(name, namespace, project string, source argoappv1.ApplicationSource, destination argoappv1.ApplicationDestination) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: argoappv1.ApplicationSpec{
			Project:     project,
			Source:      &source,
			Destination: destination,
		},
	}
}

func settingsWithRepository(repoURL, project string) config.ArgoSettings {
	settings := config.DefaultSettings()
	settings.HelmRepositories[repoURL] = config.RepositorySettings{URL: repoURL, Project: project}
	return settings
}
