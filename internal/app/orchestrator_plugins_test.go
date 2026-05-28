package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

func TestOrchestratorBuildFailsClosedForPluginSourceWithoutRenderer(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "plain directory",
			files: map[string]string{
				filepath.Join("sources", "plugin", "plain.yaml"): `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render-as-directory
data:
  value: wrong
`,
			},
		},
		{
			name: "Kustomize-shaped directory",
			files: map[string]string{
				filepath.Join("sources", "plugin", "kustomization.yaml"): `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`,
				filepath.Join("sources", "plugin", "cm.yaml"): `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render-as-kustomize
data:
  value: wrong
`,
			},
		},
		{
			name: "Helm-shaped directory",
			files: map[string]string{
				filepath.Join("sources", "plugin", "Chart.yaml"): `apiVersion: v2
name: plugin
version: 0.1.0
`,
				filepath.Join("sources", "plugin", "templates", "cm.yaml"): `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render-as-helm
data:
  value: wrong
`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plugin-app
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: sources/plugin
    plugin:
      name: cue
      env:
        - name: FEATURE
          value: enabled
  destination:
    name: in-cluster
    namespace: default
`)
			for path, content := range tt.files {
				writeTestFile(t, filepath.Join(root, path), content)
			}

			result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
			if err == nil {
				t.Fatalf("Build() error = nil, want unsupported plugin error")
			}
			if len(result.Manifests) != 0 {
				t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
			}
			if len(result.Statuses) != 1 || result.Statuses[0].Status != ApplicationStatusFail {
				t.Fatalf("statuses = %#v, want one FAIL", result.Statuses)
			}
			if !strings.Contains(result.Statuses[0].Message, "config management plugin cue is disabled in the default renderer") {
				t.Fatalf("status message = %q, want unsupported plugin renderer", result.Statuses[0].Message)
			}
			if !strings.Contains(result.Statuses[0].Message, "trusted policy") {
				t.Fatalf("status message = %q, want trusted policy guidance", result.Statuses[0].Message)
			}
			foundPluginDiagnostic := false
			for _, diag := range result.Diagnostics {
				if diag.Category == "plugin" {
					foundPluginDiagnostic = true
				}
			}
			if !foundPluginDiagnostic {
				t.Fatalf("diagnostics = %#v, want plugin diagnostic", result.Diagnostics)
			}
		})
	}
}

func TestOrchestratorBuildFailsClosedForMultiSourcePluginWithoutRenderer(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writeTestFile(t, filepath.Join(root, "apps", "multi-plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: multi-plugin
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: https://github.com/example/repo
      path: manifests/plain
      targetRevision: main
    - repoURL: https://github.com/example/repo
      path: manifests/multi-plugin
      targetRevision: main
      plugin:
        name: cue
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plain", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: plain-source
`)
	writeTestFile(t, filepath.Join(root, "manifests", "multi-plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported plugin error")
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "ok", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "multi-plugin", Status: ApplicationStatusFail},
	})
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, plugin source rendered through fallback", result.Manifests)
	}
	if _, ok := manifestByName(result.Manifests, "plain-source"); ok {
		t.Fatalf("Manifests = %#v, want no partial manifests from failed multi-source Application", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, `source[1] path="manifests/multi-plugin"`) {
		t.Fatalf("Diagnostics = %#v, want source[1] plugin diagnostic", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "trusted policy") {
		t.Fatalf("Diagnostics = %#v, want trusted policy guidance", result.Diagnostics)
	}
}

func TestOrchestratorBuildFailsClosedForApplicationSetGeneratedPluginWithoutRenderer(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writeTestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: plugin-set
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - name: generated-plugin
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: manifests/{{name}}
        targetRevision: main
        plugin:
          name: cue
      destination:
        name: in-cluster
        namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "generated-plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported plugin error")
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "generated-plugin", Status: ApplicationStatusFail},
		{Namespace: "argocd", Name: "ok", Status: ApplicationStatusPass},
	})
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, generated plugin source rendered through fallback", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersAVPCompatPolicyPluginQuietly(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "avp-directory-include")
	writePluginPolicy(t, root, "avp-directory-include", "avp-compat")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-redacted
