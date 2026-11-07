package config

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

func repoServerTestObject(t *testing.T, text string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	if err := sigsyaml.Unmarshal([]byte(text), &obj.Object); err != nil {
		t.Fatal(err)
	}
	return obj
}

// RepoServerHelmNamespace follows helm EnvSettings.Namespace and client-go
// DeferredLoadingClientConfig.Namespace in a repo-server pod without a
// kubeconfig file.
func TestRepoServerHelmNamespace(t *testing.T) {
	const deploymentHead = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: argocd-repo-server\n  namespace: argocd\nspec:\n  template:\n    spec:\n      serviceAccountName: argocd-repo-server\n"
	saOff := repoServerTestObject(t, "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: argocd-repo-server\n  namespace: argocd\nautomountServiceAccountToken: false\n")
	for _, tc := range []struct {
		name            string
		spec            string
		serviceAccounts []*unstructured.Unstructured
		want            string
		wantOK          bool
	}{
		{name: "token off", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: argocd-repo-server\n", want: "default", wantOK: true},
		{name: "token on", spec: "      automountServiceAccountToken: true\n      containers:\n        - name: repo-server\n", want: "argocd", wantOK: true},
		{name: "unset, ServiceAccount off", spec: "      containers:\n        - name: repo-server\n", serviceAccounts: []*unstructured.Unstructured{saOff}, want: "default", wantOK: true},
		{name: "unset", spec: "      containers:\n        - name: repo-server\n", want: "argocd", wantOK: true},
		{name: "pod spec wins over ServiceAccount", spec: "      automountServiceAccountToken: true\n      containers:\n        - name: repo-server\n", serviceAccounts: []*unstructured.Unstructured{saOff}, want: "argocd", wantOK: true},
		{name: "token mounted by hand", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: repo-server\n          volumeMounts:\n            - name: token\n              mountPath: /var/run/secrets/kubernetes.io/serviceaccount\n", want: "argocd", wantOK: true},
		{name: "HELM_NAMESPACE wins", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: repo-server\n          env:\n            - name: HELM_NAMESPACE\n              value: helm-ns\n", want: "helm-ns", wantOK: true},
		{name: "POD_NAMESPACE literal", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: POD_NAMESPACE\n              value: pod-ns\n", want: "pod-ns", wantOK: true},
		{name: "POD_NAMESPACE without a token", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: repo-server\n          env:\n            - name: POD_NAMESPACE\n              value: pod-ns\n", want: "default", wantOK: true},
		{name: "POD_NAMESPACE from the downward API", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: POD_NAMESPACE\n              valueFrom:\n                fieldRef:\n                  fieldPath: metadata.namespace\n", want: "argocd", wantOK: true},
		{name: "other container is not the repo-server", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: sidecar\n          env:\n            - name: HELM_NAMESPACE\n              value: wrong\n        - name: repo-server\n", want: "default", wantOK: true},
		{name: "KUBECONFIG is not known", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: KUBECONFIG\n              value: /kube/config\n", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := repoServerTestObject(t, deploymentHead+tc.spec)
			got, ok := RepoServerHelmNamespace(deployment, tc.serviceAccounts, "argocd")
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("RepoServerHelmNamespace() = %q, %v, want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
	if got := DefaultSettings().HelmNamespace(); got != DefaultHelmNamespace {
		t.Fatalf("DefaultSettings().HelmNamespace() = %q, want %q", got, DefaultHelmNamespace)
	}
}
