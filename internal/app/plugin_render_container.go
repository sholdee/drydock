package app

import (
	"context"
	"fmt"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

func (p localProvider) renderContainerPolicyPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions, name string, policyPlugin pluginpolicy.Plugin) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	prepared, diags, err := prepareCommandPluginRender(source, opts, name, policyPlugin)
	if err != nil {
		return nil, diags, true, err
	}
	sourcePath, sourceDir, params, applicationEnv := prepared.sourcePath, prepared.sourceDir, prepared.params, prepared.applicationEnv
	lifecycle, sensitive := prepared.lifecycle, prepared.sensitive
	containerConfig := *policyPlugin.Container
	containerConfig.Lifecycle = lifecycle
	extraEnv := composePolicyPluginExtraEnv(opts, applicationEnv, params, p.offline)
	result, err := p.pluginContainerRunner.Run(ctx, plugincontainer.Request{
		SourceDir:         sourceDir,
		RepositoryDir:     source.RepoRoot,
		SourceRelPath:     sourcePath,
		PluginName:        name,
		PolicyFingerprint: p.pluginPolicyFingerprint,
		Config:            containerConfig,
		Offline:           p.offline,
		ForbiddenRoots:    p.execProtectedRoots(source.RepoRoot),
		CacheRoot:         p.pluginCacheDir,
		ExtraEnv:          extraEnv,
		SensitiveValues:   sensitive,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, true, ctxErr
		}
		message := fmt.Sprintf("config management plugin %s failed: %s", pluginDisplayName(name), redactSensitiveText(err.Error(), sensitive))
		return nil, []diagnostic.Diagnostic{pluginFailedDiagnostic(message)}, true, fmt.Errorf("%s", message)
	}
	phase, decodePath := execPolicyDecodeTarget(name, source, len(policyPlugin.Container.Lifecycle.PostRenderers) > 0)
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
		engine:     string(pluginpolicy.EngineContainer),
		runtime:    string(policyPlugin.Container.Runtime),
		image:      policyPlugin.Container.Image,
		executions: result.Executions,
	})
	return manifests, nil, true, nil
}
