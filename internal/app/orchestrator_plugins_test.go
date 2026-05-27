package app

import (
	"context"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			if !strings.Contains(result.Statuses[0].Message, "config management plugin cue is not supported without an injected plugin renderer") {
				t.Fatalf("status message = %q, want unsupported plugin renderer", result.Statuses[0].Message)
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

func TestOrchestratorBuildRendersNativeKustomizePluginFromHelmValues(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
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

func TestOrchestratorBuildRendersNativeKustomizePluginFromRawHelmValues(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "kustomize-build-with-helm")
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
