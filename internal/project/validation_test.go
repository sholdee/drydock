package project

import (
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
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

func TestValidateApplicationsAllowsNamedDestinationWhenProjectAllowsWildcardServer(t *testing.T) {
	apps := []argoappv1.Application{application("cilium", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/cilium",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "kube-system",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Server:    "*",
				Namespace: "*",
			}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertNoDiagnostic(t, diags, "destination is not permitted")
	assertNoDiagnostic(t, diags, "destination name")
}

func TestValidateApplicationsReportsNamedDestinationNamespaceMismatch(t *testing.T) {
	apps := []argoappv1.Application{application("cilium", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/cilium",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "kube-system",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Server:    "*",
				Namespace: "workloads",
			}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "destination is not permitted")
}

func TestValidateApplicationsReportsNamedDestinationServerResolutionDeferred(t *testing.T) {
	apps := []argoappv1.Application{application("cilium", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/cilium",
	}, argoappv1.ApplicationDestination{
		Name:      "prod",
		Namespace: "kube-system",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://prod.example",
				Namespace: "*",
			}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "destination name \"prod\" cannot be resolved against AppProject server policy offline")
	assertNoDiagnostic(t, diags, "destination is not permitted")
}

// The cluster behind an unresolved destination is a live cluster Secret
// drydock cannot see. When it is scoped to the Application's project, Argo
// CD adds an implicit destination for its server (GetAppProjectByName), so
// a project entry list that matches nothing defers rather than denies; only
// a deny entry denies.
func TestValidateApplicationsDefersUnmatchedUnresolvedDestinationToProjectScoping(t *testing.T) {
	project := destinationProject(argoappv1.ApplicationDestination{Server: "https://other.example", Namespace: "*"})
	serverOnly := application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "apps"})

	deferred := ValidateApplications([]argoappv1.Application{serverOnly}, []argoappv1.AppProject{project}, config.DefaultSettings())
	assertDiagnostic(t, deferred, "destination server \"https://prod.example\" has no cluster Secret; its cluster name cannot be resolved against AppProject name policy offline")
	assertNoDiagnostic(t, deferred, "destination is not permitted")

	nameOnly := ValidateApplications([]argoappv1.Application{nameOnlyApplication("prod", "apps")}, []argoappv1.AppProject{
		destinationProject(argoappv1.ApplicationDestination{Name: "dev", Namespace: "*"}),
	}, config.DefaultSettings())
	assertDiagnostic(t, nameOnly, "destination name \"prod\" cannot be resolved against AppProject server policy offline")
	assertNoDiagnostic(t, nameOnly, "destination is not permitted")

	denyEntry := destinationProject(
		argoappv1.ApplicationDestination{Server: "https://other.example", Namespace: "*"},
		argoappv1.ApplicationDestination{Server: "!https://prod.example", Namespace: "*"},
	)
	denied := ValidateApplications([]argoappv1.Application{serverOnly}, []argoappv1.AppProject{denyEntry}, config.DefaultSettings())
	assertDiagnostic(t, denied, "destination is not permitted")
	assertNoDiagnostic(t, denied, "cannot be resolved against AppProject")
}

// A name-only in-cluster destination with no cluster Secret of that name is
// the in-cluster address (util/db GetClusterServersByName), resolved like a
// server-only one (GetCluster).
func TestValidateApplicationsResolvesInClusterNameWithoutSecret(t *testing.T) {
	app := nameOnlyApplication("in-cluster", "workloads")

	permitted := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{
		destinationProject(argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "*"}),
	}, config.DefaultSettings())
	if len(permitted) != 0 {
		t.Fatalf("Diagnostics = %#v, want none for in-cluster server policy", permitted)
	}

	serverPolicy := []argoappv1.AppProject{destinationProject(argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "*"})}
	denied := ValidateApplications([]argoappv1.Application{app}, serverPolicy, config.DefaultSettings())
	assertDiagnostic(t, denied, "destination is not permitted")
	assertNoDiagnostic(t, denied, "cannot be resolved against AppProject")

	// A cluster Secret at the in-cluster address under another name is the
	// cluster the name resolves to.
	renamed := settingsWithCluster("home", "https://kubernetes.default.svc", "")
	renamedDenied := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{destinationProject(
		argoappv1.ApplicationDestination{Server: "*", Namespace: "*"},
		argoappv1.ApplicationDestination{Name: "!home", Namespace: "*"},
	)}, renamed)
	assertDiagnostic(t, renamedDenied, "destination is not permitted")
}

