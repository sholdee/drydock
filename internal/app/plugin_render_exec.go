package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

func (p localProvider) renderExecPolicyPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions, name string, policyPlugin pluginpolicy.Plugin) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	prepared, diags, err := prepareCommandPluginRender(source, opts, name, policyPlugin)
	if err != nil {
		return nil, diags, true, err
	}
	sourcePath, sourceDir, params, applicationEnv := prepared.sourcePath, prepared.sourceDir, prepared.params, prepared.applicationEnv
	execConfig, sensitive := prepared.lifecycle, prepared.sensitive
	extraEnv := composePolicyPluginExtraEnv(opts, applicationEnv, params, p.offline)
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
	// Argo CD's cmp-server splits the plugin output with
	// kube.SplitYAMLToString, and the repo-server decodes each document with
	// kube.SplitYAML before GenerateManifests (cmpserver/plugin/plugin.go:278
	// and reposerver/repository/repository.go:2487 at v3.6.1). Splitting the
	// whole output first and then decoding each document is the same.
	manifests, err := render.DecodeGeneratedOutput(decodePath, result.Stdout, opts.TrackManifest)
	if err != nil {
		message := fmt.Sprintf("config management plugin %s produced invalid %s for %s at %s: %s", pluginDisplayName(name), phase, execPolicySourceLabel(source), decodePath, redactSensitiveText(err.Error(), sensitive))
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s", message)
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