data:
  domain: argocd.<path:vaults/Kubernetes/items/cluster#domain>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	manifest, ok := manifestByName(result.Manifests, "avp-redacted")
	if !ok {
		t.Fatalf("Manifests = %#v, want avp-redacted", result.Manifests)
	}
	data, ok := manifest.Object.Object["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want map", manifest.Object.Object["data"])
	}
	value, ok := data["domain"].(string)
	if !ok || !strings.HasPrefix(value, "argocd.drydock-redacted-") {
		t.Fatalf("data.domain = %#v, want redacted AVP placeholder", data["domain"])
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, policy AVP should be quiet", result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsAVPCompatPolicyPluginEnv(t *testing.T) {
	root := t.TempDir()
	writePluginPolicy(t, root, "avp-directory-include", "avp-compat")
	writeTestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plugin
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/plugin
    targetRevision: main
    plugin:
      name: avp-directory-include
      env:
        - name: FEATURE
          value: enabled
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want env rejection")
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "env or parameters") {
		t.Fatalf("Diagnostics = %#v, want env/parameters rejection", result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsExecPolicyWithoutEnablePlugins(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeExecPluginPolicy(t, root, "exec-renderer", appExecCommand(t, "manifest"))

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want exec plugin opt-in failure")
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "requires --enable-plugins") {
		t.Fatalf("Diagnostics = %#v, want enable-plugins guidance", result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsExecPolicyWithoutTrustedRef(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeExecPluginPolicy(t, root, "exec-renderer", appExecCommand(t, "manifest"))

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			EnablePlugins: true,
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want untrusted exec policy failure")
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "untrusted policy source") {
		t.Fatalf("Diagnostics = %#v, want trusted-ref guidance", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersTrustedExecPolicyPlugin(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	t.Setenv("DRYDOCK_APP_EXEC_VALUE", "allowed-value")
	policy, fingerprint := testExecPluginPolicy(t, "exec-renderer", appExecCommand(t, "manifest"))

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			EnablePlugins:           true,
			pluginPolicyLoaded:      true,
			pluginPolicy:            policy,
			pluginPolicyFingerprint: fingerprint,
			pluginPolicyExecTrusted: true,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	manifest, ok := manifestByName(result.Manifests, "exec-rendered")
	if !ok {
		t.Fatalf("Manifests = %#v, want exec-rendered", result.Manifests)
	}
	data, ok := manifest.Object.Object["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want map", manifest.Object.Object["data"])
	}
	if data["marker"] != "from-source" || data["env"] != "allowed-value" {
		t.Fatalf("data = %#v, want source marker and allowed env", data)
	}
	if _, err := os.Stat(filepath.Join(root, "manifests", "plugin", "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("original source generated.txt exists or unexpected stat error: %v", err)
	}
}

func TestOrchestratorBuildReportsInvalidExecPolicyOutput(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicy(t, "exec-renderer", appExecCommand(t, "invalid"))

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			EnablePlugins:           true,
			pluginPolicyLoaded:      true,
			pluginPolicy:            policy,
			pluginPolicyFingerprint: fingerprint,
			pluginPolicyExecTrusted: true,
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want invalid manifest output failure")
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "produced invalid manifests") {
		t.Fatalf("Diagnostics = %#v, want invalid manifest diagnostic", result.Diagnostics)
	}
}

func TestOrchestratorBuildRunsExecPolicyPostRenderer(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithPostRenderer(t, "exec-renderer", appExecCommand(t, "manifest"), appExecCommand(t, "post-render"))

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			EnablePlugins:           true,
			pluginPolicyLoaded:      true,
			pluginPolicy:            policy,
			pluginPolicyFingerprint: fingerprint,
			pluginPolicyExecTrusted: true,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	manifest, ok := manifestByName(result.Manifests, "exec-rendered")
	if !ok {
		t.Fatalf("Manifests = %#v, want exec-rendered", result.Manifests)
	}
	data, ok := manifest.Object.Object["data"].(map[string]any)
	if !ok || data["post"] != "rendered" {
		t.Fatalf("data = %#v, want post-rendered marker", manifest.Object.Object["data"])
	}
}

func TestOrchestratorBuildReportsInvalidExecPolicyPostRendererOutput(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithPostRenderer(t, "exec-renderer", appExecCommand(t, "manifest"), appExecCommand(t, "invalid"))

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			EnablePlugins:           true,
			pluginPolicyLoaded:      true,
			pluginPolicy:            policy,
			pluginPolicyFingerprint: fingerprint,
			pluginPolicyExecTrusted: true,
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want invalid post-render output failure")
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "produced invalid manifests") {
		t.Fatalf("Diagnostics = %#v, want invalid manifest diagnostic", result.Diagnostics)
	}
}

func TestOrchestratorDiffAppsUsesLeftExecPolicyForBothSides(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writePluginBuildApplication(t, left, "plugin", "exec-renderer")
	writePluginBuildApplication(t, right, "plugin", "exec-renderer")
	writeExecPluginPolicy(t, left, "exec-renderer", appExecCommand(t, "manifest"))
	writeTestFile(t, filepath.Join(right, ".drydock", "plugins.yaml"), `apiVersion: v1
kind: PluginPolicy
`)
	writeTestFile(t, filepath.Join(left, "manifests", "plugin", "marker.txt"), "left")
	writeTestFile(t, filepath.Join(right, "manifests", "plugin", "marker.txt"), "right")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:      left,
		RightPath:     right,
		ChangedOnly:   false,
		Unified:       3,
		PluginOptions: PluginOptions{EnablePlugins: true},
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginPolicyInvalid) {
		t.Fatalf("Diagnostics = %#v, right-side policy should be ignored", result.Diagnostics)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1: %#v", len(result.Results), result.Results)
	}
}

func TestApplicationRenderCacheDisablesMatchedExecPolicy(t *testing.T) {
	policy, _ := testExecPluginPolicy(t, "cue", appExecCommand(t, "manifest"))
	key, err := applicationRenderCacheKey(renderContext{
		request: BuildRequest{PluginOptions: PluginOptions{pluginPolicy: policy}},
	}, pluginApplication("plugin"))
	if err != nil {
		t.Fatalf("applicationRenderCacheKey() error = %v", err)
	}
	if key != "" {
		t.Fatalf("applicationRenderCacheKey() = %q, want empty key for matched exec policy", key)
	}
}

func TestOrchestratorBuildRejectsInvalidPresentDefaultPluginPolicy(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: v1
kind: PluginPolicy
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want invalid policy error")
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginPolicyInvalid) {
		t.Fatalf("Diagnostics = %#v, want plugin.policy.invalid", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersNativeKustomizePluginFromHelmValues(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writePluginPolicy(t, root, "kustomize-build-with-helm", "native-kustomize")
	writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build-with-helm", "", "sh, -c", "kustomize build --enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "native")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "native"); !ok {
		t.Fatalf("Manifests = %#v, want native ConfigMap", result.Manifests)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "plugin", Status: ApplicationStatusPass},
	})
}

func TestOrchestratorBuildDoesNotRenderNativeKustomizePluginWithoutPolicy(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build-with-helm", "", "sh, -c", "kustomize build --enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "native")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want trusted policy requirement")
	}
	if _, ok := manifestByName(result.Manifests, "native"); ok {
		t.Fatalf("Manifests = %#v, native Kustomize CMP rendered without policy", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersNativeKustomizePluginFromRawHelmValues(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writePluginPolicy(t, root, "kustomize-build-with-helm", "native-kustomize")
	writeNativeKustomizeCMPRawHelmValues(t, root, "kustomize-build-with-helm", "", "kustomize, build", "--enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "raw-helm-values")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "raw-helm-values"); !ok {
		t.Fatalf("Manifests = %#v, want ConfigMap from raw Helm values CMP", result.Manifests)
	}
}

func TestOrchestratorBuildRendersNativeKustomizePluginFromRenderedConfigMap(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writePluginPolicy(t, root, "kustomize-build-with-helm", "native-kustomize")
	writeNativeKustomizeCMPConfigMap(t, root, "kustomize-build-with-helm", "", "kustomize, build", "--enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "rendered-cmp")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "rendered-cmp"); !ok {
		t.Fatalf("Manifests = %#v, want ConfigMap from rendered CMP ConfigMap", result.Manifests)
	}
}

func TestOrchestratorBuildMatchesVersionedNativeKustomizePlugin(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm-v2")
	writePluginPolicy(t, root, "kustomize-build-with-helm-v2", "native-kustomize")
	writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build-with-helm", "v2", "kustomize, build", "., --enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "versioned")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "versioned"); !ok {
		t.Fatalf("Manifests = %#v, want ConfigMap from versioned plugin", result.Manifests)
	}
}

