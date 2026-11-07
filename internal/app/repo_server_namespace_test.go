package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// The tests in this file come from the second review of the repo-server
// Helm namespace: committed and rendered installs, patches, installs on
// another cluster, and repo-servers outside the Argo CD namespace.

func buildHelmNamespace(t *testing.T, root string) (string, []string) {
	t.Helper()
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:     root,
		RepoMaps: []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
		Offline:  true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	msgs := make([]string, 0, len(result.Diagnostics))
	for _, d := range result.Diagnostics {
		msgs = append(msgs, d.Message)
	}
	return result.Settings.HelmNamespace(), msgs
}

const repoServerKustomizeInstall = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-repo-server
spec:
  selector:
    matchLabels:
      app: repo-server
  template:
    metadata:
      labels:
        app: repo-server
    spec:
      automountServiceAccountToken: true
      containers:
        - name: argocd-repo-server
          image: x
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  a: b
`

// Kustomize install with a committed patch of the repo-server Deployment.
func TestHelmNamespaceCommittedInstallWithPatch(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "install.yaml"), repoServerKustomizeInstall)
	writeTestFile(t, filepath.Join(root, "argocd", "patch.yaml"), "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: argocd-repo-server\nspec:\n  template:\n    spec:\n      automountServiceAccountToken: true\n")
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), "namespace: argocd\nresources:\n  - install.yaml\npatches:\n  - path: patch.yaml\n")
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "argocd" {
		t.Errorf("HelmNamespace = %q, want argocd", ns)
	}
}

// Same without the patch: control.
func TestHelmNamespaceCommittedKustomizeInstall(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "install.yaml"), repoServerKustomizeInstall)
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), "namespace: argocd\nresources:\n  - install.yaml\n")
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "argocd" {
		t.Errorf("HelmNamespace = %q, want argocd", ns)
	}
}

// Repo-server only (no argocd-cm) in another namespace, not the controller namespace.
func TestHelmNamespaceIgnoresRepoServerOutsideArgoCDNamespace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "other", "install.yaml"), "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: argocd-repo-server\n  namespace: other\nspec:\n  selector:\n    matchLabels:\n      app: r\n  template:\n    metadata:\n      labels:\n        app: r\n    spec:\n      automountServiceAccountToken: true\n      containers:\n        - name: argocd-repo-server\n          image: x\n")
	writeReviewApplication(t, root, "other", "other", "", "other")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "default" {
		t.Errorf("HelmNamespace = %q, want default (repo-server not in controller namespace argocd)", ns)
	}
}

// Remote-cluster install with argocd-cm.
func TestHelmNamespaceIgnoresCommittedRemoteInstall(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "remote", "install.yaml"), repoServerKustomizeInstall)
	writeTestFile(t, filepath.Join(root, "remote", "kustomization.yaml"), "namespace: argocd\nresources:\n  - install.yaml\n")
	writeTestFile(t, filepath.Join(root, "apps", "remote.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: remote
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+reviewRepoURL+`
    targetRevision: HEAD
    path: remote
  destination:
    server: https://remote.example.com
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "cluster.yaml"), "apiVersion: v1\nkind: Secret\nmetadata:\n  name: remote\n  namespace: argocd\n  labels:\n    argocd.argoproj.io/secret-type: cluster\nstringData:\n  name: remote\n  server: https://remote.example.com\n")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "default" {
		t.Errorf("HelmNamespace = %q, want default (install on remote cluster)", ns)
	}
}

func writeRepoServerChartInstall(t *testing.T, root, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, dir, "Chart.yaml"), "apiVersion: v2\nname: argo-cd\nversion: 1.0.0\n")
	tpl := strings.ReplaceAll(repoServerKustomizeInstall, "metadata:\n  name: argocd-repo-server\n", "metadata:\n  name: argocd-repo-server\n  namespace: {{ .Release.Namespace }}\n")
	tpl = strings.ReplaceAll(tpl, "name: argocd-cm\n", "name: argocd-cm\n  namespace: {{ .Release.Namespace }}\n")
	writeTestFile(t, filepath.Join(root, dir, "templates", "install.yaml"), tpl)
}

func TestHelmNamespaceRenderedChartInstall(t *testing.T) {
	root := t.TempDir()
	writeRepoServerChartInstall(t, root, "argocd")
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "argocd" {
		t.Errorf("got %q want argocd", ns)
	}
}

func TestHelmNamespaceRenderedChartInstallWithUnrelatedPatch(t *testing.T) {
	root := t.TempDir()
	writeRepoServerChartInstall(t, root, "argocd")
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	writeTestFile(t, filepath.Join(root, "unrelated", "patch.yaml"), "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: argocd-repo-server\nspec:\n  template:\n    spec:\n      automountServiceAccountToken: true\n")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "argocd" {
		t.Errorf("got %q want argocd", ns)
	}
}

func TestHelmNamespaceIgnoresRenderedRepoServerOutsideArgoCDNamespace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "other", "Chart.yaml"), "apiVersion: v2\nname: x\nversion: 1.0.0\n")
	writeTestFile(t, filepath.Join(root, "other", "templates", "d.yaml"), "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: argocd-repo-server\n  namespace: {{ .Release.Namespace }}\nspec:\n  selector:\n    matchLabels:\n      app: r\n  template:\n    metadata:\n      labels:\n        app: r\n    spec:\n      automountServiceAccountToken: true\n      containers:\n        - name: argocd-repo-server\n          image: x\n")
	writeReviewApplication(t, root, "other", "other", "", "other")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "default" {
		t.Errorf("got %q want default", ns)
	}
}

