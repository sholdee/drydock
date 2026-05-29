package pluginpolicy

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

func parseExecConfig(fields map[string]*yaml.Node, path, pointer string) (ExecConfig, error) {
	allowed := map[string]bool{
		"engine":        true,
		"workdir":       true,
		"init":          true,
		"generate":      true,
		"postRenderers": true,
		"env":           true,
		"output":        true,
	}
	if err := rejectUnknownFields(fields, allowed, pointer); err != nil {
		return ExecConfig{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	workdir := ExecWorkdirSource
	if node := fields["workdir"]; node != nil {
		got, err := stringValue(node, pointer+".workdir")
		if err != nil {
			return ExecConfig{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		workdir = got
	}
	if workdir != ExecWorkdirSource {
		return ExecConfig{}, fmt.Errorf("parse plugin policy %s: %s.workdir must be %q", path, pointer, ExecWorkdirSource)
	}

	var init *ExecCommand
	if node := fields["init"]; node != nil && !isNullNode(node) {
		command, err := parseExecCommand(node, path, pointer+".init", DefaultInitTimeout)
		if err != nil {
			return ExecConfig{}, err
		}
		init = &command
	}
	if fields["generate"] == nil || isNullNode(fields["generate"]) {
		return ExecConfig{}, fmt.Errorf("parse plugin policy %s: missing required field %s.generate", path, pointer)
	}
	generate, err := parseExecCommand(fields["generate"], path, pointer+".generate", DefaultGenerateTimeout)
	if err != nil {
		return ExecConfig{}, err
	}
	postRenderers, err := parsePostRenderers(fields["postRenderers"], path, pointer+".postRenderers")
	if err != nil {
		return ExecConfig{}, err
	}
	env, err := parseExecEnv(fields["env"], path, pointer+".env")
	if err != nil {
		return ExecConfig{}, err
	}
	output, err := parseExecOutput(fields["output"], path, pointer+".output")
	if err != nil {
		return ExecConfig{}, err
	}
	return ExecConfig{
		Workdir:       workdir,
		Init:          init,
		Generate:      generate,
		PostRenderers: postRenderers,
		Env:           env,
		Output:        output,
	}, nil
}

func parsePostRenderers(node *yaml.Node, path, pointer string) ([]ExecCommand, error) {
	if node == nil {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("parse plugin policy %s: %s must be a sequence", path, pointer)
	}
	if len(node.Content) == 0 {
		return nil, fmt.Errorf("parse plugin policy %s: %s must not be empty", path, pointer)
	}
	out := make([]ExecCommand, 0, len(node.Content))
	for index, child := range node.Content {
		command, err := parseExecCommand(child, path, fmt.Sprintf("%s[%d]", pointer, index), DefaultPostRendererTimeout)
		if err != nil {
			return nil, err
		}
		out = append(out, command)
	}
	return out, nil
}

func parseExecCommand(node *yaml.Node, path, pointer string, defaultTimeout time.Duration) (ExecCommand, error) {
	if node.Kind != yaml.MappingNode {
		return ExecCommand{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return ExecCommand{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = node.Content[i+1]
	}
	if err := rejectUnknownFields(fields, map[string]bool{"command": true, "timeout": true}, pointer); err != nil {
		return ExecCommand{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if fields["command"] == nil {
		return ExecCommand{}, fmt.Errorf("parse plugin policy %s: missing required field %s.command", path, pointer)
	}
	command, err := stringSequence(fields["command"], pointer+".command")
	if err != nil {
		return ExecCommand{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if err := validateCommand(command, pointer+".command"); err != nil {
		return ExecCommand{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	timeout := defaultTimeout
	if fields["timeout"] != nil {
		timeout, err = durationValue(fields["timeout"], pointer+".timeout")
		if err != nil {
			return ExecCommand{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
	}
	if timeout <= 0 {
		return ExecCommand{}, fmt.Errorf("parse plugin policy %s: %s.timeout must be greater than zero", path, pointer)
	}
	return ExecCommand{Command: command, Timeout: timeout}, nil
}

func parseExecEnv(node *yaml.Node, path, pointer string) (ExecEnv, error) {
	if node == nil || isNullNode(node) {
		return ExecEnv{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = node.Content[i+1]
	}
	if err := rejectUnknownFields(fields, map[string]bool{"allow": true}, pointer); err != nil {
		return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	allow, err := stringSequence(fields["allow"], pointer+".allow")
	if fields["allow"] == nil {
		return ExecEnv{}, nil
	}
	if err != nil {
		return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if len(allow) > maxEnvAllowCount {
		return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %s.allow has %d entries, maximum is %d", path, pointer, len(allow), maxEnvAllowCount)
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(allow))
	for index, rawName := range allow {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %s.allow[%d] must not be empty", path, pointer, index)
		}
		if err := validateEnvName(name); err != nil {
			return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %s.allow: %w", path, pointer, err)
		}
		if _, ok := seen[name]; ok {
			return ExecEnv{}, fmt.Errorf("parse plugin policy %s: %s.allow contains duplicate env name %q", path, pointer, name)
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return ExecEnv{Allow: normalized}, nil
}

func parseExecOutput(node *yaml.Node, path, pointer string) (ExecOutput, error) {
	output := ExecOutput{
		MaxStdoutBytes: DefaultMaxStdoutBytes,
		MaxStderrBytes: DefaultMaxStderrBytes,
	}
	if node == nil || isNullNode(node) {
		return output, nil
	}
	if node.Kind != yaml.MappingNode {
		return ExecOutput{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return ExecOutput{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = node.Content[i+1]
	}
	if err := rejectUnknownFields(fields, map[string]bool{"maxStdoutBytes": true, "maxStderrBytes": true}, pointer); err != nil {
		return ExecOutput{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	var err error
	if fields["maxStdoutBytes"] != nil {
		output.MaxStdoutBytes, err = int64Value(fields["maxStdoutBytes"], pointer+".maxStdoutBytes")
		if err != nil {
			return ExecOutput{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
	}
	if fields["maxStderrBytes"] != nil {
		output.MaxStderrBytes, err = int64Value(fields["maxStderrBytes"], pointer+".maxStderrBytes")
		if err != nil {
			return ExecOutput{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
	}
	if output.MaxStdoutBytes <= 0 {
		return ExecOutput{}, fmt.Errorf("parse plugin policy %s: %s.maxStdoutBytes must be greater than zero", path, pointer)
	}
	if output.MaxStderrBytes <= 0 {
		return ExecOutput{}, fmt.Errorf("parse plugin policy %s: %s.maxStderrBytes must be greater than zero", path, pointer)
	}
	return output, nil
}

func validateCommand(command []string, pointer string) error {
	if len(command) == 0 {
		return fmt.Errorf("%s must not be empty", pointer)
	}
	for index, token := range command {
		if strings.TrimSpace(token) == "" {
			return fmt.Errorf("%s[%d] must not be empty", pointer, index)
		}
	}
	argv0 := command[0]
	if strings.ContainsAny(argv0, `/\`) && !filepath.IsAbs(argv0) {
		return fmt.Errorf("%s[0] must be a basename or absolute path", pointer)
	}
	if isDeniedInterpreter(argv0) {
		return fmt.Errorf("%s[0] %q is not permitted; use a trusted executable directly", pointer, argv0)
	}
	return nil
}

func isDeniedInterpreter(command string) bool {
	base := strings.ToLower(filepath.Base(command))
	switch base {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "env", "python", "python3", "node", "ruby", "perl", "pwsh", "powershell":
		return true
	default:
		return false
	}
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var reservedExecEnvNames = map[string]struct{}{
	"PATH":                        {},
	"BASH_ENV":                    {},
	"ENV":                         {},
	"PYTHONPATH":                  {},
	"PYTHONHOME":                  {},
	"PYTHONSTARTUP":               {},
	"RUBYOPT":                     {},
	"RUBYLIB":                     {},
	"NODE_OPTIONS":                {},
	"NODE_PATH":                   {},
	"PERL5LIB":                    {},
	"PERL5OPT":                    {},
	"PSMODULEPATH":                {},
	"POWERSHELL_TELEMETRY_OPTOUT": {},
}

var reservedExecEnvPrefixes = []string{"LD_", "DYLD_"}

func validateEnvName(name string) error {
	if !envNamePattern.MatchString(name) {
		return fmt.Errorf("env name %q is invalid", name)
	}
	upper := strings.ToUpper(name)
	if _, ok := reservedExecEnvNames[upper]; ok {
		return fmt.Errorf("env name %q is reserved", name)
	}
	for _, prefix := range reservedExecEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return fmt.Errorf("env name %q is reserved", name)
		}
	}
	return nil
}
