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
