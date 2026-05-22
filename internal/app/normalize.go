package app

import (
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func ApplyDestinationNamespace(application argoappv1.Application, obj *unstructured.Unstructured) {
	if obj.GetNamespace() != "" {
		return
	}
	namespace := application.Spec.Destination.Namespace
	if namespace == "" {
		return
	}
	obj.SetNamespace(namespace)
}
