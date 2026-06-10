package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/pluginonboarding"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPluginPolicyInitPrintsScaffoldToStdout(t *testing.T) {
	root := t.TempDir()
	result := runCLIWithDependencies(t, pluginPolicyCLIDependencies(root), "plugin-policy", "init", "--path", root, "--no-comments")

	assertStderrEmpty(t, result)
	assertStdoutContainsAll(t, result,
		pluginonboarding.SchemaModeline,
		"kind: PluginPolicy",
		"plugins:",
		`"pkl":`,
		"engine: container",
		`image: "`+pluginonboarding.PlaceholderImage+`"`,
		"parameters:",
		`name: "values"`,
	)
	if _, err := pluginpolicy.Parse("stdout", []byte(result.Stdout)); err != nil {
		t.Fatalf("generated policy parse error = %v\n%s", err, result.Stdout)
	}
}

func TestPluginPolicyInitUsesStaticDiscoveryRequest(t *testing.T) {
	root := t.TempDir()
	orchestrator := &recordingCLIOrchestrator{listResult: pluginPolicyCLIListResult(root)}
	runCLIWithDependencies(t, Dependencies{Orchestrator: orchestrator}, "plugin-policy", "init", "--path", root, "--appset-provider-fixture", "fixture.yaml")

	if len(orchestrator.listRequests) != 1 {
		t.Fatalf("list requests = %d, want 1", len(orchestrator.listRequests))
	}
	request := orchestrator.listRequests[0]
	if request.Path != root {
		t.Fatalf("Path = %q, want %q", request.Path, root)
	}
	if request.DiscoveryMode != app.DiscoveryModeStatic || request.MaxDiscoveryDepth != 0 || !request.MaxDiscoveryDepthSet {
		t.Fatalf("DiscoveryOptions = %#v, want static depth 0", request.DiscoveryOptions)
	}
	if !request.DisablePluginPolicy {
		t.Fatalf("DisablePluginPolicy = false, want true")
	}
	if got, want := request.ApplicationSetProviderFixtures, []string{"fixture.yaml"}; !slices.Equal(got, want) {
		t.Fatalf("ApplicationSetProviderFixtures = %#v, want %#v", got, want)
	}
}

func TestPluginPolicyInitWriteKeepsStdoutCleanAndRequiresOverwrite(t *testing.T) {
	root := t.TempDir()
	result := runCLIWithDependencies(t, pluginPolicyCLIDependencies(root), "plugin-policy", "init", "--path", root, "--write")
	if result.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", result.Stdout)
	}
	if !strings.Contains(result.Stderr, "Wrote .drydock/plugins.yaml") {
		t.Fatalf("stderr = %q, want write summary", result.Stderr)
	}
	written := filepath.Join(root, ".drydock", "plugins.yaml")
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read written policy: %v", err)
	}
	if _, err := pluginpolicy.Parse(written, data); err != nil {
		t.Fatalf("written policy parse error = %v\n%s", err, data)
	}

	cmd := NewRootCommandWithDependencies(VersionInfo{}, pluginPolicyCLIDependencies(root))
	_, _, err = executeCLIForPluginPolicyTest(cmd, "plugin-policy", "init", "--path", root, "--write")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Execute() error = %v, want existing file error", err)
	}

	runCLIWithDependencies(t, pluginPolicyCLIDependencies(root), "plugin-policy", "init", "--path", root, "--write", "--overwrite")
}

func TestPluginPolicyInitRejectsConflictingWriteFlags(t *testing.T) {
	root := t.TempDir()
	cmd := NewRootCommandWithDependencies(VersionInfo{}, pluginPolicyCLIDependencies(root))
	_, _, err := executeCLIForPluginPolicyTest(cmd, "plugin-policy", "init", "--path", root, "--write", "--output", ".drydock/custom.yaml")
	if err == nil || !strings.Contains(err.Error(), "--write and --output are mutually exclusive") {
		t.Fatalf("Execute() error = %v, want conflicting write flags error", err)
	}
}

func TestPluginPolicyInitRejectsSymlinkOutputTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "policy.yaml")
	target := filepath.Join(root, ".drydock", "plugins.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := writePluginPolicyScaffold(root, ".drydock/plugins.yaml", []byte("kind: PluginPolicy\n"), true)
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("writePluginPolicyScaffold() error = %v, want symlink rejection", err)
	}
}

