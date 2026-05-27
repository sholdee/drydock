package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"

	"strings"
)

func (p localProvider) renderPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	if p.pluginRenderer == nil {
		if manifests, diags, handled, err := p.renderNativeKustomizePluginSource(ctx, source, opts); handled {
			return manifests, diags, err
		}
		message := fmt.Sprintf("config management plugin %s is not supported without an injected plugin renderer", pluginDisplayName(opts.Plugin.Name))
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

func pluginDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "<unnamed>"
	}
	return name
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
