package manifest

import (
	"strings"
	"testing"

	kubescheme "github.com/argoproj/argo-cd/gitops-engine/v3/pkg/utils/kube/scheme"
	"k8s.io/apimachinery/pkg/runtime/schema"
	smdschema "sigs.k8s.io/structured-merge-diff/v6/schema"
)

func TestBuiltInScopeRecognizesStorageVersionAPIGroup(t *testing.T) {
	if !IsBuiltInClusterScoped(schema.GroupVersionKind{Group: "internal.apiserver.k8s.io", Version: "v1alpha1", Kind: "StorageVersion"}) {
		t.Fatal("StorageVersion must be recognized as a built-in cluster-scoped resource")
	}
}

func TestBuiltInScopeRecognizesLocalSubjectAccessReviewAsNamespaced(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "authorization.k8s.io", Version: "v1", Kind: "LocalSubjectAccessReview"}
	if !IsKnownNamespacedBuiltIn(gvk) {
		t.Fatal("LocalSubjectAccessReview must be recognized as a known namespaced resource")
	}
	if IsBuiltInClusterScoped(gvk) {
		t.Fatal("LocalSubjectAccessReview must not be treated as cluster-scoped")
	}
}

func TestBuiltInScopeRecognizesKubernetes137Kinds(t *testing.T) {
	// Scopes follow the +genclient:nonNamespaced markers in k8s.io/api v0.37.0.
	tests := []struct {
		gvk           schema.GroupVersionKind
		clusterScoped bool
	}{
		{gvk: schema.GroupVersionKind{Group: "certificates.k8s.io", Version: "v1", Kind: "PodCertificateRequest"}},
		{gvk: schema.GroupVersionKind{Group: "lifecycle.k8s.io", Version: "v1alpha1", Kind: "Eviction"}},
		{gvk: schema.GroupVersionKind{Group: "lifecycle.k8s.io", Version: "v1alpha1", Kind: "EvictionRequest"}},
		{gvk: schema.GroupVersionKind{Group: "scheduling.k8s.io", Version: "v1alpha3", Kind: "CompositePodGroup"}},
		{gvk: schema.GroupVersionKind{Group: "scheduling.k8s.io", Version: "v1beta1", Kind: "PodGroup"}},
		{gvk: schema.GroupVersionKind{Group: "scheduling.k8s.io", Version: "v1beta1", Kind: "Workload"}},
		{gvk: schema.GroupVersionKind{Group: "extensions", Version: "v1beta1", Kind: "Deployment"}},
		{gvk: schema.GroupVersionKind{Group: "certificates.k8s.io", Version: "v1", Kind: "ClusterTrustBundle"}, clusterScoped: true},
		{gvk: schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceTaintRule"}, clusterScoped: true},
		{gvk: schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1alpha3", Kind: "ResourcePoolStatusRequest"}, clusterScoped: true},
		{gvk: schema.GroupVersionKind{Group: "storagemigration.k8s.io", Version: "v1", Kind: "StorageVersionMigration"}, clusterScoped: true},
	}
	for _, tt := range tests {
		if got := IsBuiltInClusterScoped(tt.gvk); got != tt.clusterScoped {
			t.Errorf("IsBuiltInClusterScoped(%s) = %t, want %t", tt.gvk, got, tt.clusterScoped)
		}
		if got := IsKnownNamespacedBuiltIn(tt.gvk); got != !tt.clusterScoped {
			t.Errorf("IsKnownNamespacedBuiltIn(%s) = %t, want %t", tt.gvk, got, !tt.clusterScoped)
		}
	}
}

// staticSchemeAPIGroups maps the k8s.io/api package segment of a static
// scheme type name (io.k8s.api.<package>.<version>.<Kind>) to its API group.
var staticSchemeAPIGroups = map[string]string{
	"admissionregistration": "admissionregistration.k8s.io",
	"apiserverinternal":     "internal.apiserver.k8s.io",
	"apps":                  "apps",
	"authentication":        "authentication.k8s.io",
	"authorization":         "authorization.k8s.io",
	"autoscaling":           "autoscaling",
	"batch":                 "batch",
	"certificates":          "certificates.k8s.io",
	"coordination":          "coordination.k8s.io",
	"core":                  "",
	"discovery":             "discovery.k8s.io",
	"events":                "events.k8s.io",
	"extensions":            "extensions",
	"flowcontrol":           "flowcontrol.apiserver.k8s.io",
	"imagepolicy":           "imagepolicy.k8s.io",
	"lifecycle":             "lifecycle.k8s.io",
	"networking":            "networking.k8s.io",
	"node":                  "node.k8s.io",
	"policy":                "policy",
	"rbac":                  "rbac.authorization.k8s.io",
	"resource":              "resource.k8s.io",
	"scheduling":            "scheduling.k8s.io",
	"storage":               "storage.k8s.io",
	"storagemigration":      "storagemigration.k8s.io",
}

// TestBuiltInScopeTablesCoverStaticSchemeKinds is a tripwire for Kubernetes
// bumps: every top-level object kind in the gitops-engine static scheme that
// Argo CD diffs with must have a scope in one of the built-in tables. The
// scheme carries no scope, so a failure means classifying the new kind from
// its +genclient:nonNamespaced marker in k8s.io/api.
func TestBuiltInScopeTablesCoverStaticSchemeKinds(t *testing.T) {
	seen := 0
	for _, typeDef := range kubescheme.StaticParser().Schema.Types {
		pkg, version, kind, ok := staticSchemeTopLevelKind(typeDef)
		if !ok {
			continue
		}
		group, ok := staticSchemeAPIGroups[pkg]
		if !ok {
			t.Errorf("static scheme type %s uses unmapped API package %q", typeDef.Name, pkg)
			continue
		}
		seen++
		gvk := schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
		clusterScoped, namespaced := IsBuiltInClusterScoped(gvk), IsKnownNamespacedBuiltIn(gvk)
		switch {
		case clusterScoped && namespaced:
			t.Errorf("%s (%s) is in both built-in scope tables", gvk, typeDef.Name)
		case !clusterScoped && !namespaced:
			t.Errorf("%s (%s) is missing from the built-in scope tables", gvk, typeDef.Name)
		}
	}
	if seen < 100 {
		t.Fatalf("static scheme yielded %d top-level kinds, want the full Kubernetes API set", seen)
	}
}

// staticSchemeTopLevelKind reports the k8s.io/api package, version, and kind
// of a static scheme type that is a top-level object: a type named
// io.k8s.api.<package>.<version>.<Kind> with apiVersion, kind, and ObjectMeta
// metadata fields. List types carry ListMeta and are skipped.
func staticSchemeTopLevelKind(typeDef smdschema.TypeDef) (pkg, version, kind string, ok bool) {
	const objectMeta = "io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"
	parts := strings.Split(typeDef.Name, ".")
	if len(parts) != 6 || strings.Join(parts[:3], ".") != "io.k8s.api" || typeDef.Map == nil {
		return "", "", "", false
	}
	fields := map[string]bool{}
	for _, field := range typeDef.Fields {
		fields[field.Name] = field.Name != "metadata" ||
			(field.Type.NamedType != nil && *field.Type.NamedType == objectMeta)
	}
	if !fields["apiVersion"] || !fields["kind"] || !fields["metadata"] {
		return "", "", "", false
	}
	return parts[3], parts[4], parts[5], true
}