func TestPluginPolicyInitRejectsDirectoryOutputTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".drydock", "plugins.yaml")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := writePluginPolicyScaffold(root, ".drydock/plugins.yaml", []byte("kind: PluginPolicy\n"), true)
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("writePluginPolicyScaffold() error = %v, want regular file rejection", err)
	}
}

func TestPluginPolicyDoctorReportsMissingDefaultPolicyAsReadinessFailure(t *testing.T) {
	root := t.TempDir()
	result := runCLIWithDependencies(t, pluginPolicyCLIDependencies(root), "plugin-policy", "doctor", "--path", root, "-o", "json")
	assertStderrEmpty(t, result)

	var readiness pluginonboarding.ReadinessReport
	if err := json.Unmarshal([]byte(result.Stdout), &readiness); err != nil {
		t.Fatalf("json unmarshal error = %v\n%s", err, result.Stdout)
	}
	if readiness.Status != pluginonboarding.StatusFail {
		t.Fatalf("status = %q, want FAIL", readiness.Status)
	}
	if len(readiness.Recommendations) != 1 || readiness.Recommendations[0].Code != pluginonboarding.IssuePolicyMissing {
		t.Fatalf("recommendations = %#v, want policy.missing", readiness.Recommendations)
	}
}

func TestPluginPolicyDoctorRejectsMissingExplicitPolicyPath(t *testing.T) {
	root := t.TempDir()
	cmd := NewRootCommandWithDependencies(VersionInfo{}, pluginPolicyCLIDependencies(root))
	_, _, err := executeCLIForPluginPolicyTest(cmd, "plugin-policy", "doctor", "--path", root, "--plugin-policy-path", ".drydock/missing.yaml")
	if err == nil || !strings.Contains(err.Error(), `plugin policy ".drydock/missing.yaml" does not exist`) {
		t.Fatalf("Execute() error = %v, want missing explicit policy error", err)
	}
}

func TestPluginPolicyDoctorJSONIncludesGateIssueCodes(t *testing.T) {
	root := t.TempDir()
	runCLIWithDependencies(t, pluginPolicyCLIDependencies(root), "plugin-policy", "init", "--path", root, "--write")

	result := runCLIWithDependencies(t, pluginPolicyCLIDependencies(root), "plugin-policy", "doctor", "--path", root, "-o", "json")
	var readiness pluginonboarding.ReadinessReport
	if err := json.Unmarshal([]byte(result.Stdout), &readiness); err != nil {
		t.Fatalf("json unmarshal error = %v\n%s", err, result.Stdout)
	}
	if readiness.Status != pluginonboarding.StatusFail {
		t.Fatalf("status = %q, want FAIL", readiness.Status)
	}
	if len(readiness.Plugins) != 1 {
		t.Fatalf("plugins = %#v, want one plugin", readiness.Plugins)
	}
	codes := make([]string, 0, len(readiness.Plugins[0].Issues))
	for _, issue := range readiness.Plugins[0].Issues {
		codes = append(codes, issue.Code)
	}
	for _, want := range []string{
		pluginonboarding.IssuePluginsDisabled,
		pluginonboarding.IssuePolicyUntrusted,
		pluginonboarding.IssueImagePlaceholder,
	} {
		if !slices.Contains(codes, want) {
			t.Fatalf("issue codes = %#v, missing %q", codes, want)
		}
	}
}

