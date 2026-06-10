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
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
			if !strings.Contains(result.Statuses[0].Message, "config management plugin cue is not supported by the default renderer") {
				t.Fatalf("status message = %q, want unsupported plugin renderer", result.Statuses[0].Message)
			}
			if !strings.Contains(result.Statuses[0].Message, "no compatible native renderer") {
				t.Fatalf("status message = %q, want native renderer guidance", result.Statuses[0].Message)
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
	if !hasDiagnosticMessage(result.Diagnostics, "no compatible native renderer") {
		t.Fatalf("Diagnostics = %#v, want native renderer guidance", result.Diagnostics)
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
	if len(result.PluginExecutions) != 0 {
		t.Fatalf("PluginExecutions = %#v, native AVP policy should not record exec metadata", result.PluginExecutions)
	}
}

func TestOrchestratorBuildRendersDefaultAVPCompatPluginDirectory(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", argocdVaultPluginName)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-directory
  annotations:
    avp.kubernetes.io/path: vaults/K8s/items/cloudflare-tunnel-secret
data:
  target: <CLOUDFLARE_TUNNEL_ID>.cfargotunnel.com
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-directory", "target", ".cfargotunnel.com")
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, default AVP plugin compatibility should be quiet", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersDefaultAVPCompatPluginKustomizePath(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", argocdVaultPluginName)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-kustomize
  annotations:
    avp.kubernetes.io/path: vaults/K8s/items/ip-address
data:
  address: <COMPARTILHADO_IP>:2049
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-kustomize", "address", ":2049")
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, default AVP plugin compatibility should be quiet", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersDefaultAVPCompatPluginChartAtPath(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", argocdVaultPluginName)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "Chart.yaml"), `apiVersion: v2
name: avp-chart
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-chart-path
  annotations:
    avp.kubernetes.io/path: vaults/K8s/items/chart-secret
data:
  value: <CHART_VALUE>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-chart-path", "value", "")
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, default AVP plugin compatibility should be quiet", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersDefaultAVPCompatPluginChartOnly(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeTestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart-only-plugin
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
    plugin:
      name: argocd-vault-plugin
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-chart-only
  annotations:
    avp.kubernetes.io/path: vaults/K8s/items/chart-secret
data:
  value: <CHART_ONLY_VALUE>
`)

	result, err := (Orchestrator{ChartAcquirer: &recordingChartAcquirer{chartDir: chartDir}}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-chart-only", "value", "")
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, default AVP plugin compatibility should be quiet", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersDefaultAVPCompatPluginWithoutCompatFlag(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", argocdVaultPluginName)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-without-flag
data:
  value: <path:vaults/K8s/items/demo#value>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-without-flag", "value", "")
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, did not want plugin.unsupported", result.Diagnostics)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, default AVP plugin compatibility should be quiet", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersDiscoveredAVPCompatAliasWithoutPolicy(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "avp-directory-include")
	writeCMPConfigMapSpec(t, root, "avp-directory-include", `
generate:
  command: ["bash", "-c"]
  args: ["argocd-vault-plugin generate ./"]
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-alias
  annotations:
    avp.kubernetes.io/path: vaults/K8s/items/cloudflare-tunnel-secret
data:
  target: <CLOUDFLARE_TUNNEL_ID>.cfargotunnel.com
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-alias", "target", ".cfargotunnel.com")
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, did not want plugin.unsupported", result.Diagnostics)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, discovered AVP alias compatibility should be quiet", result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsDiscoveredAVPCompatAliasWithUnsafeCommand(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "avp-directory-include")
	writeCMPConfigMapSpec(t, root, "avp-directory-include", `
generate:
  command: ["bash", "-c"]
  args: ["echo before && argocd-vault-plugin generate ./"]
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
data:
  value: <path:vaults/K8s/items/demo#value>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported plugin error")
	}
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, unsafe AVP alias command rendered through compatibility", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsDiscoveredAVPCompatAliasWithoutPathOperand(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "avp-directory-include")
	writeCMPConfigMapSpec(t, root, "avp-directory-include", `
generate:
  command: ["argocd-vault-plugin", "generate"]
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
data:
  value: <path:vaults/K8s/items/demo#value>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported plugin error")
	}
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, pathless AVP alias command rendered through compatibility", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsDiscoveredAVPCompatAliasShellWithoutPathOperand(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "avp-directory-include")
	writeCMPConfigMapSpec(t, root, "avp-directory-include", `
generate:
  command: ["bash", "-c"]
  args: ["argocd-vault-plugin generate"]
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
data:
  value: <path:vaults/K8s/items/demo#value>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported plugin error")
	}
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, pathless shell AVP alias command rendered through compatibility", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
}

func TestOrchestratorBuildDoesNotUseAVPCompatForOtherPluginNames(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "cue")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
data:
  value: <path:vaults/K8s/items/demo#value>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, PluginOptions: PluginOptions{EnableAVPCompat: true}})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported plugin error")
	}
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, non-AVP plugin source rendered by AVP compatibility", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsDefaultAVPCompatPluginEnv(t *testing.T) {
	root := t.TempDir()
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
      name: argocd-vault-plugin
      env:
        - name: AVP_TYPE
          value: 1password
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, PluginOptions: PluginOptions{EnableAVPCompat: true}})
	if err == nil {
		t.Fatal("Build() error = nil, want env rejection")
	}
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, env-bearing AVP plugin source rendered", result.Manifests)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "env or parameters") {
		t.Fatalf("Diagnostics = %#v, want env/parameters rejection", result.Diagnostics)
	}
}

func TestOrchestratorBuildUsesPolicyBeforeDefaultAVPCompat(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", argocdVaultPluginName)
	writePluginPolicy(t, root, argocdVaultPluginName, "avp-compat")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-policy
data:
  domain: argocd.<path:vaults/Kubernetes/items/cluster#domain>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, PluginOptions: PluginOptions{EnableAVPCompat: true}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertRedactedDataValue(t, result.Manifests, "avp-policy", "domain", "")
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, trusted policy AVP should stay quiet", result.Diagnostics)
	}
}

