package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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

	result := runCLI(t, "diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false")
	assertStdoutContainsAll(t, result, "Application: argocd/demo", "-  value: old", "+  value: new")
	assertStderrEmpty(t, result)
}

func TestDiffAppsRefOrigCLIComparesWorkingTreeAgainstRef(t *testing.T) {
	root := t.TempDir()
	repo, wt := initCLIGitRepo(t, root)
	writeSimpleAppForCLI(t, root, "baseline")
	commitCLIGitRepo(t, repo, wt, "baseline")
	writeSimpleAppForCLI(t, root, "working")

	result := runCLI(t, "diff", "apps", "--path", root, "--ref-orig", "HEAD", "--exit-code=false")
	assertStdoutContainsAll(t, result, "-  value: baseline", "+  value: working")
	assertStderrEmpty(t, result)
}

func TestDiffAppsRefCLIComparesTwoRefs(t *testing.T) {
	root := t.TempDir()
	repo, wt := initCLIGitRepo(t, root)
	writeSimpleAppForCLI(t, root, "baseline")
	commitCLIGitRepo(t, repo, wt, "baseline")
	checkoutCLIGitBranch(t, wt, "feature")
	writeSimpleAppForCLI(t, root, "feature")
	commitCLIGitRepo(t, repo, wt, "feature")
	writeSimpleAppForCLI(t, root, "uncommitted")

	result := runCLI(t, "diff", "apps", "--repo", root, "--ref-orig", "master", "--ref", "feature", "--exit-code=false")
	assertStdoutContainsAll(t, result, "-  value: baseline", "+  value: feature")
	assertStdoutExcludesAll(t, result, "uncommitted")
	assertStderrEmpty(t, result)
}

func TestDiffAppPrintsOnlyNamedApplicationDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeSimpleAppForCLI(t, left, "old")
	writeNamedCLIApplication(t, left, "other", "other", "same")
	writeSimpleAppForCLI(t, right, "new")
	writeNamedCLIApplication(t, right, "other", "other", "changed-but-skipped")

	result := runCLI(t, "diff", "app", "demo", "--path-orig", left, "--path", right, "--exit-code=false")
	assertStdoutContainsAll(t, result, "Application: argocd/demo", "-  value: old", "+  value: new")
	assertStdoutExcludesAll(t, result, "other", "changed-but-skipped")
	assertStderrEmpty(t, result)
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

func TestDiffAppsGlobalCustomizationSuppressesOnlyJSONPointerDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppForCLI(t, left, 1)
	writeDeploymentAppForCLI(t, right, 2)
	writeGlobalCustomizationForCLI(t, left)
	writeGlobalCustomizationForCLI(t, right)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right})
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

func TestDiffAppsGlobalJQCustomizationSuppressesOnlyDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeDeploymentAppForCLI(t, left, 1)
	writeDeploymentAppForCLI(t, right, 2)
	writeGlobalJQCustomizationForCLI(t, left)
	writeGlobalJQCustomizationForCLI(t, right)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right})
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

func TestDiffAppsCompareOptionsNonePrintsStatusDiff(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeStatusAppForCLI(t, left, "old")
	writeStatusAppForCLI(t, right, "new")
	writeCompareOptionsForCLI(t, left, "none", false)
	writeCompareOptionsForCLI(t, right, "none", false)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diff", "apps", "--path-orig", left, "--path", right, "--exit-code=false"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{"ConfigMap: default/status", "-  value: old", "+  value: new"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
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

func TestDiffAppsFailsClosedForPluginSource(t *testing.T) {
	for _, shape := range []string{"directory", "kustomize", "helm"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writePluginCLIApplication(t, left, "cue", shape)
			writePluginCLIApplication(t, right, "cue", shape)

			cmd := NewRootCommand(VersionInfo{})
			cmd.SetArgs([]string{
				"diff", "apps",
				"--path-orig", left,
				"--path", right,
				"--changed-only=false",
			})
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			err := cmd.Execute()
			if code := commandErrorCode(err); code != 2 {
				t.Fatalf("error code = %d, want 2; err = %v", code, err)
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q, want empty diff output", stdout.String())
			}
			for _, want := range []string{
				"error plugin:",
				"config management plugin cue is not supported without an injected plugin renderer",
			} {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("stderr = %q, want %q", stderr.String(), want)
				}
			}
			for _, unwanted := range []string{"should-not-render", "kind: ConfigMap", "kind: Deployment"} {
				if strings.Contains(stdout.String(), unwanted) {
					t.Fatalf("stdout rendered plugin source through %s fallback:\n%s", shape, stdout.String())
				}
			}
		})
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