func TestHelmNamespaceIgnoresRenderedRemoteInstall(t *testing.T) {
	root := t.TempDir()
	writeRepoServerChartInstall(t, root, "remote")
	writeTestFile(t, filepath.Join(root, "apps", "remote.yaml"), "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: remote\n  namespace: argocd\nspec:\n  project: default\n  source:\n    repoURL: "+reviewRepoURL+"\n    targetRevision: HEAD\n    path: remote\n  destination:\n    server: https://remote.example.com\n    namespace: argocd\n")
	writeTestFile(t, filepath.Join(root, "cluster.yaml"), "apiVersion: v1\nkind: Secret\nmetadata:\n  name: remote\n  namespace: argocd\n  labels:\n    argocd.argoproj.io/secret-type: cluster\nstringData:\n  name: remote\n  server: https://remote.example.com\n")
	ns, msgs := buildHelmNamespace(t, root)
	t.Logf("ns=%q diags=%q", ns, msgs)
	if ns != "default" {
		t.Errorf("got %q want default", ns)
	}
}

func diagnosticsContaining(msgs []string, want string) []string {
	var out []string
	for _, msg := range msgs {
		if strings.Contains(msg, want) {
			out = append(out, msg)
		}
	}
	return out
}

// A committed install whose namespace drydock cannot tell: no namespace on
// the object, no kustomization that lists the file, and no Application that
// renders it. drydock assumes the Argo CD namespace and warns.
func TestHelmNamespaceWarnsWhenCommittedNamespaceIsUnknown(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "bootstrap", "install.yaml"), repoServerKustomizeInstall)
	ns, msgs := buildHelmNamespace(t, root)
	if ns != "argocd" {
		t.Fatalf("HelmNamespace = %q, want argocd", ns)
	}
	if got := diagnosticsContaining(msgs, `cannot tell the namespace of the Argo CD repo-server argocd/argocd-repo-server in bootstrap/install.yaml; drydock assumes the Argo CD namespace "argocd"`); len(got) != 1 {
		t.Fatalf("diagnostics = %q, want one unknown namespace warning", msgs)
	}
}

// An env input that drydock cannot know offline gives a warning that names
// the repo-server and the namespace drydock uses, never the source object.
func TestHelmNamespaceWarnsAboutUnknownEnvSources(t *testing.T) {
	root := t.TempDir()
	writeRepoServerChartInstall(t, root, "argocd")
	template := filepath.Join(root, "argocd", "templates", "install.yaml")
	writeTestFile(t, template, strings.Replace(readTestFile(t, template), "        - name: argocd-repo-server\n          image: x\n", "        - name: argocd-repo-server\n          image: x\n          env:\n            - name: HELM_NAMESPACE\n              valueFrom:\n                configMapKeyRef:\n                  name: hidden-configmap\n                  key: namespace\n", 1))
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	ns, msgs := buildHelmNamespace(t, root)
	if ns != "argocd" {
		t.Fatalf("HelmNamespace = %q, want argocd", ns)
	}
	got := diagnosticsContaining(msgs, "HELM_NAMESPACE comes from valueFrom configMapKeyRef")
	if len(got) != 1 || !strings.Contains(got[0], "argocd/argocd-repo-server") || !strings.Contains(got[0], `drydock uses "argocd"`) {
		t.Fatalf("diagnostics = %q, want one warning that names the repo-server and the namespace used", msgs)
	}
	for _, msg := range msgs {
		if strings.Contains(msg, "hidden-configmap") {
			t.Fatalf("diagnostic %q names the ConfigMap", msg)
		}
	}
}

// Two repo-servers that give different namespaces: drydock uses the one
// from the same install as the argocd-cm and warns about the other.
func TestHelmNamespacePrefersTheInstallWithArgoCDConfigMap(t *testing.T) {
	root := t.TempDir()
	writeRepoServerChartInstall(t, root, "argocd")
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	// A second committed repo-server in the Argo CD namespace without a
	// token, next to an AppProject, so discovery reads the file.
	writeTestFile(t, filepath.Join(root, "aaa", "extra.yaml"), "apiVersion: argoproj.io/v1alpha1\nkind: AppProject\nmetadata:\n  name: extra\n  namespace: argocd\nspec: {}\n---\n"+strings.Replace(strings.Split(repoServerKustomizeInstall, "---\n")[0], "metadata:\n  name: argocd-repo-server\n", "metadata:\n  name: second-repo-server\n  namespace: argocd\n  labels:\n    app.kubernetes.io/component: repo-server\n    app.kubernetes.io/part-of: argocd\n", 1))
	writeTestFile(t, filepath.Join(root, "aaa", "extra.yaml"), strings.Replace(readTestFile(t, filepath.Join(root, "aaa", "extra.yaml")), "automountServiceAccountToken: true", "automountServiceAccountToken: false", 1))
	for range 2 {
		ns, msgs := buildHelmNamespace(t, root)
		if ns != "argocd" {
			t.Fatalf("HelmNamespace = %q, want argocd from the install with argocd-cm", ns)
		}
		if got := diagnosticsContaining(msgs, `second-repo-server in aaa/extra.yaml gives the Helm namespace "default", but drydock uses "argocd"`); len(got) != 1 {
			t.Fatalf("diagnostics = %q, want one conflict warning", msgs)
		}
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
