package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
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

func TestChartCacheFlagsAreRegistered(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "build apps",
			args: []string{"build", "apps", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--path", "missing"},
		},
		{
			name: "diff apps",
			args: []string{"diff", "apps", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--path", "missing", "--path-orig", "base"},
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
				t.Fatalf("Execute() error = %v, want registered chart cache flags", err)
			}
		})
	}
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
