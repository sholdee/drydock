package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
)

type diagCommandParameterJSON struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	ValueRedacted  bool   `json:"valueRedacted"`
	Classification string `json:"classification"`
}

func TestDiagCleanRepositoryReportsNoDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := stdout.String(), "No diagnostics found.\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiagJSONCleanRepositoryUsesEmptyDiagnosticsArray(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"diagnostics": []`) {
		t.Fatalf("stdout = %q, want empty diagnostics array", stdout.String())
	}
}

func TestDiagStructuredProjectDiagnosticsModeFiltersStaticAndRenderDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		listResult app.BuildResult
		diagResult app.DiagResult
		want       string
		forbid     string
	}{
		{
			name: "default static json hides deferred project diagnostics",
			args: []string{"diag", "--path", "repo", "-o", "json"},
			listResult: app.BuildResult{Diagnostics: []diagnostic.Diagnostic{{
				Severity: diagnostic.SeverityWarning,
				Category: "project",
				Code:     diagnostic.CodeProjectResourceScopeDeferred,
				Message:  "Application argocd/demo rendered resource example.com/Widget custom has unknown scope offline; AppProject resource policy validation is deferred",
			}}},
			forbid: diagnostic.CodeProjectResourceScopeDeferred,
		},
		{
			name: "all static yaml keeps deferred project diagnostics",
			args: []string{"diag", "--path", "repo", "-o", "yaml", "--project-diagnostics", "all"},
			listResult: app.BuildResult{Diagnostics: []diagnostic.Diagnostic{{
				Severity: diagnostic.SeverityWarning,
				Category: "project",
				Code:     diagnostic.CodeProjectResourceScopeDeferred,
				Message:  "Application argocd/demo rendered resource example.com/Widget custom has unknown scope offline; AppProject resource policy validation is deferred",
			}}},
			want: diagnostic.CodeProjectResourceScopeDeferred,
		},
		{
			name: "off render json hides actionable project diagnostics",
			args: []string{"diag", "--path", "repo", "-o", "json", "--render", "--project-diagnostics", "off"},
			diagResult: app.DiagResult{Diagnostics: []diagnostic.Diagnostic{{
				Severity: diagnostic.SeverityWarning,
				Category: "project",
				Code:     diagnostic.CodeProjectSourceRepositoryDenied,
				Message:  "Application argocd/demo source repository is not permitted by AppProject \"platform\"",
			}}},
			forbid: diagnostic.CodeProjectSourceRepositoryDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := &recordingCLIOrchestrator{
				listResult: tt.listResult,
				diagResult: tt.diagResult,
			}
			cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: orchestrator})
			cmd.SetArgs(tt.args)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
			}
			if tt.want != "" && !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tt.want)
			}
			if tt.forbid != "" && strings.Contains(stdout.String(), tt.forbid) {
				t.Fatalf("stdout = %q, did not want %q", stdout.String(), tt.forbid)
			}
		})
	}
}

func TestDiagStructuredFatalErrorDoesNotWritePartialReport(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", missing, "-o", "json", "--settings"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want missing path error")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty on fatal prediagnostic error", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiagRejectsHomeDirectoryPathBeforeScanning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orchestrator := &recordingCLIOrchestrator{}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: orchestrator})
	cmd.SetArgs([]string{"diag", "--path", ".", "--settings", "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	t.Chdir(home)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want broad home directory path error")
	}
	if !strings.Contains(err.Error(), "refusing to recursively scan home directory") {
		t.Fatalf("Execute() error = %v, want home directory refusal", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty on broad path error", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if len(orchestrator.listRequests) != 0 || len(orchestrator.diagRequests) != 0 {
		t.Fatalf("orchestrator called: list=%d diag=%d", len(orchestrator.listRequests), len(orchestrator.diagRequests))
	}
}