// With argocd-cm cluster.inClusterEnabled "false", util/db GetCluster
// rejects https://kubernetes.default.svc before it looks at any cluster
// Secret and GetClusterServersByName drops it from name lookups, so Argo CD
// rejects the Application whatever its AppProject permits.
func TestValidateApplicationsRejectsDisabledInClusterDestination(t *testing.T) {
	disabled := config.DefaultSettings()
	disabled.InClusterEnabled = config.Value[bool]{Value: false, Provenance: config.Provenance{Path: "argocd-cm.yaml"}}
	withSecret := settingsWithCluster("in-cluster", "https://kubernetes.default.svc", "")
	withSecret.InClusterEnabled = disabled.InClusterEnabled
	permissive := []argoappv1.AppProject{destinationProject(argoappv1.ApplicationDestination{Server: "*", Namespace: "*"})}
	const want = "destination is not permitted: cluster \"https://kubernetes.default.svc\" is disabled by argocd-cm cluster.inClusterEnabled"

	for _, tt := range []struct {
		name     string
		dest     argoappv1.ApplicationDestination
		settings config.ArgoSettings
	}{
		{name: "server without Secret", dest: argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc/", Namespace: "workloads"}, settings: disabled},
		{name: "server with Secret", dest: argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "workloads"}, settings: withSecret},
		{name: "name resolved through Secret", dest: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}, settings: withSecret},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := application("demo", "platform", argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "apps/demo"}, tt.dest)
			diags := ValidateApplications([]argoappv1.Application{app}, permissive, tt.settings)
			if len(diags) != 1 || !strings.Contains(diags[0].Message, want) {
				t.Fatalf("Diagnostics = %#v, want only %q", diags, want)
			}
			if got := diagnostic.StableCode(diags[0]); got != diagnostic.CodeProjectDestinationDenied {
				t.Fatalf("StableCode() = %q, want %q", got, diagnostic.CodeProjectDestinationDenied)
			}
			if class := diagnostic.ClassifyProjectDiagnostic(diags[0]); class != diagnostic.ProjectDiagnosticClassActionable {
				t.Fatalf("ClassifyProjectDiagnostic() = %q, want actionable", class)
			}
		})
	}

	// A name-only in-cluster destination with no Secret of that name has no
	// server while in-cluster is disabled, unless a live Secret drydock
	// cannot see carries that name, so it stays unresolved.
	unresolved := ValidateApplications([]argoappv1.Application{nameOnlyApplication("in-cluster", "workloads")}, permissive, disabled)
	assertNoDiagnostic(t, unresolved, "is disabled by argocd-cm")
}

func TestValidateApplicationsResolvesDestinationNameToDiscoveredClusterSecretServer(t *testing.T) {
	apps := []argoappv1.Application{application("cilium", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/cilium",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "kube-system",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Server:    "https://kubernetes.default.svc",
				Namespace: "*",
			}},
		},
	}}
	settings := settingsWithCluster("in-cluster", "https://kubernetes.default.svc/", "")

	diags := ValidateApplications(apps, projects, settings)
	assertNoDiagnostic(t, diags, "destination name")
	assertNoDiagnostic(t, diags, "destination is not permitted")
}

// A name deny entry with a wildcard server never denies in Argo CD: every
// resolved cluster has a server, so the entry matches by server first
// (isDestinationMatched). An unresolved name-only destination is therefore
// permitted rather than denied.
func TestValidateApplicationsNameDenyWithWildcardServerMatchesByServer(t *testing.T) {
	diags := ValidateApplications([]argoappv1.Application{nameOnlyApplication("prod", "workloads")}, []argoappv1.AppProject{
		destinationProject(argoappv1.ApplicationDestination{Name: "!prod", Server: "*", Namespace: "*"}),
	}, config.DefaultSettings())
	assertNoDiagnostic(t, diags, "destination is not permitted")
	assertNoDiagnostic(t, diags, "destination name")
}

