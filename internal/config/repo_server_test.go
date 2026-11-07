package config

import (
	"slices"
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
		wantNotes       []string
	}{
		{name: "token off", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: argocd-repo-server\n", want: "default"},
		{name: "token on", spec: "      automountServiceAccountToken: true\n      containers:\n        - name: repo-server\n", want: "argocd"},
		{name: "unset, ServiceAccount off", spec: "      containers:\n        - name: repo-server\n", serviceAccounts: []*unstructured.Unstructured{saOff}, want: "default"},
		{name: "unset", spec: "      containers:\n        - name: repo-server\n", want: "argocd"},
		{name: "pod spec wins over ServiceAccount", spec: "      automountServiceAccountToken: true\n      containers:\n        - name: repo-server\n", serviceAccounts: []*unstructured.Unstructured{saOff}, want: "argocd"},
		{name: "token mounted by hand without the namespace file", spec: "      automountServiceAccountToken: false\n      volumes:\n        - name: token\n          drydockTokenFile: true\n          drydockNamespaceFile: false\n      containers:\n        - name: repo-server\n          volumeMounts:\n            - name: token\n              mountPath: /var/run/secrets/kubernetes.io/serviceaccount\n", want: "default"},
		{name: "token mounted by hand with the namespace file", spec: "      automountServiceAccountToken: false\n      volumes:\n        - name: token\n          drydockTokenFile: true\n          drydockNamespaceFile: true\n      containers:\n        - name: repo-server\n          volumeMounts:\n            - name: token\n              mountPath: /var/run/secrets/kubernetes.io/serviceaccount\n", want: "argocd"},
		{name: "volume at the token path without the token", spec: "      automountServiceAccountToken: false\n      volumes:\n        - name: token\n          drydockTokenFile: false\n          drydockNamespaceFile: true\n      containers:\n        - name: repo-server\n          volumeMounts:\n            - name: token\n              mountPath: /var/run/secrets/kubernetes.io/serviceaccount\n", want: "default"},
		{name: "HELM_KUBECONTEXT with a KUBECONFIG drydock cannot read", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: KUBECONFIG\n              value: /kube/config\n            - name: HELM_KUBECONTEXT\n              value: other\n", want: "default", wantNotes: []string{"KUBECONFIG names a kubeconfig file that drydock cannot read"}},
		{name: "HELM_KUBECONTEXT without a kubeconfig", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: HELM_KUBECONTEXT\n              value: other\n", want: "default"},
		{name: "literal HELM_NAMESPACE silences the other inputs", spec: "      containers:\n        - name: repo-server\n          drydockEnvFrom: true\n          env:\n            - name: POD_NAMESPACE\n              drydockUnknownSource: valueFrom configMapKeyRef\n            - name: KUBECONFIG\n              drydockUnknownSource: valueFrom secretKeyRef\n            - name: HELM_NAMESPACE\n              value: helm-ns\n", want: "helm-ns"},
		{name: "HELM_NAMESPACE wins", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: repo-server\n          env:\n            - name: HELM_NAMESPACE\n              value: helm-ns\n", want: "helm-ns"},
		{name: "POD_NAMESPACE literal", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: POD_NAMESPACE\n              value: pod-ns\n", want: "pod-ns"},
		{name: "POD_NAMESPACE without a token", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: repo-server\n          env:\n            - name: POD_NAMESPACE\n              value: pod-ns\n", want: "default"},
		{name: "POD_NAMESPACE from the downward API", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: POD_NAMESPACE\n              valueFrom:\n                fieldRef:\n                  fieldPath: metadata.namespace\n", want: "argocd"},
		{name: "other container is not the repo-server", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: sidecar\n          env:\n            - name: HELM_NAMESPACE\n              value: wrong\n        - name: repo-server\n", want: "default"},
		{name: "KUBECONFIG is not known", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: KUBECONFIG\n              value: /kube/config\n", want: "argocd", wantNotes: []string{"KUBECONFIG names a kubeconfig file that drydock cannot read"}},
		{name: "empty KUBECONFIG is unset", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: KUBECONFIG\n              value: \"\"\n", want: "argocd"},
		{name: "HELM_NAMESPACE wins over KUBECONFIG", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: KUBECONFIG\n              value: /kube/config\n            - name: HELM_NAMESPACE\n              value: helm-ns\n", want: "helm-ns"},
		{name: "HELM_NAMESPACE from an unknown source", spec: "      automountServiceAccountToken: false\n      containers:\n        - name: repo-server\n          env:\n            - name: HELM_NAMESPACE\n              drydockUnknownSource: valueFrom configMapKeyRef\n", want: "default", wantNotes: []string{"HELM_NAMESPACE comes from valueFrom configMapKeyRef"}},
		{name: "POD_NAMESPACE from an unknown source", spec: "      containers:\n        - name: repo-server\n          env:\n            - name: POD_NAMESPACE\n              drydockUnknownSource: a $(VAR) reference\n", want: "argocd", wantNotes: []string{"POD_NAMESPACE comes from a $(VAR) reference"}},
		{name: "envFrom", spec: "      containers:\n        - name: repo-server\n          drydockEnvFrom: true\n", want: "argocd", wantNotes: []string{"the repo-server container sets envFrom, which can set HELM_NAMESPACE, HELM_KUBECONTEXT, POD_NAMESPACE, or KUBECONFIG"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := repoServerTestObject(t, deploymentHead+tc.spec)
			got, notes := RepoServerHelmNamespace(deployment, tc.serviceAccounts, "argocd")
			if got != tc.want || !slices.Equal(notes, tc.wantNotes) {
				t.Fatalf("RepoServerHelmNamespace() = %q, %q, want %q, %q", got, notes, tc.want, tc.wantNotes)
			}
		})
	}
	if got := DefaultSettings().HelmNamespace(); got != DefaultHelmNamespace {
		t.Fatalf("DefaultSettings().HelmNamespace() = %q, want %q", got, DefaultHelmNamespace)
	}
}
