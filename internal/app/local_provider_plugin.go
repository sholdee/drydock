package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
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
	if opts.Plugin == nil {
		return nil, nil, false, nil
	}
	name := strings.TrimSpace(opts.Plugin.Name)
	if name == "" {
		matches, err := p.policyPluginStaticDiscoveryMatches(source)
		if err != nil {
			return nil, nil, true, err
		}
		switch len(matches) {
		case 0:
			message := unnamedPolicyPluginNoMatchMessage()
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		case 1:
			name = matches[0].name
			policyPlugin := matches[0].plugin
			plugin := *opts.Plugin
			plugin.Name = name
			opts.Plugin = &plugin
			if message := validatePolicyPluginSource(name, source, opts, policyPlugin); message != "" {
				return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
			}
			return p.renderMatchedPolicyPluginSource(ctx, source, opts, name, policyPlugin)
		default:
			message := unnamedPolicyPluginAmbiguousMessage(matches)
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
	}
	policyPlugin, ok := p.pluginPolicy.Plugin(name)
	if !ok {
		return nil, nil, false, nil
	}
	if message := validatePolicyPluginSource(name, source, opts, policyPlugin); message != "" {
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
		if policyPlugin.ConfigManagementPlugin != nil && policyPlugin.ConfigManagementPlugin.Generate != nil {
			plugin := configManagementPluginSeed(name, policyPlugin.ConfigManagementPlugin)
			manifests, diags, err := p.renderNativeKustomizePluginSourceWithConfig(ctx, source, opts, name, plugin)
			return manifests, diags, true, err
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
	case pluginpolicy.EngineContainer:
		if !opts.EnablePlugins {
			message := fmt.Sprintf("config management plugin %s uses container policy, which requires --enable-plugins", pluginDisplayName(name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		if !p.pluginPolicyExecTrusted {
			message := fmt.Sprintf("config management plugin %s uses container policy from an untrusted policy source; use a policy from the diff baseline or pass --plugin-policy-ref for a trusted Git ref", pluginDisplayName(name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		return p.renderContainerPolicyPluginSource(ctx, source, opts, name, policyPlugin)
	default:
		message := fmt.Sprintf("config management plugin %s has unsupported trusted policy engine %q", pluginDisplayName(name), policyPlugin.Engine)
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
}

func validatePolicyPluginSource(name string, source render.ResolvedSource, opts render.RenderOptions, policyPlugin pluginpolicy.Plugin) string {
	if len(opts.Plugin.Env) > 0 {
		return fmt.Sprintf("config management plugin %s uses env or parameters, which are unsupported by trusted native plugin policy", pluginDisplayName(name))
	}
	if len(opts.Plugin.Parameters) > 0 && policyPlugin.Engine != pluginpolicy.EngineExec && policyPlugin.Engine != pluginpolicy.EngineContainer {
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
	params, message := validateExecPluginParameters(name, policyPlugin, opts.Plugin, source.RepoRoot, sourcePath)
	if message != "" {
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	execConfig, sensitive, message := expandExecPluginCommandTemplates(*policyPlugin.Exec, params)
	if message != "" {
		return nil, unsupportedPluginDiagnostic(fmt.Sprintf("config management plugin %s %s", pluginDisplayName(name), message)), true, unsupportedPolicyPluginError(message)
	}
	extraEnv := append([]string(nil), params.extraEnv...)
	if p.offline {
		extraEnv = append(extraEnv, "DRYDOCK_OFFLINE=true")
	}
	result, err := p.pluginExecRunner.Run(ctx, pluginexec.Request{
		SourceDir:       sourceDir,
		RepositoryDir:   source.RepoRoot,
		SourceRelPath:   sourcePath,
		Config:          execConfig,
		ProtectedRoots:  p.execProtectedRoots(source.RepoRoot),
		ExtraEnv:        extraEnv,
		SensitiveValues: sensitive,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, true, ctxErr
		}
		message := fmt.Sprintf("config management plugin %s failed: %s", pluginDisplayName(name), redactSensitiveText(err.Error(), sensitive))
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s", message)
	}
	phase, decodePath := execPolicyDecodeTarget(name, source, len(policyPlugin.Exec.PostRenderers) > 0)
	docs, err := manifest.DecodeDocuments(decodePath, bytes.NewReader(result.Stdout))
	if err != nil {
		message := fmt.Sprintf("config management plugin %s produced invalid %s for %s at %s: %s", pluginDisplayName(name), phase, execPolicySourceLabel(source), decodePath, redactSensitiveText(err.Error(), sensitive))
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s", message)
	}
	manifests := make([]render.Manifest, 0, len(docs))
	for _, doc := range docs {
		manifests = append(manifests, render.Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	p.recordPluginExecutions(opts, source, name, pluginExecutionDetails{
		engine:     string(pluginpolicy.EngineExec),
		executions: result.Executions,
	})
	return manifests, nil, true, nil
}

func (p localProvider) renderContainerPolicyPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions, name string, policyPlugin pluginpolicy.Plugin) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	if source.Chart != "" {
		message := fmt.Sprintf("config management plugin %s uses chart source, which is unsupported by container policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	if source.Path == "" {
		message := fmt.Sprintf("config management plugin %s must define path for container policy", pluginDisplayName(name))
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	if policyPlugin.Container == nil {
		message := fmt.Sprintf("config management plugin %s has invalid container policy", pluginDisplayName(name))
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
	params, message := validateContainerPluginParameters(name, policyPlugin, opts.Plugin, source.RepoRoot, sourcePath)
	if message != "" {
		return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	lifecycle, sensitive, message := expandExecPluginCommandTemplates(policyPlugin.Container.Lifecycle, params)
	if message != "" {
		return nil, unsupportedPluginDiagnostic(fmt.Sprintf("config management plugin %s %s", pluginDisplayName(name), message)), true, unsupportedPolicyPluginError(message)
	}
	containerConfig := *policyPlugin.Container
	containerConfig.Lifecycle = lifecycle
	extraEnv := append([]string(nil), params.extraEnv...)
	if p.offline {
		extraEnv = append(extraEnv, "DRYDOCK_OFFLINE=true")
	}
	result, err := p.pluginContainerRunner.Run(ctx, plugincontainer.Request{
		SourceDir:       sourceDir,
		RepositoryDir:   source.RepoRoot,
		SourceRelPath:   sourcePath,
		Config:          containerConfig,
		Offline:         p.offline,
		ExtraEnv:        extraEnv,
		SensitiveValues: sensitive,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, true, ctxErr
		}
		message := fmt.Sprintf("config management plugin %s failed: %s", pluginDisplayName(name), redactSensitiveText(err.Error(), sensitive))
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s", message)
	}
	phase, decodePath := execPolicyDecodeTarget(name, source, len(policyPlugin.Container.Lifecycle.PostRenderers) > 0)
	docs, err := manifest.DecodeDocuments(decodePath, bytes.NewReader(result.Stdout))
	if err != nil {
		message := fmt.Sprintf("config management plugin %s produced invalid %s for %s at %s: %s", pluginDisplayName(name), phase, execPolicySourceLabel(source), decodePath, redactSensitiveText(err.Error(), sensitive))
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s", message)
	}
	manifests := make([]render.Manifest, 0, len(docs))
	for _, doc := range docs {
		manifests = append(manifests, render.Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	p.recordPluginExecutions(opts, source, name, pluginExecutionDetails{
		engine:     string(pluginpolicy.EngineContainer),
		runtime:    string(policyPlugin.Container.Runtime),
		image:      policyPlugin.Container.Image,
		executions: result.Executions,
	})
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
	return fmt.Sprintf("config management plugin %s is not supported by the default renderer; no compatible native renderer or trusted drydock plugin policy matched", pluginDisplayName(name))
}

func execPolicyDecodeTarget(name string, source render.ResolvedSource, hasPostRenderers bool) (string, string) {
	displayName := pluginDisplayName(name)
	if hasPostRenderers {
		return "final post-render manifests", "plugin/" + displayName + "/" + execPolicySourcePath(source) + "/final-post-render-output"
	}
	return "generated manifests", "plugin/" + displayName + "/" + execPolicySourcePath(source) + "/generate-output"
}

func redactSensitiveText(value string, sensitive []string) string {
	sortSensitiveValues(sensitive)
	for _, item := range sensitive {
		if item == "" {
			continue
		}
		value = strings.ReplaceAll(value, item, "<redacted>")
	}
	return value
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
