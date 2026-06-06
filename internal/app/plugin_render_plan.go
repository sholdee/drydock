package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

type policyPluginRenderPlan struct {
	name   string
	plugin pluginpolicy.Plugin
	opts   render.RenderOptions
}

func (p localProvider) planPolicyPluginRender(source render.ResolvedSource, opts render.RenderOptions) (policyPluginRenderPlan, []diagnostic.Diagnostic, bool, error) {
	if opts.Plugin == nil {
		return policyPluginRenderPlan{}, nil, false, nil
	}
	name := strings.TrimSpace(opts.Plugin.Name)
	if name == "" {
		return p.planUnnamedPolicyPluginRender(source, opts)
	}
	policyPlugin, ok := p.pluginPolicy.Plugin(name)
	if !ok {
		return policyPluginRenderPlan{}, nil, false, nil
	}
	if message := validatePolicyPluginSource(name, source, opts, policyPlugin); message != "" {
		return policyPluginRenderPlan{}, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
	return policyPluginRenderPlan{name: name, plugin: policyPlugin, opts: opts}, nil, true, nil
}

func (p localProvider) planUnnamedPolicyPluginRender(source render.ResolvedSource, opts render.RenderOptions) (policyPluginRenderPlan, []diagnostic.Diagnostic, bool, error) {
	matches, err := p.policyPluginStaticDiscoveryMatches(source)
	if err != nil {
		return policyPluginRenderPlan{}, nil, true, err
	}
	switch len(matches) {
	case 0:
		message := unnamedPolicyPluginNoMatchMessage()
		return policyPluginRenderPlan{}, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	case 1:
		name := matches[0].name
		policyPlugin := matches[0].plugin
		plugin := *opts.Plugin
		plugin.Name = name
		opts.Plugin = &plugin
		if message := validatePolicyPluginSource(name, source, opts, policyPlugin); message != "" {
			return policyPluginRenderPlan{}, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		return policyPluginRenderPlan{name: name, plugin: policyPlugin, opts: opts}, nil, true, nil
	default:
		message := unnamedPolicyPluginAmbiguousMessage(matches)
		return policyPluginRenderPlan{}, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
	}
}

func (p localProvider) renderPolicyPluginPlan(ctx context.Context, source render.ResolvedSource, plan policyPluginRenderPlan) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	switch plan.plugin.Engine {
	case pluginpolicy.EngineAVPCompat:
		return p.renderAVPCompatPolicyPluginSource(ctx, source, plan.opts)
	case pluginpolicy.EngineNativeKustomize:
		return p.renderNativeKustomizePolicyPluginSource(ctx, source, plan)
	case pluginpolicy.EngineExec:
		if !plan.opts.EnablePlugins {
			message := fmt.Sprintf("config management plugin %s uses exec policy, which requires --enable-plugins", pluginDisplayName(plan.name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		if !p.pluginPolicyExecTrusted {
			message := fmt.Sprintf("config management plugin %s uses exec policy from an untrusted policy source; use a policy from the diff baseline or pass --plugin-policy-ref for a trusted Git ref", pluginDisplayName(plan.name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		return p.renderExecPolicyPluginSource(ctx, source, plan.opts, plan.name, plan.plugin)
	case pluginpolicy.EngineContainer:
		if !plan.opts.EnablePlugins {
			message := fmt.Sprintf("config management plugin %s uses container policy, which requires --enable-plugins", pluginDisplayName(plan.name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		if !p.pluginPolicyExecTrusted {
			message := fmt.Sprintf("config management plugin %s uses container policy from an untrusted policy source; use a policy from the diff baseline or pass --plugin-policy-ref for a trusted Git ref", pluginDisplayName(plan.name))
			return nil, unsupportedPluginDiagnostic(message), true, unsupportedPolicyPluginError(message)
		}
		return p.renderContainerPolicyPluginSource(ctx, source, plan.opts, plan.name, plan.plugin)
	default:
		message := fmt.Sprintf("config management plugin %s has unsupported trusted policy engine %q", pluginDisplayName(plan.name), plan.plugin.Engine)
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
