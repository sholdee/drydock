package app

import (
	"path/filepath"
	"strings"
	"testing"
)

const helmNamespaceRepoServer = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-repo-server
spec:
  template:
    spec:
      containers:
        - name: argocd-repo-server
          args: [/usr/local/bin/argocd-repo-server]
          env:
            - name: HELM_NAMESPACE
              value: tools
            - name: ARGOCD_REPO_SERVER_INCLUDE_HIDDEN_DIRECTORIES
              valueFrom:
                configMapKeyRef: {name: argocd-cmd-params-cm, key: reposerver.include.hidden.directories, optional: true}
`

const helmNamespaceSidecar = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-repo-server
spec:
  template:
    spec:
      containers:
        - name: avp
          command: [/var/run/argocd/argocd-cmp-server]
          env:
            - name: HELM_NAMESPACE
              value: wrong
`

// The Helm namespace follows the rules of the workload settings: a listed
// Kustomize patch does not count, and drydock reads only the container that
// runs argocd-repo-server, never the first container of another file.
func TestRepoServerHelmNamespaceSkipsPatchesAndSidecars(t *testing.T) {
	for _, tc := range []struct {
		name    string
		patches string
		sidecar string
	}{
		{name: "listed sidecar patch", patches: "patches:\n  - path: a-patch.yaml\n", sidecar: "a-patch.yaml"},
		{name: "unlisted sidecar-only file", sidecar: "0-sidecar.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "bootstrap", "argocd", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: argocd\nresources:\n  - https://raw.githubusercontent.com/argoproj/argo-cd/v3.6.1/manifests/install.yaml\n  - argocd-cmd-params-cm.yaml\n  - repo-server.yaml\n"+tc.patches)
			writeTestFile(t, filepath.Join(root, "bootstrap", "argocd", "argocd-cmd-params-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cmd-params-cm\ndata:\n  reposerver.include.hidden.directories: \"true\"\n")
			writeTestFile(t, filepath.Join(root, "bootstrap", "argocd", "repo-server.yaml"), helmNamespaceRepoServer)
			writeTestFile(t, filepath.Join(root, "bootstrap", "argocd", tc.sidecar), helmNamespaceSidecar)
			writeDiffApplication(t, root, "other", "other", "same")
			result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
			if got := result.Settings.HelmNamespace(); got != "tools" {
				t.Errorf("HelmNamespace = %q, want tools from the container that runs argocd-repo-server", got)
			}
			for _, diag := range result.Diagnostics {
				if strings.Contains(diag.Message, "wrong") {
					t.Errorf("diagnostic %q, want none about the sidecar", diag.Message)
				}
			}
		})
	}
}

// When drydock cannot tell the pod namespace of a repo-server but a
// literal HELM_NAMESPACE decides the Helm namespace, the pod namespace does
// not matter, so drydock does not warn that it assumes one.
func TestRepoServerHelmNamespaceLiteralNeedsNoPodNamespace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\ndata:\n  timeout.reconciliation: 180s\n")
	writeTestFile(t, filepath.Join(root, "settings", "repo-server.yaml"), helmNamespaceRepoServer)
	writeDiffApplication(t, root, "other", "other", "same")
	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if got := result.Settings.HelmNamespace(); got != "tools" {
		t.Fatalf("HelmNamespace = %q, want tools", got)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "assumes the Argo CD namespace") {
			t.Fatalf("diagnostic %q, want none: HELM_NAMESPACE decides", diag.Message)
		}
	}
}