func TestDiagPrintsUnsupportedApplicationSetWarning(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "warning appset: unsupported ApplicationSet generator; supported generators are git directories, git files, list, matrix, and merge (path: unsupported-appset.yaml, pointer: spec.generators)\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestDiagReportsPluginSourceFailure(t *testing.T) {
	root := t.TempDir()
	writePluginCLIApplication(t, root, "cue", "directory")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--render"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if code := commandErrorCode(err); code != 2 {
		t.Fatalf("error code = %d, want 2; err = %v", code, err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	for _, want := range []string{
		"error plugin:",
		"config management plugin cue is not supported by the default renderer",
		"no compatible native renderer",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}

func TestDiagDefaultDoesNotRenderApplications(t *testing.T) {
	root := t.TempDir()
	writePluginCLIApplication(t, root, "cue", "directory")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "No diagnostics found.\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRenderDiagnosticsWithColorColorsWarningAndErrorLabels(t *testing.T) {
	diags := []diagnostic.Diagnostic{
		{Severity: diagnostic.SeverityWarning, Category: "settings", Message: "metadata only"},
		{Severity: diagnostic.SeverityError, Category: "render", Message: "decode failed"},
	}
	var stderr bytes.Buffer

	if err := renderDiagnosticsWithColor(&stderr, diags, true); err != nil {
		t.Fatalf("renderDiagnosticsWithColor() error = %v", err)
	}

	want := "\x1b[33mwarning\x1b[0m settings: metadata only\n" +
		"\x1b[31merror\x1b[0m render: decode failed\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestRenderDiagnosticsWithoutColorKeepsPlainOutput(t *testing.T) {
	diags := []diagnostic.Diagnostic{
		{Severity: diagnostic.SeverityWarning, Category: "settings", Message: "metadata only"},
	}
	var stderr bytes.Buffer

	if err := renderDiagnosticsWithColor(&stderr, diags, false); err != nil {
		t.Fatalf("renderDiagnosticsWithColor() error = %v", err)
	}

	if got, want := stderr.String(), "warning settings: metadata only\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestDiagJSONOutputContainsDiagnosticsWithCodes(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
	}
	var report struct {
		Diagnostics []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Category string `json:"category"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", report.Diagnostics)
	}
	if got := report.Diagnostics[0].Code; got != "appset.unsupported-generator" {
		t.Fatalf("diagnostic code = %q, want appset.unsupported-generator", got)
	}
}

func TestDiagYAMLOutputContainsDiagnosticsWithCodes(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "yaml"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
	}
	if !strings.Contains(stdout.String(), "code: appset.unsupported-generator") {
		t.Fatalf("stdout = %q, want diagnostic code", stdout.String())
	}
}

func TestDiagJSONIncludesSettingsSummary(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeSettingsSummaryConfigMapForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "json", "--settings"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
	}
	var report struct {
		Settings struct {
			ResourceCustomizations map[string]struct {
				HasHealthLua    bool   `json:"hasHealthLua"`
				HealthLuaSHA256 string `json:"healthLuaSHA256"`
				Actions         struct {
					HasActions         bool   `json:"hasActions"`
					HasDiscoveryLua    bool   `json:"hasDiscoveryLua"`
					DiscoveryLuaSHA256 string `json:"discoveryLuaSHA256"`
					ActionNames        []string
					ActionLuaSHA256    []struct {
						Name   string `json:"name"`
						Index  int    `json:"index"`
						SHA256 string `json:"sha256"`
					} `json:"actionLuaSHA256"`
				} `json:"actions"`
			} `json:"resourceCustomizations"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	customization := report.Settings.ResourceCustomizations["apps/Deployment"]
	if !customization.HasHealthLua {
		t.Fatalf("settings = %#v, want health Lua summary", report.Settings)
	}
	if customization.HealthLuaSHA256 != "5891509de2d4c98e33ce3c17387504bc74033b0bfc02f2a307ccf58a8e826a9b" {
		t.Fatalf("healthLuaSHA256 = %q", customization.HealthLuaSHA256)
	}
	if !customization.Actions.HasActions || !customization.Actions.HasDiscoveryLua {
		t.Fatalf("actions = %#v, want actions and discovery summary", customization.Actions)
	}
	if customization.Actions.DiscoveryLuaSHA256 != "0596745fe0c0878b1a95592a3bbdcb73f103017dc971de03e911b4074303afbd" {
		t.Fatalf("discoveryLuaSHA256 = %q", customization.Actions.DiscoveryLuaSHA256)
	}
	if len(customization.Actions.ActionLuaSHA256) != 1 || customization.Actions.ActionLuaSHA256[0].SHA256 != "e5b0bdd6e3d65ea212b780b9c00247603f5da7a2b90cb024311732875322b51e" {
		t.Fatalf("actionLuaSHA256 = %#v", customization.Actions.ActionLuaSHA256)
	}
}

func TestDiagYAMLIncludesSettingsSummary(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeSettingsSummaryConfigMapForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "yaml", "--settings"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
	}
	for _, want := range []string{
		"settings:",
		"resourceCustomizations:",
		"healthLuaSHA256: 5891509de2d4c98e33ce3c17387504bc74033b0bfc02f2a307ccf58a8e826a9b",
		"discoveryLuaSHA256: 0596745fe0c0878b1a95592a3bbdcb73f103017dc971de03e911b4074303afbd",
		"sha256: e5b0bdd6e3d65ea212b780b9c00247603f5da7a2b90cb024311732875322b51e",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestDiagSettingsSummaryDoesNotLeakLuaBodies(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeSettingsSummaryConfigMapForCLI(t, root)

	for _, output := range []string{"json", "yaml"} {
		t.Run(output, func(t *testing.T) {
			cmd := NewRootCommand(VersionInfo{})
			cmd.SetArgs([]string{"diag", "--path", root, "-o", output, "--settings"})
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			combined := stdout.String() + stderr.String()
			for _, forbidden := range []string{
				"SUPER_SECRET_HEALTH_TOKEN",
				"SUPER_SECRET_ACTION_TOKEN",
				"return { status",
				"obj.metadata.annotations",
			} {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("%s output leaked %q\nstdout:\n%s\nstderr:\n%s", output, forbidden, stdout.String(), stderr.String())
				}
			}
		})
	}
}

func TestDiagSettingsSummaryDoesNotLeakConfigManagementPluginCommands(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeCLITestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cmp:
    plugins:
      kustomize-build-with-helm:
        generate:
          command: [sh, -c]
          args: [kustomize build --enable-helm --secret-token]
`)

	for _, output := range []string{"json", "yaml"} {
		t.Run(output, func(t *testing.T) {
			cmd := NewRootCommand(VersionInfo{})
			cmd.SetArgs([]string{"diag", "--path", root, "-o", output, "--settings"})
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			combined := stdout.String() + stderr.String()
			for _, forbidden := range []string{"kustomize build", "--secret-token", "ConfigManagementPlugins"} {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("%s output leaked %q\nstdout:\n%s\nstderr:\n%s", output, forbidden, stdout.String(), stderr.String())
				}
			}
		})
	}
}

func TestDiagSettingsSummaryIncludesCmdParamsWithoutSecrets(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")
	writeCLITestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
data:
  reposerver.include.hidden.directories: "true"
  reposerver.git.askpass.enabled: SUPER_SECRET_GIT_TOKEN
  unknown.cmd.param: SUPER_SECRET_UNKNOWN_VALUE
  repo.server: argocd-repo-server:8081
`)

	result := runCLI(t, "diag", "--path", root, "-o", "json", "--settings")
	assertStderrEmpty(t, result)
	for _, forbidden := range []string{"SUPER_SECRET_GIT_TOKEN", "SUPER_SECRET_UNKNOWN_VALUE"} {
		if strings.Contains(result.Stdout, forbidden) {
			t.Fatalf("stdout leaked %q:\n%s", forbidden, result.Stdout)
		}
	}

	var report struct {
		Diagnostics []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"diagnostics"`
		Settings struct {
			CommandParameters []diagCommandParameterJSON `json:"commandParameters"`
		} `json:"settings"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, result.Stdout)
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "settings.metadata-only" {
		t.Fatalf("diagnostics = %#v, want one cmd-params metadata-only warning", report.Diagnostics)
	}
	assertDiagMessageOmits(t, report.Diagnostics[0].Message, []string{"unknown.cmd.param", "repo.server"})
	assertDiagCommandParameter(t, report.Settings.CommandParameters, "reposerver.include.hidden.directories", "true", false, "runtime-only")
	assertDiagCommandParameter(t, report.Settings.CommandParameters, "reposerver.git.askpass.enabled", "", true, "runtime-only")
	assertDiagCommandParameter(t, report.Settings.CommandParameters, "unknown.cmd.param", "", false, "unknown")
}

func TestDiagJSONOutputCanIncludeCacheEvents(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeCLITestFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    chart: demo
    targetRevision: 1.2.3
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeCLITestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{ChartAcquirer: &recordingDiagChartAcquirer{chartDir: chartDir, fromCache: true}},
	})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "json", "--cache-events"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var report struct {
		CacheEvents []struct {
			Source string `json:"source"`
			Action string `json:"action"`
			Target string `json:"target"`
		} `json:"cacheEvents"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(report.CacheEvents) == 0 {
		t.Fatalf("cacheEvents = %#v, want non-empty", report.CacheEvents)
	}
	if report.CacheEvents[0].Source != "chart" || report.CacheEvents[0].Action != "hit" || report.CacheEvents[0].Target != "https://charts.example.test" {
		t.Fatalf("cacheEvents = %#v, want chart hit", report.CacheEvents)
	}
}

func TestDiagJSONOutputIncludesPluginExecutions(t *testing.T) {
	orchestrator := &recordingCLIOrchestrator{
		diagResult: app.DiagResult{
			PluginExecutions: []app.PluginExecution{{
				AppNamespace: "argocd",
				AppName:      "plugin-app",
				SourceIndex:  0,
				SourcePath:   "manifests/plugin",
				PluginName:   "exec-renderer",
				Engine:       "exec",
				Runtime:      "docker",
				Image:        "registry.example.test/plugins/render@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Phase:        "generate",
				Command:      "argocd-vault-plugin",
				Duration:     "12ms",
			}},
		},
	}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: orchestrator})
	cmd.SetArgs([]string{"diag", "--path", "ignored", "-o", "json", "--plugin-executions"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var report struct {
		PluginExecutions []struct {
			AppName    string `json:"appName"`
			PluginName string `json:"pluginName"`
			Engine     string `json:"engine"`
			Runtime    string `json:"runtime"`
			Image      string `json:"image"`
			Phase      string `json:"phase"`
			Command    string `json:"command"`
			Duration   string `json:"duration"`
		} `json:"pluginExecutions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(report.PluginExecutions) != 1 {
		t.Fatalf("pluginExecutions = %#v, want one execution", report.PluginExecutions)
	}
	execution := report.PluginExecutions[0]
	if execution.AppName != "plugin-app" || execution.PluginName != "exec-renderer" || execution.Phase != "generate" || execution.Command != "argocd-vault-plugin" || execution.Duration != "12ms" {
		t.Fatalf("pluginExecutions[0] = %#v, want exec metadata", execution)
	}
	if execution.Engine != "exec" || execution.Runtime != "docker" || execution.Image != "registry.example.test/plugins/render@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("pluginExecutions[0] = %#v, want engine/runtime/image metadata", execution)
	}
}

