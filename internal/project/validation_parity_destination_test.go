package project

import (
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

// TestValidateApplicationsDestinationParityWithArgoCD checks drydock's offline
// destination verdict against Argo CD's own AppProject.IsDestinationPermitted,
// fed the project and cluster Argo CD would build (argoDestinationPermitted):
// the project with its project-scoped cluster destinations
// (GetAppProjectByName) and the destination cluster resolved through the
// cluster Secrets Argo CD sees, built-in in-cluster cluster included
// (GetDestinationCluster).
//
// clusters are the cluster Secrets the repository declares, which drydock
// sees. live lists cluster Secret sets the running Argo CD might hold instead
// (each in addition to clusters); without it Argo CD sees exactly clusters.
// drydock cannot see a live cluster's project either, so each live set
// without a project is also tried scoped to the Application's project
// (argoDestinationVerdicts). A definite drydock verdict must match Argo CD
// for every live set. A deferred one must be justified: Argo CD must permit
// for one live set and deny for another.
func TestValidateApplicationsDestinationParityWithArgoCD(t *testing.T) {
	const projectName = "platform"

	tests := []struct {
		name           string
		destinations   []argoappv1.ApplicationDestination
		appDestination argoappv1.ApplicationDestination
		clusters       []destinationParityCluster
		live           [][]destinationParityCluster
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
				Server:    "https://prod.example",
				Namespace: "workloads",
			}},
			appDestination: argoappv1.ApplicationDestination{
				Name:      "prod",
				Namespace: "workloads",
			},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
				{{Name: "prod", Server: "https://other.example", Project: "other"}},
			},
			expectDeferred: true,
		},
		// A name-only in-cluster destination with no cluster Secret of that
		// name is the in-cluster address (util/db GetClusterServersByName).
		{
			name: "in-cluster name without Secret is permitted by server policy",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "workloads",
			}},
			appDestination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"},
		},
		{
			name: "in-cluster name without Secret is denied by server policy",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://prod.example",
				Namespace: "*",
			}},
			appDestination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"},
		},
		{
			name: "in-cluster name resolves through a renamed Secret at the in-cluster address",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Name: "!home", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"},
			clusters:       []destinationParityCluster{{Name: "home", Server: "https://kubernetes.default.svc"}},
		},
		{
			name: "in-cluster name uses the Secret with that name",
			destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "*",
			}},
			appDestination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"},
			clusters:       []destinationParityCluster{{Name: "in-cluster", Server: "https://other.example"}},
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
				{Server: "*", Namespace: "*"},
				{Server: "*", Namespace: "!kube-system"},
			},
			appDestination: argoappv1.ApplicationDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: "kube-system",
			},
		},
		{
			name: "server deny pattern",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Server: "!https://kubernetes.default.svc", Namespace: "*"},
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
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example", Project: projectName}},
				{{Name: "prod", Server: "https://prod.example", Project: "other"}},
			},
			projectScoped:  true,
			expectDeferred: true,
		},
		// Upstream d9e7e38c6 (#28854) regression cases: a name deny entry
		// denies only when its namespace matches.
		{
			name: "name deny entry in another namespace does not deny",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "*", Namespace: "*"},
				{Name: "!bad", Namespace: "other"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "bad", Namespace: "test"},
			clusters:       []destinationParityCluster{{Name: "bad", Server: "https://bad.example"}},
		},
		{
			name: "name deny entry in the destination namespace denies",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "*", Namespace: "*"},
				{Name: "!bad", Namespace: "test"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "bad", Namespace: "test"},
			clusters:       []destinationParityCluster{{Name: "bad", Server: "https://bad.example"}},
		},
		{
			name: "name deny entry in another namespace does not deny an unresolved name",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "*", Namespace: "*"},
				{Name: "!bad", Namespace: "other"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "bad", Namespace: "test"},
			live: [][]destinationParityCluster{
				{{Name: "bad", Server: "https://bad.example"}},
				{{Name: "bad", Server: "https://kubernetes.default.svc"}},
			},
		},
		{
			name: "name deny entry in the destination namespace denies an unresolved name",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "*", Namespace: "*"},
				{Name: "!bad", Namespace: "test"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "bad", Namespace: "test"},
			live: [][]destinationParityCluster{
				{{Name: "bad", Server: "https://bad.example"}},
				{{Name: "bad", Server: "https://kubernetes.default.svc"}},
			},
		},
		{
			name: "namespace-scoped name deny with unresolved name-only destination defers",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "!prod", Server: "*", Namespace: "kube-system"},
				{Server: "https://prod.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "prod", Namespace: "apps"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
				{{Name: "prod", Server: "https://other.example"}},
			},
			expectDeferred: true,
		},
		{
			name: "name deny entry with wildcard server matches by server",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "!prod", Server: "*", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "prod", Namespace: "workloads"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
			},
		},
		{
			name: "name deny entry with specific server defers an unresolved name",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Name: "!prod", Server: "https://a.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "prod", Namespace: "workloads"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://a.example"}},
				{{Name: "prod", Server: "https://b.example"}},
			},
			expectDeferred: true,
		},
		// A server-only destination at the in-cluster address with no
		// cluster Secret is Argo CD's built-in in-cluster cluster.
		{
			name: "in-cluster server without Secret matches a name wildcard",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "*", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "workloads"},
		},
		{
			name: "in-cluster server without Secret is not another name's deny",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Name: "!legacy", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "workloads"},
		},
		{
			name: "in-cluster server without Secret is denied by name",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Name: "!in-cluster", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "workloads"},
		},
		{
			name: "in-cluster server with a renamed Secret uses the Secret name",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Name: "!in-cluster", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "workloads"},
			clusters:       []destinationParityCluster{{Name: "home", Server: "https://kubernetes.default.svc"}},
		},
		// Any other server-only destination with no cluster Secret has a
		// cluster name drydock cannot know.
		{
			name: "server-only destination without Secret defers a name deny",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Name: "!legacy", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "workloads"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
				{{Name: "legacy", Server: "https://prod.example"}},
			},
			expectDeferred: true,
		},
		{
			name: "server-only destination without Secret defers a name allow",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "prod", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "workloads"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
				{{Name: "staging", Server: "https://prod.example"}},
			},
			expectDeferred: true,
		},
		{
			name: "server-only destination without Secret matches a name wildcard",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "*", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "workloads"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
				{{Name: "legacy", Server: "https://prod.example"}},
			},
		},
		// The live cluster behind an unresolved destination may be scoped to
		// the Application's project, which adds an implicit destination for
		// its server: only a deny entry decides when no entry matches.
		{
			name: "server-only destination without Secret defers server policy to project scoping",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "https://other.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "workloads"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
			},
			expectDeferred: true,
		},
		{
			name: "name-only destination without Secret defers name policy to project scoping",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "dev", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "prod", Namespace: "apps"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
			},
			expectDeferred: true,
		},
		{
			name: "server-only destination without Secret is denied by a server deny entry",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
				{Server: "!https://prod.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "workloads"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
				{{Name: "prod", Server: "https://prod.example", Project: projectName}},
			},
		},
		{
			name: "name-only destination without Secret is denied by a name deny entry",
			destinations: []argoappv1.ApplicationDestination{
				{Name: "*", Namespace: "*"},
				{Name: "!prod", Namespace: "apps"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "prod", Namespace: "apps"},
			live: [][]destinationParityCluster{
				{{Name: "prod", Server: "https://prod.example"}},
				{{Name: "prod", Server: "https://prod.example", Project: projectName}},
			},
		},
		// Project-scoped cluster Secrets add implicit destinations
		// (GetAppProjectByName).
		{
			name: "project-scoped cluster adds an implicit destination",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "https://other.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://team.example", Namespace: "apps"},
			clusters:       []destinationParityCluster{{Name: "team", Server: "https://team.example", Project: projectName}},
		},
		{
			name: "project-scoped cluster namespaces limit its implicit destinations",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "https://other.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://team.example", Namespace: "apps"},
			clusters: []destinationParityCluster{{
				Name: "team", Server: "https://team.example", Project: projectName, Namespaces: []string{"team-a", "team-b"},
			}},
		},
		{
			name: "project-scoped cluster namespace is an implicit destination",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "https://other.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Name: "team", Namespace: "team-b"},
			clusters: []destinationParityCluster{{
				Name: "team", Server: "https://team.example", Project: projectName, Namespaces: []string{"team-a", "team-b"},
			}},
		},
		{
			name: "project deny entry still wins over an implicit destination",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "https://other.example", Namespace: "*"},
				{Server: "!https://team.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://team.example", Namespace: "apps"},
			clusters:       []destinationParityCluster{{Name: "team", Server: "https://team.example", Project: projectName}},
		},
		{
			name: "cluster scoped to another project adds no implicit destination",
			destinations: []argoappv1.ApplicationDestination{
				{Server: "https://other.example", Namespace: "*"},
			},
			appDestination: argoappv1.ApplicationDestination{Server: "https://team.example", Namespace: "apps"},
			clusters:       []destinationParityCluster{{Name: "team", Server: "https://team.example", Project: "other"}},
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

			argoVerdicts := argoDestinationVerdicts(t, project, tt.appDestination, tt.clusters, tt.live)

			apps := []argoappv1.Application{application("demo", projectName, argoappv1.ApplicationSource{
				RepoURL: "https://github.com/example/repo",
				Path:    "apps/demo",
			}, tt.appDestination)}
			diags := ValidateApplications(apps, []argoappv1.AppProject{project}, settings)

			assertDestinationParity(t, diags, argoVerdicts, tt.expectDeferred)
		})
	}
}

