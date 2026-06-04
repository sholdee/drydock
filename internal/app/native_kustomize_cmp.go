package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

func (p localProvider) renderNativeKustomizePluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, bool, error) {
	if opts.Plugin == nil {
		return nil, nil, false, nil
	}
	plugin, ok := p.configManagementPlugins[strings.TrimSpace(opts.Plugin.Name)]
	if !ok {
		return nil, nil, false, nil
	}
	manifests, diags, err := p.renderNativeKustomizePluginSourceWithConfig(ctx, source, opts, opts.Plugin.Name, plugin)
	return manifests, diags, true, err
}

func (p localProvider) renderNativeKustomizePluginSourceWithConfig(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions, name string, plugin config.ConfigManagementPlugin) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	if source.Chart != "" {
		reason := "chart sources are unsupported by the native Kustomize adapter"
		return nil, unsupportedNativeKustomizePluginDiagnostic(name, reason), unsupportedNativeKustomizePluginError(name, reason)
	}
	if source.Path == "" {
		reason := "path source is required for the native Kustomize adapter"
		return nil, unsupportedNativeKustomizePluginDiagnostic(name, reason), unsupportedNativeKustomizePluginError(name, reason)
	}
	if len(opts.Plugin.Env) != 0 || len(opts.Plugin.Parameters) != 0 {
		reason := "Application plugin env or parameters are unsupported by the native Kustomize adapter"
		return nil, unsupportedNativeKustomizePluginDiagnostic(name, reason), unsupportedNativeKustomizePluginError(name, reason)
	}
	buildOptions, err := nativeKustomizePluginBuildOptions(plugin)
	if err != nil {
		return nil, unsupportedNativeKustomizePluginDiagnostic(name, err.Error()), unsupportedNativeKustomizePluginError(name, err.Error())
	}
	nativeOptions := opts
	nativeOptions.Plugin = nil
	nativeOptions.BuildOptions = append([]string(nil), buildOptions...)
	manifests, diags, err := (render.KustomizeRenderer{}).Render(ctx, source, nativeOptions)
	return manifests, diags, err
}

func configManagementPluginSeed(name string, seed *pluginpolicy.ConfigManagementPluginSeed) config.ConfigManagementPlugin {
	if seed == nil {
		return config.ConfigManagementPlugin{Name: name}
	}
	plugin := config.ConfigManagementPlugin{Name: name}
	if seed.Generate != nil {
		plugin.GenerateCommand = append([]string(nil), seed.Generate.Command...)
		plugin.GenerateArgs = append([]string(nil), seed.Generate.Args...)
	}
	if seed.Discover != nil {
		plugin.Discover = config.ConfigManagementPluginDiscovery{
			FileName: seed.Discover.FileName,
			FindGlob: seed.Discover.FindGlob,
		}
	}
	return plugin
}

func nativeKustomizePluginBuildOptions(plugin config.ConfigManagementPlugin) ([]string, error) {
	if plugin.HasInit {
		return nil, fmt.Errorf("plugin init is unsupported")
	}
	tokens, err := nativeKustomizePluginTokens(plugin)
	if err != nil {
		return nil, err
	}
	return normalizeNativeKustomizeBuildTokens(tokens)
}

func nativeKustomizePluginTokens(plugin config.ConfigManagementPlugin) ([]string, error) {
	command := trimCommandTokens(plugin.GenerateCommand)
	args := trimCommandTokens(plugin.GenerateArgs)
	if isShellCommand(command) {
		if len(args) != 1 {
			return nil, fmt.Errorf("shell wrapper must provide exactly one command string")
		}
		return safeShellFields(args[0])
	}
	tokens := append(append([]string(nil), command...), args...)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("generate command is empty")
	}
	return tokens, nil
}

func trimCommandTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token != "" {
			out = append(out, token)
		}
	}
	return out
}

func isShellCommand(command []string) bool {
	if len(command) != 2 {
		return false
	}
	shell := filepath.Clean(command[0])
	return (shell == "sh" || shell == "/bin/sh") && command[1] == "-c"
}

func safeShellFields(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("shell command is empty")
	}
	if strings.ContainsAny(command, "\n\r|&;<>(){}[]*$`\"'!?\\") {
		return nil, fmt.Errorf("shell command uses unsupported syntax")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("shell command is empty")
	}
	return fields, nil
}

func normalizeNativeKustomizeBuildTokens(tokens []string) ([]string, error) {
	if len(tokens) < 2 || filepath.Base(tokens[0]) != "kustomize" || tokens[1] != "build" {
		return nil, fmt.Errorf("generate command is not kustomize build")
	}
	options := make([]string, 0, len(tokens)-2)
	for i := 2; i < len(tokens); i++ {
		token := strings.TrimSpace(tokens[i])
		if token == "" || token == "." {
			continue
		}
		switch {
		case token == "--enable-helm",
			strings.HasPrefix(token, "--helm-api-versions="),
			strings.HasPrefix(token, "--load-restrictor="):
			options = append(options, token)
		case token == "--helm-api-versions" || token == "--load-restrictor":
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("kustomize build option requires a value")
			}
			i++
			value := strings.TrimSpace(tokens[i])
			if value == "" || strings.HasPrefix(value, "-") {
				return nil, fmt.Errorf("kustomize build option requires a value")
			}
			options = append(options, token, value)
		default:
			if strings.HasPrefix(token, "-") {
				return nil, fmt.Errorf("unsupported kustomize build option")
			}
			return nil, fmt.Errorf("unsupported kustomize build path or remote operand")
		}
	}
	if err := render.ValidateKustomizeBuildOptions(options); err != nil {
		return nil, fmt.Errorf("unsupported kustomize build option")
	}
	return options, nil
}

func unsupportedNativeKustomizePluginDiagnostic(name, reason string) []diagnostic.Diagnostic {
	message := unsupportedNativeKustomizePluginMessage(name, reason)
	return []diagnostic.Diagnostic{{
		Code:     diagnostic.CodePluginUnsupported,
		Severity: diagnostic.SeverityError,
		Category: "plugin",
		Message:  message,
	}}
}

func unsupportedNativeKustomizePluginError(name, reason string) error {
	message := unsupportedNativeKustomizePluginMessage(name, reason)
	return fmt.Errorf("%s: %w", message, render.ErrUnsupportedPlugin)
}

func unsupportedNativeKustomizePluginMessage(name, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "the discovered CMP definition is incompatible"
	}
	return fmt.Sprintf("config management plugin %s is not supported by the native Kustomize adapter: %s", pluginDisplayName(name), reason)
}
