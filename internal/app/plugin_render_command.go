package app

import (
	"fmt"
	"path/filepath"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

// preparedCommandPlugin is what the render of an exec or container policy
// plugin needs before it runs the command.
type preparedCommandPlugin struct {
	sourcePath     string
	sourceDir      string
	params         validatedPluginParameters
	applicationEnv []string
	// lifecycle is the exec block of an exec plugin, or the lifecycle of a
	// container plugin, with the parameter templates expanded.
	lifecycle pluginpolicy.ExecConfig
	sensitive []string
}

// prepareCommandPluginRender runs every check that can stop the render of
// an exec or container policy plugin before its command runs: a chart
// source, an empty path, a missing exec or container block, an unsafe
// source path, rejected parameters, Application plugin env outside
// applicationEnv.allow, and command templates that do not expand. The
// renderer (renderExecPolicyPluginSource, renderContainerPolicyPluginSource)
// and the changed-only selection (pluginRunsWithBuildEnv) both call it, so
// they agree on which plugin sources run a command. A failed check returns
// plugin.unsupported diagnostics and an error, or only an error for a path
// that is not safe.
func prepareCommandPluginRender(source render.ResolvedSource, opts render.RenderOptions, name string, policyPlugin pluginpolicy.Plugin) (preparedCommandPlugin, []diagnostic.Diagnostic, error) {
	var engine string
	var block *pluginpolicy.ExecConfig
	switch policyPlugin.Engine {
	case pluginpolicy.EngineExec:
		engine, block = "exec", policyPlugin.Exec
	case pluginpolicy.EngineContainer:
		engine = "container"
		if policyPlugin.Container != nil {
			block = &policyPlugin.Container.Lifecycle
		}
	case pluginpolicy.EngineAVPCompat, pluginpolicy.EngineNativeKustomize:
		return preparedCommandPlugin{}, nil, fmt.Errorf("config management plugin %s policy engine %q runs no command", pluginDisplayName(name), policyPlugin.Engine)
	default:
		return preparedCommandPlugin{}, nil, fmt.Errorf("config management plugin %s policy engine %q runs no command", pluginDisplayName(name), policyPlugin.Engine)
	}
	unsupported := func(message string) (preparedCommandPlugin, []diagnostic.Diagnostic, error) {
		return preparedCommandPlugin{}, unsupportedPluginDiagnostic(message), unsupportedPolicyPluginError(message)
	}
	if source.Chart != "" {
		return unsupported(fmt.Sprintf("config management plugin %s uses chart source, which is unsupported by %s policy", pluginDisplayName(name), engine))
	}
	if source.Path == "" {
		return unsupported(fmt.Sprintf("config management plugin %s must define path for %s policy", pluginDisplayName(name), engine))
	}
	if block == nil {
		return unsupported(fmt.Sprintf("config management plugin %s has invalid %s policy", pluginDisplayName(name), engine))
	}
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return preparedCommandPlugin{}, nil, err
	}
	if err := rejectLocalSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return preparedCommandPlugin{}, nil, err
	}
	var params validatedPluginParameters
	var message string
	if engine == "exec" {
		params, message = validateExecPluginParameters(name, policyPlugin, opts.Plugin, source.RepoRoot, sourcePath)
	} else {
		params, message = validateContainerPluginParameters(name, policyPlugin, opts.Plugin, source.RepoRoot, sourcePath)
	}
	if message != "" {
		return unsupported(message)
	}
	applicationEnv, message := validateApplicationPluginEnv(name, block.ApplicationEnv.Allow, opts.Plugin.Env, policyPluginBuildEnvEntries(opts))
	if message != "" {
		return unsupported(message)
	}
	lifecycle, sensitive, message := expandExecPluginCommandTemplates(*block, params)
	if message != "" {
		return preparedCommandPlugin{}, unsupportedPluginDiagnostic(fmt.Sprintf("config management plugin %s %s", pluginDisplayName(name), message)), unsupportedPolicyPluginError(message)
	}
	return preparedCommandPlugin{
		sourcePath:     sourcePath,
		sourceDir:      filepath.Join(source.RepoRoot, sourcePath),
		params:         params,
		applicationEnv: applicationEnv,
		lifecycle:      lifecycle,
		sensitive:      sensitive,
	}, nil, nil
}
