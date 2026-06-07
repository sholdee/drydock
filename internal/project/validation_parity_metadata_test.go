package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestValidateApplicationsUsesDiscoveredRepositorySecretProjectMetadataForHelmAndOCI(t *testing.T) {
	settings, diags := loadRepositorySecretFixture(t, `apiVersion: v1
kind: Secret
metadata:
  name: platform-helm
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: platform-helm
  type: helm
  url: https://charts.example.invalid/platform
  project: platform
  username: REDACTED_TEST_REPO_USERNAME
  password: REDACTED_TEST_REPO_PASSWORD
---
apiVersion: v1
kind: Secret
metadata:
  name: platform-oci
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: platform-oci
  type: helm
  url: ghcr.io/redacted/platform-charts
  enableOCI: "true"
  project: platform
  bearerToken: REDACTED_TEST_REPO_BEARER_TOKEN
`)
	assertNoMetadataFixtureCredentialLeak(t, settings, diags)
	assertRepositoryProject(t, settings, "https://charts.example.invalid/platform", "platform")
	assertRepositoryProject(t, settings, "ghcr.io/redacted/platform-charts", "platform")

	apps := []argoappv1.Application{
		application("http-chart", "platform", argoappv1.ApplicationSource{
			RepoURL:        "https://charts.example.invalid/platform",
			Chart:          "demo",
			TargetRevision: "1.2.3",
		}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}),
		application("oci-chart", "platform", argoappv1.ApplicationSource{
			RepoURL:        "ghcr.io/redacted/platform-charts",
			Chart:          "demo",
			TargetRevision: "1.2.3",
		}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}),
	}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"https://git.example.invalid/platform.git"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	validationDiags := ValidateApplications(apps, projects, settings)
	assertNoDiagnostic(t, validationDiags, "source repository")
	assertNoDiagnostic(t, validationDiags, "repository metadata")
}

func TestValidateApplicationsReportsRepositorySecretProjectMismatchFromMetadata(t *testing.T) {
	settings, diags := loadRepositorySecretFixture(t, `apiVersion: v1
kind: Secret
metadata:
  name: shared-helm
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: shared-helm
  type: helm
  url: https://charts.example.invalid/shared
  project: shared
  username: REDACTED_TEST_REPO_USERNAME
  password: REDACTED_TEST_REPO_PASSWORD
`)
	assertNoMetadataFixtureCredentialLeak(t, settings, diags)

	apps := []argoappv1.Application{application("shared-chart", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://charts.example.invalid/shared",
		Chart:   "demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	validationDiags := ValidateApplications(apps, projects, settings)
	assertDiagnostic(t, validationDiags, "repository metadata")
	assertDiagnostic(t, validationDiags, "scoped to project \"shared\"")
	assertNoDiagnostic(t, validationDiags, "source repository")
}

func TestValidateApplicationsUsesDiscoveredClusterSecretProjectMetadataForProjectScopedDestinations(t *testing.T) {
	settings, diags := loadClusterSecretFixture(t, `apiVersion: v1
kind: Secret
metadata:
  name: platform-cluster
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: platform-cluster
  server: https://cluster-platform.example.invalid/
  namespaces: workloads,platform-system
  clusterResources: "true"
  project: platform
  config: '{"bearerToken":"REDACTED_TEST_CLUSTER_TOKEN"}'
---
apiVersion: v1
kind: Secret
metadata:
  name: shared-cluster
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: shared-cluster
  server: https://cluster-shared.example.invalid/
  namespaces: workloads
  clusterResources: "true"
  project: shared
  config: '{"tlsClientConfig":{"certData":"REDACTED_TEST_CLUSTER_CERT"}}'
`)
	assertNoMetadataFixtureCredentialLeak(t, settings, diags)
	assertClusterProject(t, settings, "https://cluster-platform.example.invalid", "platform")
	assertClusterProject(t, settings, "https://cluster-shared.example.invalid", "shared")

	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:                     []string{"*"},
			Destinations:                    []argoappv1.ApplicationDestination{{Server: "*", Namespace: "workloads"}},
			PermitOnlyProjectScopedClusters: true,
		},
	}}

	allowedDiags := ValidateApplications([]argoappv1.Application{application("platform-workload", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://git.example.invalid/platform.git",
		Path:    "apps/platform-workload",
	}, argoappv1.ApplicationDestination{Name: "platform-cluster", Namespace: "workloads"})}, projects, settings)
	assertNoDiagnostic(t, allowedDiags, "project-scoped cluster Secrets")
	assertNoDiagnostic(t, allowedDiags, "destination is not permitted")
	assertNoDiagnostic(t, allowedDiags, "cannot be resolved")

	deniedDiags := ValidateApplications([]argoappv1.Application{application("shared-workload", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://git.example.invalid/platform.git",
		Path:    "apps/shared-workload",
	}, argoappv1.ApplicationDestination{Name: "shared-cluster", Namespace: "workloads"})}, projects, settings)
	assertDiagnostic(t, deniedDiags, "destination is not permitted")
	assertNoDiagnostic(t, deniedDiags, "project-scoped cluster Secrets")
	assertNoDiagnostic(t, deniedDiags, "cannot be resolved")
}

func loadRepositorySecretFixture(t *testing.T, content string) (config.ArgoSettings, []diagnostic.Diagnostic) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repository-secrets.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	settings, diags, err := config.LoadRepositorySecret(path)
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("repository Secret diagnostics = %#v, want none", diags)
	}
	return settings, diags
}

func loadClusterSecretFixture(t *testing.T, content string) (config.ArgoSettings, []diagnostic.Diagnostic) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cluster-secrets.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	settings, diags, err := config.LoadClusterSecret(path)
	if err != nil {
		t.Fatalf("LoadClusterSecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("cluster Secret diagnostics = %#v, want none", diags)
	}
	return settings, diags
}

func assertRepositoryProject(t *testing.T, settings config.ArgoSettings, repoURL, project string) {
	t.Helper()
	repo, ok := settings.HelmRepositories[repoURL]
	if !ok {
		t.Fatalf("repository %q missing from settings: %#v", repoURL, settings.HelmRepositories)
	}
	if repo.Project != project {
		t.Fatalf("repository %q project = %q, want %q", repoURL, repo.Project, project)
	}
}

func assertClusterProject(t *testing.T, settings config.ArgoSettings, server, project string) {
	t.Helper()
	cluster, ok := settings.Clusters[server]
	if !ok {
		t.Fatalf("cluster %q missing from settings: %#v", server, settings.Clusters)
	}
	if cluster.Project != project {
		t.Fatalf("cluster %q project = %q, want %q", server, cluster.Project, project)
	}
}

func assertNoMetadataFixtureCredentialLeak(t *testing.T, settings config.ArgoSettings, diags []diagnostic.Diagnostic) {
	t.Helper()
	serialized, err := json.Marshal(struct {
		Settings    config.ArgoSettings     `json:"settings"`
		Diagnostics []diagnostic.Diagnostic `json:"diagnostics"`
	}{
		Settings:    settings,
		Diagnostics: diags,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, forbidden := range []string{
		"REDACTED_TEST_",
		"username",
		"password",
		"bearerToken",
		"tlsClientConfig",
		"certData",
		"config",
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("Secret fixture credential data was retained or leaked: %s", serialized)
		}
	}
}