// v3.6.1 (d9e7e38c6): a name deny entry denies only when its namespace
// matches the destination namespace, also for an unresolved name.
func TestValidateApplicationsNameDenyAppliesOnlyInMatchingNamespace(t *testing.T) {
	projects := []argoappv1.AppProject{destinationProject(
		argoappv1.ApplicationDestination{Server: "*", Namespace: "*"},
		argoappv1.ApplicationDestination{Name: "!prod", Namespace: "kube-system"},
	)}

	denied := ValidateApplications([]argoappv1.Application{nameOnlyApplication("prod", "kube-system")}, projects, config.DefaultSettings())
	assertDiagnostic(t, denied, "destination is not permitted")

	permitted := ValidateApplications([]argoappv1.Application{nameOnlyApplication("prod", "apps")}, projects, config.DefaultSettings())
	assertNoDiagnostic(t, permitted, "destination is not permitted")
	assertNoDiagnostic(t, permitted, "destination name")
}

func TestValidateApplicationsNamespaceScopedNameDenyDefersUnresolvedServerPolicy(t *testing.T) {
	diags := ValidateApplications([]argoappv1.Application{nameOnlyApplication("prod", "apps")}, []argoappv1.AppProject{destinationProject(
		argoappv1.ApplicationDestination{Name: "!prod", Server: "*", Namespace: "kube-system"},
		argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "*"},
	)}, config.DefaultSettings())
	assertDiagnostic(t, diags, "destination name \"prod\" cannot be resolved against AppProject server policy offline")
	assertNoDiagnostic(t, diags, "destination is not permitted")
}

// A server-only destination at https://kubernetes.default.svc with no
// cluster Secret is Argo CD's built-in cluster named in-cluster.
func TestValidateApplicationsResolvesInClusterServerWithoutSecret(t *testing.T) {
	app := application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc/", Namespace: "workloads"})

	permitted := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{
		destinationProject(argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "*"}),
	}, config.DefaultSettings())
	if len(permitted) != 0 {
		t.Fatalf("Diagnostics = %#v, want none for in-cluster name policy", permitted)
	}

	denyInCluster := []argoappv1.AppProject{destinationProject(
		argoappv1.ApplicationDestination{Server: "*", Namespace: "*"},
		argoappv1.ApplicationDestination{Name: "!in-cluster", Namespace: "*"},
	)}
	denied := ValidateApplications([]argoappv1.Application{app}, denyInCluster, config.DefaultSettings())
	assertDiagnostic(t, denied, "destination is not permitted")

	// With cluster.inClusterEnabled "false" Argo CD rejects the in-cluster
	// address outright (TestValidateApplicationsRejectsDisabledInClusterDestination).
	disabled := config.DefaultSettings()
	disabled.InClusterEnabled = config.Value[bool]{Value: false, Provenance: config.Provenance{Path: "argocd-cm.yaml"}}
	rejected := ValidateApplications([]argoappv1.Application{app}, denyInCluster, disabled)
	assertDiagnostic(t, rejected, "is disabled by argocd-cm cluster.inClusterEnabled")
	assertNoDiagnostic(t, rejected, "cannot be resolved against AppProject")
}