func TestOrchestratorInjectedPluginRendererOverridesDefaultAVPCompat(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", argocdVaultPluginName)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render
data:
  value: <path:vaults/K8s/items/demo#value>
`)

	rendered := false
	result, err := (Orchestrator{PluginRenderer: internalPluginRendererFunc(func(_ context.Context, request render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		rendered = true
		if request.Plugin.Name != argocdVaultPluginName {
			t.Fatalf("Plugin.Name = %q, want %s", request.Plugin.Name, argocdVaultPluginName)
		}
		return []render.Manifest{{
			Object: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name": "injected-avp-renderer",
				},
			}},
		}}, nil, nil
	})}).Build(context.Background(), BuildRequest{Path: root, PluginOptions: PluginOptions{EnableAVPCompat: true}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !rendered {
		t.Fatal("injected plugin renderer was not called")
	}
	if _, ok := manifestByName(result.Manifests, "injected-avp-renderer"); !ok {
		t.Fatalf("Manifests = %#v, want injected renderer manifest", result.Manifests)
	}
	if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
		t.Fatalf("Manifests = %#v, default AVP compat hijacked injected renderer", result.Manifests)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, injected renderer should own plugin source", result.Diagnostics)
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

func assertRedactedDataValue(t *testing.T, manifests []render.Manifest, name string, key string, suffix string) {
	t.Helper()
	manifest, ok := manifestByName(manifests, name)
	if !ok {
		t.Fatalf("Manifests = %#v, want %s", manifests, name)
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", key)
	if err != nil || !found {
		t.Fatalf("data.%s = %q, found %v, err %v", key, value, found, err)
	}
	if strings.Contains(value, "<") || !strings.Contains(value, "drydock-redacted-") {
		t.Fatalf("data.%s = %q, want redacted AVP value", key, value)
	}
	if suffix != "" && !strings.HasSuffix(value, suffix) {
		t.Fatalf("data.%s = %q, want suffix %q", key, value, suffix)
	}
}

func TestOrchestratorBuildRejectsExecPolicyWithoutEnablePlugins(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeExecPluginPolicy(t, root, "exec-renderer", appExecCommand(t, "manifest"))
	writeNativeKustomizeCMPHelmValues(t, root, "exec-renderer", "", "kustomize, build", "")
	writeNativeKustomizeSource(t, root, "plugin", "native-fallback")

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
	if _, ok := manifestByName(result.Manifests, "native-fallback"); ok {
		t.Fatalf("Manifests = %#v, exec policy failure fell through to auto-native Kustomize", result.Manifests)
	}
}

func TestOrchestratorBuildRejectsExecPolicyWithoutTrustedRef(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeExecPluginPolicy(t, root, "exec-renderer", appExecCommand(t, "manifest"))
	writeNativeKustomizeCMPHelmValues(t, root, "exec-renderer", "", "kustomize, build", "")
	writeNativeKustomizeSource(t, root, "plugin", "native-fallback")

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
	if !hasDiagnosticMessage(result.Diagnostics, "diff baseline") || !hasDiagnosticMessage(result.Diagnostics, "--plugin-policy-ref") {
		t.Fatalf("Diagnostics = %#v, want baseline or plugin-policy-ref guidance", result.Diagnostics)
	}
	if _, ok := manifestByName(result.Manifests, "native-fallback"); ok {
		t.Fatalf("Manifests = %#v, untrusted exec policy failure fell through to auto-native Kustomize", result.Manifests)
	}
}

func TestOrchestratorBuildRendersTrustedExecPolicyPlugin(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	writeCMPConfigMapSpec(t, root, "auto-cmp", `discover:
  fileName: marker.txt
`)
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
	assertExecPolicyManifestData(t, result.Manifests)
	if _, err := os.Stat(filepath.Join(root, "manifests", "plugin", "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("original source generated.txt exists or unexpected stat error: %v", err)
	}
	assertExecPolicyPluginExecution(t, result.PluginExecutions)
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginAutoDiscovery) {
		t.Fatalf("Diagnostics = %#v, did not want auto-discovery warning for trusted exec policy plugin", result.Diagnostics)
	}
}

func TestOrchestratorBuildUsesInjectedExecPolicyRunner(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	policy, fingerprint := testExecPluginPolicy(t, "exec-renderer", []string{"renderer"})
	runner := &recordingExecRunner{
		result: pluginexec.Result{
			Stdout: []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: injected-runner
data:
  value: from-fake
`),
			Executions: []pluginexec.Execution{{
				Phase:    "generate",
				Command:  "renderer",
				Duration: 12 * time.Millisecond,
			}},
		},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).Build(context.Background(), BuildRequest{
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
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if len(result.Manifests) != 1 || result.Manifests[0].Object.GetName() != "injected-runner" {
		t.Fatalf("Manifests = %#v, want injected runner manifest", result.Manifests)
	}
	assertExecPolicyPluginExecution(t, result.PluginExecutions)
}

func TestOrchestratorBuildRendersTrustedContainerPolicyPlugin(t *testing.T) {
	root := t.TempDir()
	pluginCacheDir := filepath.Join(t.TempDir(), "plugin-cache")
	writePluginBuildApplication(t, root, "plugin", "container-renderer")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	policy, fingerprint := testContainerPluginPolicy(t, "container-renderer")
	runner := &recordingContainerRunner{
		result: pluginexec.Result{
			Stdout: []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: container-runner
data:
  value: from-fake
`),
			Executions: []pluginexec.Execution{{
				Phase:    "generate",
				Command:  "pkl",
				Duration: 12 * time.Millisecond,
			}},
		},
	}

	result, err := (Orchestrator{PluginContainerRunner: runner}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			EnablePlugins:           true,
			PluginCacheDir:          pluginCacheDir,
			pluginPolicyLoaded:      true,
			pluginPolicy:            policy,
			pluginPolicyFingerprint: fingerprint,
			pluginPolicyExecTrusted: true,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if runner.lastRequest.CacheRoot != pluginCacheDir {
		t.Fatalf("container CacheRoot = %q, want %q", runner.lastRequest.CacheRoot, pluginCacheDir)
	}
	if len(result.Manifests) != 1 || result.Manifests[0].Object.GetName() != "container-runner" {
		t.Fatalf("Manifests = %#v, want container runner manifest", result.Manifests)
	}
	if len(result.PluginExecutions) != 1 {
		t.Fatalf("PluginExecutions = %#v, want one container execution", result.PluginExecutions)
	}
	execution := result.PluginExecutions[0]
	if execution.Engine != "container" || execution.Runtime != "docker" || execution.Image != "registry.example.test/plugins/render@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("PluginExecution = %#v, want container runtime/image metadata", execution)
	}
}

func TestOrchestratorBuildRejectsContainerPolicyWithoutEnablePlugins(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "container-renderer")
	policy, fingerprint := testContainerPluginPolicy(t, "container-renderer")

	result, err := (Orchestrator{PluginContainerRunner: &recordingContainerRunner{}}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			pluginPolicyLoaded:      true,
			pluginPolicy:            policy,
			pluginPolicyFingerprint: fingerprint,
			pluginPolicyExecTrusted: true,
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want enable-plugins failure")
	}
	if !hasDiagnosticMessage(result.Diagnostics, "requires --enable-plugins") {
		t.Fatalf("Diagnostics = %#v, want enable-plugins guidance", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersTrustedExecPolicyUnnamedPluginMatch(t *testing.T) {
	tests := []struct {
		name      string
		matchYAML string
		files     map[string]string
	}{
		{
			name: "fileName",
			matchYAML: `    match:
      discover:
        fileName: match.txt
`,
			files: map[string]string{"match.txt": "match"},
		},
		{
			name: "find glob",
			matchYAML: `    match:
      discover:
        find:
          glob: "**/match.txt"
`,
			files: map[string]string{"nested/match.txt": "match"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeUnnamedPluginBuildApplication(t, root, "plugin")
			writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
			for path, content := range tt.files {
				writeTestFile(t, filepath.Join(root, "manifests", "plugin", filepath.FromSlash(path)), content)
			}
			t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
			t.Setenv("DRYDOCK_APP_EXEC_VALUE", "allowed-value")
			policy, fingerprint := testExecPluginPolicyWithMatch(t, "exec-renderer", appExecCommand(t, "manifest"), tt.matchYAML)

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
			assertExecPolicyManifestData(t, result.Manifests)
			assertExecPolicyPluginExecution(t, result.PluginExecutions)
		})
	}
}

func TestOrchestratorBuildRejectsUnnamedPluginWithoutPolicyOwnedMatch(t *testing.T) {
	root := t.TempDir()
	writeUnnamedPluginBuildApplication(t, root, "plugin")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "match")
	writeCMPConfigMapSpec(t, root, "exec-renderer", `discover:
  fileName: marker.txt
`)
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
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
	if err == nil {
		t.Fatal("Build() error = nil, want unnamed plugin match failure")
	}
	if !hasDiagnosticMessage(result.Diagnostics, "no trusted plugin policy match.discover") {
		t.Fatalf("Diagnostics = %#v, want policy-owned match failure", result.Diagnostics)
	}
	if len(result.PluginExecutions) != 0 {
		t.Fatalf("PluginExecutions = %#v, want no execution", result.PluginExecutions)
	}
}

func TestOrchestratorBuildRejectsAmbiguousUnnamedPluginPolicyMatch(t *testing.T) {
	root := t.TempDir()
	writeUnnamedPluginBuildApplication(t, root, "plugin")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "match.txt"), "match")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policyRoot := t.TempDir()
	command := strings.Join(yamlSingleQuotedList(appExecCommand(t, "manifest")), ", ")
	writeTestFile(t, filepath.Join(policyRoot, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  alpha:
    engine: exec
    match:
      discover:
        fileName: match.txt
    generate:
      command: [`+command+`]
      timeout: `+testExecPolicyCommandTimeout+`
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
  beta:
    engine: exec
    match:
      discover:
        fileName: match.txt
    generate:
      command: [`+command+`]
      timeout: `+testExecPolicyCommandTimeout+`
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
`)
	policy, fingerprint := readTestPluginPolicy(t, policyRoot)

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
		t.Fatal("Build() error = nil, want ambiguous unnamed plugin match")
	}
	if !hasDiagnosticMessage(result.Diagnostics, "ambiguous") || !hasDiagnosticMessage(result.Diagnostics, "alpha via match.discover, beta via match.discover") {
		t.Fatalf("Diagnostics = %#v, want ambiguous alpha, beta match", result.Diagnostics)
	}
	if len(result.PluginExecutions) != 0 {
		t.Fatalf("PluginExecutions = %#v, want no execution", result.PluginExecutions)
	}
}

func TestOrchestratorBuildDoesNotSelectUnnamedPluginPolicyMatchThroughSymlink(t *testing.T) {
	root := t.TempDir()
	writeUnnamedPluginBuildApplication(t, root, "plugin")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "target.txt"), "match")
	if err := os.Symlink("target.txt", filepath.Join(root, "manifests", "plugin", "match.txt")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithMatch(t, "exec-renderer", appExecCommand(t, "manifest"), `    match:
      discover:
        fileName: match.txt
`)

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
		t.Fatal("Build() error = nil, want symlink match to fail closed")
	}
	if !hasDiagnosticMessage(result.Diagnostics, "no trusted plugin policy match.discover") {
		t.Fatalf("Diagnostics = %#v, want no match through symlink", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersTrustedExecPolicyPluginParameters(t *testing.T) {
	root := t.TempDir()
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
      name: exec-renderer
      parameters:
        - name: path
          string: components/app.pkl
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithParameters(t, "exec-renderer", append(appExecCommand(t, "params"), "{{param:path}}"))

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
	manifest, ok := manifestByName(result.Manifests, "exec-params")
	if !ok {
		t.Fatalf("Manifests = %#v, want exec-params", result.Manifests)
	}
	data := configMapData(t, manifest)
	if data["arg"] != "components/app.pkl" || data["param"] != "components/app.pkl" {
		t.Fatalf("data = %#v, want expanded argv and PARAM_PATH env", data)
	}
	if !strings.Contains(fmt.Sprint(data["json"]), `"name":"path"`) || !strings.Contains(fmt.Sprint(data["json"]), `"components/app.pkl"`) {
		t.Fatalf("data.json = %#v, want ARGOCD_APP_PARAMETERS JSON", data["json"])
	}
}

func TestOrchestratorBuildRendersTrustedExecPolicyPluginRepositoryParameter(t *testing.T) {
	root := t.TempDir()
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
      name: exec-renderer
      parameters:
        - name: path
          string: shared/config.txt
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "source")
	writeTestFile(t, filepath.Join(root, "shared", "config.txt"), "repo-level")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithRepositoryParameter(t, "exec-renderer", append(appExecCommand(t, "repo-param"), "{{param:path}}"))

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
		t.Fatalf("Build() error = %v", err)
	}
	manifest, ok := manifestByName(result.Manifests, "exec-repo-param")
	if !ok {
		t.Fatalf("Manifests = %#v, want exec repo parameter manifest", result.Manifests)
	}
	data := configMapData(t, manifest)
	if data["value"] != "repo-level" {
		t.Fatalf("data.value = %#v, want repo-level", data["value"])
	}
}

