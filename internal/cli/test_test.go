package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"go.yaml.in/yaml/v4"
)

func TestTestAppsPassesAllApplications(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--offline"})
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

func TestTestAppsReportsNoApplicationsInTextOutput(t *testing.T) {
	root := t.TempDir()

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := stdout.String(), "No Applications discovered.\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestTestAppsReportsEmptyJSONArrayWhenNoApplicationsDiscovered(t *testing.T) {
	root := t.TempDir()

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "-o", "json"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := strings.TrimSpace(stdout.String()), "[]"; got != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestTestAppsReportsFailures(t *testing.T) {
	root := t.TempDir()
	writeFailingCLIApplication(t, root, "broken")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--offline"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	for _, want := range []string{"FAIL argocd/broken", "offline cache miss"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String(), "kind: ConfigMap") {
		t.Fatalf("stdout contains manifest body:\n%s", stdout.String())
	}
}

func TestTestAppsReportsPluginSourceFailure(t *testing.T) {
	root := t.TempDir()
	writePluginCLIApplication(t, root, "cue", "directory")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	for _, want := range []string{
		"FAIL argocd/plugin-app",
		"config management plugin cue is disabled in the default renderer",
		"trusted policy",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
	if !strings.Contains(stderr.String(), "error plugin:") {
		t.Fatalf("stderr = %q, want plugin diagnostic", stderr.String())
	}
}

func TestTestAppReportsPluginSourceFailure(t *testing.T) {
	root := t.TempDir()
	writePluginCLIApplication(t, root, "cue", "directory")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "app", "plugin-app", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	for _, want := range []string{
		"FAIL argocd/plugin-app",
		"config management plugin cue is disabled in the default renderer",
		"trusted policy",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
	if !strings.Contains(stderr.String(), "error plugin:") {
		t.Fatalf("stderr = %q, want plugin diagnostic", stderr.String())
	}
}

func TestTestAppsFailsOnLuaHealthRuntimeErrorByDefault(t *testing.T) {
	root := t.TempDir()
	writeLuaHealthFailureCLIApplication(t, root, "widget")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--offline"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if !strings.Contains(stdout.String(), "FAIL argocd/widget") {
		t.Fatalf("stdout = %q, want failed widget status", stdout.String())
	}
	if !strings.Contains(stderr.String(), "health Lua failed") {
		t.Fatalf("stderr = %q, want health Lua diagnostic", stderr.String())
	}
}

func TestTestAppsSkipLuaHealthPassesRuntimeErrorRepo(t *testing.T) {
	root := t.TempDir()
	writeLuaHealthFailureCLIApplication(t, root, "widget")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--offline", "--skip-lua-health"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "PASS argocd/widget\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "health Lua failed") {
		t.Fatalf("stderr = %q, want no health Lua diagnostic", stderr.String())
	}
}

func TestTestAppFailsOnLuaHealthRuntimeErrorByDefault(t *testing.T) {
	root := t.TempDir()
	writeLuaHealthFailureCLIApplication(t, root, "widget")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "app", "widget", "--path", root, "--offline"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if !strings.Contains(stdout.String(), "FAIL argocd/widget") {
		t.Fatalf("stdout = %q, want failed widget status", stdout.String())
	}
	if !strings.Contains(stderr.String(), "health Lua failed") {
		t.Fatalf("stderr = %q, want health Lua diagnostic", stderr.String())
	}
}

func TestTestAppSkipLuaHealthPassesRuntimeErrorRepo(t *testing.T) {
	root := t.TempDir()
	writeLuaHealthFailureCLIApplication(t, root, "widget")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "app", "widget", "--path", root, "--offline", "--skip-lua-health"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "PASS argocd/widget\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "health Lua failed") {
		t.Fatalf("stderr = %q, want no health Lua diagnostic", stderr.String())
	}
}