// argoDestinationVerdicts returns the set of Argo CD verdicts for dest over
// each live cluster Secret set (added to clusters), or over clusters alone.
// A live set whose clusters name no project is also evaluated with them
// scoped to project, since drydock sees neither those clusters nor their
// project.
func argoDestinationVerdicts(t *testing.T, project argoappv1.AppProject, dest argoappv1.ApplicationDestination, clusters []destinationParityCluster, live [][]destinationParityCluster) map[bool]bool {
	t.Helper()
	if len(live) == 0 {
		live = [][]destinationParityCluster{nil}
	}
	verdicts := make(map[bool]bool, 2)
	for _, extra := range live {
		verdicts[argoDestinationPermitted(t, project, dest, slices.Concat(clusters, extra))] = true
		scoped := make([]destinationParityCluster, len(extra))
		copy(scoped, extra)
		changed := false
		for i := range scoped {
			if scoped[i].Project == "" {
				scoped[i].Project = project.Name
				changed = true
			}
		}
		if changed {
			verdicts[argoDestinationPermitted(t, project, dest, slices.Concat(clusters, scoped))] = true
		}
	}
	return verdicts
}

// assertDestinationParity checks drydock's destination diagnostics against
// the Argo CD verdicts: a definite verdict is reported as Argo CD reports
// it, and a deferral needs Argo CD to both permit and deny.
func assertDestinationParity(t *testing.T, diags []diagnostic.Diagnostic, argoVerdicts map[bool]bool, expectDeferred bool) {
	t.Helper()
	hasDestinationDeny := hasProjectDestinationDiagnostic(diags, "destination is not permitted")
	hasDeferred := hasProjectDestinationDiagnostic(diags, "cannot be resolved against AppProject server policy offline") ||
		hasProjectDestinationDiagnostic(diags, "cannot be resolved against AppProject name policy offline") ||
		hasProjectDestinationDiagnostic(diags, "project-scoped cluster Secrets enforcement is deferred offline")

	if expectDeferred {
		if len(argoVerdicts) != 2 {
			t.Fatalf("Argo CD verdicts over live cluster sets = %v, want both a permit and a deny to justify deferral", argoVerdicts)
		}
		if !hasDeferred {
			t.Fatalf("Diagnostics = %#v, want deferred destination diagnostic", diags)
		}
		if hasDestinationDeny {
			t.Fatalf("Diagnostics = %#v, want deferred validation without hard destination denial", diags)
		}
		return
	}
	if len(argoVerdicts) != 1 {
		t.Fatalf("Argo CD verdicts over live cluster sets = %v, want one verdict for a definite drydock result", argoVerdicts)
	}
	expectedPermitted := argoVerdicts[true]
	if expectedPermitted && hasDestinationDeny {
		t.Fatalf("Diagnostics = %#v, want no destination denial because Argo CD permits destination", diags)
	}
	if !expectedPermitted && !hasDestinationDeny {
		t.Fatalf("Diagnostics = %#v, want destination denial because Argo CD denies destination", diags)
	}
	if hasDeferred {
		t.Fatalf("Diagnostics = %#v, want no deferred destination diagnostic when metadata is sufficient", diags)
	}
}

