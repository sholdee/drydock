package app

import (
	"fmt"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// manifestObjectPtrs returns the non-nil object pointers backing a manifest slice
// WITHOUT copying. Safe for read-only registry construction, which completes
// before normalizeCRDScope mutates the objects in place. Prefer this over the
// DeepCopy-ing manifestObjects when no snapshot is needed.
func manifestObjectPtrs(manifests []render.Manifest) []*unstructured.Unstructured {
	objects := make([]*unstructured.Unstructured, 0, len(manifests))
	for _, item := range manifests {
		if item.Object == nil {
			continue
		}
		objects = append(objects, item.Object)
	}
	return objects
}

// normalizeCRDScope strips metadata.namespace from every manifest that is
// cluster-scoped per the registry (or a built-in cluster-scoped kind), mirroring
// Argo CD. It mutates the shared unstructured objects in place. When two stripped
// cluster-scoped resources collide on group/kind/name it warns rather than dropping.
func normalizeCRDScope(manifests []render.Manifest, registry manifest.CRDScopeRegistry) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	seen := map[manifest.Identity]struct{}{}
	for _, item := range manifests {
		obj := item.Object
		if obj == nil {
			continue
		}
		if !registry.IsClusterScoped(obj.GroupVersionKind()) {
			continue
		}
		if obj.GetNamespace() != "" {
			obj.SetNamespace("")
		}
		id := manifest.IdentityOf(obj)
		if _, ok := seen[id]; ok {
			diags = append(diags, diagnostic.Diagnostic{
				Code:     "build.crd-scope-collision",
				Severity: diagnostic.SeverityWarning,
				Category: "crd-scope",
				Message:  fmt.Sprintf("cluster-scoped resource %s appears more than once after namespace normalization; both copies are retained", id.String()),
			})
			continue
		}
		seen[id] = struct{}{}
	}
	return diags
}