func TestOrchestratorBuildRedactsExecPolicyPluginParameterValues(t *testing.T) {
	root := t.TempDir()
	const secret = "top-secret-parameter"
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
      name: exec-renderer
      parameters:
        - name: token
          string: `+secret+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "source")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithStringParameter(t, "exec-renderer", append(appExecCommand(t, "manifest"), "../{{param:token}}"))

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
		t.Fatal("Build() error = nil, want invalid argument failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked parameter value: %v", err)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, secret) {
			t.Fatalf("diagnostic leaked parameter value: %#v", diag)
		}
	}
}

func TestOrchestratorBuildRedactsExecPolicyPluginRepositoryParameterFailures(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		command     []string
		include     string
		allow       string
		want        string
		writeSecret bool
	}{
		{
			name:    "escaped repo path",
			value:   "../sentinel-secret.yaml",
			include: `["shared/**"]`,
			allow:   `["**/*.yaml"]`,
			want:    "escapes",
		},
		{
			name:        "excluded repo path",
			value:       "private/sentinel-secret.txt",
			include:     `["shared/**"]`,
			allow:       `["private/*.txt"]`,
			want:        "copy.include",
			writeSecret: true,
		},
		{
			name:        "argv escape",
			value:       "shared/sentinel-secret.txt",
			command:     append(appExecCommand(t, "repo-param"), "../{{param:path}}"),
			include:     `["shared/**"]`,
			allow:       `["shared/*.txt"]`,
			want:        "<redacted>",
			writeSecret: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			command := tt.command
			if len(command) == 0 {
				command = append(appExecCommand(t, "repo-param"), "{{param:path}}")
			}
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
      name: exec-renderer
      parameters:
        - name: path
          string: `+tt.value+`
  destination:
    name: in-cluster
    namespace: default
`)
			writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "source")
			if tt.writeSecret {
				writeTestFile(t, filepath.Join(root, filepath.FromSlash(tt.value)), "repo-level")
			}
			t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
			policyRoot := t.TempDir()
			writeExecPluginPolicyWithParameters(t, policyRoot, "exec-renderer", command, `      allow:
        - name: path
          type: string
          required: true
          path:
            base: repository
            allow: `+tt.allow+`
`, `    copy:
      scope: repository
      include: `+tt.include+`
`)
			policy, fingerprint := readTestPluginPolicy(t, policyRoot)

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
				t.Fatal("Build() error = nil, want repository parameter failure")
			}
			if strings.Contains(err.Error(), "sentinel-secret") || hasDiagnosticMessage(result.Diagnostics, "sentinel-secret") {
				t.Fatalf("result leaked repository parameter: err=%v diagnostics=%#v", err, result.Diagnostics)
			}
			if !strings.Contains(err.Error(), tt.want) && !hasDiagnosticMessage(result.Diagnostics, tt.want) {
				t.Fatalf("result = err=%v diagnostics=%#v, want %q", err, result.Diagnostics, tt.want)
			}
		})
	}
}