func TestOrchestratorBuildRejectsNativeKustomizePluginWithInit(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writePluginPolicy(t, root, "kustomize-build-with-helm", "native-kustomize")
	writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cmp:
    plugins:
      kustomize-build-with-helm:
        init:
          command: [sh, -c]
          args: [echo preparing]
        generate:
          command: [sh, -c]
          args: [kustomize build --enable-helm]
`)
	writeNativeKustomizeSource(t, root, "plugin", "native")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported plugin error")
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("Manifests = %#v, want none", result.Manifests)
	}
}

func TestOrchestratorNativeKustomizePluginDoesNotInheritGlobalBuildOptions(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build")
	writePluginPolicy(t, root, "kustomize-build", "native-kustomize")
	writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cm:
    kustomize.buildOptions: --load-restrictor=LoadRestrictionsNone
  cmp:
    plugins:
      kustomize-build:
        generate:
          command: [kustomize, build]
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../shared/cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", "shared", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want load-restrictor failure")
	}
	if !strings.Contains(result.Statuses[0].Message, "security") && !strings.Contains(result.Statuses[0].Message, "restrict") {
		t.Fatalf("status message = %q, want root-only Kustomize restriction", result.Statuses[0].Message)
	}
}

func TestOrchestratorInjectedPluginRendererOverridesNativeKustomizePlugin(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cmp:
    plugins:
      kustomize-build-with-helm:
        init:
          command: [sh, -c]
          args: [echo would-fail-native]
        generate:
          command: [sh, -c]
          args: [kustomize build --enable-helm]
`)

	rendered := false
	result, err := (Orchestrator{PluginRenderer: internalPluginRendererFunc(func(_ context.Context, request render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		rendered = true
		if request.Plugin.Name != "kustomize-build-with-helm" {
			t.Fatalf("Plugin.Name = %q, want kustomize-build-with-helm", request.Plugin.Name)
		}
		return nil, nil, nil
	})}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !rendered {
		t.Fatal("injected plugin renderer was not called")
	}
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, did not expect native unsupported diagnostic", result.Diagnostics)
	}
}

func TestNativeKustomizePluginBuildOptionsFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		plugin  config.ConfigManagementPlugin
		wantErr bool
	}{
		{
			name: "direct argv with supported flags",
			plugin: config.ConfigManagementPlugin{
				Name:            "kustomize-build",
				GenerateCommand: []string{"kustomize", "build"},
				GenerateArgs:    []string{".", "--enable-helm", "--helm-api-versions", "example.io/v1/Foo"},
			},
		},
		{
			name: "safe shell wrapper",
			plugin: config.ConfigManagementPlugin{
				Name:            "kustomize-build",
				GenerateCommand: []string{"sh", "-c"},
				GenerateArgs:    []string{"kustomize build --enable-helm"},
			},
		},
		{
			name: "pipeline rejected",
			plugin: config.ConfigManagementPlugin{
				Name:            "kustomize-build",
				GenerateCommand: []string{"sh", "-c"},
				GenerateArgs:    []string{"kustomize build --enable-helm | sed s/a/b/"},
			},
			wantErr: true,
		},
		{
			name: "remote operand rejected",
			plugin: config.ConfigManagementPlugin{
				Name:            "kustomize-build",
				GenerateCommand: []string{"kustomize", "build"},
				GenerateArgs:    []string{"https://github.com/example/repo"},
			},
			wantErr: true,
		},
		{
			name: "init rejected",
			plugin: config.ConfigManagementPlugin{
				Name:            "kustomize-build",
				GenerateCommand: []string{"kustomize", "build"},
				HasInit:         true,
			},
			wantErr: true,
		},
		{
			name: "unknown option rejected",
			plugin: config.ConfigManagementPlugin{
				Name:            "kustomize-build",
				GenerateCommand: []string{"kustomize", "build"},
				GenerateArgs:    []string{"--enable-alpha-plugins"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := nativeKustomizePluginBuildOptions(tt.plugin)
			if tt.wantErr && err == nil {
				t.Fatal("nativeKustomizePluginBuildOptions() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("nativeKustomizePluginBuildOptions() error = %v", err)
			}
		})
	}
}

func TestOrchestratorPluginTimeoutReturnsPartialStatus(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writePluginBuildApplication(t, root, "plugin", "cue")

	result, err := (Orchestrator{PluginRenderer: blockingInternalPluginRenderer{}}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			PluginTimeout: time.Nanosecond,
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want plugin timeout")
	}
	if _, ok := manifestByName(result.Manifests, "ok"); !ok {
		t.Fatalf("Manifests = %#v, want successful non-plugin manifest", result.Manifests)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "ok", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "plugin", Status: ApplicationStatusFail},
	})
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func writeNativeKustomizeCMPHelmValues(t *testing.T, root, name, version, command, args string) {
	t.Helper()
	versionBlock := ""
	if version != "" {
		versionBlock = "        version: " + version + "\n"
	}
	writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cmp:
    plugins:
      `+name+`:
`+versionBlock+`        generate:
          command: [`+command+`]
          args: [`+args+`]
`)
}

func writePluginPolicy(t *testing.T, root, name, engine string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: `+engine+`
`)
}

