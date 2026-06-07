package project

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestValidateApplicationsDestinationParityWithArgoCD(t *testing.T) {
	const projectName = "platform"

	tests := []struct {
		name           string
		destinations   []argoappv1.ApplicationDestination
		appDestination argoappv1.ApplicationDestination
		clusters       []destinationParityCluster
		projectScoped  bool
		expectDeferred bool
	}{
		{
			name: "server-only destination",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			},
		},
		{
			name: "name-only destination with known redacted cluster metadata",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Name:      "in-cluster",
				Namespace: "workloads",
			},
			clusters: []destinationParityCluster{{
				Name:    "in-cluster",
				Server:  "https://kubernetes.default.svc",
				Project: projectName,
			}},
		},
		{
			name: "name-only destination without metadata defers server policy resolution",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Name:      "in-cluster",
				Namespace: "workloads",
			},
			expectDeferred: true,
		},
		{
			name: "destination with both server and name",
			destinations: []argoappv1.ApplicationDestination{{
				Name:      "in-cluster",
				Namespace: "workloads",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Name:      "in-cluster",
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			},
		},
		{
			name: "namespace wildcard",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "*",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: "kube-system",
			},
		},
		{
			name: "namespace deny pattern",
			destinations: []argoappv1.ApplicationDestination{
				{
					Server:    "*",
					Namespace: "*",
				},
				{
					Server:    "*",
					Namespace: "!kube-system",
				},
			},
			appDestination: argoappv1.ApplicationDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: "kube-system",
			},
		},
		{
			name: "server deny pattern",
			destinations: []argoappv1.ApplicationDestination{
				{
					Server:    "*",
					Namespace: "*",
				},
				{
					Server:    "!https://kubernetes.default.svc",
					Namespace: "*",
				},
			},
			appDestination: argoappv1.ApplicationDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			},
		},
		{
			name: "permitOnlyProjectScopedClusters explicit allow with matching metadata",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "*",
				Namespace: "*",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Name:      "prod",
				Namespace: "workloads",
			},
			clusters: []destinationParityCluster{{
				Name:    "prod",
				Server:  "https://prod.example",
				Project: projectName,
			}},
			projectScoped: true,
		},
		{
			name: "permitOnlyProjectScopedClusters deny with mismatched metadata",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "*",
				Namespace: "*",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Name:      "prod",
				Namespace: "workloads",
			},
			clusters: []destinationParityCluster{{
				Name:    "prod",
				Server:  "https://prod.example",
				Project: "other",
			}},
			projectScoped: true,
		},
		{
			name: "permitOnlyProjectScopedClusters without metadata defers enforcement",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "*",
				Namespace: "*",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Name:      "prod",
				Namespace: "workloads",
			},
			projectScoped:  true,
			expectDeferred: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := argoappv1.AppProject{
				ObjectMeta: objectMeta(projectName),
				Spec: argoappv1.AppProjectSpec{
					SourceRepos:                     []string{"*"},
					Destinations:                    tt.destinations,
					PermitOnlyProjectScopedClusters: tt.projectScoped,
				},
			}
			settings := destinationParitySettings(tt.clusters)
			expectedPermitted := destinationParityPermitted(t, project, tt.appDestination, settings)

			apps := []argoappv1.Application{application("demo", projectName, argoappv1.ApplicationSource{
				RepoURL: "https://github.com/example/repo",
				Path:    "apps/demo",
			}, tt.appDestination)}
			diags := ValidateApplications(apps, []argoappv1.AppProject{project}, settings)

			hasDestinationDeny := hasProjectDestinationDiagnostic(diags, "destination is not permitted")
			hasDeferred := hasProjectDestinationDiagnostic(diags, "cannot be resolved against AppProject server policy offline") ||
				hasProjectDestinationDiagnostic(diags, "project-scoped cluster Secrets enforcement is deferred offline")

			if tt.expectDeferred {
				if !hasDeferred {
					t.Fatalf("Diagnostics = %#v, want deferred destination diagnostic", diags)
				}
				if hasDestinationDeny {
					t.Fatalf("Diagnostics = %#v, want deferred validation without hard destination denial", diags)
				}
				return
			}
			if expectedPermitted && hasDestinationDeny {
				t.Fatalf("Diagnostics = %#v, want no destination denial because Argo CD permits destination", diags)
			}
			if !expectedPermitted && !hasDestinationDeny {
				t.Fatalf("Diagnostics = %#v, want destination denial because Argo CD denies destination", diags)
			}
			if hasDeferred {
				t.Fatalf("Diagnostics = %#v, want no deferred destination diagnostic when metadata is sufficient", diags)
			}
		})
	}
}

type destinationParityCluster struct {
	Name    string
	Server  string
	Project string
}

func destinationParityPermitted(t *testing.T, project argoappv1.AppProject, dest argoappv1.ApplicationDestination, settings config.ArgoSettings) bool {
	t.Helper()

	destCluster, _ := destinationCluster(dest, settings)
	permitted, err := project.IsDestinationPermitted(redactedClusterMetadata(destCluster), dest.Namespace, func(projectName string) ([]*argoappv1.Cluster, error) {
		clusters, _ := projectScopedClusters(projectName, settings)
		redacted := make([]*argoappv1.Cluster, 0, len(clusters))
		for _, cluster := range clusters {
			redacted = append(redacted, redactedClusterMetadata(cluster))
		}
		return redacted, nil
	})
	if err != nil {
		t.Fatalf("Argo CD IsDestinationPermitted returned error: %v", err)
	}
	return permitted
}

func redactedClusterMetadata(cluster *argoappv1.Cluster) *argoappv1.Cluster {
	if cluster == nil {
		return nil
	}
	return cluster.Sanitized()
}

func destinationParitySettings(clusters []destinationParityCluster) config.ArgoSettings {
	settings := config.DefaultSettings()
	for _, cluster := range clusters {
		server := strings.TrimRight(strings.TrimSpace(cluster.Server), "/")
		settings.Clusters[server] = config.ClusterSettings{
			Name:    cluster.Name,
			Server:  server,
			Project: cluster.Project,
		}
	}
	return settings
}

func hasProjectDestinationDiagnostic(diags []diagnostic.Diagnostic, fragment string) bool {
	for _, diag := range diags {
		if diag.Category == projectDiagnosticCategory && strings.Contains(diag.Message, fragment) {
			return true
		}
	}
	return false
}
