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
	if err := rejectUnknownFields(fields, execPluginAllowedFields(), pointer); err != nil {
		return ExecConfig{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	return parseExecLifecycleConfig(fields, path, pointer)
}

func parseExecLifecycleConfig(fields map[string]*yaml.Node, path, pointer string) (ExecConfig, error) {
	workdir, err := parseExecWorkdir(fields, path, pointer)
	if err != nil {
		return ExecConfig{}, err
	}
	copyConfig, err := parseExecCopy(fields["copy"], path, pointer+".copy")
	if err != nil {
		return ExecConfig{}, err
	}

	initCommand, hasInit, err := parseExecInit(fields, path, pointer)
	if err != nil {
		return ExecConfig{}, err
	}
	var init *ExecCommand
	if hasInit {
		init = &initCommand
	}
	generate, err := parseExecGenerate(fields, path, pointer)
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
	parameters, err := parseExecParameters(fields["parameters"], copyConfig.Scope, path, pointer+".parameters")
	if err != nil {
		return ExecConfig{}, err
	}
	output, err := parseExecOutput(fields["output"], path, pointer+".output")
	if err != nil {
		return ExecConfig{}, err
	}
	return ExecConfig{
		Workdir:       workdir,
		Copy:          copyConfig,
		Init:          init,
		Generate:      generate,
		PostRenderers: postRenderers,
		Env:           env,
		Parameters:    parameters,
		Output:        output,
	}, nil
}

func parseExecWorkdir(fields map[string]*yaml.Node, path, pointer string) (string, error) {
	workdir := ExecWorkdirSource
	if node := fields["workdir"]; node != nil {
		got, err := stringValue(node, pointer+".workdir")
		if err != nil {
			return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		workdir = got
	}
	if workdir != ExecWorkdirSource {
		return "", fmt.Errorf("parse plugin policy %s: %s.workdir must be %q", path, pointer, ExecWorkdirSource)
	}
	return workdir, nil
}

func parseExecInit(fields map[string]*yaml.Node, path, pointer string) (ExecCommand, bool, error) {
	node := fields["init"]
	if node == nil || isNullNode(node) {
		return ExecCommand{}, false, nil
	}
	command, err := parseExecCommand(node, path, pointer+".init", DefaultInitTimeout)
	if err != nil {
		return ExecCommand{}, false, err
	}
	return command, true, nil
}

func parseExecGenerate(fields map[string]*yaml.Node, path, pointer string) (ExecCommand, error) {
	if fields["generate"] == nil || isNullNode(fields["generate"]) {
		return ExecCommand{}, fmt.Errorf("parse plugin policy %s: missing required field %s.generate", path, pointer)
	}
	return parseExecCommand(fields["generate"], path, pointer+".generate", DefaultGenerateTimeout)
}

func parseExecCopy(node *yaml.Node, path, pointer string) (ExecCopy, error) {
	out := ExecCopy{Scope: ExecCopyScopeSource}
	if node == nil || isNullNode(node) {
		return out, nil
	}
	if node.Kind != yaml.MappingNode {
		return ExecCopy{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return ExecCopy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = node.Content[i+1]
	}
	if err := rejectUnknownFields(fields, map[string]bool{"scope": true, "include": true}, pointer); err != nil {
		return ExecCopy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if fields["scope"] != nil {
		scope, err := stringValue(fields["scope"], pointer+".scope")
		if err != nil {
			return ExecCopy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		out.Scope = strings.TrimSpace(scope)
	}
	switch out.Scope {
	case ExecCopyScopeSource, ExecCopyScopeRepository:
	default:
		return ExecCopy{}, fmt.Errorf("parse plugin policy %s: %s.scope has unsupported copy scope %q", path, pointer, out.Scope)
	}
	include, err := parseExecCopyInclude(fields["include"], path, pointer+".include")
	if err != nil {
		return ExecCopy{}, err
	}
	out.Include = include
	if out.Scope == ExecCopyScopeRepository && len(out.Include) == 0 {
		return ExecCopy{}, fmt.Errorf("parse plugin policy %s: %s.include is required when scope is %q", path, pointer, ExecCopyScopeRepository)
	}
	if out.Scope == ExecCopyScopeSource && len(out.Include) > 0 {
		return ExecCopy{}, fmt.Errorf("parse plugin policy %s: %s.include is only supported when scope is %q", path, pointer, ExecCopyScopeRepository)
	}
	return out, nil
}

func parseExecCopyInclude(node *yaml.Node, path, pointer string) ([]string, error) {
	if node == nil || isNullNode(node) {
		return nil, nil
	}
	include, err := stringSequence(node, pointer)
	if err != nil {
		return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if len(include) > maxExecCopyIncludeCount {
		return nil, fmt.Errorf("parse plugin policy %s: %s has %d entries, maximum is %d", path, pointer, len(include), maxExecCopyIncludeCount)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(include))
	for index, raw := range include {
		raw = strings.TrimSpace(raw)
		if strings.Contains(raw, `\`) {
			return nil, fmt.Errorf("parse plugin policy %s: %s[%d] is invalid: backslashes are not allowed", path, pointer, index)
		}
		pattern := filepath.ToSlash(raw)
		if pattern == "" {
			return nil, fmt.Errorf("parse plugin policy %s: %s[%d] must not be empty", path, pointer, index)
		}
		if err := validateRepositoryRelativePattern(pattern); err != nil {
			return nil, fmt.Errorf("parse plugin policy %s: %s[%d] is invalid: %w", path, pointer, index, err)
		}
		if _, ok := seen[pattern]; ok {
			return nil, fmt.Errorf("parse plugin policy %s: %s contains duplicate include pattern %q", path, pointer, pattern)
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
	}
	sort.Strings(out)
	return out, nil
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

func parseExecParameters(node *yaml.Node, copyScope string, path, pointer string) (ExecParameters, error) {
	if node == nil || isNullNode(node) {
		return ExecParameters{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return ExecParameters{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return ExecParameters{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"allow": true}, pointer); err != nil {
		return ExecParameters{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	allowNode := fields["allow"]
	if allowNode == nil || isNullNode(allowNode) {
		return ExecParameters{}, nil
	}
	if allowNode.Kind != yaml.SequenceNode {
		return ExecParameters{}, fmt.Errorf("parse plugin policy %s: %s.allow must be a sequence", path, pointer)
	}
	if len(allowNode.Content) > maxExecParameterAllowCount {
		return ExecParameters{}, fmt.Errorf("parse plugin policy %s: %s.allow has %d entries, maximum is %d", path, pointer, len(allowNode.Content), maxExecParameterAllowCount)
	}
	allow, err := parseExecParameterAllow(allowNode, copyScope, path, pointer)
	if err != nil {
		return ExecParameters{}, err
	}
	return ExecParameters{Allow: allow}, nil
}

type execParameterSeen struct {
	names    map[string]struct{}
	envNames map[string]string
}

func newExecParameterSeen() execParameterSeen {
	return execParameterSeen{
		names:    map[string]struct{}{},
		envNames: map[string]string{},
	}
}

func parseExecParameterAllow(node *yaml.Node, copyScope string, path, pointer string) ([]ExecParameter, error) {
	seen := newExecParameterSeen()
	allow := make([]ExecParameter, 0, len(node.Content))
	for index, child := range node.Content {
		item, err := parseExecParameter(child, path, fmt.Sprintf("%s.allow[%d]", pointer, index))
		if err != nil {
			return nil, err
		}
		if err := validateExecParameterCopyScope(item, copyScope, path, pointer, index); err != nil {
			return nil, err
		}
		if err := seen.add(item, path, pointer); err != nil {
			return nil, err
		}
		allow = append(allow, item)
	}
	sort.Slice(allow, func(i, j int) bool { return allow[i].Name < allow[j].Name })
	return allow, nil
}

func validateExecParameterCopyScope(item ExecParameter, copyScope string, path, pointer string, index int) error {
	if item.Path == nil || item.Path.Base != ExecParameterPathBaseRepository || copyScope == ExecCopyScopeRepository {
		return nil
	}
	return fmt.Errorf("parse plugin policy %s: %s.allow[%d].path.base %q requires copy.scope %q", path, pointer, index, ExecParameterPathBaseRepository, ExecCopyScopeRepository)
}

func (seen execParameterSeen) add(item ExecParameter, path, pointer string) error {
	if _, ok := seen.names[item.Name]; ok {
		return fmt.Errorf("parse plugin policy %s: %s.allow contains duplicate parameter name %q", path, pointer, item.Name)
	}
	seen.names[item.Name] = struct{}{}
	envName := parameterEnvName(item.Name)
	if existing, ok := seen.envNames[envName]; ok {
		return fmt.Errorf("parse plugin policy %s: %s.allow parameter names %q and %q collide in environment variable %q", path, pointer, existing, item.Name, envName)
	}
	seen.envNames[envName] = item.Name
	return nil
}

func parseExecParameter(node *yaml.Node, path, pointer string) (ExecParameter, error) {
	if node.Kind != yaml.MappingNode {
		return ExecParameter{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return ExecParameter{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"name": true, "type": true, "required": true, "path": true}, pointer); err != nil {
		return ExecParameter{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	name, err := parseExecParameterName(fields, path, pointer)
	if err != nil {
		return ExecParameter{}, err
	}
	typ, err := parseExecParameterType(fields, path, pointer)
	if err != nil {
		return ExecParameter{}, err
	}
	required, err := parseExecParameterRequired(fields, path, pointer)
	if err != nil {
		return ExecParameter{}, err
	}
	pathConstraintValue, hasPathConstraint, err := parseExecParameterPathConstraint(fields["path"], typ, path, pointer)
	if err != nil {
		return ExecParameter{}, err
	}
	var pathConstraint *ExecParameterPath
	if hasPathConstraint {
		pathConstraint = &pathConstraintValue
	}
	return ExecParameter{Name: name, Type: typ, Required: required, Path: pathConstraint}, nil
}

func parseExecParameterName(fields map[string]*yaml.Node, path, pointer string) (string, error) {
	name, err := requiredString(fields, "name", pointer+".name")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("parse plugin policy %s: %s.name must not be empty", path, pointer)
	}
	if !parameterNamePattern.MatchString(name) {
		return "", fmt.Errorf("parse plugin policy %s: %s.name %q is invalid", path, pointer, name)
	}
	return name, nil
}

func parseExecParameterType(fields map[string]*yaml.Node, path, pointer string) (ExecParameterType, error) {
	typValue, err := requiredString(fields, "type", pointer+".type")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	typ := ExecParameterType(strings.TrimSpace(typValue))
	switch typ {
	case ExecParameterTypeString, ExecParameterTypeArray, ExecParameterTypeMap:
		return typ, nil
	default:
		return "", fmt.Errorf("parse plugin policy %s: %s.type has unsupported parameter type %q", path, pointer, typValue)
	}
}

func parseExecParameterRequired(fields map[string]*yaml.Node, path, pointer string) (bool, error) {
	if fields["required"] == nil {
		return false, nil
	}
	required, err := boolValue(fields["required"], pointer+".required")
	if err != nil {
		return false, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	return required, nil
}

func parseExecParameterPathConstraint(node *yaml.Node, typ ExecParameterType, path, pointer string) (ExecParameterPath, bool, error) {
	if node == nil || isNullNode(node) {
		return ExecParameterPath{}, false, nil
	}
	if typ != ExecParameterTypeString {
		return ExecParameterPath{}, false, fmt.Errorf("parse plugin policy %s: %s.path is only supported for string parameters", path, pointer)
	}
	pathConstraint, err := parseExecParameterPath(node, path, pointer+".path")
	if err != nil {
		return ExecParameterPath{}, false, err
	}
	return *pathConstraint, true, nil
}

func parseExecParameterPath(node *yaml.Node, path, pointer string) (*ExecParameterPath, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = node.Content[i+1]
	}
	if err := rejectUnknownFields(fields, map[string]bool{"base": true, "allow": true}, pointer); err != nil {
		return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	base := ExecParameterPathBaseSource
	if fields["base"] != nil {
		value, err := stringValue(fields["base"], pointer+".base")
		if err != nil {
			return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		base = strings.TrimSpace(value)
	}
	switch base {
	case ExecParameterPathBaseSource, ExecParameterPathBaseRepository:
	default:
		return nil, fmt.Errorf("parse plugin policy %s: %s.base has unsupported path base %q", path, pointer, base)
	}
	allow, err := stringSequence(fields["allow"], pointer+".allow")
	if fields["allow"] != nil && err != nil {
		return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	normalized := make([]string, 0, len(allow))
	seen := map[string]struct{}{}
	for index, raw := range allow {
		raw = strings.TrimSpace(raw)
		if strings.Contains(raw, `\`) {
			return nil, fmt.Errorf("parse plugin policy %s: %s.allow[%d] must use slash-normalized paths", path, pointer, index)
		}
		value := filepath.ToSlash(raw)
		if value == "" {
			return nil, fmt.Errorf("parse plugin policy %s: %s.allow[%d] must not be empty", path, pointer, index)
		}
		if err := validateRepositoryRelativePattern(value); err != nil {
			return nil, fmt.Errorf("parse plugin policy %s: %s.allow[%d] must be a relative non-escaping glob: %w", path, pointer, index, err)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return &ExecParameterPath{Base: base, Allow: normalized}, nil
}

func validateRepositoryRelativePattern(pattern string) error {
	if filepath.IsAbs(pattern) {
		return fmt.Errorf("absolute paths are not allowed")
	}
	if pattern == "" {
		return fmt.Errorf("empty paths are not allowed")
	}
	for part := range strings.SplitSeq(pattern, "/") {
		switch part {
		case "..":
			return fmt.Errorf("parent directory segments are not allowed")
		case ".git":
			return fmt.Errorf(".git paths are not allowed")
		}
	}
	return nil
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
var parameterNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var bootstrapEntrypointNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

var reservedExecEnvNames = map[string]struct{}{
	"PATH":                        {},
	"ARGOCD_APP_PARAMETERS":       {},
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

var reservedExecEnvPrefixes = []string{"LD_", "DYLD_", "PARAM_"}

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

func parameterEnvName(name string) string {
	name = strings.ToUpper(name)
	return regexp.MustCompile(`[^A-Z0-9_]`).ReplaceAllString(name, "_")
}