func TestValidateApplicationsDefersNamePolicyForServerOnlyDestinationWithoutSecret(t *testing.T) {
	app := application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "workloads"})

	diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{destinationProject(
		argoappv1.ApplicationDestination{Server: "*", Namespace: "*"},
		argoappv1.ApplicationDestination{Name: "!legacy", Namespace: "*"},
	)}, config.DefaultSettings())
	assertNoDiagnostic(t, diags, "destination is not permitted")
	for _, diag := range diags {
		if strings.Contains(diag.Message, "cannot be resolved against AppProject name policy offline") {
			if class := diagnostic.ClassifyProjectDiagnostic(diag); class != diagnostic.ProjectDiagnosticClassDeferred {
				t.Fatalf("ClassifyProjectDiagnostic(%q) = %q, want deferred", diag.Message, class)
			}
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want deferred name policy diagnostic", diags)
}

func TestValidateApplicationsAddsProjectScopedClusterDestinations(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Clusters["https://team.example"] = config.ClusterSettings{Name: "team", Server: "https://team.example", Project: "platform"}
	settings.Clusters["https://scoped.example"] = config.ClusterSettings{Name: "scoped", Server: "https://scoped.example", Project: "platform", Namespaces: []string{"team-a", "team-b"}}
	settings.Clusters["https://other.example"] = config.ClusterSettings{Name: "other", Server: "https://other.example", Project: "other"}
	project := destinationProject(argoappv1.ApplicationDestination{Server: "https://declared.example", Namespace: "*"})

	effective := effectiveProject(project, settings)
	want := []argoappv1.ApplicationDestination{
		{Server: "https://declared.example", Namespace: "*"},
		{Server: "https://scoped.example", Namespace: "team-a"},
		{Server: "https://scoped.example", Namespace: "team-b"},
		{Server: "https://team.example", Namespace: "*"},
	}
	if !reflect.DeepEqual(effective.Spec.Destinations, want) {
		t.Fatalf("effective destinations = %#v, want %#v", effective.Spec.Destinations, want)
	}
	if len(project.Spec.Destinations) != 1 {
		t.Fatalf("project destinations = %#v, want the declared list unchanged", project.Spec.Destinations)
	}

	for _, tt := range []struct {
		dest      argoappv1.ApplicationDestination
		permitted bool
	}{
		{dest: argoappv1.ApplicationDestination{Server: "https://team.example", Namespace: "apps"}, permitted: true},
		{dest: argoappv1.ApplicationDestination{Name: "team", Namespace: "apps"}, permitted: true},
		{dest: argoappv1.ApplicationDestination{Server: "https://scoped.example", Namespace: "team-b"}, permitted: true},
		{dest: argoappv1.ApplicationDestination{Server: "https://scoped.example", Namespace: "apps"}, permitted: false},
		{dest: argoappv1.ApplicationDestination{Server: "https://other.example", Namespace: "apps"}, permitted: false},
	} {
		app := application("demo", "platform", argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "apps/demo"}, tt.dest)
		diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{project}, settings)
		if denied := hasProjectDestinationDiagnostic(diags, "destination is not permitted"); denied == tt.permitted {
			t.Fatalf("destination %#v diagnostics = %#v, want permitted=%t", tt.dest, diags, tt.permitted)
		}
	}
}

func TestValidateApplicationsDenyEntryWinsOverProjectScopedClusterDestination(t *testing.T) {
	settings := settingsWithCluster("team", "https://team.example", "platform")
	app := application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Server: "https://team.example", Namespace: "kube-system"})

	diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{destinationProject(
		argoappv1.ApplicationDestination{Server: "*", Namespace: "!kube-system"},
	)}, settings)
	assertDiagnostic(t, diags, "destination is not permitted")
}

func nameOnlyApplication(cluster, namespace string) argoappv1.Application {
	return application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: cluster, Namespace: namespace})
}

func destinationProject(destinations ...argoappv1.ApplicationDestination) argoappv1.AppProject {
	return argoappv1.AppProject{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: destinations,
		},
	}
}

