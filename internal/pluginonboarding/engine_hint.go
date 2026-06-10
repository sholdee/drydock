package pluginonboarding

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

func suggestedEngineForPlugin(plugin PluginReport) pluginpolicy.Engine {
	if plugin.CMP == nil {
		return ""
	}
	switch {
	case commandLooksLikeAVPCompat(plugin.Generate):
		return pluginpolicy.EngineAVPCompat
	case nativeKustomizeCMPAccepted(*plugin.CMP):
		return pluginpolicy.EngineNativeKustomize
	default:
		return ""
	}
}

func commandLooksLikeAVPCompat(tokens []string) bool {
	for i, token := range tokens {
		fields := strings.Fields(token)
		if len(fields) == 0 {
			fields = []string{token}
		}
		for j, field := range fields {
			if filepath.Base(field) != "argocd-vault-plugin" {
				continue
			}
			if hasGenerateToken(fields[j+1:]) || hasGenerateToken(tokens[i+1:]) {
				return true
			}
		}
	}
	return false
}

func hasGenerateToken(tokens []string) bool {
	for _, token := range tokens {
		if slices.Contains(strings.Fields(token), "generate") {
			return true
		}
	}
	return false
}

func nativeKustomizeCMPAccepted(plugin config.ConfigManagementPlugin) bool {
	if plugin.HasInit {
		return false
	}
	tokens, ok := nativeKustomizeTokens(plugin)
	if !ok {
		return false
	}
	_, err := normalizeNativeKustomizeTokens(tokens)
	return err == nil
}

func nativeKustomizeTokens(plugin config.ConfigManagementPlugin) ([]string, bool) {
	command := cleanArgv(plugin.GenerateCommand)
	args := cleanArgv(plugin.GenerateArgs)
	if len(command) == 2 && isNativeKustomizeShell(command[0], command[1]) {
		if len(args) != 1 {
			return nil, false
		}
		fields, ok := safeNativeKustomizeShellFields(args[0])
		return fields, ok
	}
	tokens := append(append([]string(nil), command...), args...)
	return tokens, len(tokens) > 0
}

func isNativeKustomizeShell(command, flag string) bool {
	shell := filepath.Clean(command)
	return (shell == "sh" || shell == "/bin/sh") && flag == "-c"
}

func safeNativeKustomizeShellFields(command string) ([]string, bool) {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, "\n\r|&;<>(){}[]*$`\"'!?\\") {
		return nil, false
	}
	fields := strings.Fields(command)
	return fields, len(fields) > 0
}

func normalizeNativeKustomizeTokens(tokens []string) ([]string, error) {
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

func nativeKustomizeGenerateSeed(plugin config.ConfigManagementPlugin) (command []string, args []string, ok bool) {
	tokens, ok := nativeKustomizeTokens(plugin)
	if !ok {
		return nil, nil, false
	}
	options, err := normalizeNativeKustomizeTokens(tokens)
	if err != nil {
		return nil, nil, false
	}
	return []string{"kustomize", "build"}, options, true
}