func TestPluginPolicyDoctorIgnoresUnusedCMPDescriptors(t *testing.T) {
	root := t.TempDir()
	policyPath := filepath.Join(root, ".drydock", "plugins.yaml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  pkl:
    engine: avp-compat
`), 0o644); err != nil {
		t.Fatal(err)
	}
	result := pluginPolicyCLIListResult(root)
	result.Settings.ConfigManagementPlugins["unused-ytt"] = config.ConfigManagementPlugin{
		Name:            "unused-ytt",
		GenerateCommand: []string{"ytt"},
		GenerateArgs:    []string{"-f", "."},
		Discover: config.ConfigManagementPluginDiscovery{
			FileName: "ytt.yaml",
		},
	}
	deps := Dependencies{Orchestrator: &recordingCLIOrchestrator{listResult: result}}

	run := runCLIWithDependencies(t, deps, "plugin-policy", "doctor", "--path", root, "-o", "json")
	var readiness pluginonboarding.ReadinessReport
	if err := json.Unmarshal([]byte(run.Stdout), &readiness); err != nil {
		t.Fatalf("json unmarshal error = %v\n%s", err, run.Stdout)
	}
	if readiness.Status != pluginonboarding.StatusPass {
		t.Fatalf("status = %q, want PASS; readiness = %#v", readiness.Status, readiness)
	}
	if len(readiness.Plugins) != 1 || readiness.Plugins[0].Name != "pkl" {
		t.Fatalf("plugins = %#v, want only used pkl plugin", readiness.Plugins)
	}
}

func TestPluginPolicyDoctorReportsExistingPolicyWithoutApplications(t *testing.T) {
	root := t.TempDir()
	policyPath := filepath.Join(root, ".drydock", "plugins.yaml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  pkl:
    engine: container
    image: registry.example.com/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    configManagementPlugin:
      discover:
        fileName: PklProject
    copy:
      scope: source
    generate:
      command: ["pkl", "eval", "."]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{Orchestrator: &recordingCLIOrchestrator{listResult: app.BuildResult{Settings: config.DefaultSettings()}}}
	run := runCLIWithDependencies(t, deps, "plugin-policy", "doctor", "--path", root, "-o", "json")

	var readiness pluginonboarding.ReadinessReport
	if err := json.Unmarshal([]byte(run.Stdout), &readiness); err != nil {
		t.Fatalf("json unmarshal error = %v\n%s", err, run.Stdout)
	}
	if readiness.Status != pluginonboarding.StatusFail {
		t.Fatalf("status = %q, want FAIL for local untrusted command-backed policy", readiness.Status)
	}
	if len(readiness.Plugins) != 1 || readiness.Plugins[0].Name != "pkl" {
		t.Fatalf("plugins = %#v, want existing pkl policy reported", readiness.Plugins)
	}
}

func TestPluginPolicyDoctorStrictReturnsErrorAfterRendering(t *testing.T) {
	root := t.TempDir()
	runCLIWithDependencies(t, pluginPolicyCLIDependencies(root), "plugin-policy", "init", "--path", root, "--write")

	cmd := NewRootCommandWithDependencies(VersionInfo{}, pluginPolicyCLIDependencies(root))
	stdout, _, err := executeCLIForPluginPolicyTest(cmd, "plugin-policy", "doctor", "--path", root, "--strict", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "plugin policy readiness failed") {
		t.Fatalf("Execute() error = %v, want strict readiness failure", err)
	}
	if !strings.Contains(stdout, `"status": "FAIL"`) {
		t.Fatalf("stdout = %q, want rendered readiness JSON", stdout)
	}
}

func pluginPolicyCLIDependencies(root string) Dependencies {
	return Dependencies{Orchestrator: &recordingCLIOrchestrator{listResult: pluginPolicyCLIListResult(root)}}
}

func pluginPolicyCLIListResult(root string) app.BuildResult {
	settings := config.DefaultSettings()
	settings.ConfigManagementPlugins["pkl"] = config.ConfigManagementPlugin{
		Name:            "pkl",
		GenerateCommand: []string{"pkl"},
		GenerateArgs:    []string{"eval", "."},
		Discover: config.ConfigManagementPluginDiscovery{
			FileName: "PklProject",
		},
	}
	value := "prod"
	application := argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "argocd",
			Name:      "demo",
			Labels:    map[string]string{"app.kubernetes.io/name": "demo"},
		},
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				Path: "apps/demo",
				Plugin: &argoappv1.ApplicationSourcePlugin{
					Name: "pkl",
					Parameters: argoappv1.ApplicationSourcePluginParameters{
						{Name: "values", String_: &value},
					},
					Env: argoappv1.Env{
						{Name: "PKL_ENV", Value: "prod"},
					},
				},
			},
		},
	}
	return app.BuildResult{
		Applications: []argoappv1.Application{application},
		ApplicationInputs: []app.ApplicationSelectionInput{
			{
				Application: application,
				Paths:       []string{filepath.Join(root, "apps", "demo", "app.yaml")},
			},
		},
		Settings: settings,
	}
}

func executeCLIForPluginPolicyTest(cmd *cobra.Command, args ...string) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}