func TestSkipLuaHealthFlagIsNotGlobal(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--skip-lua-health"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --skip-lua-health") {
		t.Fatalf("Execute() error = %v, want unknown --skip-lua-health flag", err)
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

func TestAllowNetworkFlagRemoved(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--allow-network"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --allow-network") {
		t.Fatalf("Execute() error = %v, want unknown --allow-network flag", err)
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

func TestTestAppTTYColorsTextStatus(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{},
		IsTerminal: func(w io.Writer) bool {
			return w == &stdout
		},
	})
	cmd.SetArgs([]string{"test", "app", "demo", "--path", root, "--offline"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "\x1b[32mPASS\x1b[0m argocd/demo\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
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

func TestTestAppsJSONOutputSuppressesLuaHealthPrints(t *testing.T) {
	root := t.TempDir()
	writeLuaHealthSecretPrintCLIApplication(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--offline", "-o", "json"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	processStdout, executeErr := captureProcessStdoutDuring(t, cmd.Execute)
	if executeErr != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s\nprocess stdout:\n%s", executeErr, stdout.String(), stderr.String(), processStdout)
	}

	if processStdout != "" {
		t.Fatalf("process stdout = %q, want empty", processStdout)
	}
	var statuses []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &statuses); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s\nprocess stdout:\n%s", err, stdout.String(), processStdout)
	}
	assertStatusOutputContains(t, statuses, "secret-health", "PASS")
	for name, got := range map[string]string{
		"stdout":         stdout.String(),
		"stderr":         stderr.String(),
		"process stdout": processStdout,
	} {
		if strings.Contains(got, "super-secret") {
			t.Fatalf("%s leaked Secret value: %q", name, got)
		}
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

func TestTestAppsTTYStreamsColoredStatusesProgressAndSummary(t *testing.T) {
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses: []app.ApplicationStatus{
				{Namespace: "argocd", Name: "slow", Status: app.ApplicationStatusPass},
				{Namespace: "argocd", Name: "fast", Status: app.ApplicationStatusFail, Message: "boom"},
			},
		},
		buildHook: func(request app.BuildRequest) error {
			if err := request.StatusCallback(app.ApplicationStatusEvent{
				Status:    app.ApplicationStatus{Namespace: "argocd", Name: "fast", Status: app.ApplicationStatusFail, Message: "boom"},
				Completed: 1,
				Total:     2,
			}); err != nil {
				return err
			}
			if err := request.StatusCallback(app.ApplicationStatusEvent{
				Status:    app.ApplicationStatus{Namespace: "argocd", Name: "slow", Status: app.ApplicationStatusPass},
				Completed: 2,
				Total:     2,
			}); err != nil {
				return err
			}
			return nil
		},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: recorder,
		IsTerminal: func(io.Writer) bool {
			return true
		},
	})
	cmd.SetArgs([]string{"test", "apps"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	out := stdout.String()
	if !strings.Contains(out, "\x1b[31mFAIL\x1b[0m argocd/fast boom\n") {
		t.Fatalf("stdout = %q, want colored FAIL line", out)
	}
	if !strings.Contains(out, "\x1b[32mPASS\x1b[0m argocd/slow\n") {
		t.Fatalf("stdout = %q, want colored PASS line", out)
	}
	if strings.Index(out, "fast") > strings.Index(out, "slow") {
		t.Fatalf("stdout = %q, want completion order", out)
	}
	if !strings.Contains(out, "2 applications: 1 passed, 1 failed, 0 skipped in ") {
		t.Fatalf("stdout = %q, want summary", out)
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "\rTesting apps 1/2") || !strings.Contains(errOut, "\rTesting apps 2/2") {
		t.Fatalf("stderr = %q, want in-place progress", errOut)
	}
	if strings.Contains(errOut, "\n") {
		t.Fatalf("stderr = %q, progress must not add one line per app", errOut)
	}
	if strings.Count(out, "\n") != 3 {
		t.Fatalf("stdout = %q, want two status lines plus summary", out)
	}
}

func TestTestAppsTTYSuppressesProgressWhenStderrIsNotTerminal(t *testing.T) {
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses: []app.ApplicationStatus{{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass}},
		},
		buildHook: func(request app.BuildRequest) error {
			return request.StatusCallback(app.ApplicationStatusEvent{
				Status:    app.ApplicationStatus{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass},
				Completed: 1,
				Total:     1,
			})
		},
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: recorder,
		IsTerminal: func(w io.Writer) bool {
			return w == &stdout
		},
	})
	cmd.SetArgs([]string{"test", "apps"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "\x1b[32mPASS\x1b[0m argocd/demo\n") {
		t.Fatalf("stdout = %q, want live colored status", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want no progress control bytes when stderr is not a terminal", stderr.String())
	}
}

func TestTestAppsNonTTYKeepsBufferedPlainOutput(t *testing.T) {
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses: []app.ApplicationStatus{
				{Namespace: "argocd", Name: "slow", Status: app.ApplicationStatusPass},
				{Namespace: "argocd", Name: "fast", Status: app.ApplicationStatusPass},
			},
		},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: recorder,
		IsTerminal: func(io.Writer) bool {
			return false
		},
	})
	cmd.SetArgs([]string{"test", "apps"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := stdout.String(), "PASS argocd/slow\nPASS argocd/fast\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if recorder.buildRequests[0].StatusCallback != nil {
		t.Fatalf("StatusCallback configured for non-TTY output")
	}
}