func TestDiagDefaultUsesApplicationListing(t *testing.T) {
	orchestrator := &recordingCLIOrchestrator{}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: orchestrator})
	cmd.SetArgs([]string{"diag", "--path", "repo"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if len(orchestrator.listRequests) != 1 {
		t.Fatalf("list requests = %d, want 1", len(orchestrator.listRequests))
	}
	request := orchestrator.listRequests[0]
	if request.DiscoveryMode != app.DiscoveryModeStatic || request.MaxDiscoveryDepth != 0 || !request.MaxDiscoveryDepthSet {
		t.Fatalf("list request discovery = mode=%q depth=%d set=%t, want static depth 0", request.DiscoveryMode, request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
	}
	if len(orchestrator.diagRequests) != 0 {
		t.Fatalf("diag requests = %d, want 0", len(orchestrator.diagRequests))
	}
}

func TestDiagRenderBackedFlagsUseDiag(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "render", args: []string{"diag", "--path", "repo", "--render"}},
		{name: "cache events", args: []string{"diag", "--path", "repo", "--cache-events"}},
		{name: "plugin executions", args: []string{"diag", "--path", "repo", "--plugin-executions"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := &recordingCLIOrchestrator{}
			cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: orchestrator})
			cmd.SetArgs(tt.args)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if len(orchestrator.diagRequests) != 1 {
				t.Fatalf("diag requests = %d, want 1", len(orchestrator.diagRequests))
			}
			request := orchestrator.diagRequests[0]
			if request.DiscoveryMode != app.DiscoveryModeFleet {
				t.Fatalf("diag request discovery mode = %q, want fleet", request.DiscoveryMode)
			}
			if len(orchestrator.listRequests) != 0 {
				t.Fatalf("list requests = %d, want 0", len(orchestrator.listRequests))
			}
		})
	}
}

