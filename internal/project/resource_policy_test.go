package project

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestValidateRenderedResourcePolicyAllowsNamespacedResourcesByDefault(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "workloads", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyDeniesNamespacedResourceOutsideWhitelist(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{{Group: "apps", Kind: "Deployment"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "workloads", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "ConfigMap workloads/settings")
}

func TestValidateRenderedResourcePolicyDeniesNamespacedResourceOnBlacklist(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.NamespaceResourceBlacklist = []metav1.GroupKind{{Group: "", Kind: "Secret"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "Secret", "workloads", "credentials"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "Secret workloads/credentials")
}

func TestValidateRenderedResourcePolicyPreservesNilVsEmptyNamespaceWhitelist(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	nilWhitelistProject := resourcePolicyProject("platform")
	nilWhitelistProject.Spec.NamespaceResourceWhitelist = nil

	nilWhitelistDiags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("apps/v1", "Deployment", "workloads", "api"),
	}, []argoappv1.AppProject{nilWhitelistProject}, config.DefaultSettings())
	assertNoResourcePolicyDiagnostics(t, nilWhitelistDiags)

	emptyWhitelistProject := resourcePolicyProject("platform")
	emptyWhitelistProject.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{}
	emptyWhitelistDiags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("apps/v1", "Deployment", "workloads", "api"),
	}, []argoappv1.AppProject{emptyWhitelistProject}, config.DefaultSettings())
	assertResourcePolicyDenied(t, emptyWhitelistDiags, "apps/Deployment workloads/api")
}

