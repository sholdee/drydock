package app

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

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
