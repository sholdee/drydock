package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

type assertErr struct{}

func (assertErr) Error() string {
	return "assert error"
}

func TestExitCodeForDiff(t *testing.T) {
	tests := []struct {
		name                string
		err                 error
		disableDiffExitCode bool
		hasDiff             bool
		want                int
	}{
		{
			name:    "diff found",
			hasDiff: true,
			want:    1,
		},
		{
			name: "no diff",
			want: 0,
		},
		{
			name: "error",
			err:  assertErr{},
			want: 2,
		},
		{
			name:                "diff exit code disabled",
			disableDiffExitCode: true,
			hasDiff:             true,
			want:                0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err, tt.disableDiffExitCode, tt.hasDiff); got != tt.want {
				t.Fatalf("exitCode(%v, %v, %v) = %d, want %d", tt.err, tt.disableDiffExitCode, tt.hasDiff, got, tt.want)
			}
		})
	}
}

func TestDiffAppsPrintsManifestDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, want := range []string{"Application: argocd/demo", "-  value: old", "+  value: new"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("diff output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDiffAppPrintsOnlyNamedApplicationDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeNamedCLIApplication(t, left, "other", "other", "same")
	writeSimpleAppForCLI(t, right, "new")
	writeNamedCLIApplication(t, right, "other", "other", "changed-but-skipped")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "app", "demo", "--path-orig", left, "--path", right, "--exit-code=false"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{"Application: argocd/demo", "-  value: old", "+  value: new"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "other") || strings.Contains(stdout.String(), "changed-but-skipped") {
		t.Fatalf("stdout included non-selected app:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffAppReportsMissingApplication(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "app", "missing", "--path-orig", left, "--path", right})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want missing app error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found in either tree`) {
		t.Fatalf("error = %v, want missing app message", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestDiffAppsStripAttrSuppressesOnlyAttributeDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeAttributedAppForCLI(t, left, "1.0.0", "same")
	writeAttributedAppForCLI(t, right, "2.0.0", "same")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "--strip-attr", "app.kubernetes.io/version"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no diff", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffAppsSkipKindSuppressesFilteredResourceDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "--skip-kind", "ConfigMap"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no diff", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffAppsJSONOutput(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "-o", "json", "--exit-code=false"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var results []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(results) != 1 || results[0]["change"] != "modified" {
		t.Fatalf("results = %#v, want one modified result", results)
	}
	diff, ok := results[0]["diff"].(string)
	if !ok {
		t.Fatalf("diff = %T, want string", results[0]["diff"])
	}
	if !strings.Contains(diff, "+  value: new") {
		t.Fatalf("json diff = %#v, want changed value", results[0]["diff"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffAppYAMLOutput(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "app", "demo", "--path-orig", left, "--path", right, "-o", "yaml", "--exit-code=false"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var results []map[string]any
	if err := yaml.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(results) != 1 || results[0]["change"] != "modified" {
		t.Fatalf("results = %#v, want one modified result", results)
	}
	diff, ok := results[0]["diff"].(string)
	if !ok {
		t.Fatalf("diff = %T, want string", results[0]["diff"])
	}
	if !strings.Contains(diff, "-  value: old") {
		t.Fatalf("yaml diff = %#v, want changed value", results[0]["diff"])
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffManifestCommandsRejectNameOutput(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "apps",
			args: []string{"diff", "apps", "--path-orig", left, "--path", right, "-o", "name"},
			want: "name output is not supported for diff apps",
		},
		{
			name: "app",
			args: []string{"diff", "app", "demo", "--path-orig", left, "--path", right, "-o", "name"},
			want: "name output is not supported for diff app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewRootCommand(VersionInfo{})
			cmd.SetArgs(tt.args)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			err := cmd.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want unsupported output error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestDiffAppsStructuredOutputKeepsDiagnosticsOnStderr(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")
	writeCLITestFile(t, filepath.Join(left, "README.md"), "left\n")
	writeCLITestFile(t, filepath.Join(right, "README.md"), "right\n")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "-o", "json", "--exit-code=false"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var results []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	for _, want := range []string{"warning changed-only:", "README.md"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
}

func TestDiffImagesPrintsImageDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeImageAppForCLI(t, left, "example/app:v1")
	writeImageAppForCLI(t, right, "example/app:v2")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "images", "--path-orig", left, "--path", right, "--exit-code=false"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, want := range []string{"- example/app:v1", "+ example/app:v2"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffAppsPrintsDiagnosticsOnStrictChangedOnlyError(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeSimpleAppForCLI(t, right, "new")
	writeCLITestFile(t, filepath.Join(left, "README.md"), "left\n")
	writeCLITestFile(t, filepath.Join(right, "README.md"), "right\n")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "--strict-changed-only"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want strict changed-only error")
	}

	for _, want := range []string{"error changed-only:", "README.md"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty output on strict changed-only error", stdout.String())
	}
}

func TestDiffImagesPrintsDiagnosticsOnStrictChangedOnlyError(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeImageAppForCLI(t, left, "example/app:v1")
	writeImageAppForCLI(t, right, "example/app:v2")
	writeCLITestFile(t, filepath.Join(left, "README.md"), "left\n")
	writeCLITestFile(t, filepath.Join(right, "README.md"), "right\n")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "images", "--path-orig", left, "--path", right, "--strict-changed-only"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want strict changed-only error")
	}

	for _, want := range []string{"error changed-only:", "README.md"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty output on strict changed-only error", stdout.String())
	}
}

func TestNetworkCacheFlagsAreRegistered(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "build apps",
			args: []string{"build", "apps", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--refresh-remotes", "--remote-cache-dir", "/tmp/remotes", "--path", "missing"},
		},
		{
			name: "build app",
			args: []string{"build", "app", "demo", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--refresh-remotes", "--remote-cache-dir", "/tmp/remotes", "--path", "missing"},
		},
		{
			name: "diff apps",
			args: []string{"diff", "apps", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--refresh-remotes", "--remote-cache-dir", "/tmp/remotes", "--path", "missing", "--path-orig", "base"},
		},
		{
			name: "diff app",
			args: []string{"diff", "app", "demo", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--refresh-remotes", "--remote-cache-dir", "/tmp/remotes", "--path", "missing", "--path-orig", "base"},
		},
		{
			name: "diff images",
			args: []string{"diff", "images", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--refresh-remotes", "--remote-cache-dir", "/tmp/remotes", "--path", "missing", "--path-orig", "base"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewRootCommand(VersionInfo{})
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want runtime error after parsing")
			}
			if strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("Execute() error = %v, want registered network cache flags", err)
			}
		})
	}
}

func writeNamedCLIApplication(t *testing.T, root, appName, configMapName, value string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
data:
  value: `+value+`
`)
}

func writeSimpleAppForCLI(t *testing.T, root, value string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  value: `+value+`
`)
}

func writeAttributedAppForCLI(t *testing.T, root, version, value string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "demo", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
  annotations:
    app.kubernetes.io/version: `+version+`
data:
  value: `+value+`
`)
}

func writeImageAppForCLI(t *testing.T, root, image string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: demo
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: `+image+`
`)
}