type destinationParityCluster struct {
	Name       string
	Server     string
	Project    string
	Namespaces []string
}

// argoDestinationPermitted is Argo CD's destination decision for an
// Application whose controller sees the cluster Secrets in live: the project
// as util/argo GetAppProjectByName builds it, and the destination cluster as
// util/argo GetDestinationCluster resolves it through util/db, built-in
// in-cluster cluster included. A destination Argo CD cannot resolve fails the
// test: the Application would be rejected before project policy, so the row
// does not test project policy.
func argoDestinationPermitted(t *testing.T, project argoappv1.AppProject, dest argoappv1.ApplicationDestination, live []destinationParityCluster) bool {
	t.Helper()

	clusters := make([]*argoappv1.Cluster, 0, len(live))
	for _, cluster := range live {
		clusters = append(clusters, &argoappv1.Cluster{
			Name:       cluster.Name,
			Server:     cluster.Server,
			Project:    cluster.Project,
			Namespaces: append([]string(nil), cluster.Namespaces...),
		})
	}
	projectClusters := func(projectName string) []*argoappv1.Cluster {
		var out []*argoappv1.Cluster
		for _, cluster := range clusters {
			if cluster.Project == projectName {
				out = append(out, cluster)
			}
		}
		return out
	}

	proj := project.DeepCopy()
	for _, cluster := range projectClusters(proj.Name) {
		if len(cluster.Namespaces) == 0 {
			proj.Spec.Destinations = append(proj.Spec.Destinations, argoappv1.ApplicationDestination{Server: cluster.Server, Namespace: "*"})
			continue
		}
		for _, namespace := range cluster.Namespaces {
			proj.Spec.Destinations = append(proj.Spec.Destinations, argoappv1.ApplicationDestination{Server: cluster.Server, Namespace: namespace})
		}
	}

	destCluster, reason := argoDestinationCluster(dest, clusters)
	if destCluster == nil {
		t.Fatalf("Argo CD cannot resolve destination %#v with cluster Secrets %#v: %s", dest, live, reason)
	}
	permitted, err := proj.IsDestinationPermitted(destCluster.Sanitized(), dest.Namespace, func(projectName string) ([]*argoappv1.Cluster, error) {
		return projectClusters(projectName), nil
	})
	if err != nil {
		t.Fatalf("Argo CD IsDestinationPermitted returned error: %v", err)
	}
	return permitted
}

