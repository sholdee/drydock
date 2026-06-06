package app

import (
	"context"
	"fmt"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

func (p localProvider) renderAVPCompatPolicyPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	nativeOptions := opts
	nativeOptions.Plugin = nil
	nativeOptions.EnableAVPCompat = true
	nativeOptions.QuietAVPCompat = true
	var (
		manifests []render.Manifest
		diags     []diagnostic.Diagnostic
		err       error
	)
	if source.Path != "" {
		var renderer render.Renderer
		renderer, err = selectLocalRenderer(source)
		if err == nil {
			manifests, diags, err = renderer.Render(ctx, source, nativeOptions)
		}
	} else {
		manifests, diags, err = p.renderChartOnlySource(ctx, source, nativeOptions)
	}
	if err != nil {
		return manifests, diags, true, err
	}
	for i := range manifests {
		if manifests[i].Object != nil {
			manifests[i].Object = manifests[i].Object.DeepCopy()
		}
		applyAVPCompatToManifest(&manifests[i], nativeOptions)
	}
	return manifests, diags, true, nil
}

func (p localProvider) renderNativeKustomizePolicyPluginSource(ctx context.Context, source render.ResolvedSource, plan policyPluginRenderPlan) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	if source.Chart != "" {
		message := fmt.Sprintf("config management plugin %s uses chart source, which is unsupported by native-kustomize policy", pluginDisplayName(plan.name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	if plan.plugin.ConfigManagementPlugin != nil && plan.plugin.ConfigManagementPlugin.Generate != nil {
		plugin := configManagementPluginSeed(plan.name, plan.plugin.ConfigManagementPlugin)
		manifests, diags, err := p.renderNativeKustomizePluginSourceWithConfig(ctx, source, plan.opts, plan.name, plugin)
		return manifests, diags, true, err
	}
	manifests, diags, handled, err := p.renderNativeKustomizePluginSource(ctx, source, plan.opts)
	if handled {
		return manifests, diags, true, err
	}
	message := fmt.Sprintf("config management plugin %s is permitted by policy but no compatible native Kustomize plugin settings were discovered", pluginDisplayName(plan.name))
	return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
}