func writeExecPluginPolicy(t *testing.T, root, name string, command []string) {
	t.Helper()
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, yamlSingleQuoted(arg))
	}
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: exec
    generate:
      command: [`+strings.Join(quoted, ", ")+`]
      timeout: 2s
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
`)
}

func testExecPluginPolicy(t *testing.T, name string, command []string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeExecPluginPolicy(t, root, name, command)
	return readTestPluginPolicy(t, root)
}

func testExecPluginPolicyWithPostRenderer(t *testing.T, name string, command, postRenderer []string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeExecPluginPolicyWithPostRenderer(t, root, name, command, postRenderer)
	return readTestPluginPolicy(t, root)
}

func readTestPluginPolicy(t *testing.T, root string) (pluginpolicy.Policy, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".drydock", "plugins.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	policy, err := pluginpolicy.Parse(".drydock/plugins.yaml", data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	fingerprint, err := pluginpolicy.Fingerprint(policy)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	return policy, fingerprint
}

func writeExecPluginPolicyWithPostRenderer(t *testing.T, root, name string, command, postRenderer []string) {
	t.Helper()
	quotedCommand := make([]string, 0, len(command))
	for _, arg := range command {
		quotedCommand = append(quotedCommand, yamlSingleQuoted(arg))
	}
	quotedPostRenderer := make([]string, 0, len(postRenderer))
	for _, arg := range postRenderer {
		quotedPostRenderer = append(quotedPostRenderer, yamlSingleQuoted(arg))
	}
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: exec
    generate:
      command: [`+strings.Join(quotedCommand, ", ")+`]
      timeout: 2s
    postRenderers:
      - command: [`+strings.Join(quotedPostRenderer, ", ")+`]
        timeout: 2s
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
`)
}

func appExecCommand(t *testing.T, mode string) []string {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return []string{path, "-test.run=TestAppExecPluginHelperProcess", "--", mode}
}

func yamlSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func TestAppExecPluginHelperProcess(t *testing.T) {
	if os.Getenv("DRYDOCK_APP_EXEC_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	switch args[1] {
	case "manifest":
		marker, _ := os.ReadFile("marker.txt")
		value := strings.TrimSpace(string(marker))
		if value == "" {
			value = "rendered"
		}
		_ = os.WriteFile("generated.txt", []byte("temp"), 0o644)
		fmt.Println("apiVersion: v1")
		fmt.Println("kind: ConfigMap")
		fmt.Println("metadata:")
		fmt.Println("  name: exec-rendered")
		fmt.Println("data:")
		fmt.Printf("  marker: %q\n", value)
		fmt.Printf("  env: %q\n", os.Getenv("DRYDOCK_APP_EXEC_VALUE"))
		os.Exit(0)
	case "invalid":
		fmt.Println("metadata: [")
		os.Exit(0)
	case "post-render":
		input, _ := io.ReadAll(os.Stdin)
		fmt.Print(string(input))
		fmt.Println("  post: rendered")
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func writeNativeKustomizeCMPRawHelmValues(t *testing.T, root, name, version, command, args string) {
	t.Helper()
	versionBlock := ""
	if version != "" {
		versionBlock = "          version: " + version + "\n"
	}
	writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cmp:
    plugins:
      `+name+`.yaml: |
        apiVersion: argoproj.io/v1alpha1
        kind: ConfigManagementPlugin
        metadata:
          name: `+name+`
        spec:
`+versionBlock+`          generate:
            command: [`+command+`]
            args: [`+args+`]
`)
}

func writeNativeKustomizeCMPConfigMap(t *testing.T, root, name, version, command, args string) {
	t.Helper()
	versionBlock := ""
	if version != "" {
		versionBlock = "      version: " + version + "\n"
	}
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cmp-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmp-cm
data:
  `+name+`.yaml: |
    apiVersion: argoproj.io/v1alpha1
    kind: ConfigManagementPlugin
    metadata:
      name: `+name+`
    spec:
`+versionBlock+`      generate:
        command: [`+command+`]
        args: [`+args+`]
`)
}

func writeNativeKustomizeSource(t *testing.T, root, appName, configMapName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "manifests", appName, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
`)
}
func TestOrchestratorInjectedPluginRendererErrorPreservesDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writePluginBuildApplication(t, root, "plugin", "cue")

	result, err := (Orchestrator{PluginRenderer: failingInternalPluginRenderer{}}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want plugin renderer error")
	}
	if _, ok := manifestByName(result.Manifests, "ok"); !ok {
		t.Fatalf("Manifests = %#v, want successful non-plugin manifest", result.Manifests)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "ok", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "plugin", Status: ApplicationStatusFail},
	})
	if !hasDiagnosticCode(result.Diagnostics, "plugin.custom") {
		t.Fatalf("Diagnostics = %#v, want renderer diagnostic", result.Diagnostics)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}