func TestDiagJSONOutputOmitsCacheEventsWithoutFlag(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeCLITestFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    chart: demo
    targetRevision: 1.2.3
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeCLITestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{ChartAcquirer: &recordingDiagChartAcquirer{chartDir: chartDir, fromCache: true}},
	})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var report map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if _, ok := report["cacheEvents"]; ok {
		t.Fatalf("stdout = %s, want no cacheEvents without --cache-events", stdout.String())
	}
	if _, ok := report["pluginExecutions"]; ok {
		t.Fatalf("stdout = %s, want no pluginExecutions without exec plugin runs", stdout.String())
	}
}

func TestDiagRejectsUnsupportedStructuredOutput(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "-o", "name"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "diag output supports text, json, or yaml") {
		t.Fatalf("Execute() error = %v, want diag output error", err)
	}
}

func TestDiagRejectsUnsupportedOutputBeforeAcquisition(t *testing.T) {
	root := t.TempDir()
	writeCLITestFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/external
    path: missing
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	gitAcquirer := &recordingCLIGitAcquirer{err: errors.New("git acquirer should not be called")}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{GitAcquirer: gitAcquirer},
	})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "name"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "diag output supports text, json, or yaml") {
		t.Fatalf("Execute() error = %v, want diag output error", err)
	}
	if len(gitAcquirer.requests) != 0 {
		t.Fatalf("Git requests = %#v, want none before output validation", gitAcquirer.requests)
	}
}