func TestDiffRefFlagsAreOnlyRegisteredOnDiffCommands(t *testing.T) {
	commands := map[string][]string{
		"diff apps":   {"diff", "apps", "--path", "missing"},
		"diff app":    {"diff", "app", "demo", "--path", "missing"},
		"diff images": {"diff", "images", "--path", "missing"},
	}
	flags := []struct {
		name  string
		value string
	}{
		{name: "--repo", value: "."},
		{name: "--ref", value: "feature"},
		{name: "--ref-orig", value: "main"},
	}
	for commandName, commandArgs := range commands {
		for _, flag := range flags {
			t.Run(commandName+" "+flag.name, func(t *testing.T) {
				cmd := NewRootCommand(VersionInfo{})
				args := append(append([]string(nil), commandArgs...), flag.name, flag.value)
				cmd.SetArgs(args)
				err := cmd.Execute()
				if err == nil {
					t.Fatal("Execute() error = nil, want runtime error after parsing")
				}
				if strings.Contains(err.Error(), "unknown flag") {
					t.Fatalf("Execute() error = %v, want %s registered", err, flag.name)
				}
			})
		}
	}
}

func TestDiffRefFlagsAreRejectedOutsideDiffCommands(t *testing.T) {
	commands := map[string][]string{
		"build apps":   {"build", "apps"},
		"build app":    {"build", "app", "demo"},
		"test apps":    {"test", "apps"},
		"test app":     {"test", "app", "demo"},
		"get apps":     {"get", "apps"},
		"get images":   {"get", "images"},
		"diag":         {"diag"},
		"cache path":   {"cache", "path"},
		"cache list":   {"cache", "list"},
		"cache prune":  {"cache", "prune"},
		"cache delete": {"cache", "delete", "--key", "git/demo"},
	}
	flags := []struct {
		name  string
		value string
	}{
		{name: "--repo", value: "."},
		{name: "--ref", value: "main"},
		{name: "--ref-orig", value: "main"},
	}
	for commandName, commandArgs := range commands {
		for _, flag := range flags {
			t.Run(commandName+" "+flag.name, func(t *testing.T) {
				cmd := NewRootCommand(VersionInfo{})
				args := append(append([]string(nil), commandArgs...), flag.name, flag.value)
				cmd.SetArgs(args)
				err := cmd.Execute()
				if err == nil {
					t.Fatal("Execute() error = nil, want unknown flag error")
				}
				if !strings.Contains(err.Error(), "unknown flag: "+flag.name) {
					t.Fatalf("Execute() error = %v, want %s rejected outside diff commands", err, flag.name)
				}
			})
		}
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

func initCLIGitRepo(t *testing.T, root string) (*git.Repository, *git.Worktree) {
	t.Helper()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	return repo, wt
}

func commitCLIGitRepo(t *testing.T, repo *git.Repository, wt *git.Worktree, message string) plumbing.Hash {
	t.Helper()
	if _, err := wt.Add("."); err != nil {
		t.Fatalf("Add(.) error = %v", err)
	}
	hash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Commit(%s) error = %v", message, err)
	}
	return hash
}

func checkoutCLIGitBranch(t *testing.T, wt *git.Worktree, name string) {
	t.Helper()
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	}); err != nil {
		t.Fatalf("Checkout(create %s) error = %v", name, err)
	}
}

func writePluginCLIApplication(t *testing.T, root, pluginName, shape string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plugin-app
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/plugin
    targetRevision: main
    plugin:
      name: `+pluginName+`
  destination:
    name: in-cluster
    namespace: default
`)
	switch shape {
	case "directory":
		writeCLITestFile(t, filepath.Join(root, "manifests", "plugin", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
data:
  value: directory
`)
	case "kustomize":
		writeCLITestFile(t, filepath.Join(root, "manifests", "plugin", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - configmap.yaml
`)
		writeCLITestFile(t, filepath.Join(root, "manifests", "plugin", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
data:
  value: kustomize
`)
	case "helm":
		writeCLITestFile(t, filepath.Join(root, "manifests", "plugin", "Chart.yaml"), `apiVersion: v2
name: should-not-render
version: 0.1.0
`)
		writeCLITestFile(t, filepath.Join(root, "manifests", "plugin", "templates", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: should-not-render
spec:
  selector:
    matchLabels:
      app: should-not-render
  template:
    metadata:
      labels:
        app: should-not-render
    spec:
      containers:
        - name: should-not-render
          image: example/should-not-render:v1
`)
	default:
		t.Fatalf("unknown plugin source shape %q", shape)
	}
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

func writeDeploymentAppForCLI(t *testing.T, root string, replicas int) {
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
  replicas: `+strconv.Itoa(replicas)+`
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
          image: example/app:v1
`)
}

func writeGlobalCustomizationForCLI(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      ignoreDifferences: |
        jsonPointers:
          - /spec/replicas
`)
}

func writeGlobalJQCustomizationForCLI(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      ignoreDifferences: |
        jqPathExpressions:
          - .spec.replicas
`)
}

func writeStatusAppForCLI(t *testing.T, root, statusValue string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "status.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: status
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/status
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "status", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: status
status:
  value: `+statusValue+`
`)
}

func writeCompareOptionsForCLI(t *testing.T, root, statusMode string, ignoreAggregatedRoles bool) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.compareoptions: |
    ignoreResourceStatusField: `+statusMode+`
    ignoreAggregatedRoles: `+strconv.FormatBool(ignoreAggregatedRoles)+`
`)
}
