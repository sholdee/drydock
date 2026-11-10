package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// writeSpokeClusterChart writes an Application that renders, from a local
// chart, a cluster Secret that names the cluster in-cluster but points at
// another server. Static discovery does not read chart templates, so only
// the discovery frontier finds the Secret.
func writeSpokeClusterChart(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "clusters.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: clusters
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: clusters
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "clusters", "Chart.yaml"), "apiVersion: v2\nname: clusters\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "clusters", "templates", "cluster.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: spoke
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: in-cluster
  server: https://spoke.example.com
`)
}

// The first discovery round finds the install on this cluster and renders
// it for its settings. Then the frontier renders a cluster Secret that moves
// the destination name in-cluster to another server, so the install deploys
// Argo CD on that cluster, and its settings are not this cluster's. With
// the Secret, the install never counts, so this is the settings that
// settle: the settings of the install from the first round must not stay.
func TestSettingsDropInstallThatStopsCountingInALaterRound(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    reposerver.max.combined.directory.manifests.size: 50M
`)
	writeSpokeClusterChart(t, root)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if got := result.Settings.MaxCombinedDirectoryManifestsSize; got.Value != "" {
		t.Fatalf("MaxCombinedDirectoryManifestsSize = %q from %q, want no value from an install on another cluster", got.Value, got.Provenance.Path)
	}
	if got := result.Settings.InstanceLabelKey.Value; got == "argocd.argoproj.io/instance" {
		t.Fatalf("InstanceLabelKey = %q, want no chart default from an install on another cluster", got)
	}
	for _, diag := range result.Diagnostics {
		if diag.Code == diagnostic.CodeSettingsInstallsUnsettled {
			t.Fatalf("diagnostic = %#v, want none: the settings settle", diag)
		}
	}
}

// The install renders an argocd-cm that turns the in-cluster destination
// off. With its settings, the install deploys nowhere and does not count;
// without them, it counts. The settings never settle. drydock keeps the
// settings of the install, as the cluster holds them once the install ran,
// and says so with a warning.
func TestSettingsKeepInstallThatDoesNotSettleWithWarning(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  params:
    reposerver.max.combined.directory.manifests.size: 50M
`)
	acquirer := fakeArgoCDChartAcquirer(t)
	writeTestFile(t, filepath.Join(acquirer.chartDir, "values.yaml"), strings.Replace(fakeArgoCDChartValues,
		"    timeout.reconciliation: 120s\n",
		"    timeout.reconciliation: 120s\n    cluster.inClusterEnabled: \"false\"\n", 1))

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, acquirer)
	if got := result.Settings.MaxCombinedDirectoryManifestsSize.Value; got != "50M" {
		t.Fatalf("MaxCombinedDirectoryManifestsSize = %q, want the install value", got)
	}
	var unsettled []diagnostic.Diagnostic
	for _, diag := range result.Diagnostics {
		if diag.Code == diagnostic.CodeSettingsInstallsUnsettled {
			unsettled = append(unsettled, diag)
		}
	}
	if len(unsettled) != 1 || unsettled[0].Severity != diagnostic.SeverityWarning || !strings.Contains(unsettled[0].Message, "argocd/argocd") {
		t.Fatalf("unsettled diagnostics = %#v, want one warning that names argocd/argocd", unsettled)
	}
}

// Two installs: argocd deploys to the destination name in-cluster, which
// the cluster Secret that the frontier finds moves to another cluster, and
// argocd-b deploys to the in-cluster server and still counts. The settings
// of argocd go, and those of argocd-b stay. argocd renders only
// argocd-cmd-params-cm and argocd-b only argocd-cm, so they do not collide.
func TestSettingsKeepTheInstallThatStillCounts(t *testing.T) {
	root := t.TempDir()
	writeKustomizeArgoCDInstall(t, root, []string{"values.yaml"})
	writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), `configs:
  cm:
    create: false
  params:
    reposerver.max.combined.directory.manifests.size: 50M
`)
	writeTestFile(t, filepath.Join(root, "apps", "argocd-b.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd-b
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: argocd-b
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "argocd-b", "kustomization.yaml"), strings.Replace(kustomizeArgoCDInstallYAML("", []string{"values.yaml"}), "releaseName: argocd", "releaseName: argocd-b", 1))
	writeTestFile(t, filepath.Join(root, "argocd-b", "values.yaml"), `configs:
  cm:
    application.resourceTrackingMethod: annotation
  params:
    create: false
`)
	writeSpokeClusterChart(t, root)

	result := listArgoCDInstallSettings(t, root, argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	if got := result.Settings.MaxCombinedDirectoryManifestsSize; got.Value != "" {
		t.Errorf("MaxCombinedDirectoryManifestsSize = %q from %q, want no value from the install on another cluster", got.Value, got.Provenance.Path)
	}
	if got := result.Settings.TrackingMethod; got.Value != "annotation" || !strings.HasPrefix(got.Provenance.Path, "rendered/argocd/argocd-b/") {
		t.Errorf("TrackingMethod = %q from %q, want annotation from the install that still counts", got.Value, got.Provenance.Path)
	}
}

// The install reads a committed argocd-cm as a Kustomize resource, so its
// render replaces that file as a settings source. When the install stops
// counting, the committed file comes back: the settings are those of a
// run in which the install never counted, with the cluster Secret
// committed.
func TestSettingsRestoreCommittedFilesOfAnInstallThatStopsCounting(t *testing.T) {
	write := func(t *testing.T, committedSecret bool) string {
		t.Helper()
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: argocd
  destination:
    name: in-cluster
    namespace: argocd
`)
		writeTestFile(t, filepath.Join(root, "argocd", "kustomization.yaml"), kustomizeArgoCDInstallYAML("resources:\n  - argocd-cm.yaml\n", []string{"values.yaml"}))
		writeTestFile(t, filepath.Join(root, "argocd", "values.yaml"), "configs:\n  cm:\n    create: false\n  params:\n    reposerver.max.combined.directory.manifests.size: 50M\n")
		writeTestFile(t, filepath.Join(root, "argocd", "argocd-cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\ndata:\n  application.resourceTrackingMethod: annotation\n")
		writeDiffApplication(t, root, "other", "other", "same")
		if committedSecret {
			writeTestFile(t, filepath.Join(root, "clusters", "spoke.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: spoke
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: in-cluster
  server: https://spoke.example.com
`)
		} else {
			writeSpokeClusterChart(t, root)
		}
		return root
	}
	committed := listArgoCDInstallSettings(t, write(t, true), argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	rendered := listArgoCDInstallSettings(t, write(t, false), argoCDInstallShape{}, fakeArgoCDChartAcquirer(t))
	// The two runs name the same file, relative or under the root.
	if got, want := rendered.Settings.TrackingMethod, committed.Settings.TrackingMethod; got.Value != want.Value || !strings.HasSuffix(filepath.ToSlash(got.Provenance.Path), "argocd/argocd-cm.yaml") || !strings.HasSuffix(filepath.ToSlash(want.Provenance.Path), "argocd/argocd-cm.yaml") {
		t.Errorf("TrackingMethod = %q from %q, want %q from %q as without the install", got.Value, got.Provenance.Path, want.Value, want.Provenance.Path)
	}
	if got := committed.Settings.TrackingMethod.Value; got != "annotation" {
		t.Errorf("TrackingMethod without the install = %q, want the committed argocd-cm", got)
	}
	if got := rendered.Settings.MaxCombinedDirectoryManifestsSize.Value; got != "" {
		t.Errorf("MaxCombinedDirectoryManifestsSize = %q, want no value from the install on another cluster", got)
	}
}
