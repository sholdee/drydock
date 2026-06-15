package manifest

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CRDScope is a CustomResourceDefinition spec.scope value.
type CRDScope string

const (
	CRDScopeCluster    CRDScope = "Cluster"
	CRDScopeNamespaced CRDScope = "Namespaced"
)

// CRDScopeEntry is the scope (and cheaply-captured versions) for one CRD's
// group/kind. Versions are retained for a later api-version inference plan.
type CRDScopeEntry struct {
	Scope    CRDScope
	Versions []string
}

// CRDScopeRegistry maps a custom resource's {group, kind} to its declared scope.
type CRDScopeRegistry map[groupKind]CRDScopeEntry

// BuildCRDScopeRegistry scans rendered objects for CustomResourceDefinition
// manifests and records each declared {group, kind} -> scope. Objects that are
// not v1 CRDs or that omit spec.group/spec.names.kind/spec.scope are ignored.
func BuildCRDScopeRegistry(objects []*unstructured.Unstructured) CRDScopeRegistry {
	registry := CRDScopeRegistry{}
	for _, obj := range objects {
		if obj == nil {
			continue
		}
		if obj.GetKind() != "CustomResourceDefinition" || obj.GroupVersionKind().Group != "apiextensions.k8s.io" {
			continue
		}
		group, _, _ := unstructured.NestedString(obj.Object, "spec", "group")
		kind, _, _ := unstructured.NestedString(obj.Object, "spec", "names", "kind")
		scope, _, _ := unstructured.NestedString(obj.Object, "spec", "scope")
		if kind == "" || scope == "" {
			continue
		}
		entry := CRDScopeEntry{Scope: CRDScope(scope)}
		if versions, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "versions"); ok {
			for _, raw := range versions {
				version, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if name, ok := version["name"].(string); ok && name != "" {
					entry.Versions = append(entry.Versions, name)
				}
			}
		}
		registry[groupKind{Group: group, Kind: kind}] = entry
	}
	return registry
}

// Scope returns the declared scope for a custom resource's group/kind.
func (r CRDScopeRegistry) Scope(gvk schema.GroupVersionKind) (CRDScope, bool) {
	entry, ok := r[groupKind{Group: gvk.Group, Kind: gvk.Kind}]
	if !ok {
		return "", false
	}
	return entry.Scope, true
}

// IsClusterScoped consults the registry first, then the built-in cluster-scoped list.
func (r CRDScopeRegistry) IsClusterScoped(gvk schema.GroupVersionKind) bool {
	if scope, ok := r.Scope(gvk); ok {
		return scope == CRDScopeCluster
	}
	return IsBuiltInClusterScoped(gvk)
}
