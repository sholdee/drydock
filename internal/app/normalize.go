package app

import (
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var knownClusterScopedKinds = map[string]struct{}{
	"/Namespace":        {},
	"/Node":             {},
	"/PersistentVolume": {},
	"admissionregistration.k8s.io/MutatingWebhookConfiguration":   {},
	"admissionregistration.k8s.io/ValidatingWebhookConfiguration": {},
	"apiextensions.k8s.io/CustomResourceDefinition":               {},
	"apiregistration.k8s.io/APIService":                           {},
	"rbac.authorization.k8s.io/ClusterRole":                       {},
	"rbac.authorization.k8s.io/ClusterRoleBinding":                {},
	"scheduling.k8s.io/PriorityClass":                             {},
	"storage.k8s.io/StorageClass":                                 {},
}

func ApplyDestinationNamespace(application argoappv1.Application, obj *unstructured.Unstructured) {
	if obj.GetNamespace() != "" {
		return
	}
	if isKnownClusterScoped(obj) {
		return
	}
	namespace := application.Spec.Destination.Namespace
	if namespace == "" {
		return
	}
	obj.SetNamespace(namespace)
}

func isKnownClusterScoped(obj *unstructured.Unstructured) bool {
	gvk := obj.GroupVersionKind()
	_, ok := knownClusterScopedKinds[gvk.Group+"/"+gvk.Kind]
	return ok
}
