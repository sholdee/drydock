package app

import (
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

func TestCMPAutoDiscoveryWarnsForStaticFileNameMatch(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "demo", "plugin.yaml"), "kind: ConfigMap\n")
	provider := localProvider{configManagementPlugins: map[string]config.ConfigManagementPlugin{
		"sidecar": {
			Discover:   config.ConfigManagementPluginDiscovery{FileName: "plugin.yaml"},
			Provenance: diagnostic.Provenance{Path: "settings/argocd-cmp-cm.yaml", Pointer: "data.plugin.yaml"},
		},
	}}

	diags := provider.cmpAutoDiscoveryDeferredDiagnostics(render.ResolvedSource{RepoRoot: root, Path: "apps/demo"}, render.RenderOptions{})
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one warning", diags)
	}
	if diags[0].Code != diagnostic.CodePluginAutoDiscovery || diags[0].Category != "plugin" {
		t.Fatalf("diagnostic = %#v, want plugin auto-discovery warning", diags[0])
	}
}

func TestCMPAutoDiscoveryWarnsForStaticFindGlobMatch(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "demo", "nested", "Chart.yaml"), "apiVersion: v2\nname: demo\n")
	provider := localProvider{configManagementPlugins: map[string]config.ConfigManagementPlugin{
		"helm-sidecar": {
			Discover:   config.ConfigManagementPluginDiscovery{FindGlob: "**/Chart.yaml"},
			Provenance: diagnostic.Provenance{Path: "settings/plugin.yaml"},
		},
	}}

	diags := provider.cmpAutoDiscoveryDeferredDiagnostics(render.ResolvedSource{RepoRoot: root, Path: "apps/demo"}, render.RenderOptions{})
	if len(diags) != 1 || diags[0].Code != diagnostic.CodePluginAutoDiscovery {
		t.Fatalf("diagnostics = %#v, want plugin auto-discovery warning", diags)
	}
}

func TestCMPAutoDiscoveryDoesNotWarnForCommandOnlyOrExplicitPlugin(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "demo", "plugin.yaml"), "kind: ConfigMap\n")
	provider := localProvider{configManagementPlugins: map[string]config.ConfigManagementPlugin{
		"command-only": {
			Discover: config.ConfigManagementPluginDiscovery{FindCommand: []string{"sh", "-c", "find . -name plugin.yaml"}},
		},
		"file-match": {
			Discover: config.ConfigManagementPluginDiscovery{FileName: "plugin.yaml"},
		},
	}}
	source := render.ResolvedSource{RepoRoot: root, Path: "apps/demo"}

	commandOnlyProvider := localProvider{configManagementPlugins: map[string]config.ConfigManagementPlugin{
		"command-only": provider.configManagementPlugins["command-only"],
	}}
	if diags := commandOnlyProvider.cmpAutoDiscoveryDeferredDiagnostics(source, render.RenderOptions{}); len(diags) != 0 {
		t.Fatalf("command-only diagnostics = %#v, want none", diags)
	}

	opts := render.RenderOptions{Plugin: &render.PluginConfig{Name: "file-match"}}
	if diags := provider.cmpAutoDiscoveryDeferredDiagnostics(source, opts); len(diags) != 0 {
		t.Fatalf("explicit plugin diagnostics = %#v, want none", diags)
	}
}

func TestCMPAutoDiscoveryDoesNotWarnForUnrelatedStaticDiscovery(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources: []\n")
	provider := localProvider{configManagementPlugins: map[string]config.ConfigManagementPlugin{
		"unrelated": {
			Discover: config.ConfigManagementPluginDiscovery{FileName: "plugin.yaml"},
		},
	}}

	diags := provider.cmpAutoDiscoveryDeferredDiagnostics(render.ResolvedSource{RepoRoot: root, Path: "apps/demo"}, render.RenderOptions{})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}
