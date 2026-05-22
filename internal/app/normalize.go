package app

import (
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type groupKind struct {
	Group string
	Kind  string
}

var builtInClusterScopedKinds = map[groupKind]struct{}{
	{Kind: "ComponentStatus"}:  {},
	{Kind: "Namespace"}:        {},
	{Kind: "Node"}:             {},
	{Kind: "PersistentVolume"}: {},

	{Group: "admissionregistration.k8s.io", Kind: "MutatingAdmissionPolicy"}:          {},
	{Group: "admissionregistration.k8s.io", Kind: "MutatingAdmissionPolicyBinding"}:   {},
	{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration"}:     {},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicy"}:        {},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicyBinding"}: {},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingWebhookConfiguration"}:   {},
	{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}:                 {},
	{Group: "apiregistration.k8s.io", Kind: "APIService"}:                             {},
	{Group: "apiserverinternal.k8s.io", Kind: "StorageVersion"}:                       {},
	{Group: "authentication.k8s.io", Kind: "SelfSubjectReview"}:                       {},
	{Group: "authentication.k8s.io", Kind: "TokenReview"}:                             {},
	{Group: "authorization.k8s.io", Kind: "SelfSubjectAccessReview"}:                  {},
	{Group: "authorization.k8s.io", Kind: "SelfSubjectRulesReview"}:                   {},
	{Group: "authorization.k8s.io", Kind: "SubjectAccessReview"}:                      {},
	{Group: "certificates.k8s.io", Kind: "CertificateSigningRequest"}:                 {},
	{Group: "certificates.k8s.io", Kind: "ClusterTrustBundle"}:                        {},
	{Group: "flowcontrol.apiserver.k8s.io", Kind: "FlowSchema"}:                       {},
	{Group: "flowcontrol.apiserver.k8s.io", Kind: "PriorityLevelConfiguration"}:       {},
	{Group: "imagepolicy.k8s.io", Kind: "ImageReview"}:                                {},
	{Group: "networking.k8s.io", Kind: "IngressClass"}:                                {},
	{Group: "networking.k8s.io", Kind: "IPAddress"}:                                   {},
	{Group: "networking.k8s.io", Kind: "ServiceCIDR"}:                                 {},
	{Group: "node.k8s.io", Kind: "RuntimeClass"}:                                      {},
	{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}:                         {},
	{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"}:                  {},
	{Group: "resource.k8s.io", Kind: "DeviceClass"}:                                   {},
	{Group: "resource.k8s.io", Kind: "DeviceTaintRule"}:                               {},
	{Group: "resource.k8s.io", Kind: "ResourceSlice"}:                                 {},
	{Group: "scheduling.k8s.io", Kind: "PriorityClass"}:                               {},
	{Group: "storage.k8s.io", Kind: "CSIDriver"}:                                      {},
	{Group: "storage.k8s.io", Kind: "CSINode"}:                                        {},
	{Group: "storage.k8s.io", Kind: "StorageClass"}:                                   {},
	{Group: "storage.k8s.io", Kind: "VolumeAttachment"}:                               {},
	{Group: "storage.k8s.io", Kind: "VolumeAttributesClass"}:                          {},
	{Group: "storagemigration.k8s.io", Kind: "StorageVersionMigration"}:               {},
}

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
	_, ok := builtInClusterScopedKinds[groupKind{Group: gvk.Group, Kind: gvk.Kind}]
	return ok
}
