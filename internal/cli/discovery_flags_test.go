package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

const cliDiscoverIgnoreHint = "(use --discover-ignore to exclude non-deployable manifests from discovery)"

// Scaffolding template that passes discovery's content sniff but fails typed
// decoding: requeueAfterSeconds is a string placeholder where int64 is expected.
func cliUndecodableScaffold(placeholder string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: scaffold
spec:
  generators:
    - pullRequest:
        requeueAfterSeconds: ` + placeholder + `
`
}

func executeCLIExpectingError(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs(args)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("Execute() error = nil, want error\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String(), err
}

func TestTestAppsDiscoverIgnoreExcludesUndecodableCandidates(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "stable")
	writeCLITestFile(t, filepath.Join(root, "templates", "scaffold.yaml"), cliUndecodableScaffold("$PR_REQUEUE"))

	_, _, err := executeCLIExpectingError(t, "test", "apps", "--path", root, "--offline")
	if !strings.Contains(err.Error(), cliDiscoverIgnoreHint) {
		t.Fatalf("Execute() error = %v, want remediation hint %q", err, cliDiscoverIgnoreHint)
	}

	result := runCLI(t, "test", "apps", "--path", root, "--offline", "--discover-ignore", "templates/**")
	assertStdoutContainsAll(t, result, "PASS argocd/demo")
}

func TestDiffAppsDiscoverIgnoreExcludesUndecodableCandidates(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")
	writeCLITestFile(t, filepath.Join(left, "templates", "scaffold.yaml"), cliUndecodableScaffold("$PR_REQUEUE"))
	writeCLITestFile(t, filepath.Join(right, "templates", "scaffold.yaml"), cliUndecodableScaffold("$PR_REQUEUE"))

	_, _, err := executeCLIExpectingError(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false")
	if !strings.Contains(err.Error(), cliDiscoverIgnoreHint) {
		t.Fatalf("Execute() error = %v, want remediation hint %q", err, cliDiscoverIgnoreHint)
	}

	result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false", "--discover-ignore", "templates/**")
	assertStdoutContainsAll(t, result, "Application: argocd/demo", "-  value: old", "+  value: new")
}

func TestDiffAppsRefDiffPropagatesDiscoverIgnoreToBothSides(t *testing.T) {
	root := t.TempDir()
	repo, wt := initCLIGitRepo(t, root)
	writeSimpleAppForCLI(t, root, "baseline")
	// The undecodable candidate is committed on BOTH refs so a one-side
	// propagation drop of the ignore globs fails the command.
	writeCLITestFile(t, filepath.Join(root, "templates", "scaffold.yaml"), cliUndecodableScaffold("$PR_REQUEUE"))
	commitCLIGitRepo(t, repo, wt, "baseline")
	checkoutCLIGitBranch(t, wt, "feature")
	writeSimpleAppForCLI(t, root, "feature")
	commitCLIGitRepo(t, repo, wt, "feature")

	_, _, err := executeCLIExpectingError(t, "diff", "apps", "--repo", root, "--ref-orig", "master", "--ref", "feature", "--exit-code=false")
	if !strings.Contains(err.Error(), cliDiscoverIgnoreHint) {
		t.Fatalf("Execute() error = %v, want remediation hint %q", err, cliDiscoverIgnoreHint)
	}

	result := runCLI(t, "diff", "apps", "--repo", root, "--ref-orig", "master", "--ref", "feature", "--exit-code=false", "--discover-ignore", "templates/**")
	assertStdoutContainsAll(t, result, "-  value: baseline", "+  value: feature")
}

func TestDiffAppsDiscoverIgnoreKeepsStrictChangedOnlyIndependent(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")
	writeCLITestFile(t, filepath.Join(left, "templates", "scaffold.yaml"), cliUndecodableScaffold("$PR_REQUEUE"))
	writeCLITestFile(t, filepath.Join(right, "templates", "scaffold.yaml"), cliUndecodableScaffold("$PR_REQUEUE_CHANGED"))

	// A discover-ignored file that changes still surfaces as an unowned changed
	// path under --strict-changed-only; the flag families are independent.
	_, stderr, _ := executeCLIExpectingError(t, "diff", "apps", "--path-orig", left, "--path", right,
		"--strict-changed-only", "--discover-ignore", "templates/**")
	for _, want := range []string{"error changed-only:", "templates/scaffold.yaml"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}

	result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false",
		"--strict-changed-only", "--discover-ignore", "templates/**", "--changed-only-ignore", "templates/**")
	assertStdoutContainsAll(t, result, "Application: argocd/demo", "-  value: old", "+  value: new")
}

func TestMaxDiscoveryDepthFlagDistinguishesDefaultFromExplicitZero(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "get", "apps")
	if len(recorder.listRequests) != 1 {
		t.Fatalf("len(listRequests) = %d, want 1", len(recorder.listRequests))
	}
	if request := recorder.listRequests[0]; request.MaxDiscoveryDepth != 4 || request.MaxDiscoveryDepthSet {
		t.Fatalf("default max discovery depth = %d set=%t, want 4 set=false", request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
	}

	recorder = &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "get", "apps", "--max-discovery-depth", "0")
	if len(recorder.listRequests) != 1 {
		t.Fatalf("len(listRequests) = %d, want 1", len(recorder.listRequests))
	}
	if request := recorder.listRequests[0]; request.MaxDiscoveryDepth != 0 || !request.MaxDiscoveryDepthSet {
		t.Fatalf("explicit max discovery depth = %d set=%t, want 0 set=true", request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
	}
}
