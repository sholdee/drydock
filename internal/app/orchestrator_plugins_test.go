package app

import (
	"context"
	"github.com/sholdee/drydock/internal/diagnostic"
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
func TestOrchestratorPluginTimeoutReturnsPartialStatus(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writePluginBuildApplication(t, root, "plugin", "cue")

	result, err := (Orchestrator{PluginRenderer: blockingInternalPluginRenderer{}}).Build(context.Background(), BuildRequest{
		Path:          root,
		PluginTimeout: time.Nanosecond,
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
