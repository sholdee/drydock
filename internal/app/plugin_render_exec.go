package app

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

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