func TestOrchestratorBuildRedactsExecPolicyPluginRepositoryParameterStagingFailure(t *testing.T) {
	root := t.TempDir()
	const sentinel = "sentinel-secret"
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
      name: exec-renderer
      parameters:
        - name: path
          string: shared/`+sentinel+`.txt
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "source")
	writeTestFile(t, filepath.Join(root, "shared", "target.txt"), "repo-level")
	if err := os.Symlink("target.txt", filepath.Join(root, "shared", sentinel+".txt")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithRepositoryParameter(t, "exec-renderer", append(appExecCommand(t, "repo-param"), "{{param:path}}"))

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
		t.Fatal("Build() error = nil, want symlink staging failure")
	}
	if strings.Contains(err.Error(), sentinel) || hasDiagnosticMessage(result.Diagnostics, sentinel) {
		t.Fatalf("result leaked repository parameter: err=%v diagnostics=%#v", err, result.Diagnostics)
	}
	if !strings.Contains(err.Error(), "<redacted>") && !hasDiagnosticMessage(result.Diagnostics, "<redacted>") {
		t.Fatalf("result = err=%v diagnostics=%#v, want redacted marker", err, result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsExecPolicyPluginParameterTemplateInExecutable(t *testing.T) {
	root := t.TempDir()
	const secret = "top-secret-tool"
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
      name: exec-renderer
      parameters:
        - name: tool
          string: `+secret+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "source")
	policy, fingerprint := testExecPluginPolicyWithStringParameterName(t, "exec-renderer", []string{"{{param:tool}}"}, "tool")

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
		t.Fatal("Build() error = nil, want executable template rejection")
	}
	if strings.Contains(err.Error(), secret) || hasDiagnosticMessage(result.Diagnostics, secret) {
		t.Fatalf("result leaked parameter value: err=%v diagnostics=%#v", err, result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "command executable") {
		t.Fatalf("Diagnostics = %#v, want command executable rejection", result.Diagnostics)
	}
}

func TestOrchestratorBuildRedactsExecPolicyPluginParameterInvalidOutput(t *testing.T) {
	root := t.TempDir()
	const secret = "top-secret-output-token"
	const secretPrefix = "top-secret-output"
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
      name: exec-renderer
      parameters:
        - name: token
          string: `+secret+`
        - name: prefix
          string: `+secretPrefix+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "source")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policyRoot := t.TempDir()
	writeExecPluginPolicyWithParameters(t, policyRoot, "exec-renderer", append(appExecCommand(t, "invalid-param"), "{{param:token}}"), `      allow:
        - name: token
          type: string
        - name: prefix
          type: string
`)
	policy, fingerprint := readTestPluginPolicy(t, policyRoot)

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
		t.Fatal("Build() error = nil, want invalid output failure")
	}
	if strings.Contains(err.Error(), secret) || hasDiagnosticMessage(result.Diagnostics, secret) ||
		strings.Contains(err.Error(), secretPrefix) || hasDiagnosticMessage(result.Diagnostics, secretPrefix) ||
		strings.Contains(err.Error(), "<redacted>-token") || hasDiagnosticMessage(result.Diagnostics, "<redacted>-token") {
		t.Fatalf("result leaked parameter value: err=%v diagnostics=%#v", err, result.Diagnostics)
	}
}

