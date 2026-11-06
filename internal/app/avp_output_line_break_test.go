package app

import (
	"context"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// avpLineBreakSources writes a source whose last printed document ends with
// a "|+" value and a blank line: a Kustomize base whose last field is spec,
// or a Helm chart whose last printed document is a hook that Argo CD does
// not treat as a hook (argocd.argoproj.io/hook: Skip).
var avpLineBreakSources = map[string]map[string]string{
	"kustomize": {
		"kustomization.yaml": "resources:\n  - widget.yaml\n",
		"widget.yaml":        "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: kept\nspec:\n  kept: |+\n    x\n\n",
	},
	"helm": {
		"Chart.yaml":       "apiVersion: v2\nname: avp-line-break\nversion: 0.1.0\n",
		"templates/a.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n",
		"templates/b.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: kept\n  annotations:\n    helm.sh/hook: pre-install\n    argocd.argoproj.io/hook: Skip\nspec:\n  kept: |+\n    x\n\n",
	},
}

// avpLineBreakWant is the value Argo CD v3.6.1 renders for the last
// document of each source when the AVP sidecar reads the output, and when
// the repo-server reads it straight from the command.
//
// The AVP sidecar pipes helm template or kustomize build into
// argocd-vault-plugin generate, which prints each object followed by
// "---\n". The cmp-server removes only the line break after the last "---"
// (cmpserver/plugin/plugin.go:155). The repo-server removes one line break
// from the command output itself (util/exec/exec.go:285). helm template
// prints a line break after each document, so the Helm value keeps one
// more line break than the Kustomize value.
var avpLineBreakWant = map[string]struct{ avp, direct string }{
	"kustomize": {avp: "x\n\n", direct: "x\n"},
	"helm":      {avp: "x\n\n\n", direct: "x\n\n"},
}

func keptValue(t *testing.T, result BuildResult) string {
	t.Helper()
	manifest, ok := manifestByName(result.Manifests, "kept")
	if !ok {
		t.Fatalf("Manifests = %#v, want kept", result.Manifests)
	}
	value, _, _ := unstructured.NestedString(manifest.Object.Object, "spec", "kept")
	return value
}

// TestOrchestratorBuildAVPCompatKeepsOutputLineBreak renders Helm and
// Kustomize sources through both argocd-vault-plugin compatibility paths:
// the default plugin name and a policy with engine avp-compat. Argo CD keeps
// the line break at the end of the helm template or kustomize build output
// there, because the output goes through AVP.
func TestOrchestratorBuildAVPCompatKeepsOutputLineBreak(t *testing.T) {
	for kind, files := range avpLineBreakSources {
		for _, path := range []string{"default plugin", "avp-compat policy"} {
			t.Run(kind+" "+path, func(t *testing.T) {
				root := t.TempDir()
				pluginName := argocdVaultPluginName
				if path == "avp-compat policy" {
					pluginName = "avp-policy"
					writePluginPolicy(t, root, pluginName, "avp-compat")
				}
				writePluginBuildApplication(t, root, "plugin", pluginName)
				for name, content := range files {
					writeTestFile(t, filepath.Join(root, "manifests", "plugin", filepath.FromSlash(name)), content)
				}
				result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
				if err != nil {
					t.Fatalf("Build() error = %v", err)
				}
				if got, want := keptValue(t, result), avpLineBreakWant[kind].avp; got != want {
					t.Fatalf("spec.kept = %q, want %q", got, want)
				}
			})
		}
	}
}

// TestOrchestratorBuildAVPCompatFlagTrimsOutputLineBreak renders the same
// sources as ordinary Helm and Kustomize sources with AVP compatibility on.
// Argo CD renders such a source without AVP, so the repo-server removes the
// line break at the end of the command output.
func TestOrchestratorBuildAVPCompatFlagTrimsOutputLineBreak(t *testing.T) {
	for kind, files := range avpLineBreakSources {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "apps", "plain.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plain
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/plain
    targetRevision: main
  destination:
    server: https://kubernetes.default.svc
    namespace: default
`)
			for name, content := range files {
				writeTestFile(t, filepath.Join(root, "manifests", "plain", filepath.FromSlash(name)), content)
			}
			result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root, EnableAVPCompat: true})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if got, want := keptValue(t, result), avpLineBreakWant[kind].direct; got != want {
				t.Fatalf("spec.kept = %q, want %q", got, want)
			}
		})
	}
}