func TestValidateApplicationsDoesNotUseWildcardServerFallbackForDeniedDestinationNamespace(t *testing.T) {
	apps := []argoappv1.Application{application("system-app", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/system-app",
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "kube-system",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{
				{
					Server:    "*",
					Namespace: "*",
				},
				{
					Server:    "*",
					Namespace: "!kube-system",
				},
			},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "destination is not permitted")
}

func TestValidateApplicationsDoesNotUseWildcardServerFallbackForDeniedDestinationServer(t *testing.T) {
	apps := []argoappv1.Application{application("system-app", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/system-app",
	}, argoappv1.ApplicationDestination{
		Name:      "prod",
		Namespace: "kube-system",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{
				{
					Server:    "*",
					Namespace: "*",
				},
				{
					Server:    "!https://prod.example",
					Namespace: "*",
				},
			},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "destination name \"prod\" cannot be resolved against AppProject server policy offline")
	assertNoDiagnostic(t, diags, "destination is not permitted")
}

func TestValidateApplicationsDoesNotUseWildcardServerFallbackForServerScopedDestinationNamespaceDeny(t *testing.T) {
	apps := []argoappv1.Application{application("system-app", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/system-app",
	}, argoappv1.ApplicationDestination{
		Name:      "prod",
		Namespace: "kube-system",
	})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{
				{
					Server:    "*",
					Namespace: "*",
				},
				{
					Server:    "https://prod.example",
					Namespace: "!kube-system",
				},
			},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "destination name \"prod\" cannot be resolved against AppProject server policy offline")
	assertNoDiagnostic(t, diags, "destination is not permitted")
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

func TestValidateApplicationsAllowsProjectScopedClusterSecretDestination(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:                     []string{"*"},
			Destinations:                    []argoappv1.ApplicationDestination{{Server: "*", Namespace: "*"}},
			PermitOnlyProjectScopedClusters: true,
		},
	}}
	settings := settingsWithCluster("in-cluster", "https://kubernetes.default.svc", "platform")

	diags := ValidateApplications(apps, projects, settings)
	assertNoDiagnostic(t, diags, "project-scoped cluster Secrets")
	assertNoDiagnostic(t, diags, "destination is not permitted")
}

func TestValidateApplicationsDeniesDestinationOutsideProjectScopedClusterSecretList(t *testing.T) {
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/demo",
	}, argoappv1.ApplicationDestination{Name: "prod", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:                     []string{"*"},
			Destinations:                    []argoappv1.ApplicationDestination{{Server: "*", Namespace: "*"}},
			PermitOnlyProjectScopedClusters: true,
		},
	}}
	settings := settingsWithCluster("prod", "https://prod.example", "other")

	diags := ValidateApplications(apps, projects, settings)
	assertDiagnostic(t, diags, "destination is not permitted")
	assertNoDiagnostic(t, diags, "project-scoped cluster Secrets")
	assertNoDiagnostic(t, diags, "cannot be resolved")
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

func TestValidateApplicationsDoesNotRequireRepositoryMetadataForPublicSources(t *testing.T) {
	apps := []argoappv1.Application{
		application("http-chart", "platform", argoappv1.ApplicationSource{
			RepoURL: "https://charts.example.test",
			Chart:   "demo",
		}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}),
		application("oci-chart", "platform", argoappv1.ApplicationSource{
			RepoURL: "ghcr.io/example/charts",
			Chart:   "demo",
		}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}),
		application("git-path", "platform", argoappv1.ApplicationSource{
			RepoURL: "https://github.com/example/repo",
			Path:    "apps/demo",
		}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}),
	}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertNoDiagnostic(t, diags, "missing repository metadata")
}