func TestOrchestratorBuildRejectsExecPolicyPluginParameterNameWhitespace(t *testing.T) {
	root := t.TempDir()
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
      name: exec-renderer
      parameters:
        - name: " path "
          string: components/app.pkl
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "source")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicyWithParameters(t, "exec-renderer", append(appExecCommand(t, "params"), "{{param:path}}"))

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
		t.Fatal("Build() error = nil, want invalid parameter name")
	}
	if !hasDiagnosticMessage(result.Diagnostics, "invalid Application plugin parameter name") {
		t.Fatalf("Diagnostics = %#v, want invalid parameter name", result.Diagnostics)
	}
}

func assertExecPolicyManifestData(t *testing.T, manifests []render.Manifest) {
	t.Helper()
	manifest, ok := manifestByName(manifests, "exec-rendered")
	if !ok {
		t.Fatalf("Manifests = %#v, want exec-rendered", manifests)
	}
	data := configMapData(t, manifest)
	if data["marker"] != "from-source" || data["env"] != "allowed-value" {
		t.Fatalf("data = %#v, want source marker and allowed env", data)
	}
}

func configMapData(t *testing.T, manifest render.Manifest) map[string]any {
	t.Helper()
	data, ok := manifest.Object.Object["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want map", manifest.Object.Object["data"])
	}
	return data
}

func assertExecPolicyPluginExecution(t *testing.T, executions []PluginExecution) {
	t.Helper()
	if len(executions) != 1 {
		t.Fatalf("PluginExecutions = %#v, want one generate execution", executions)
	}
	execution := executions[0]
	if execution.AppNamespace != "argocd" || execution.AppName != "plugin" || execution.PluginName != "exec-renderer" {
		t.Fatalf("PluginExecution app/plugin = %#v, want argocd/plugin exec-renderer", execution)
	}
	if execution.Engine != "exec" || execution.Phase != "generate" || execution.Command == "" || execution.Duration == "" {
		t.Fatalf("PluginExecution = %#v, want exec generate metadata", execution)
	}
	if execution.SourceIndex != 0 || execution.SourcePath != "manifests/plugin" {
		t.Fatalf("PluginExecution source = %#v, want source index 0 path manifests/plugin", execution)
	}
}

type recordingExecRunner struct {
	calls  int
	result pluginexec.Result
	err    error
}

func (r *recordingExecRunner) Run(context.Context, pluginexec.Request) (pluginexec.Result, error) {
	r.calls++
	return r.result, r.err
}

type recordingContainerRunner struct {
	calls       int
	lastRequest plugincontainer.Request
	result      pluginexec.Result
	err         error
}

func (r *recordingContainerRunner) Run(_ context.Context, request plugincontainer.Request) (pluginexec.Result, error) {
	r.calls++
	r.lastRequest = request
	return r.result, r.err
}

