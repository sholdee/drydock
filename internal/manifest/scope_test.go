package manifest

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
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
