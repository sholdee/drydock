package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/pluginexec"
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
	if message := validatePolicyPluginSource(name, source, opts); message != "" {
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}

	return p.renderMatchedPolicyPluginSource(ctx, source, opts, name, policyPlugin)
}

func (p localProvider) renderMatchedPolicyPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions, name string, policyPlugin pluginpolicy.Plugin) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
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
	case pluginpolicy.EngineExec:
		if !opts.EnablePlugins {
			message := fmt.Sprintf("config management plugin %s uses exec policy, which requires --enable-plugins", pluginDisplayName(name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		if !p.pluginPolicyExecTrusted {
			message := fmt.Sprintf("config management plugin %s uses exec policy from an untrusted policy source; use a policy from the diff baseline or pass --plugin-policy-ref for a trusted Git ref", pluginDisplayName(name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		return p.renderExecPolicyPluginSource(ctx, source, opts, name, policyPlugin)
	default:
		message := fmt.Sprintf("config management plugin %s has unsupported trusted policy engine %q", pluginDisplayName(name), policyPlugin.Engine)
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
}

func validatePolicyPluginSource(name string, source render.ResolvedSource, opts render.RenderOptions) string {
	if len(opts.Plugin.Env) > 0 || len(opts.Plugin.Parameters) > 0 {
		return fmt.Sprintf("config management plugin %s uses env or parameters, which are unsupported by trusted native plugin policy", pluginDisplayName(name))
	}
	if source.Path == "" && source.Chart == "" {
		return fmt.Sprintf("config management plugin %s must define path or chart for trusted native plugin policy", pluginDisplayName(name))
	}
	if source.Path != "" && source.Chart != "" {
		return fmt.Sprintf("config management plugin %s cannot define both path and chart for trusted native plugin policy", pluginDisplayName(name))
	}
	return ""
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

func (p localProvider) renderExecPolicyPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions, name string, policyPlugin pluginpolicy.Plugin) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	if source.Chart != "" {
		message := fmt.Sprintf("config management plugin %s uses chart source, which is unsupported by exec policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	if source.Path == "" {
		message := fmt.Sprintf("config management plugin %s must define path for exec policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	if policyPlugin.Exec == nil {
		message := fmt.Sprintf("config management plugin %s has invalid exec policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return nil, nil, true, err
	}
	if err := rejectLocalSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return nil, nil, true, err
	}
	sourceDir := filepath.Join(source.RepoRoot, sourcePath)
	result, err := (pluginexec.DefaultRunner{}).Run(ctx, pluginexec.Request{
		SourceDir:      sourceDir,
		Config:         *policyPlugin.Exec,
		ProtectedRoots: p.execProtectedRoots(source.RepoRoot),
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, true, ctxErr
		}
		message := fmt.Sprintf("config management plugin %s failed: %s", pluginDisplayName(name), err)
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s: %w", message, err)
	}
	phase, decodePath := execPolicyDecodeTarget(name, source, len(policyPlugin.Exec.PostRenderers) > 0)
	docs, err := manifest.DecodeDocuments(decodePath, bytes.NewReader(result.Stdout))
	if err != nil {
		message := fmt.Sprintf("config management plugin %s produced invalid %s for %s at %s: %s", pluginDisplayName(name), phase, execPolicySourceLabel(source), decodePath, err)
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s: %w", message, err)
	}
	manifests := make([]render.Manifest, 0, len(docs))
	for _, doc := range docs {
		manifests = append(manifests, render.Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	return manifests, nil, true, nil
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
	return fmt.Sprintf("config management plugin %s is disabled in the default renderer; use an explicit trusted policy, and pass --enable-plugins for exec policy entries", pluginDisplayName(name))
}

func execPolicyDecodeTarget(name string, source render.ResolvedSource, hasPostRenderers bool) (string, string) {
	displayName := pluginDisplayName(name)
	if hasPostRenderers {
		return "final post-render manifests", "plugin/" + displayName + "/" + execPolicySourcePath(source) + "/final-post-render-output"
	}
	return "generated manifests", "plugin/" + displayName + "/" + execPolicySourcePath(source) + "/generate-output"
}

func execPolicySourcePath(source render.ResolvedSource) string {
	if source.Path != "" {
		return "path/" + strings.Trim(source.Path, `/\`)
	}
	if source.Chart != "" {
		return "chart/" + source.Chart
	}
	return "source"
}

func execPolicySourceLabel(source render.ResolvedSource) string {
	if source.Path != "" {
		return fmt.Sprintf("source path %q", source.Path)
	}
	if source.Chart != "" {
		return fmt.Sprintf("source chart %q", source.Chart)
	}
	return "source"
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