func TestTestAppsStructuredOutputDoesNotConfigureLiveReporter(t *testing.T) {
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses: []app.ApplicationStatus{{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass}},
		},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: recorder,
		IsTerminal: func(io.Writer) bool {
			return true
		},
	})
	cmd.SetArgs([]string{"test", "apps", "-o", "json"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var statuses []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &statuses); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if recorder.buildRequests[0].StatusCallback != nil {
		t.Fatalf("StatusCallback configured for structured output")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestTestAppsYAMLOutputDoesNotConfigureLiveReporter(t *testing.T) {
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses: []app.ApplicationStatus{{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass}},
		},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: recorder,
		IsTerminal: func(io.Writer) bool {
			return true
		},
	})
	cmd.SetArgs([]string{"test", "apps", "-o", "yaml"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var statuses []map[string]any
	if err := yaml.Unmarshal(stdout.Bytes(), &statuses); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if recorder.buildRequests[0].StatusCallback != nil {
		t.Fatalf("StatusCallback configured for YAML output")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestTestAppsTTYSetsLiveOutputWriteErrorApartFromTestFailure(t *testing.T) {
	writeErr := errors.New("stdout closed")
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses: []app.ApplicationStatus{{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass}},
		},
		buildHook: func(request app.BuildRequest) error {
			return request.StatusCallback(app.ApplicationStatusEvent{
				Status:    app.ApplicationStatus{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass},
				Completed: 1,
				Total:     1,
			})
		},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: recorder,
		IsTerminal: func(io.Writer) bool {
			return true
		},
	})
	cmd.SetArgs([]string{"test", "apps"})
	cmd.SetOut(failingWriter{err: writeErr})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil || !errors.Is(err, writeErr) {
		t.Fatalf("Execute() error = %v, want write error", err)
	}
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("Execute() error = %v, must not be converted to ExitError", err)
	}
}

func TestTestAppsTTYSkippedPreconditionStatusesAreColoredAndSummarized(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{},
		IsTerminal: func(io.Writer) bool {
			return true
		},
	})
	cmd.SetArgs([]string{"test", "apps", "--path", root, "--strict"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	out := stdout.String()
	if !strings.Contains(out, "\x1b[33mSKIPPED\x1b[0m argocd/demo") {
		t.Fatalf("stdout = %q, want colored SKIPPED", out)
	}
	if !strings.Contains(out, "1 applications: 0 passed, 0 failed, 1 skipped in ") {
		t.Fatalf("stdout = %q, want summary", out)
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func captureProcessStdoutDuring(t *testing.T, run func() error) (string, error) {
	t.Helper()
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	readDone := make(chan struct {
		output string
		err    error
	}, 1)
	go func() {
		var buffer bytes.Buffer
		_, copyErr := io.Copy(&buffer, reader)
		readDone <- struct {
			output string
			err    error
		}{output: buffer.String(), err: copyErr}
	}()

	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()
	runErr := run()
	if err := writer.Close(); err != nil {
		t.Fatalf("stdout pipe Close() error = %v", err)
	}
	result := <-readDone
	if err := reader.Close(); err != nil {
		t.Fatalf("stdout pipe reader Close() error = %v", err)
	}
	if result.err != nil {
		t.Fatalf("reading process stdout error = %v", result.err)
	}
	return result.output, runErr
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

func writeLuaHealthSecretPrintCLIApplication(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", "secret-health.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: secret-health
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/secret-health
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "secret-health", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: secret-health
type: Opaque
data:
  token: super-secret
`)
	writeCLITestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.health.Secret: |
    print(obj.data.token)
    hs = {}
    hs.status = "Healthy"
    hs.message = "ok"
    return hs
`)
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

func writeLuaHealthFailureCLIApplication(t *testing.T, root, appName string) {
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
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+appName+`
data:
  value: `+appName+`
`)
	writeCLITestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.health.ConfigMap: |
    error("boom")
`)
}