// argoDestinationCluster mirrors util/argo GetDestinationServer and
// util/db GetClusterServersByName and GetCluster over clusters, with the
// default argocd-cm cluster.inClusterEnabled. With in-cluster disabled Argo
// CD rejects every in-cluster destination before project policy, so no
// parity row can exercise it.
func argoDestinationCluster(dest argoappv1.ApplicationDestination, clusters []*argoappv1.Cluster) (*argoappv1.Cluster, string) {
	var server string
	switch {
	case dest.Name != "" && dest.Server != "":
		return nil, "application destination can't have both name and server defined"
	case dest.Server != "":
		server = strings.TrimRight(dest.Server, "/")
	case dest.Name != "":
		var servers []string
		for _, cluster := range clusters {
			if cluster.Name == dest.Name {
				servers = append(servers, cluster.Server)
			}
		}
		if len(servers) == 0 && dest.Name == argoappv1.KubernetesInClusterName {
			servers = []string{argoappv1.KubernetesInternalAPIServerAddr}
		}
		if len(servers) != 1 {
			return nil, "destination name does not resolve to exactly one cluster"
		}
		server = servers[0]
	default:
		return nil, "destination has neither name nor server"
	}

	for _, cluster := range clusters {
		if cluster.Server == server {
			return cluster, ""
		}
	}
	if server == argoappv1.KubernetesInternalAPIServerAddr {
		return &argoappv1.Cluster{Name: argoappv1.KubernetesInClusterName, Server: server}, ""
	}
	return nil, "no cluster Secret for server"
}

func destinationParitySettings(clusters []destinationParityCluster) config.ArgoSettings {
	settings := config.DefaultSettings()
	for _, cluster := range clusters {
		server := strings.TrimRight(strings.TrimSpace(cluster.Server), "/")
		settings.Clusters[server] = config.ClusterSettings{
			Name:       cluster.Name,
			Server:     server,
			Project:    cluster.Project,
			Namespaces: append([]string(nil), cluster.Namespaces...),
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