func TestValidateApplicationsMatchesCanonicalOCIRepositoryMetadata(t *testing.T) {
	settings := config.DefaultSettings()
	settings.HelmRepositories["ghcr.io/example/charts"] = config.RepositorySettings{
		URL:       "ghcr.io/example/charts",
		Type:      "helm",
		EnableOCI: true,
		Project:   "platform",
	}
	apps := []argoappv1.Application{application("demo", "platform", argoappv1.ApplicationSource{
		RepoURL: "oci://ghcr.io/example/charts/",
		Chart:   "demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, settings)
	assertNoDiagnostic(t, diags, "missing repository metadata")
}

func TestValidateApplicationsAllowsProjectScopedRepositorySecretsAsSources(t *testing.T) {
	settings := config.DefaultSettings()
	settings.HelmRepositories["https://charts.example.test"] = config.RepositorySettings{
		URL:     "https://charts.example.test",
		Type:    "helm",
		Project: "platform",
	}
	settings.HelmRepositories["ghcr.io/example/charts"] = config.RepositorySettings{
		URL:       "ghcr.io/example/charts",
		Type:      "helm",
		EnableOCI: true,
		Project:   "platform",
	}
	apps := []argoappv1.Application{
		application("http-chart", "platform", argoappv1.ApplicationSource{
			RepoURL: "https://charts.example.test",
			Chart:   "demo",
		}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}),
		application("oci-chart", "platform", argoappv1.ApplicationSource{
			RepoURL: "ghcr.io/example/charts",
			Chart:   "demo",
		}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"}),
	}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"https://github.com/example/gitops"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, settings)
	assertNoDiagnostic(t, diags, "source repository")
	assertNoDiagnostic(t, diags, "missing repository metadata")
}

func TestValidateApplicationsDisplaysBareOCIRepositoryURLsInDiagnostics(t *testing.T) {
	apps := []argoappv1.Application{application("oci-chart", "platform", argoappv1.ApplicationSource{
		RepoURL: "ghcr.io/example/charts",
		Chart:   "demo",
	}, argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "workloads"})}
	projects := []argoappv1.AppProject{{
		ObjectMeta: objectMeta("platform"),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"https://github.com/example/gitops"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}}

	diags := ValidateApplications(apps, projects, config.DefaultSettings())
	assertDiagnostic(t, diags, "ghcr.io/example/charts")
	assertNoDiagnostic(t, diags, "[invalid-url]")
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
		Name: name, Namespace: namespace,
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

func settingsWithCluster(name, server, project string) config.ArgoSettings {
	settings := config.DefaultSettings()
	normalizedServer := strings.TrimRight(strings.TrimSpace(server), "/")
	settings.Clusters[normalizedServer] = config.ClusterSettings{
		Name:    name,
		Server:  normalizedServer,
		Project: project,
	}
	return settings
}

func TestValidateApplicationsImplicitDefaultProjectPermitsAnySourceNamespace(t *testing.T) {
	// With no AppProject declared, the implicit default stands in for a
	// project drydock cannot see, so it must not deny source namespaces it
	// knows nothing about (apps-in-any-namespace layouts, issue #389).
	apps := []argoappv1.Application{applicationInNamespace("myapp", "argocd-tenant-a", "", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/myapp",
	}, argoappv1.ApplicationDestination{
		Server:    "https://kubernetes.default.svc",
		Namespace: "workloads",
	})}

	diags := ValidateApplications(apps, nil, config.DefaultSettings())
	if len(diags) != 0 {
		t.Fatalf("Diagnostics = %#v, want none for the implicit default project", diags)
	}
}

func TestValidateApplicationsDefersUnresolvedProjectWithoutLocalProjects(t *testing.T) {
	apps := []argoappv1.Application{applicationInNamespace("myapp", "argocd-tenant-a", "myproject", argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/myapp",
	}, argoappv1.ApplicationDestination{
		Server:    "https://kubernetes.default.svc",
		Namespace: "workloads",
	})}

	diags := ValidateApplications(apps, nil, config.DefaultSettings())
	if len(diags) != 1 {
		t.Fatalf("Diagnostics = %#v, want exactly one unresolved-project diagnostic", diags)
	}
	diag := diags[0]
	if diag.Code != diagnostic.CodeProjectUnresolved {
		t.Fatalf("Code = %q, want %q", diag.Code, diagnostic.CodeProjectUnresolved)
	}
	want := `Application argocd-tenant-a/myapp references AppProject "myproject", which is not declared in the repository; validating against the implicit default project`
	if diag.Message != want {
		t.Fatalf("Message = %q, want %q", diag.Message, want)
	}
	if class := diagnostic.ClassifyProjectDiagnostic(diag); class != diagnostic.ProjectDiagnosticClassDeferred {
		t.Fatalf("ClassifyProjectDiagnostic() = %q, want deferred", class)
	}
}

