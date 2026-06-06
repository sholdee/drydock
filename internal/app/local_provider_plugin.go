package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/render"
)

func (p localProvider) renderPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	if p.pluginRenderer == nil {
		if manifests, diags, handled, err := p.renderPolicyPluginSource(ctx, source, opts); handled {
			return manifests, diags, err
		}
		if manifests, diags, handled, err := p.renderNativeKustomizePluginSource(ctx, source, opts); handled {
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
		KubeVersion:  opts.KubeVersion,
		APIVersions:  append([]string(nil), opts.APIVersions...),
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
	plan, diags, handled, err := p.planPolicyPluginRender(source, opts)
	if !handled || err != nil {
		return nil, diags, handled, err
	}
	return p.renderPolicyPluginPlan(ctx, source, plan)
}

func (p localProvider) execProtectedRoots(sourceRoot string) []string {
	roots := append([]string(nil), p.remoteResourceForbiddenRoots...)
	roots = append(roots, p.repoRoot, sourceRoot, p.chartCacheDir, p.gitCacheDir, p.remoteResourceCacheDir)
	return compactStrings(roots...)
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
	return fmt.Sprintf("config management plugin %s is not supported by the default renderer; no compatible native renderer or trusted drydock plugin policy matched", pluginDisplayName(name))
}

type pluginExecutionDetails struct {
	engine     string
	runtime    string
	image      string
	executions []pluginexec.Execution
}

func (p localProvider) recordPluginExecutions(opts render.RenderOptions, source render.ResolvedSource, name string, details pluginExecutionDetails) {
	if p.pluginExecutions == nil || len(details.executions) == 0 {
		return
	}
	for _, execution := range details.executions {
		*p.pluginExecutions = append(*p.pluginExecutions, PluginExecution{
			AppNamespace: opts.AppNamespace,
			AppName:      opts.AppName,
			SourceIndex:  opts.SourceIndex,
			SourceName:   opts.SourceName,
			SourcePath:   source.Path,
			PluginName:   pluginDisplayName(name),
			Engine:       details.engine,
			Runtime:      details.runtime,
			Image:        details.image,
			Phase:        execution.Phase,
			Command:      execution.Command,
			Duration:     formatPluginExecutionDuration(execution.Duration),
		})
	}
}

func formatPluginExecutionDuration(duration time.Duration) string {
	if duration < time.Millisecond {
		return duration.String()
	}
	return duration.Round(time.Millisecond).String()
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
