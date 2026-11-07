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

const remoteClusterSecret = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: remote\n  namespace: argocd\n  labels:\n    argocd.argoproj.io/secret-type: cluster\nstringData:\n  name: remote\n  server: https://remote.example.com\n"

func writeRemoteApplication(t *testing.T, root, name, repoURL, sourcePath, directory string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+repoURL+`
    targetRevision: HEAD
    path: "`+sourcePath+`"
`+directory+`  destination:
    server: https://remote.example.com
    namespace: elsewhere
`)
}

// Only a source that renders the committed file decides about it. An OCI
// artifact with path "." renders the artifact, not this repository, and a
// directory source reads a subdirectory only with directory.recurse.
func TestHelmNamespaceCommittedFileNeedsASourceThatRendersIt(t *testing.T) {
	writeInstall := func(t *testing.T, root string) {
		t.Helper()
		writeTestFile(t, filepath.Join(root, "bootstrap", "argocd", "install.yaml"), strings.Replace(repoServerKustomizeInstall, "metadata:\n  name: argocd-repo-server\n", "metadata:\n  name: argocd-repo-server\n  namespace: argocd\n", 1))
		writeTestFile(t, filepath.Join(root, "cluster.yaml"), remoteClusterSecret)
	}
	t.Run("OCI artifact with path dot", func(t *testing.T) {
		root := t.TempDir()
		writeInstall(t, root)
		writeRemoteApplication(t, root, "oci", "oci://registry.example.com/artifact", ".", "")
		// List only: the OCI artifact cannot render offline.
		result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Offline: true})
		if err != nil {
			t.Fatalf("ListApplications() error = %v", err)
		}
		ns := result.Settings.HelmNamespace()
		msgs := make([]string, 0, len(result.Diagnostics))
		for _, item := range result.Diagnostics {
			msgs = append(msgs, item.Message)
		}
		if ns != "argocd" || len(diagnosticsContaining(msgs, "repo-server")) != 0 {
			t.Fatalf("HelmNamespace = %q, diagnostics %q, want argocd without a warning", ns, msgs)
		}
	})
	t.Run("directory without recurse", func(t *testing.T) {
		root := t.TempDir()
		writeInstall(t, root)
		writeRemoteApplication(t, root, "plain", reviewRepoURL, "bootstrap", "")
		if ns, msgs := buildHelmNamespace(t, root); ns != "argocd" {
			t.Fatalf("HelmNamespace = %q, diagnostics %q, want argocd", ns, msgs)
		}
	})
	t.Run("directory with recurse on another cluster", func(t *testing.T) {
		root := t.TempDir()
		writeInstall(t, root)
		writeRemoteApplication(t, root, "plain", reviewRepoURL, "bootstrap", "    directory:\n      recurse: true\n")
		if ns, msgs := buildHelmNamespace(t, root); ns != "default" {
			t.Fatalf("HelmNamespace = %q, diagnostics %q, want default: the install runs on another cluster", ns, msgs)
		}
	})
	t.Run("kustomization that does not list the file", func(t *testing.T) {
		root := t.TempDir()
		writeInstall(t, root)
		writeTestFile(t, filepath.Join(root, "bootstrap", "kustomization.yaml"), "resources:\n  - other.yaml\n")
		writeTestFile(t, filepath.Join(root, "bootstrap", "other.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n")
		writeRemoteApplication(t, root, "kust", reviewRepoURL, "bootstrap", "")
		if ns, msgs := buildHelmNamespace(t, root); ns != "argocd" {
			t.Fatalf("HelmNamespace = %q, diagnostics %q, want argocd", ns, msgs)
		}
	})
}

// The kustomize NamespaceTransformer sets the kustomization namespace over
// the object's own namespace, and the Argo CD destination applies only
// when both are empty.
func TestHelmNamespaceKustomizationNamespaceWinsOverTheObject(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "install.yaml"), strings.Replace(repoServerKustomizeInstall, "metadata:\n  name: argocd-repo-server\n", "metadata:\n  name: argocd-repo-server\n  namespace: other\n", 1))
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), "namespace: argocd\nresources:\n  - install.yaml\n")
	writeReviewApplication(t, root, "argocd", "argocd", "", "dest")
	if ns, msgs := buildHelmNamespace(t, root); ns != "argocd" {
		t.Fatalf("HelmNamespace = %q, diagnostics %q, want the kustomization namespace argocd", ns, msgs)
	}
	// Without the kustomization namespace, the object's own wins over the
	// destination.
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), "resources:\n  - install.yaml\n")
	writeTestFile(t, filepath.Join(root, "argocd", "install.yaml"), strings.Replace(repoServerKustomizeInstall, "metadata:\n  name: argocd-repo-server\n", "metadata:\n  name: argocd-repo-server\n  namespace: argocd\n", 1))
	if ns, msgs := buildHelmNamespace(t, root); ns != "argocd" {
		t.Fatalf("HelmNamespace = %q, diagnostics %q, want the object namespace argocd", ns, msgs)
	}
	// With neither, the destination decides.
	writeTestFile(t, filepath.Join(root, "argocd", "install.yaml"), repoServerKustomizeInstall)
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	if ns, msgs := buildHelmNamespace(t, root); ns != "argocd" {
		t.Fatalf("HelmNamespace = %q, diagnostics %q, want the destination namespace argocd", ns, msgs)
	}
}

