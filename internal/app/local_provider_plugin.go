package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"

	"strings"
)

func (p localProvider) renderPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	if p.pluginRenderer == nil {
		if manifests, diags, handled, err := p.renderPolicyPluginSource(ctx, source, opts); handled {
			return manifests, diags, err
		}
		message := unsupportedPluginMessage(opts.Plugin.Name)
		return nil, []diagnostic.Diagnostic{{
			Code:     diagnostic.CodePluginUnsupported,
			Severity: diagnostic.SeverityError,
			Category: "plugin",
			Message:  message,
		}}, fmt.Errorf("%s: %w", message, render.ErrUnsupportedPlugin)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	renderCtx := ctx
	cancel := func() {}
	if p.pluginTimeout > 0 {
		renderCtx, cancel = context.WithTimeout(ctx, p.pluginTimeout)
	}
	defer cancel()
	request := render.PluginRequest{
		AppName:      opts.AppName,
		AppNamespace: opts.AppNamespace,
		Project:      opts.Project,
		Namespace:    opts.Namespace,
		Source:       source,
		Plugin:       *opts.Plugin,
		RefRoots:     cloneStringMap(opts.RefRoots),
		RefSources:   cloneResolvedSourceMap(opts.RefSources),
	}
	manifests, diags, err := p.pluginRenderer.RenderPlugin(renderCtx, request)
	diags = diagnostic.WithStableCodes(diags)
	if err == nil {
		return manifests, diags, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return manifests, diags, ctxErr
	}
	if renderCtx.Err() == context.DeadlineExceeded {
		message := fmt.Sprintf("config management plugin %s timed out", pluginDisplayName(opts.Plugin.Name))
		diags = append(diags, pluginFailedDiagnostic(message))
		return manifests, diags, fmt.Errorf("%s: %w", message, err)
	}
	if errors.Is(err, render.ErrUnsupportedPlugin) || diagnosticsContainCode(diags, diagnostic.CodePluginUnsupported) {
		return manifests, diags, err
	}
	message := fmt.Sprintf("config management plugin %s failed: %s", pluginDisplayName(opts.Plugin.Name), err)
	diags = append(diags, pluginFailedDiagnostic(message))
	return manifests, diags, err
}

func (p localProvider) renderPolicyPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	if opts.Plugin == nil {
		return nil, nil, false, nil
	}
	name := strings.TrimSpace(opts.Plugin.Name)
	if name == "" {
		return nil, unsupportedPluginDiagnostic("config management plugin name is required"), true, unsupportedPolicyPluginError("config management plugin name is required")
	}
	policyPlugin, ok := p.pluginPolicy.Plugin(name)
	if !ok {
		return nil, nil, false, nil
	}
	if len(opts.Plugin.Env) > 0 || len(opts.Plugin.Parameters) > 0 {
		message := fmt.Sprintf("config management plugin %s uses env or parameters, which are unsupported by trusted native plugin policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	if source.Path == "" && source.Chart == "" {
		message := fmt.Sprintf("config management plugin %s must define path or chart for trusted native plugin policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	if source.Path != "" && source.Chart != "" {
		message := fmt.Sprintf("config management plugin %s cannot define both path and chart for trusted native plugin policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}

	switch policyPlugin.Engine {
	case pluginpolicy.EngineAVPCompat:
		return p.renderAVPCompatPolicyPluginSource(ctx, source, opts)
	case pluginpolicy.EngineNativeKustomize:
		if source.Chart != "" {
			message := fmt.Sprintf("config management plugin %s uses chart source, which is unsupported by native-kustomize policy", pluginDisplayName(name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		manifests, diags, handled, err := p.renderNativeKustomizePluginSource(ctx, source, opts)
		if handled {
			return manifests, diags, true, err
		}
		message := fmt.Sprintf("config management plugin %s is permitted by policy but no compatible native Kustomize plugin settings were discovered", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	default:
		message := fmt.Sprintf("config management plugin %s has unsupported trusted policy engine %q", pluginDisplayName(name), policyPlugin.Engine)
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
}

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

func unsupportedPluginDiagnostic(message string) []diagnostic.Diagnostic {
	return []diagnostic.Diagnostic{{
		Code:     diagnostic.CodePluginUnsupported,
		Severity: diagnostic.SeverityError,
		Category: "plugin",
		Message:  message,
	}}
}

func unsupportedPolicyPluginError(message string) error {
	return fmt.Errorf("%s: %w", message, render.ErrUnsupportedPlugin)
}

func pluginDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "<unnamed>"
	}
	return name
}

func unsupportedPluginMessage(name string) string {
	return fmt.Sprintf("config management plugin %s is disabled in the default renderer; future plugin policy support will require an explicit trusted policy and plugin execution opt-in", pluginDisplayName(name))
}

func pluginFailedDiagnostic(message string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     diagnostic.CodePluginFailed,
		Severity: diagnostic.SeverityError,
		Category: "plugin",
		Message:  message,
	}
}

func diagnosticsContainCode(diags []diagnostic.Diagnostic, code string) bool {
	for _, diag := range diags {
		if diag.Code == code {
			return true
		}
	}
	return false
}
