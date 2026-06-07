package app

import (
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func ApplyDestinationNamespace(application argoappv1.Application, obj *unstructured.Unstructured) {
	if obj.GetNamespace() != "" {
		return
	}
	if IsBuiltInClusterScoped(obj.GroupVersionKind()) {
		return
	}
	namespace := application.Spec.Destination.Namespace
	if namespace == "" {
		return
	}
	obj.SetNamespace(namespace)
}

func IsBuiltInClusterScoped(gvk schema.GroupVersionKind) bool {
	return manifest.IsBuiltInClusterScoped(gvk)
}
