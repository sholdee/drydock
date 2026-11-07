package config

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DefaultHelmNamespace is the namespace helm uses when no --namespace, no
// HELM_NAMESPACE, no kubeconfig, and no in-cluster service account token
// name one (EnvSettings.Namespace, pkg/cli/environment.go:276-286 at helm
// v4.3.1).
const DefaultHelmNamespace = "default"

// serviceAccountTokenPath is the directory where the kubelet mounts the
// service account token. client-go reads the in-cluster namespace from the
// namespace file in it.
const serviceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount"

// HelmNamespace returns the namespace helm uses in the Argo CD repo-server
// when Argo CD or kustomize passes no --namespace: the rendered
// RepoServerHelmNamespace, or DefaultHelmNamespace without a rendered
// install.
func (s ArgoSettings) HelmNamespace() string {
	if s.RepoServerHelmNamespace != nil && s.RepoServerHelmNamespace.Value != "" {
		return s.RepoServerHelmNamespace.Value
	}
	return DefaultHelmNamespace
}

// RepoServerHelmNamespace works out the namespace that helm uses in the
// repo-server pod of deployment when no --namespace is given. The Argo CD
// repo-server runs helm, and kustomize runs helm for helmCharts, with the
// environment of the repo-server container (util/helm/cmd.go:73 and
// HelmChartInflationGenerator.go:182 at kustomize/api v0.21.3).
//
// helm reads HELM_NAMESPACE first (EnvSettings). Then client-go
// DeferredLoadingClientConfig.Namespace
// (tools/clientcmd/merged_client_builder.go:131-168 at client-go v0.37.0)
// finds no kubeconfig in the repo-server and uses the in-cluster namespace
// when a service account token is mounted: POD_NAMESPACE, else the
// namespace of the token, which is the pod namespace
// (inClusterClientConfig.Namespace, client_config.go:646-661). Without a
// token it uses "default".
//
// The token is mounted when the pod spec automountServiceAccountToken is
// true, or unset while the ServiceAccount does not set it to false, or when
// the container mounts a volume at the token path. podNamespace is the
// namespace of the Deployment. serviceAccounts holds the rendered
// ServiceAccounts in that namespace. ok is false when the container sets
// KUBECONFIG, because drydock cannot read the kubeconfig.
func RepoServerHelmNamespace(deployment *unstructured.Unstructured, serviceAccounts []*unstructured.Unstructured, podNamespace string) (string, bool) {
	podSpec, _, _ := unstructured.NestedMap(deployment.Object, "spec", "template", "spec")
	container := repoServerContainer(podSpec)
	env := containerNamespaceEnv(container, podNamespace)
	if _, ok := env["KUBECONFIG"]; ok {
		return "", false
	}
	if namespace := env["HELM_NAMESPACE"]; namespace != "" {
		return namespace, true
	}
	if !serviceAccountTokenMounted(podSpec, container, serviceAccounts) {
		return DefaultHelmNamespace, true
	}
	if namespace := env["POD_NAMESPACE"]; namespace != "" {
		return namespace, true
	}
	if podNamespace == "" {
		return DefaultHelmNamespace, true
	}
	return podNamespace, true
}

// repoServerContainer returns the repo-server container of a pod spec: the
// container named argocd-repo-server (upstream install.yaml) or repo-server
// (argo-cd Helm chart), else the first container.
func repoServerContainer(podSpec map[string]any) map[string]any {
	containers, _, _ := unstructured.NestedSlice(podSpec, "containers")
	var first map[string]any
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if first == nil {
			first = container
		}
		switch container["name"] {
		case "argocd-repo-server", "repo-server":
			return container
		}
	}
	return first
}

// containerNamespaceEnv returns the env entries of container that decide
// the namespace, with a literal value or the pod namespace through the
// downward API. Another valueFrom is not known offline and is left out.
func containerNamespaceEnv(container map[string]any, podNamespace string) map[string]string {
	out := map[string]string{}
	items, _ := container["env"].([]any)
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if value, ok := entry["value"].(string); ok {
			out[name] = value
			continue
		}
		fieldPath, _, _ := unstructured.NestedString(entry, "valueFrom", "fieldRef", "fieldPath")
		if fieldPath == "metadata.namespace" {
			out[name] = podNamespace
		} else if name == "KUBECONFIG" {
			out[name] = ""
		}
	}
	return out
}

func serviceAccountTokenMounted(podSpec, container map[string]any, serviceAccounts []*unstructured.Unstructured) bool {
	mounts, _ := container["volumeMounts"].([]any)
	for _, item := range mounts {
		if mount, ok := item.(map[string]any); ok && mount["mountPath"] == serviceAccountTokenPath {
			return true
		}
	}
	if automount, ok := podSpec["automountServiceAccountToken"].(bool); ok {
		return automount
	}
	name, _ := podSpec["serviceAccountName"].(string)
	if name == "" {
		name, _ = podSpec["serviceAccount"].(string)
	}
	if name == "" {
		name = "default"
	}
	for _, serviceAccount := range serviceAccounts {
		if serviceAccount.GetName() != name {
			continue
		}
		if automount, ok := serviceAccount.Object["automountServiceAccountToken"].(bool); ok {
			return automount
		}
	}
	return true
}