func TestOrchestratorDiagReturnsExecPolicyPluginMetadata(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	policy, fingerprint := testExecPluginPolicy(t, "exec-renderer", appExecCommand(t, "manifest"))

	result, err := (Orchestrator{}).Diag(context.Background(), DiagRequest{
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
		t.Fatalf("Diag() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if len(result.PluginExecutions) != 1 {
		t.Fatalf("PluginExecutions = %#v, want one generate execution", result.PluginExecutions)
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
	if !hasDiagnosticMessage(result.Diagnostics, "produced invalid generated manifests") {
		t.Fatalf("Diagnostics = %#v, want invalid generated manifest diagnostic", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, `source path "manifests/plugin"`) || !hasDiagnosticMessage(result.Diagnostics, "generate-output") {
		t.Fatalf("Diagnostics = %#v, want source path and decode target", result.Diagnostics)
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
	if !hasDiagnosticMessage(result.Diagnostics, "produced invalid final post-render manifests") {
		t.Fatalf("Diagnostics = %#v, want invalid final post-render diagnostic", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "final-post-render-output") {
		t.Fatalf("Diagnostics = %#v, want final post-render decode target", result.Diagnostics)
	}
	if hasDiagnosticMessage(result.Diagnostics, "metadata: [") {
		t.Fatalf("Diagnostics = %#v, leaked invalid output bytes", result.Diagnostics)
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

func TestApplicationRenderCacheDisablesUnnamedStaticExecPolicyMatch(t *testing.T) {
	policy, _ := testExecPluginPolicyWithMatch(t, "cue", appExecCommand(t, "manifest"), `    match:
      discover:
        fileName: match.txt
`)
	application := pluginApplication("plugin")
	application.Spec.Source.Plugin.Name = ""
	key, err := applicationRenderCacheKey(renderContext{
		request: BuildRequest{PluginOptions: PluginOptions{pluginPolicy: policy}},
	}, application)
	if err != nil {
		t.Fatalf("applicationRenderCacheKey() error = %v", err)
	}
	if key != "" {
		t.Fatalf("applicationRenderCacheKey() = %q, want empty key for unnamed static exec policy match", key)
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

func TestOrchestratorBuildRendersNativeKustomizePluginWithoutPolicy(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build-with-helm", "", "sh, -c", "kustomize build --enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "native")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "native"); !ok {
		t.Fatalf("Manifests = %#v, want native Kustomize CMP rendered without policy", result.Manifests)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "plugin", Status: ApplicationStatusPass},
	})
}

func TestOrchestratorBuildDisablePluginPolicyAllowsAutoNativeKustomize(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build-with-helm", "", "kustomize, build", "--enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "native")
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: v1
kind: PluginPolicy
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path: root,
		PluginOptions: PluginOptions{
			DisablePluginPolicy: true,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "native"); !ok {
		t.Fatalf("Manifests = %#v, want auto-native Kustomize despite disabled policy", result.Manifests)
	}
}

func TestOrchestratorBuildWarnsWhenSidecarCMPStaticDiscoveryMatchesImplicitNativeSource(t *testing.T) {
	tests := []struct {
		name     string
		discover string
	}{
		{
			name: "fileName",
			discover: `discover:
  fileName: cm.yaml
`,
		},
		{
			name: "find glob",
			discover: `discover:
  find:
    glob: "**/cm.yaml"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuildApplication(t, root, "implicit", "implicit-native")
			writeCMPConfigMapSpec(t, root, "auto-cmp", tt.discover)

			result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if _, ok := manifestByName(result.Manifests, "implicit-native"); !ok {
				t.Fatalf("Manifests = %#v, want native rendered ConfigMap", result.Manifests)
			}
			if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginAutoDiscovery) {
				t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, diagnostic.CodePluginAutoDiscovery)
			}
			if !hasDiagnosticMessage(result.Diagnostics, "did not run sidecar CMP auto-discovery") {
				t.Fatalf("Diagnostics = %#v, want auto-discovery boundary message", result.Diagnostics)
			}
		})
	}
}

func TestOrchestratorBuildDoesNotWarnForNonMatchingOrUnprovableSidecarCMPDiscovery(t *testing.T) {
	tests := []struct {
		name     string
		discover string
	}{
		{
			name: "static glob does not match",
			discover: `discover:
  fileName: no-such.yaml
`,
		},
		{
			name: "find command only",
			discover: `discover:
  find:
    command: [sh, -c]
    args: [find . -name cm.yaml]
`,
		},
		{
			name: "fileName precedence suppresses matching find glob",
			discover: `discover:
  fileName: no-such.yaml
  find:
    glob: "**/cm.yaml"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuildApplication(t, root, "implicit", "implicit-native")
			writeCMPConfigMapSpec(t, root, "auto-cmp", tt.discover)

			result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginAutoDiscovery) {
				t.Fatalf("Diagnostics = %#v, did not want auto-discovery warning", result.Diagnostics)
			}
		})
	}
}

func TestOrchestratorBuildDoesNotWarnForExplicitPluginSourcesWhenSidecarCMPDiscoveryMatches(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "cue")
	writeCMPConfigMapSpec(t, root, "auto-cmp", `discover:
  fileName: .keep
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unsupported explicit plugin")
	}
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginAutoDiscovery) {
		t.Fatalf("Diagnostics = %#v, did not want auto-discovery warning for explicit plugin source", result.Diagnostics)
	}
}

func TestOrchestratorBuildDoesNotWarnForNativeKustomizeCMPCompatibilityWhenDiscoveryMatches(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
	writeCMPConfigMapSpec(t, root, "kustomize-build-with-helm", `discover:
  fileName: kustomization.yaml
generate:
  command: [kustomize, build]
  args: [--enable-helm]
`)
	writeNativeKustomizeSource(t, root, "plugin", "native")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := manifestByName(result.Manifests, "native"); !ok {
		t.Fatalf("Manifests = %#v, want native Kustomize CMP rendered", result.Manifests)
	}
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginAutoDiscovery) {
		t.Fatalf("Diagnostics = %#v, did not want auto-discovery warning for explicit native CMP plugin", result.Diagnostics)
	}
}

func TestOrchestratorBuildRendersNativeKustomizePolicySeed(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-seed")
	writeNativeKustomizePluginPolicySeed(t, root, "kustomize-seed", "kustomize, build", "--enable-helm")
	writeNativeKustomizeSource(t, root, "plugin", "seeded")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if _, ok := manifestByName(result.Manifests, "seeded"); !ok {
		t.Fatalf("Manifests = %#v, want native Kustomize seed render", result.Manifests)
	}
}

func TestOrchestratorBuildPrefersNativeKustomizePolicySeedOverCurrentTreeCMP(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-seed")
	writeNativeKustomizePluginPolicySeed(t, root, "kustomize-seed", "kustomize, build", "--enable-helm")
	writeNativeKustomizeCMPHelmValues(t, root, "kustomize-seed", "", "sh, -c", "kustomize build | sed s/a/b/")
	writeNativeKustomizeSource(t, root, "plugin", "seeded")

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	if _, ok := manifestByName(result.Manifests, "seeded"); !ok {
		t.Fatalf("Manifests = %#v, want trusted seed to take precedence over current-tree CMP", result.Manifests)
	}
}

func TestOrchestratorBuildExecPolicyUsesSeedDiscoveryButNotSeedGenerate(t *testing.T) {
	root := t.TempDir()
	writeUnnamedPluginBuildApplication(t, root, "plugin")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "match.txt"), "match")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	t.Setenv("DRYDOCK_APP_EXEC_HELPER", "1")
	t.Setenv("DRYDOCK_APP_EXEC_VALUE", "allowed-value")
	policyRoot := t.TempDir()
	writeExecPluginPolicyWithSeed(t, policyRoot, "exec-renderer", appExecCommand(t, "manifest"), `    configManagementPlugin:
      discover:
        fileName: match.txt
      generate:
        command: ["drydock-seed-should-not-run"]
`)
	policy, fingerprint := readTestPluginPolicy(t, policyRoot)

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
	assertExecPolicyManifestData(t, result.Manifests)
	assertExecPolicyPluginExecution(t, result.PluginExecutions)
}

func TestOrchestratorBuildDoesNotWarnForAVPCompatPolicyWhenDiscoveryMatches(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "avp-directory-include")
	writePluginPolicy(t, root, "avp-directory-include", "avp-compat")
	writeCMPConfigMapSpec(t, root, "auto-cmp", `discover:
  fileName: cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: avp-redacted
data:
  domain: argocd.<path:vaults/Kubernetes/items/cluster#domain>
`)

	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginAutoDiscovery) {
		t.Fatalf("Diagnostics = %#v, did not want auto-discovery warning for AVP policy plugin", result.Diagnostics)
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
	if !hasDiagnosticMessage(result.Diagnostics, "plugin init is unsupported") {
		t.Fatalf("Diagnostics = %#v, want init rejection reason", result.Diagnostics)
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("Manifests = %#v, want none", result.Manifests)
	}
}

func TestOrchestratorBuildRejectsUnsafeAutoNativeKustomizePlugins(t *testing.T) {
	tests := []struct {
		name         string
		writeApp     func(t *testing.T, root string)
		writeCMP     func(t *testing.T, root string)
		writeSource  func(t *testing.T, root string)
		wantFragment string
		wantAbsent   string
	}{
		{
			name: "init",
			writeApp: func(t *testing.T, root string) {
				writePluginBuildApplication(t, root, "plugin", "kustomize-build")
			},
			writeCMP: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cmp:
    plugins:
      kustomize-build:
        init:
          command: [sh, -c]
          args: [echo preparing]
        generate:
          command: [kustomize, build]
`)
			},
			writeSource: func(t *testing.T, root string) {
				writeNativeKustomizeSource(t, root, "plugin", "should-not-render")
			},
			wantFragment: "plugin init is unsupported",
		},
		{
			name: "shell syntax",
			writeApp: func(t *testing.T, root string) {
				writePluginBuildApplication(t, root, "plugin", "kustomize-build")
			},
			writeCMP: func(t *testing.T, root string) {
				writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build", "", "sh, -c", "kustomize build | sed s/a/b/")
			},
			writeSource: func(t *testing.T, root string) {
				writeNativeKustomizeSource(t, root, "plugin", "should-not-render")
			},
			wantFragment: "shell command uses unsupported syntax",
		},
		{
			name: "remote operand",
			writeApp: func(t *testing.T, root string) {
				writePluginBuildApplication(t, root, "plugin", "kustomize-build")
			},
			writeCMP: func(t *testing.T, root string) {
				writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build", "", "kustomize, build", "https://user:secret-token@example.com/repo")
			},
			writeSource: func(t *testing.T, root string) {
				writeNativeKustomizeSource(t, root, "plugin", "should-not-render")
			},
			wantFragment: "unsupported kustomize build path or remote operand",
			wantAbsent:   "secret-token",
		},
		{
			name: "unsupported option",
			writeApp: func(t *testing.T, root string) {
				writePluginBuildApplication(t, root, "plugin", "kustomize-build")
			},
			writeCMP: func(t *testing.T, root string) {
				writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build", "", "kustomize, build", "--enable-alpha-plugins")
			},
			writeSource: func(t *testing.T, root string) {
				writeNativeKustomizeSource(t, root, "plugin", "should-not-render")
			},
			wantFragment: "unsupported kustomize build option",
		},
		{
			name: "Application plugin env",
			writeApp: func(t *testing.T, root string) {
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
      name: kustomize-build
      env:
        - name: FEATURE
          value: enabled
  destination:
    name: in-cluster
    namespace: default
`)
			},
			writeCMP: func(t *testing.T, root string) {
				writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build", "", "kustomize, build", "")
			},
			writeSource: func(t *testing.T, root string) {
				writeNativeKustomizeSource(t, root, "plugin", "should-not-render")
			},
			wantFragment: "Application plugin env or parameters are unsupported",
		},
		{
			name: "chart source",
			writeApp: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plugin
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://charts.example.test
    chart: plugin
    targetRevision: 1.0.0
    plugin:
      name: kustomize-build
  destination:
    name: in-cluster
    namespace: default
`)
			},
			writeCMP: func(t *testing.T, root string) {
				writeNativeKustomizeCMPHelmValues(t, root, "kustomize-build", "", "kustomize, build", "")
			},
			writeSource:  func(t *testing.T, root string) {},
			wantFragment: "chart sources are unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.writeApp(t, root)
			tt.writeCMP(t, root)
			tt.writeSource(t, root)

			result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
			if err == nil {
				t.Fatal("Build() error = nil, want unsupported native Kustomize plugin")
			}
			if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginUnsupported) {
				t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
			}
			if !hasDiagnosticMessage(result.Diagnostics, tt.wantFragment) {
				t.Fatalf("Diagnostics = %#v, want %q", result.Diagnostics, tt.wantFragment)
			}
			if tt.wantAbsent != "" && hasDiagnosticMessage(result.Diagnostics, tt.wantAbsent) {
				t.Fatalf("Diagnostics = %#v, did not want sensitive fragment %q", result.Diagnostics, tt.wantAbsent)
			}
			if _, ok := manifestByName(result.Manifests, "should-not-render"); ok {
				t.Fatalf("Manifests = %#v, unsafe plugin rendered", result.Manifests)
			}
		})
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

