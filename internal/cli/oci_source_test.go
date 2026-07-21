package cli

import (
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
)

// writeOCIAppForCLI writes an Application sourcing an OCI artifact from the
// hermetic registry.
func writeOCIAppForCLI(t *testing.T, root, appName, repoURL, sourcePath, revision string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: "`+revision+`"
    path: `+sourcePath+`
  destination:
    name: in-cluster
    namespace: default
`)
}

func pushCLIChartArtifact(t *testing.T, reg *ocitest.Registry, repoName, tag, marker string) {
	t.Helper()
	ocitest.PushPlainManifestsArtifact(t, reg, repoName, tag, map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + marker + "\ndata: {}\n",
	})
}

// CLI integration against the hermetic registry through the REAL acquirer
// wrapper (no injected fakes): the nil-EventHandlers panic class would
// surface on any of these paths.
func TestCLIGetTestBuildOCISource(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "v1", "cli-artifact-content")
	root := t.TempDir()
	writeOCIAppForCLI(t, root, "demo", reg.RepoURL("manifests/app"), ".", "v1")
	ociCacheDir := t.TempDir()

	getResult := runCLI(t, "get", "apps", "--path", root, "--oci-cache-dir", ociCacheDir)
	assertStdoutContainsAll(t, getResult, "demo")

	testResult := runCLI(t, "test", "apps", "--path", root, "--oci-cache-dir", ociCacheDir)
	assertStdoutContainsAll(t, testResult, "demo", "PASS")

	buildResult := runCLI(t, "build", "apps", "--path", root, "--oci-cache-dir", ociCacheDir)
	assertStdoutContainsAll(t, buildResult, "cli-artifact-content")
}

func TestCLIDiffOCISourceBothSidesAndOneSided(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushCLIChartArtifact(t, reg, "manifests/app", "v1", "artifact-v1")
	pushCLIChartArtifact(t, reg, "manifests/app", "v2", "artifact-v2")
	repoURL := reg.RepoURL("manifests/app")
	ociCacheDir := t.TempDir()

	// Both sides at different tags: the diff reflects artifact content change.
	left := t.TempDir()
	right := t.TempDir()
	writeOCIAppForCLI(t, left, "demo", repoURL, ".", "v1")
	writeOCIAppForCLI(t, right, "demo", repoURL, ".", "v2")
	diffResult := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--oci-cache-dir", ociCacheDir, "--exit-code=false")
	assertStdoutContainsAll(t, diffResult, "artifact-v1", "artifact-v2")

	// One-sided: the app exists only on the right side (added).
	emptyLeft := t.TempDir()
	writeCLITestFile(t, filepath.Join(emptyLeft, ".keep"), "")
	oneSided := runCLI(t, "diff", "apps", "--path-orig", emptyLeft, "--path", right, "--oci-cache-dir", ociCacheDir, "--exit-code=false")
	assertStdoutContainsAll(t, oneSided, "artifact-v2")
}