func TestDiagStrictUnsupportedApplicationSetErrors(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--strict"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want strict diagnostic error")
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("error = %v, want unsupported ApplicationSet generator", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "error appset:") {
		t.Fatalf("stderr = %q, want error appset diagnostic", stderr.String())
	}
}

func TestDiagRejectsInvalidRepoMap(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--repo-map", "https://github.com/example/repo"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want repo-map error")
	}
	if !strings.Contains(err.Error(), "must use URL=PATH") {
		t.Fatalf("error = %v, want repo-map parse error", err)
	}
}

func writeUnsupportedApplicationSetForCLI(t *testing.T, root string) {
	t.Helper()
	writeSimpleAppForCLI(t, root, "ok")
	writeCLITestFile(t, filepath.Join(root, "unsupported-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
  namespace: argocd
spec:
  generators:
    - scmProvider: {}
  template:
    metadata:
      name: generated
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: manifests/generated
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

func writeSettingsSummaryConfigMapForCLI(t *testing.T, root string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      health.lua: |
        return { status = "Healthy", message = "SUPER_SECRET_HEALTH_TOKEN" }
      actions: |
        discovery.lua: |
          return { { name = "restart" } }
        definitions:
          - name: restart
            action.lua: |
              obj.metadata.annotations = { token = "SUPER_SECRET_ACTION_TOKEN" }
              return obj
`)
}

func requireDiagCommandParameter(t *testing.T, parameters []diagCommandParameterJSON, key string) diagCommandParameterJSON {
	t.Helper()
	for _, parameter := range parameters {
		if parameter.Key == key {
			return parameter
		}
	}
	t.Fatalf("commandParameters = %#v, missing %q", parameters, key)
	return diagCommandParameterJSON{}
}

func assertDiagMessageOmits(t *testing.T, message string, values []string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(message, value) {
			t.Fatalf("diagnostic message = %q, did not want %q", message, value)
		}
	}
}

func assertDiagCommandParameter(t *testing.T, parameters []diagCommandParameterJSON, key, value string, redacted bool, classification string) {
	t.Helper()
	parameter := requireDiagCommandParameter(t, parameters, key)
	if parameter.Value != value || parameter.ValueRedacted != redacted || parameter.Classification != classification {
		t.Fatalf("command parameter %q = %#v, want value=%q redacted=%t classification=%s", key, parameter, value, redacted, classification)
	}
}

type recordingDiagChartAcquirer struct {
	chartDir  string
	fromCache bool
}

func (acquirer *recordingDiagChartAcquirer) Acquire(_ context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	return chart.Result{
		ChartDir:   acquirer.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  acquirer.fromCache,
	}, nil
}