func TestLocalProviderPassesPluginRefsAndCapabilitiesToInjectedRenderer(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "refs", "shared", "marker.txt"), "ref")
	var got render.PluginRequest
	provider := localProvider{
		repoRoot: root,
		pluginRenderer: internalPluginRendererFunc(func(_ context.Context, request render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
			got = request
			return nil, nil, nil
		}),
	}

	_, diags, err := provider.RenderSource(context.Background(), render.ResolvedSource{
		RepoRoot:       root,
		Path:           "apps/plugin",
		RepoURL:        "https://github.com/example/repo",
		TargetRevision: "main",
	}, render.RenderOptions{
		Plugin:      &render.PluginConfig{Name: "cue"},
		RefRoots:    map[string]string{"$values": "values"},
		RefSources:  map[string]render.ResolvedSource{"$shared": {RepoURL: "https://github.com/example/repo", TargetRevision: "main", Path: "refs/shared"}},
		KubeVersion: "1.30.1",
		APIVersions: []string{"example.com/v1/Foo"},
	})
	if err != nil {
		t.Fatalf("RenderSource() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
	if got.RefRoots["$values"] != filepath.Join(root, "values") {
		t.Fatalf("RefRoots[$values] = %q, want anchored values root", got.RefRoots["$values"])
	}
	if got.RefRoots["$shared"] != root {
		t.Fatalf("RefRoots[$shared] = %q, want resolved repo root", got.RefRoots["$shared"])
	}
	if got.RefSources["$shared"].Path != "refs/shared" || got.RefSources["$shared"].RepoURL != "https://github.com/example/repo" {
		t.Fatalf("RefSources[$shared] = %#v, want ref source metadata", got.RefSources["$shared"])
	}
	if got.KubeVersion != "1.30.1" {
		t.Fatalf("KubeVersion = %q, want 1.30.1", got.KubeVersion)
	}
	if len(got.APIVersions) != 1 || got.APIVersions[0] != "example.com/v1/Foo" {
		t.Fatalf("APIVersions = %#v, want example.com/v1/Foo", got.APIVersions)
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

func writeCMPConfigMapSpec(t *testing.T, root, name, spec string) {
	t.Helper()
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
`+indentYAMLBlock(spec, 6))
}

func indentYAMLBlock(input string, spaces int) string {
	input = strings.Trim(input, "\n")
	if input == "" {
		return ""
	}
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(input, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func writeUnnamedPluginBuildApplication(t *testing.T, root, appName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
    plugin: {}
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, ".keep"), "")
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

func writeNativeKustomizePluginPolicySeed(t *testing.T, root, name, command, args string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: native-kustomize
    configManagementPlugin:
      generate:
        command: [`+command+`]
        args: [`+args+`]
`)
}

const testExecPolicyCommandTimeout = "15s"

func writeExecPluginPolicy(t *testing.T, root, name string, command []string) {
	t.Helper()
	quoted := yamlSingleQuotedList(command)
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: exec
    generate:
      command: [`+strings.Join(quoted, ", ")+`]
      timeout: `+testExecPolicyCommandTimeout+`
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
`)
}

func writeExecPluginPolicyWithMatch(t *testing.T, root, name string, command []string, matchYAML string) {
	t.Helper()
	quoted := yamlSingleQuotedList(command)
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: exec
`+matchYAML+`    generate:
      command: [`+strings.Join(quoted, ", ")+`]
      timeout: `+testExecPolicyCommandTimeout+`
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
`)
}

func writeExecPluginPolicyWithSeed(t *testing.T, root, name string, command []string, seedYAML string) {
	t.Helper()
	quoted := yamlSingleQuotedList(command)
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: exec
`+seedYAML+`    generate:
      command: [`+strings.Join(quoted, ", ")+`]
      timeout: `+testExecPolicyCommandTimeout+`
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

func testContainerPluginPolicy(t *testing.T, name string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: container
    image: registry.example.test/plugins/render@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    configManagementPlugin:
      discover:
        fileName: marker.txt
    generate:
      command: ["pkl", "eval", "index.pkl"]
`)
	return readTestPluginPolicy(t, root)
}

func testExecPluginPolicyWithMatch(t *testing.T, name string, command []string, matchYAML string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeExecPluginPolicyWithMatch(t, root, name, command, matchYAML)
	return readTestPluginPolicy(t, root)
}

func testExecPluginPolicyWithPostRenderer(t *testing.T, name string, command, postRenderer []string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeExecPluginPolicyWithPostRenderer(t, root, name, command, postRenderer)
	return readTestPluginPolicy(t, root)
}

func testExecPluginPolicyWithParameters(t *testing.T, name string, command []string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeExecPluginPolicyWithParameters(t, root, name, command, `      allow:
        - name: path
          type: string
          required: true
          path:
            allow: ["components/*.pkl"]
`)
	return readTestPluginPolicy(t, root)
}

func testExecPluginPolicyWithRepositoryParameter(t *testing.T, name string, command []string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeExecPluginPolicyWithParameters(t, root, name, command, `      allow:
        - name: path
          type: string
          required: true
          path:
            base: repository
            allow: ["shared/*.txt"]
`, `    copy:
      scope: repository
      include: ["shared/**"]
`)
	return readTestPluginPolicy(t, root)
}

func testExecPluginPolicyWithStringParameter(t *testing.T, name string, command []string) (pluginpolicy.Policy, string) {
	t.Helper()
	return testExecPluginPolicyWithStringParameterName(t, name, command, "token")
}

func testExecPluginPolicyWithStringParameterName(t *testing.T, name string, command []string, parameterName string) (pluginpolicy.Policy, string) {
	t.Helper()
	root := t.TempDir()
	writeExecPluginPolicyWithParameters(t, root, name, command, `      allow:
        - name: `+parameterName+`
          type: string
`)
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

func writeExecPluginPolicyWithParameters(t *testing.T, root, name string, command []string, parameters string, extraExecFields ...string) {
	t.Helper()
	quoted := yamlSingleQuotedList(command)
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: exec
    generate:
      command: [`+strings.Join(quoted, ", ")+`]
      timeout: `+testExecPolicyCommandTimeout+`
`+strings.Join(extraExecFields, "")+`
    parameters:
`+parameters+`
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
`)
}

func writeExecPluginPolicyWithPostRenderer(t *testing.T, root, name string, command, postRenderer []string) {
	t.Helper()
	quotedCommand := yamlSingleQuotedList(command)
	quotedPostRenderer := yamlSingleQuotedList(postRenderer)
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  `+name+`:
    engine: exec
    generate:
      command: [`+strings.Join(quotedCommand, ", ")+`]
      timeout: `+testExecPolicyCommandTimeout+`
    postRenderers:
      - command: [`+strings.Join(quotedPostRenderer, ", ")+`]
        timeout: `+testExecPolicyCommandTimeout+`
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER", "DRYDOCK_APP_EXEC_VALUE"]
`)
}

func yamlSingleQuotedList(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, yamlSingleQuoted(value))
	}
	return quoted
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
	args := appExecPluginHelperArgs(os.Args)
	if len(args) < 2 {
		os.Exit(2)
	}
	switch args[1] {
	case "manifest":
		appExecPluginHelperManifest()
		os.Exit(0)
	case "params":
		appExecPluginHelperParams(args)
		os.Exit(0)
	case "repo-param":
		if err := appExecPluginHelperRepoParam(args); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	case "invalid":
		appExecPluginHelperInvalid()
		os.Exit(0)
	case "invalid-param":
		appExecPluginHelperInvalidParam(args)
		os.Exit(0)
	case "post-render":
		appExecPluginHelperPostRender()
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func appExecPluginHelperArgs(args []string) []string {
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	return args
}

func appExecPluginHelperManifest() {
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
}

func appExecPluginHelperParams(args []string) {
	paramArg := ""
	if len(args) > 2 {
		paramArg = args[2]
	}
	fmt.Println("apiVersion: v1")
	fmt.Println("kind: ConfigMap")
	fmt.Println("metadata:")
	fmt.Println("  name: exec-params")
	fmt.Println("data:")
	fmt.Printf("  arg: %q\n", paramArg)
	fmt.Printf("  param: %q\n", os.Getenv("PARAM_PATH"))
	fmt.Printf("  json: %q\n", os.Getenv("ARGOCD_APP_PARAMETERS"))
}

func appExecPluginHelperRepoParam(args []string) error {
	paramArg := ""
	if len(args) > 2 {
		paramArg = args[2]
	}
	value, err := os.ReadFile(paramArg)
	if err != nil {
		return fmt.Errorf("read repo param: %w", err)
	}
	fmt.Println("apiVersion: v1")
	fmt.Println("kind: ConfigMap")
	fmt.Println("metadata:")
	fmt.Println("  name: exec-repo-param")
	fmt.Println("data:")
	fmt.Printf("  value: %q\n", strings.TrimSpace(string(value)))
	return nil
}

func appExecPluginHelperInvalid() {
	fmt.Println("metadata: [")
}

func appExecPluginHelperInvalidParam(args []string) {
	key := "token"
	if len(args) > 2 {
		key = args[2]
	}
	fmt.Println("apiVersion: v1")
	fmt.Println("kind: ConfigMap")
	fmt.Println("metadata:")
	fmt.Println("  name: invalid")
	fmt.Println("data:")
	fmt.Printf("  %s: [\n", key)
}

func appExecPluginHelperPostRender() {
	input, _ := io.ReadAll(os.Stdin)
	fmt.Print(string(input))
	fmt.Println("  post: rendered")
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