func TestValidateRenderedResourcePolicyTreatsKnownNamespacedBuiltInWithEmptyNamespaceAsNamespaced(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{{Group: "", Kind: "ConfigMap"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyTreatsLocalSubjectAccessReviewAsNamespaced(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{{Group: "", Kind: "ConfigMap"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("authorization.k8s.io/v1", "LocalSubjectAccessReview", "", "review"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "authorization.k8s.io/LocalSubjectAccessReview review")
	assertNoDiagnostic(t, diags, "unknown scope offline")
}

func TestValidateRenderedResourcePolicyTreatsKnownNamespacedBuiltInWithoutDestinationNamespaceAsNamespaced(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	app.Spec.Destination.Namespace = ""
	project := resourcePolicyProject("platform")
	project.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{{Group: "apps", Kind: "Deployment"}}
	project.Spec.ClusterResourceWhitelist = []argoappv1.ClusterResourceRestrictionItem{{Group: "", Kind: "ConfigMap"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "ConfigMap settings")
}

func TestValidateRenderedResourcePolicyDoesNotFabricateDefaultNamespace(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	app.Spec.Destination.Namespace = ""
	project := resourcePolicyProject("platform")
	project.Spec.Destinations = []argoappv1.ApplicationDestination{{
		Name:      "in-cluster",
		Namespace: "",
	}}
	project.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{{Group: "", Kind: "ConfigMap"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "", "settings"),
	}, []argoappv1.AppProject{project}, settingsWithCluster("in-cluster", "https://kubernetes.default.svc", ""))

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyTreatsRenderedNamespacedObjectAsNamespaced(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("example.com/v1", "Widget", "workloads", "custom"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyAllowsClusterResourceOnWhitelist(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.ClusterResourceWhitelist = []argoappv1.ClusterResourceRestrictionItem{{Group: "", Kind: "Namespace"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "Namespace", "", "workloads"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyAllowsImplicitDefaultProjectClusterResource(t *testing.T) {
	app := resourcePolicyApplication("demo", argoappv1.DefaultAppProjectName)

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "Namespace", "", "workloads"),
	}, nil, config.DefaultSettings())

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyDeniesClusterResourceWithoutWhitelist(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "Namespace", "", "workloads"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "Namespace workloads")
}

func TestValidateRenderedResourcePolicyDeniesClusterResourceOnBlacklist(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.ClusterResourceWhitelist = []argoappv1.ClusterResourceRestrictionItem{{Group: "*", Kind: "*"}}
	project.Spec.ClusterResourceBlacklist = []argoappv1.ClusterResourceRestrictionItem{{Group: "", Kind: "Namespace"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "Namespace", "", "workloads"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "Namespace workloads")
}

func TestValidateRenderedResourcePolicyAppliesClusterResourceNameGlobs(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.ClusterResourceWhitelist = []argoappv1.ClusterResourceRestrictionItem{{Group: "", Kind: "Namespace", Name: "team-*"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "Namespace", "", "team-a"),
		renderedObject("v1", "Namespace", "", "other"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "Namespace other")
	assertNoDiagnostic(t, diags, "Namespace team-a is not permitted")
}

func TestValidateRenderedResourcePolicyUsesProjectScopedClusterMetadata(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.PermitOnlyProjectScopedClusters = true

	allowedDiags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "workloads", "settings"),
	}, []argoappv1.AppProject{project}, settingsWithCluster("in-cluster", "https://kubernetes.default.svc", "platform"))
	assertNoResourcePolicyDiagnostics(t, allowedDiags)

	deniedDiags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "workloads", "settings"),
	}, []argoappv1.AppProject{project}, settingsWithCluster("in-cluster", "https://kubernetes.default.svc", "other"))
	assertNoResourcePolicyDenied(t, deniedDiags)
	assertNoResourcePolicyDestinationDenied(t, deniedDiags)
}

func TestValidateRenderedResourcePolicyDeniesRenderedObjectNamespaceOutsideDestinations(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.Destinations = []argoappv1.ApplicationDestination{{
		Name:      "in-cluster",
		Namespace: "workloads",
	}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "kube-system", "settings"),
	}, []argoappv1.AppProject{project}, settingsWithCluster("in-cluster", "https://kubernetes.default.svc", ""))

	assertResourcePolicyDestinationDenied(t, diags, "ConfigMap kube-system/settings")
	assertNoResourcePolicyDenied(t, diags)
}

func TestValidateRenderedResourcePolicyDefersWhenProjectScopedClusterMetadataIsUnavailable(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.PermitOnlyProjectScopedClusters = true

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "workloads", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertDiagnostic(t, diags, "project-scoped cluster Secrets enforcement is deferred offline")
	assertNoResourcePolicyDenied(t, diags)
}

func TestValidateRenderedResourcePolicyStillChecksNamespaceWhenProjectScopedMetadataIsUnavailable(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.PermitOnlyProjectScopedClusters = true
	project.Spec.Destinations = []argoappv1.ApplicationDestination{{
		Name:      "*",
		Namespace: "workloads",
	}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "kube-system", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDestinationDenied(t, diags, "ConfigMap kube-system/settings")
	assertNoResourcePolicyDenied(t, diags)
}

func TestValidateRenderedResourcePolicyDefersNameOnlyDestinationServerPolicy(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.Destinations = []argoappv1.ApplicationDestination{{
		Server:    "https://kubernetes.default.svc",
		Namespace: "*",
	}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "workloads", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertDiagnostic(t, diags, "destination name \"in-cluster\" cannot be resolved against AppProject server policy offline")
	assertNoResourcePolicyDenied(t, diags)
}

func TestValidateRenderedResourcePolicyDefersNameOnlyDestinationServerPolicyForRenderedNamespace(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.Destinations = []argoappv1.ApplicationDestination{
		{
			Name:      "in-cluster",
			Namespace: "workloads",
		},
		{
			Server:    "https://kubernetes.default.svc",
			Namespace: "kube-system",
		},
	}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "kube-system", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertDiagnostic(t, diags, "destination name \"in-cluster\" cannot be resolved against AppProject server policy offline")
	assertNoResourcePolicyDenied(t, diags)
	assertNoResourcePolicyDestinationDenied(t, diags)
}

func TestValidateRenderedResourcePolicyAllowsNameOnlyRenderedNamespaceWithWildcardServer(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.Destinations = []argoappv1.ApplicationDestination{
		{
			Name:      "in-cluster",
			Namespace: "workloads",
		},
		{
			Server:    "*",
			Namespace: "kube-system",
		},
	}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "kube-system", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyDoesNotBypassNameOnlyRenderedNamespaceDeny(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.Destinations = []argoappv1.ApplicationDestination{
		{
			Name:      "in-cluster",
			Namespace: "workloads",
		},
		{
			Server:    "*",
			Namespace: "*",
		},
		{
			Server:    "*",
			Namespace: "!kube-system",
		},
	}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("v1", "ConfigMap", "kube-system", "settings"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDestinationDenied(t, diags, "ConfigMap kube-system/settings")
	assertNoResourcePolicyDenied(t, diags)
}

func TestValidateRenderedResourcePolicyDefersUnknownUnnamespacedCRScope(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("example.com/v1", "Widget", "", "custom"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertDiagnostic(t, diags, "unknown scope offline")
	assertResourcePolicyScopeDeferred(t, diags)
	assertNoResourcePolicyDenied(t, diags)
}

func TestValidateRenderedResourcePolicyAllowsUnknownCRWhenPossibleScopesPermit(t *testing.T) {
	app := resourcePolicyApplication("demo", argoappv1.DefaultAppProjectName)

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("example.com/v1", "Widget", "", "custom"),
	}, nil, config.DefaultSettings())

	assertNoResourcePolicyDiagnostics(t, diags)
}

func TestValidateRenderedResourcePolicyDeniesUnknownCRWhenPossibleScopesDeny(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")
	project.Spec.NamespaceResourceWhitelist = []metav1.GroupKind{{Group: "", Kind: "ConfigMap"}}

	diags := ValidateRenderedResourcePolicy(app, []*unstructured.Unstructured{
		renderedObject("example.com/v1", "Widget", "", "custom"),
	}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertResourcePolicyDenied(t, diags, "example.com/Widget custom")
	assertNoDiagnostic(t, diags, "unknown scope offline")
}

func TestValidateRenderedResourcePolicyDefersUnknownCRNormalizedFromDestinationNamespace(t *testing.T) {
	app := resourcePolicyApplication("demo", "platform")
	project := resourcePolicyProject("platform")

	diags := ValidateRenderedResourcePolicyResources(app, []RenderedResource{{
		Object:                       renderedObject("example.com/v1", "Widget", "workloads", "custom"),
		NamespaceBeforeNormalization: "",
	}}, []argoappv1.AppProject{project}, config.DefaultSettings())

	assertDiagnostic(t, diags, "unknown scope offline")
	assertResourcePolicyScopeDeferred(t, diags)
	assertNoResourcePolicyDenied(t, diags)
}

func resourcePolicyApplication(name, project string) argoappv1.Application {
	return application(name, project, argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "apps/" + name,
	}, argoappv1.ApplicationDestination{
		Name:      "in-cluster",
		Namespace: "workloads",
	})
}

func resourcePolicyProject(name string) argoappv1.AppProject {
	return argoappv1.AppProject{
		ObjectMeta: objectMeta(name),
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:  []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{Name: "*", Namespace: "*"}},
		},
	}
}

func renderedObject(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	return obj
}

func assertNoResourcePolicyDiagnostics(t *testing.T, diags []diagnostic.Diagnostic) {
	t.Helper()
	if len(diags) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", diags)
	}
}

func assertResourcePolicyDenied(t *testing.T, diags []diagnostic.Diagnostic, fragment string) {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == projectResourceDeniedCode && strings.Contains(diag.Message, fragment) {
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want resource policy denial containing %q with code %q", diags, fragment, projectResourceDeniedCode)
}

func assertNoResourcePolicyDenied(t *testing.T, diags []diagnostic.Diagnostic) {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == projectResourceDeniedCode {
			t.Fatalf("Diagnostics = %#v, want no resource policy denial", diags)
		}
	}
}

func assertResourcePolicyDestinationDenied(t *testing.T, diags []diagnostic.Diagnostic, fragment string) {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == projectResourceDestinationDeniedCode && strings.Contains(diag.Message, fragment) {
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want rendered resource destination denial containing %q with code %q", diags, fragment, projectResourceDestinationDeniedCode)
}

func assertNoResourcePolicyDestinationDenied(t *testing.T, diags []diagnostic.Diagnostic) {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == projectResourceDestinationDeniedCode {
			t.Fatalf("Diagnostics = %#v, want no rendered resource destination denial", diags)
		}
	}
}

func assertResourcePolicyScopeDeferred(t *testing.T, diags []diagnostic.Diagnostic) {
	t.Helper()
	for _, diag := range diags {
		if diag.Code == projectResourceScopeDeferredCode {
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want resource policy scope deferral code %q", diags, projectResourceScopeDeferredCode)
}
