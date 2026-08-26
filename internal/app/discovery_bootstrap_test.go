package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

func TestListApplicationsRunsPluginPolicyBootstrapDuringFleetDiscovery(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	writeTestFile(t, filepath.Join(root, "workloads", "bootstrap-child", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: bootstrap-child
`)
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("bootstrap-child", "workloads/bootstrap-child"))},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:          root,
		PluginOptions: trustedBootstrapPluginOptions(t, root),
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if runner.calls != 1 {
		t.Fatalf("exec runner calls = %d, want one bootstrap render", runner.calls)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "bootstrap-child" {
		t.Fatalf("Applications = %#v, want bootstrap-rendered Application", names)
	}
	inputs, ok := applicationInputPathsForName(result.ApplicationInputs, "bootstrap-child")
	if !ok {
		t.Fatalf("ApplicationInputs = %#v, missing bootstrap-child", result.ApplicationInputs)
	}
	for _, want := range []string{".drydock/plugins.yaml", "bootstrap"} {
		if !containsPath(inputs, want) {
			t.Fatalf("bootstrap-child inputs = %#v, missing %q", inputs, want)
		}
	}
}

func TestListApplicationsUsesCustomPluginPolicyPathForBootstrapInputs(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicyAt(t, root, "policy/plugins.yaml", "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("custom-policy", "workloads/custom-policy"))},
	}
	options := trustedBootstrapPluginOptionsAt(t, root, "policy/plugins.yaml")

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:          root,
		PluginOptions: options,
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	inputs, ok := applicationInputPathsForName(result.ApplicationInputs, "custom-policy")
	if !ok {
		t.Fatalf("ApplicationInputs = %#v, missing custom-policy", result.ApplicationInputs)
	}
	if !containsPath(inputs, "policy/plugins.yaml") {
		t.Fatalf("custom-policy inputs = %#v, missing custom policy path", inputs)
	}
	if containsPath(inputs, ".drydock/plugins.yaml") {
		t.Fatalf("custom-policy inputs = %#v, should not include default policy path", inputs)
	}
}

func TestListApplicationsRunsPluginPolicyBootstrapWhenMaxDiscoveryDepthZero(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("depth-zero", "workloads/depth-zero"))},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:                 root,
		MaxDiscoveryDepth:    0,
		MaxDiscoveryDepthSet: true,
		PluginOptions:        trustedBootstrapPluginOptions(t, root),
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if runner.calls != 1 {
		t.Fatalf("exec runner calls = %d, want one bootstrap render", runner.calls)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "depth-zero" {
		t.Fatalf("Applications = %#v, want bootstrap-rendered Application with max depth 0", names)
	}
}

func TestListApplicationsDiscoveryModeStaticDisablesPluginPolicyBootstrap(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("disabled", "workloads/disabled"))},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:          root,
		DiscoveryMode: DiscoveryModeStatic,
		PluginOptions: trustedBootstrapPluginOptions(t, root),
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if runner.calls != 0 {
		t.Fatalf("exec runner calls = %d, want static discovery to skip bootstrap", runner.calls)
	}
	if len(result.Applications) != 0 {
		t.Fatalf("Applications = %#v, want no bootstrap-discovered Applications in static mode", applicationNames(result.Applications))
	}
}

func TestListApplicationsExpandsPluginPolicyBootstrapApplicationSetsWhenMaxDiscoveryDepthZero(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationSetYAML())},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:                 root,
		MaxDiscoveryDepth:    0,
		MaxDiscoveryDepthSet: true,
		PluginOptions:        trustedBootstrapPluginOptions(t, root),
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if runner.calls != 1 {
		t.Fatalf("exec runner calls = %d, want one bootstrap render", runner.calls)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "generated" {
		t.Fatalf("Applications = %#v, want Application generated from bootstrap-rendered ApplicationSet", names)
	}
	inputs, ok := applicationInputPathsForName(result.ApplicationInputs, "generated")
	if !ok {
		t.Fatalf("ApplicationInputs = %#v, missing generated", result.ApplicationInputs)
	}
	for _, want := range []string{".drydock/plugins.yaml", "bootstrap"} {
		if !containsPath(inputs, want) {
			t.Fatalf("generated inputs = %#v, missing %q", inputs, want)
		}
	}
}

func TestListApplicationsKeepsStaticApplicationAheadOfPluginPolicyBootstrapDuplicate(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: apps/static
  destination:
    name: in-cluster
    namespace: demo
`)
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("demo", "apps/bootstrap"))},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:          root,
		PluginOptions: trustedBootstrapPluginOptions(t, root),
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if runner.calls != 1 {
		t.Fatalf("exec runner calls = %d, want one bootstrap render", runner.calls)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "demo" {
		t.Fatalf("Applications = %#v, want one demo Application", names)
	}
	if got := result.Applications[0].Spec.GetSource().Path; got != "apps/static" {
		t.Fatalf("demo source path = %q, want static committed Application to win", got)
	}
	if diag, ok := diagnosticByCategory(result.Diagnostics, "discovery"); !ok || !strings.Contains(diag.Message, "duplicate Application") {
		t.Fatalf("Diagnostics = %#v, want duplicate discovery warning", result.Diagnostics)
	}
}

