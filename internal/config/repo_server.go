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

// RepoServerUnknownSourceKey and RepoServerEnvFromKey mark, in a sanitized
// repo-server Deployment (discovery.sanitizedRepoServerDeployment), an env
// entry whose value drydock cannot know offline and a container with
// envFrom. They record only that such a source exists, never its value.
const (
	RepoServerUnknownSourceKey = "drydockUnknownSource"
	RepoServerEnvFromKey       = "drydockEnvFrom"
)

// RepoServerHelmNamespace works out the namespace that helm uses in the
// repo-server pod of deployment when no --namespace is given. The Argo CD
// repo-server runs helm, and kustomize runs helm for helmCharts, with the
// environment of the repo-server container (util/helm/cmd.go:73 and
// HelmChartInflationGenerator.go:182 at kustomize/api v0.21.3).
//
// helm reads HELM_NAMESPACE into the namespace override first
// (EnvSettings, pkg/cli/environment.go:99 and 276-286 at helm v4.3.1), and
// client-go returns an override before it reads any kubeconfig
// (DirectClientConfig.Namespace, tools/clientcmd/client_config.go:399-406
// at client-go v0.37.0). Otherwise client-go reads the kubeconfig that
// KUBECONFIG names; an empty KUBECONFIG is unset, and the repo-server has no
// kubeconfig in its home directory. Without one,
// DeferredLoadingClientConfig.Namespace (merged_client_builder.go:131-168)
// uses the in-cluster namespace when a service account token is mounted:
// POD_NAMESPACE, else the namespace of the token, which is the pod
// namespace (inClusterClientConfig.Namespace, client_config.go:646-661).
// Without a token it uses "default".
//
// The token is mounted when the pod spec automountServiceAccountToken is
// true, or unset while the ServiceAccount does not set it to false, or when
// the container mounts a volume at the token path. podNamespace is the
// namespace of the Deployment. serviceAccounts holds the ServiceAccounts in
// that namespace.
//
// notes names each input that drydock cannot know offline, such as an env
// entry from a ConfigMap or a KUBECONFIG file. The namespace is then the one
// that drydock works out without that input, and the caller warns.
func RepoServerHelmNamespace(deployment *unstructured.Unstructured, serviceAccounts []*unstructured.Unstructured, podNamespace string) (namespace string, notes []string) {
	podSpec, _, _ := unstructured.NestedMap(deployment.Object, "spec", "template", "spec")
	container := repoServerContainer(podSpec)
	env, unknown := containerNamespaceEnv(container, podNamespace)
	if container[RepoServerEnvFromKey] == true {
		notes = append(notes, "the repo-server container sets envFrom, which can set HELM_NAMESPACE, POD_NAMESPACE, or KUBECONFIG")
	}
	for _, name := range []string{"HELM_NAMESPACE", "KUBECONFIG", "POD_NAMESPACE"} {
		if source, ok := unknown[name]; ok {
			notes = append(notes, name+" comes from "+source)
		}
	}
	if value := env["HELM_NAMESPACE"]; value != "" {
		return value, notes
	}
	if env["KUBECONFIG"] != "" {
		notes = append(notes, "KUBECONFIG names a kubeconfig file that drydock cannot read")
	}
	if !serviceAccountTokenMounted(podSpec, container, serviceAccounts) {
		return DefaultHelmNamespace, notes
	}
	if value := env["POD_NAMESPACE"]; value != "" {
		return value, notes
	}
	if podNamespace == "" {
		return DefaultHelmNamespace, notes
	}
	return podNamespace, notes
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
// the namespace: a literal value, or the pod namespace through the
// downward API. unknown names each entry whose value drydock cannot know,
// with the kind of its source.
func containerNamespaceEnv(container map[string]any, podNamespace string) (known, unknown map[string]string) {
	known = map[string]string{}
	unknown = map[string]string{}
	items, _ := container["env"].([]any)
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if source, ok := entry[RepoServerUnknownSourceKey].(string); ok {
			unknown[name] = source
			delete(known, name)
			continue
		}
		if value, ok := entry["value"].(string); ok {
			known[name] = value
			delete(unknown, name)
			continue
		}
		fieldPath, _, _ := unstructured.NestedString(entry, "valueFrom", "fieldRef", "fieldPath")
		if fieldPath == "metadata.namespace" {
			known[name] = podNamespace
			delete(unknown, name)
			continue
		}
		// An entry without a value is the empty string.
		known[name] = ""
		delete(unknown, name)
	}
	return known, unknown
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
