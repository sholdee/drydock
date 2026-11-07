package app

import (
	"context"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/app/discovery"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/render"
)

// upstreamGatedSourceType is how the Argo CD v3.6.1 repo-server picks the
// source type with the enabled source types (GetAppSourceType,
// reposerver/repository/repository.go:1955-1975, which cannot be imported
// without new modules): an explicit type that is turned off is a
// directory, and discovery.AppType skips the types that are off.
func upstreamGatedSourceType(t *testing.T, dir string, explicit argoappv1.ApplicationSourceType, enabled map[string]bool) argoappv1.ApplicationSourceType {
	t.Helper()
	if explicit != "" {
		if !discovery.IsManifestGenerationEnabled(explicit, enabled) {
			return argoappv1.ApplicationSourceTypeDirectory
		}
		return explicit
	}
	appType, err := discovery.AppType(context.Background(), dir, dir, enabled, nil, nil)
	if err != nil {
		t.Fatalf("AppType() error = %v", err)
	}
	return argoappv1.ApplicationSourceType(appType)
}

func rendererSourceType(t *testing.T, renderer render.Renderer) argoappv1.ApplicationSourceType {
	t.Helper()
	switch renderer.(type) {
	case render.HelmRenderer:
		return argoappv1.ApplicationSourceTypeHelm
	case render.KustomizeRenderer:
		return argoappv1.ApplicationSourceTypeKustomize
	case render.DirectoryRenderer:
		return argoappv1.ApplicationSourceTypeDirectory
	}
	t.Fatalf("unexpected renderer %T", renderer)
	return ""
}

// selectLocalRenderer picks the same source type as the Argo CD
// repo-server for every combination of helm.enable and kustomize.enable.
func TestSelectLocalRendererSourceTypeGatesMatchUpstream(t *testing.T) {
	t.Setenv("ARGOCD_PLUGINSOCKFILEPATH", t.TempDir())
	root := t.TempDir()
	dirs := map[string][]string{
		"chart":        {"Chart.yaml"},
		"kustomize":    {"kustomization.yaml"},
		"both":         {"Chart.yaml", "kustomization.yaml"},
		"helmchart":    {"kustomization.yaml", "traefik-HelmChart.yaml"},
		"plain":        {"configmap.yaml"},
		"jsonnet-only": {"main.jsonnet"},
	}
	for dir, files := range dirs {
		for _, name := range files {
			writeTestFile(t, filepath.Join(root, dir, name), "{}\n")
		}
	}
	for _, helmEnable := range []string{"", "true", "false", "True"} {
		for _, kustomizeEnable := range []string{"", "true", "false"} {
			settings := config.DefaultSettings()
			settings.SourceTypeEnable = map[string]config.Value[string]{
				config.HelmEnableKey:      {Value: helmEnable},
				config.KustomizeEnableKey: {Value: kustomizeEnable},
			}
			gates := sourceTypeGatesFromSettings(settings)
			enabled := map[string]bool{
				string(argoappv1.ApplicationSourceTypeHelm):      settings.SourceTypeEnabled(config.HelmEnableKey),
				string(argoappv1.ApplicationSourceTypeKustomize): settings.SourceTypeEnabled(config.KustomizeEnableKey),
			}
			for dir := range dirs {
				for _, explicit := range []argoappv1.ApplicationSourceType{"", argoappv1.ApplicationSourceTypeHelm, argoappv1.ApplicationSourceTypeKustomize, argoappv1.ApplicationSourceTypeDirectory} {
					want := upstreamGatedSourceType(t, filepath.Join(root, dir), explicit, enabled)
					renderer, err := selectLocalRenderer(render.ResolvedSource{RepoRoot: root, Path: dir, ExplicitType: explicit}, gates)
					if err != nil {
						t.Fatalf("selectLocalRenderer() error = %v", err)
					}
					if got := rendererSourceType(t, renderer); got != want {
						t.Errorf("helm.enable=%q kustomize.enable=%q dir=%s explicit=%q: drydock %s, Argo CD %s", helmEnable, kustomizeEnable, dir, explicit, got, want)
					}
				}
			}
		}
	}
}

// The source type keys are render settings: they change the render
// signature, and the default spellings keep the signature of settings
// without the keys.
func TestRenderSettingsSignatureSignsSourceTypeGates(t *testing.T) {
	base, err := renderSettingsSignature(config.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "true"} {
		settings := config.DefaultSettings()
		settings.SourceTypeEnable = map[string]config.Value[string]{config.HelmEnableKey: {Value: value, Provenance: config.Provenance{Path: "argocd-cm.yaml"}}}
		got, err := renderSettingsSignature(settings)
		if err != nil {
			t.Fatal(err)
		}
		if got != base {
			t.Errorf("helm.enable=%q signature = %s, want the default %s", value, got, base)
		}
	}
	seen := map[string]string{"": base}
	for _, key := range []string{config.HelmEnableKey, config.KustomizeEnableKey, config.JsonnetEnableKey} {
		settings := config.DefaultSettings()
		settings.SourceTypeEnable = map[string]config.Value[string]{key: {Value: "false"}}
		got, err := renderSettingsSignature(settings)
		if err != nil {
			t.Fatal(err)
		}
		for other, signature := range seen {
			if signature == got {
				t.Errorf("%s=false signature equals the signature of %q", key, other)
			}
		}
		seen[key] = got
	}
}