// Argo CD v3.6.1 matches AppProject patterns with util/glob on gobwas/glob
// v1.0.0 (821e090d6, #29482): an empty brace alternative matches, and a
// prefix and suffix no longer overlap. Destination patterns hold for a
// resolved cluster (IsDestinationPermitted) and for an unresolved name
// (offlineDestinationPermitted) alike.
func TestValidateApplicationsProjectGlobSemanticsMatchGobwasV1(t *testing.T) {
	settings := settingsWithCluster("prod", "https://prod.example", "")
	for _, tt := range []struct {
		name      string
		entry     argoappv1.ApplicationDestination
		dest      argoappv1.ApplicationDestination
		settings  config.ArgoSettings
		permitted bool
	}{
		{name: "namespace empty brace alternative", entry: argoappv1.ApplicationDestination{Server: "*", Namespace: "{*-,}deploy"}, dest: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "deploy"}, settings: settings, permitted: true},
		{name: "namespace brace alternative", entry: argoappv1.ApplicationDestination{Server: "*", Namespace: "{*-,}deploy"}, dest: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "team-deploy"}, settings: settings, permitted: true},
		{name: "namespace overlapping prefix and suffix", entry: argoappv1.ApplicationDestination{Server: "*", Namespace: "a*ant"}, dest: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "ant"}, settings: settings, permitted: false},
		{name: "server empty brace alternative", entry: argoappv1.ApplicationDestination{Server: "{https://*.,https://}prod.example", Namespace: "*"}, dest: argoappv1.ApplicationDestination{Server: "https://prod.example", Namespace: "apps"}, settings: settings, permitted: true},
		{name: "resolved name empty brace alternative", entry: argoappv1.ApplicationDestination{Name: "{*-,}prod", Namespace: "*"}, dest: argoappv1.ApplicationDestination{Name: "prod", Namespace: "apps"}, settings: settings, permitted: true},
		{name: "resolved name overlapping prefix and suffix", entry: argoappv1.ApplicationDestination{Name: "p*prod", Namespace: "*"}, dest: argoappv1.ApplicationDestination{Name: "prod", Namespace: "apps"}, settings: settings, permitted: false},
		{name: "unresolved name empty brace alternative", entry: argoappv1.ApplicationDestination{Name: "{*-,}edge", Namespace: "*"}, dest: argoappv1.ApplicationDestination{Name: "edge", Namespace: "apps"}, settings: config.DefaultSettings(), permitted: true},
		{name: "unresolved name empty brace alternative deny", entry: argoappv1.ApplicationDestination{Name: "!{*-,}edge", Namespace: "*"}, dest: argoappv1.ApplicationDestination{Name: "edge", Namespace: "apps"}, settings: config.DefaultSettings(), permitted: false},
		{name: "unresolved name overlapping prefix and suffix deny", entry: argoappv1.ApplicationDestination{Name: "!e*edge", Namespace: "*"}, dest: argoappv1.ApplicationDestination{Name: "edge", Namespace: "apps"}, settings: config.DefaultSettings(), permitted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := application("demo", "platform", argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "apps/demo"}, tt.dest)
			diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{destinationProject(tt.entry)}, tt.settings)
			if denied := hasProjectDestinationDiagnostic(diags, "destination is not permitted"); denied == tt.permitted {
				t.Fatalf("Diagnostics = %#v, want permitted=%t", diags, tt.permitted)
			}
			if len(diags) > 0 && tt.permitted {
				t.Fatalf("Diagnostics = %#v, want none", diags)
			}
		})
	}

	for _, tt := range []struct {
		pattern   string
		permitted bool
	}{
		{pattern: "https://github.com/example/re*po", permitted: true},
		{pattern: "https://github.com/example/r*repo", permitted: false},
	} {
		project := destinationProject(argoappv1.ApplicationDestination{Server: "*", Namespace: "*"})
		project.Spec.SourceRepos = []string{tt.pattern}
		app := application("demo", "platform", argoappv1.ApplicationSource{RepoURL: "https://github.com/example/repo", Path: "apps/demo"}, argoappv1.ApplicationDestination{Server: "https://kubernetes.default.svc", Namespace: "apps"})
		diags := ValidateApplications([]argoappv1.Application{app}, []argoappv1.AppProject{project}, config.DefaultSettings())
		if denied := hasProjectDestinationDiagnostic(diags, "source repository"); denied == tt.permitted {
			t.Fatalf("sourceRepos %q diagnostics = %#v, want permitted=%t", tt.pattern, diags, tt.permitted)
		}
	}
}
