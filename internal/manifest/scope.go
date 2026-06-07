package manifest

import "k8s.io/apimachinery/pkg/runtime/schema"

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
	{Group: "internal.apiserver.k8s.io", Kind: "StorageVersion"}:                      {},
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

var knownNamespacedBuiltInKinds = map[groupKind]struct{}{
	{Kind: "Binding"}:                                       {},
	{Kind: "ConfigMap"}:                                     {},
	{Kind: "Endpoints"}:                                     {},
	{Kind: "Event"}:                                         {},
	{Kind: "LimitRange"}:                                    {},
	{Kind: "PersistentVolumeClaim"}:                         {},
	{Kind: "Pod"}:                                           {},
	{Kind: "PodTemplate"}:                                   {},
	{Kind: "ReplicationController"}:                         {},
	{Kind: "ResourceQuota"}:                                 {},
	{Kind: "Secret"}:                                        {},
	{Kind: "Service"}:                                       {},
	{Kind: "ServiceAccount"}:                                {},
	{Group: "apps", Kind: "ControllerRevision"}:             {},
	{Group: "apps", Kind: "DaemonSet"}:                      {},
	{Group: "apps", Kind: "Deployment"}:                     {},
	{Group: "apps", Kind: "ReplicaSet"}:                     {},
	{Group: "apps", Kind: "StatefulSet"}:                    {},
	{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}: {},
	{Group: "batch", Kind: "CronJob"}:                       {},
	{Group: "batch", Kind: "Job"}:                           {},
	{Group: "authorization.k8s.io", Kind: "LocalSubjectAccessReview"}: {},
	{Group: "coordination.k8s.io", Kind: "Lease"}:                     {},
	{Group: "coordination.k8s.io", Kind: "LeaseCandidate"}:            {},
	{Group: "discovery.k8s.io", Kind: "EndpointSlice"}:                {},
	{Group: "events.k8s.io", Kind: "Event"}:                           {},
	{Group: "networking.k8s.io", Kind: "Ingress"}:                     {},
	{Group: "networking.k8s.io", Kind: "NetworkPolicy"}:               {},
	{Group: "policy", Kind: "Eviction"}:                               {},
	{Group: "policy", Kind: "PodDisruptionBudget"}:                    {},
	{Group: "rbac.authorization.k8s.io", Kind: "Role"}:                {},
	{Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"}:         {},
	{Group: "resource.k8s.io", Kind: "PodSchedulingContext"}:          {},
	{Group: "resource.k8s.io", Kind: "ResourceClaim"}:                 {},
	{Group: "resource.k8s.io", Kind: "ResourceClaimTemplate"}:         {},
	{Group: "storage.k8s.io", Kind: "CSIStorageCapacity"}:             {},
}

func IsBuiltInClusterScoped(gvk schema.GroupVersionKind) bool {
	_, ok := builtInClusterScopedKinds[groupKind{Group: gvk.Group, Kind: gvk.Kind}]
	return ok
}

func IsKnownNamespacedBuiltIn(gvk schema.GroupVersionKind) bool {
	_, ok := knownNamespacedBuiltInKinds[groupKind{Group: gvk.Group, Kind: gvk.Kind}]
	return ok
}
