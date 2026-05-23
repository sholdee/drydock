package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestTestAppsPassesAllApplications(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "PASS argocd/demo\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestTestAppsReportsFailures(t *testing.T) {
	root := t.TempDir()
	writeFailingCLIApplication(t, root, "broken")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	for _, want := range []string{"FAIL argocd/broken", "--repo-map"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String(), "kind: ConfigMap") {
		t.Fatalf("stdout contains manifest body:\n%s", stdout.String())
	}
}

func TestTestAppsReportsSkippedPreconditionFailures(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--strict"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	for _, want := range []string{"SKIPPED argocd/demo", "unsupported ApplicationSet generator"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTestAppsReportsSkippedNetworkPreconditionFailures(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--offline", "--allow-network"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	for _, want := range []string{"SKIPPED argocd/demo", "--offline cannot be combined with --allow-network"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTestAppReportsSkippedNetworkPreconditionFailures(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "app", "demo", "--path", root, "--offline", "--allow-network"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	for _, want := range []string{"SKIPPED argocd/demo", "--offline cannot be combined with --allow-network"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTestAppMissingReturnsOriginalError(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "app", "missing", "--path", root})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want missing app error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found`) {
		t.Fatalf("error = %v, want missing app error", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestTestAppScopesToNamedApplication(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeFailingCLIApplication(t, root, "broken")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "app", "demo", "--path", root})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "PASS argocd/demo\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stdout.String(), "broken") {
		t.Fatalf("stdout included non-selected app:\n%s", stdout.String())
	}
}

func TestTestAppsJSONOutputContainsStatusesOnly(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeFailingCLIApplication(t, root, "broken")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "-o", "json"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	var statuses []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &statuses); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	assertStatusOutputContains(t, statuses, "demo", "PASS")
	assertStatusOutputContains(t, statuses, "broken", "FAIL")
	if strings.Contains(stdout.String(), "apiVersion") || strings.Contains(stdout.String(), "kind:") {
		t.Fatalf("json output contains manifest-like body:\n%s", stdout.String())
	}
}

func TestTestAppsYAMLOutputContainsSkippedStatusesOnly(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--strict", "-o", "yaml"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	var statuses []map[string]any
	if err := yaml.Unmarshal(stdout.Bytes(), &statuses); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	assertStatusOutputContains(t, statuses, "demo", "SKIPPED")
	if strings.Contains(stdout.String(), "apiVersion") {
		t.Fatalf("yaml output contains manifest-like body:\n%s", stdout.String())
	}
}

func assertStatusOutputContains(t *testing.T, statuses []map[string]any, name, status string) {
	t.Helper()
	for _, got := range statuses {
		if got["name"] == name && got["status"] == status {
			return
		}
	}
	t.Fatalf("statuses = %#v, want %s %s", statuses, name, status)
}

func writeFailingCLIApplication(t *testing.T, root, appName string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/missing
    path: manifests/missing
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
}