func TestListApplicationsKeepsStaticNamespaceLessApplicationAheadOfPluginPolicyBootstrapDuplicate(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: apps/static
  destination:
    name: in-cluster
    namespace: demo
`)
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("demo", "apps/bootstrap"))},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:          root,
		PluginOptions: trustedBootstrapPluginOptions(t, root),
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if runner.calls != 1 {
		t.Fatalf("exec runner calls = %d, want one bootstrap render", runner.calls)
	}
	if names := applicationNames(result.Applications); strings.Join(names, ",") != "demo" {
		t.Fatalf("Applications = %#v, want one demo Application", names)
	}
	if got := result.Applications[0].Spec.GetSource().Path; got != "apps/static" {
		t.Fatalf("demo source path = %q, want namespace-less static Application to win over bootstrap", got)
	}
	if got := result.Applications[0].Namespace; got != "" {
		t.Fatalf("demo namespace = %q, want namespace-less static Application", got)
	}
}

func TestListApplicationsFailsWhenPluginPolicyBootstrapDiscoverRuleDoesNotMatch(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "other.txt"), "")
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("unmatched", "workloads/unmatched"))},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:          root,
		PluginOptions: trustedBootstrapPluginOptions(t, root),
	})
	if err == nil {
		t.Fatalf("ListApplications() error = nil, want unmatched bootstrap discover rule error; result = %#v", result)
	}
	if runner.calls != 0 {
		t.Fatalf("exec runner calls = %d, want unmatched bootstrap source to fail before render", runner.calls)
	}
	message := err.Error()
	for _, want := range []string{`bootstrap entrypoint "cluster-root"`, `plugin "pkl"`, "discover", "did not match"} {
		if !strings.Contains(message, want) {
			t.Fatalf("ListApplications() error = %q, want fragment %q", message, want)
		}
	}
	if !strings.Contains(message, `sourcePath "bootstrap"`) && !strings.Contains(message, `source path "bootstrap"`) {
		t.Fatalf("ListApplications() error = %q, want bootstrap source path context", message)
	}
}

func writeBootstrapExecPluginPolicy(t *testing.T, root, sourcePath, discoverFileName string) {
	t.Helper()
	writeBootstrapExecPluginPolicyAt(t, root, defaultPluginPolicyPath, sourcePath, discoverFileName)
}

func writeBootstrapExecPluginPolicyAt(t *testing.T, root, policyPath, sourcePath, discoverFileName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, filepath.FromSlash(policyPath)), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
bootstrap:
  entrypoints:
    - name: cluster-root
      plugin: pkl
      sourcePath: `+sourcePath+`
plugins:
  pkl:
    engine: exec
    match:
      discover:
        fileName: `+discoverFileName+`
    generate:
      command: ["pkl", "eval", "index.pkl"]
      timeout: `+testExecPolicyCommandTimeout+`
`)
}

func trustedBootstrapPluginOptions(t *testing.T, root string) PluginOptions {
	t.Helper()
	return trustedBootstrapPluginOptionsAt(t, root, defaultPluginPolicyPath)
}

func trustedBootstrapPluginOptionsAt(t *testing.T, root, policyPath string) PluginOptions {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(policyPath)))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	policy, err := pluginpolicy.Parse(policyPath, data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	fingerprint, err := pluginpolicy.Fingerprint(policy)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	return PluginOptions{
		EnablePlugins:            true,
		PluginPolicyPath:         policyPath,
		PluginPolicyPathExplicit: policyPath != defaultPluginPolicyPath,
		pluginPolicyLoaded:       true,
		pluginPolicy:             policy,
		pluginPolicyFingerprint:  fingerprint,
		pluginPolicyExecTrusted:  true,
	}
}

func bootstrapApplicationYAML(name, sourcePath string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + name + `
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: ` + sourcePath + `
  destination:
    name: in-cluster
    namespace: ` + name + `
`
}

func bootstrapApplicationSetYAML() string {
	return `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: generated
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - name: generated
  template:
    metadata:
      name: "{{name}}"
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: workloads/{{name}}
      destination:
        name: in-cluster
        namespace: "{{name}}"
`
}