// A ServiceAccount whose namespace drydock cannot tell gets the same
// fallback namespace as its Deployment, so its automountServiceAccountToken
// counts.
func TestHelmNamespaceServiceAccountWithUnknownNamespace(t *testing.T) {
	root := t.TempDir()
	install := strings.Replace(repoServerKustomizeInstall, "      automountServiceAccountToken: true\n", "      serviceAccountName: argocd-repo-server\n", 1)
	install += "---\napiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: argocd-repo-server\nautomountServiceAccountToken: false\n"
	writeTestFile(t, filepath.Join(root, "bootstrap", "install.yaml"), install)
	ns, msgs := buildHelmNamespace(t, root)
	if ns != "default" {
		t.Fatalf("HelmNamespace = %q, diagnostics %q, want default: the ServiceAccount turns the token off", ns, msgs)
	}
}

// testdata/repo-server-probe-ns is the probe-ns repository of the third
// review: a committed kustomize install whose repo-server sets a literal
// HELM_NAMESPACE next to an env entry from a ConfigMap, a second committed
// repo-server in other/, and a Helm Application without a namespace. With
// an OCI Application on another cluster at path ".", which renders its
// artifact and not this repository, the committed install still counts.
func TestHelmNamespaceProbeNSRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "repo-server-probe-ns"))); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "cluster.yaml"), remoteClusterSecret)
	writeRemoteApplication(t, root, "oci", "oci://registry.example.com/artifact", ".", "")
	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Offline: true})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if got := result.Settings.HelmNamespace(); got != "committed-ns" {
		t.Fatalf("HelmNamespace = %q, want committed-ns from the install with argocd-cm", got)
	}
	msgs := make([]string, 0, len(result.Diagnostics))
	for _, item := range result.Diagnostics {
		msgs = append(msgs, item.Message)
	}
	if got := diagnosticsContaining(msgs, `in other/rs.yaml gives the Helm namespace "other-ns", but drydock uses "committed-ns"`); len(got) != 1 {
		t.Fatalf("diagnostics = %q, want one conflict warning for other/rs.yaml", msgs)
	}
	if got := diagnosticsContaining(msgs, "cannot tell the Helm namespace"); len(got) != 0 {
		t.Fatalf("diagnostics = %q, want no unknown-input warning: a literal HELM_NAMESPACE decides", msgs)
	}
}

// A Kustomize overlay that reads a base outside its path renders the
// committed install, and kustomize applies the namespace of every
// kustomization on the way, the outermost last, so it wins.
func TestHelmNamespaceOverlayReadsACommittedBase(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "base", "argocd", "install.yaml"), repoServerKustomizeInstall)
	writeTestFile(t, filepath.Join(root, "base", "argocd", "kustomization.yaml"), "namespace: base-ns\nresources:\n  - install.yaml\n")
	writeTestFile(t, filepath.Join(root, "overlays", "prod", "kustomization.yaml"), "namespace: argocd\nresources:\n  - ../../base/argocd\n")
	writeReviewApplication(t, root, "argocd", "overlays/prod", "", "dest")
	if ns, msgs := buildHelmNamespace(t, root); ns != "argocd" {
		t.Fatalf("HelmNamespace = %q, diagnostics %q, want the outermost kustomization namespace argocd", ns, msgs)
	}
	// The same overlay on another cluster runs another Argo CD.
	root = t.TempDir()
	writeTestFile(t, filepath.Join(root, "base", "argocd", "install.yaml"), repoServerKustomizeInstall)
	writeTestFile(t, filepath.Join(root, "base", "argocd", "kustomization.yaml"), "namespace: argocd\nresources:\n  - install.yaml\n")
	writeTestFile(t, filepath.Join(root, "overlays", "prod", "kustomization.yaml"), "resources:\n  - ../../base/argocd\n")
	writeTestFile(t, filepath.Join(root, "cluster.yaml"), remoteClusterSecret)
	writeRemoteApplication(t, root, "remote", reviewRepoURL, "overlays/prod", "")
	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Offline: true})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if got := result.Settings.HelmNamespace(); got != "default" {
		t.Fatalf("HelmNamespace = %q, want default: only an Application on another cluster renders the base", got)
	}
}

// A directory source reads only the files that its include and exclude
// globs let through.
func TestHelmNamespaceDirectorySourceHonorsIncludeAndExclude(t *testing.T) {
	for _, tc := range []struct {
		name, directory, want string
	}{
		{name: "excluded", directory: "    directory:\n      exclude: install.yaml\n", want: "argocd"},
		{name: "not included", directory: "    directory:\n      include: other.yaml\n", want: "argocd"},
		{name: "included", directory: "    directory:\n      include: install.yaml\n", want: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "bootstrap", "install.yaml"), strings.Replace(repoServerKustomizeInstall, "metadata:\n  name: argocd-repo-server\n", "metadata:\n  name: argocd-repo-server\n  namespace: argocd\n", 1))
			writeTestFile(t, filepath.Join(root, "cluster.yaml"), remoteClusterSecret)
			writeRemoteApplication(t, root, "plain", reviewRepoURL, "bootstrap", tc.directory)
			result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Offline: true})
			if err != nil {
				t.Fatalf("ListApplications() error = %v", err)
			}
			if got := result.Settings.HelmNamespace(); got != tc.want {
				t.Fatalf("HelmNamespace = %q, want %q", got, tc.want)
			}
		})
	}
}

// The kustomize.namespace of an Application wins over every kustomization
// namespace, because Argo CD sets it on the source kustomization. It
// applies only while the source renders with Kustomize: with
// kustomize.enable off, the source is a plain directory (probe of the
// fourth review).
func TestHelmNamespaceKustomizeNamespaceOverride(t *testing.T) {
	root := t.TempDir()
	install := strings.Replace(repoServerKustomizeInstall, "metadata:\n  name: argocd-repo-server\n", "metadata:\n  name: argocd-repo-server\n  namespace: other\n", 1)
	writeTestFile(t, filepath.Join(root, "argocd", "install.yaml"), install)
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), "namespace: other\nresources:\n  - install.yaml\n")
	writeReviewApplication(t, root, "argocd", "argocd", "    kustomize:\n      namespace: argocd\n", "argocd")
	if ns, msgs := buildHelmNamespace(t, root); ns != "argocd" {
		t.Fatalf("HelmNamespace = %q, diagnostics %q, want the kustomize.namespace argocd", ns, msgs)
	}
	writeReviewArgoCDConfigMap(t, root, "  kustomize.enable: \"false\"\n")
	if ns, msgs := buildHelmNamespace(t, root); ns != "other" {
		t.Fatalf("HelmNamespace = %q, diagnostics %q, want other: with Kustomize off the object keeps its namespace", ns, msgs)
	}
}

// A chart from a chart repository with a version range and no destination
// namespace renders in the repo-server namespace, with the resolved version
// as ARGOCD_APP_REVISION. Its Chart.yaml kubeVersion is not checked
// without an explicit kube version.
func TestHelmNamespaceReachesChartOnlyRangeSource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "install.yaml"), repoServerKustomizeInstall)
	writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), "namespace: argocd\nresources:\n  - install.yaml\n")
	writeReviewApplication(t, root, "argocd", "argocd", "", "argocd")
	writeTestFile(t, filepath.Join(root, "apps", "ranged.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ranged
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://charts.example.test
    chart: demo
    targetRevision: '>=1.0.0'
    helm:
      parameters:
      - name: revision
        value: $ARGOCD_APP_REVISION
  destination:
    name: in-cluster
`)
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.4.0\nkubeVersion: '<1.20.0-0'\n")
	writeTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ranged\ndata:\n  namespace: {{ .Release.Namespace | quote }}\n  revision: {{ .Values.revision | quote }}\n")
	acquirer := &recordingChartAcquirer{chartDir: chartDir, resolvedVersion: "1.4.0"}
	result, err := Orchestrator{ChartAcquirer: acquirer}.Build(context.Background(), BuildRequest{
		Path:          root,
		RepoMaps:      []sourcepkg.RepoMap{{URL: reviewRepoURL, Path: root}},
		Offline:       true,
		ChartCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := result.Settings.HelmNamespace(); got != "argocd" {
		t.Fatalf("HelmNamespace = %q, want argocd", got)
	}
	for _, item := range result.ApplicationManifests {
		if item.Application.Name != "ranged" {
			continue
		}
		data, _ := item.Manifest.Object.Object["data"].(map[string]any)
		if data["namespace"] != "argocd" || data["revision"] != "1.4.0" {
			t.Fatalf("data = %v, want namespace argocd and revision 1.4.0", data)
		}
		return
	}
	t.Fatal("no manifest for Application ranged")
}
