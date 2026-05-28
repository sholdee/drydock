package pluginpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

const (
	apiVersion = "drydock.sholdee.dev/v1alpha1"
	kind       = "PluginPolicy"

	NoPolicyFingerprint = ""

	ExecWorkdirSource          = "source"
	DefaultInitTimeout         = 10 * time.Second
	DefaultGenerateTimeout     = 60 * time.Second
	DefaultPostRendererTimeout = 30 * time.Second
	DefaultMaxStdoutBytes      = int64(10 * 1024 * 1024)
	DefaultMaxStderrBytes      = int64(64 * 1024)

	maxEnvAllowCount = 64
)

type Engine string

const (
	EngineAVPCompat       Engine = "avp-compat"
	EngineNativeKustomize Engine = "native-kustomize"
	EngineExec            Engine = "exec"
)

type Policy struct {
	Plugins map[string]Plugin
}

type Plugin struct {
	Engine Engine
	Exec   *ExecConfig
}

type ExecConfig struct {
	Workdir       string
	Init          *ExecCommand
	Generate      ExecCommand
	PostRenderers []ExecCommand
	Env           ExecEnv
	Output        ExecOutput
}

type ExecCommand struct {
	Command []string
	Timeout time.Duration
}

type ExecEnv struct {
	Allow []string
}

type ExecOutput struct {
	MaxStdoutBytes int64
	MaxStderrBytes int64
}

func Parse(path string, data []byte) (Policy, error) {
	documents, err := decodeDocuments(data)
	if err != nil {
		return Policy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if len(documents) == 0 {
		return Policy{}, fmt.Errorf("parse plugin policy %s: policy document is empty", path)
	}
	if len(documents) > 1 {
		return Policy{}, fmt.Errorf("parse plugin policy %s: expected exactly one YAML document, got %d", path, len(documents))
	}

	root := documentRoot(&documents[0])
	if root == nil || isNullNode(root) {
		return Policy{}, fmt.Errorf("parse plugin policy %s: policy document is empty", path)
	}
	if err := validateYAMLTree(root, "$"); err != nil {
		return Policy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	return parsePolicy(root, path)
}

func (p Policy) Plugin(name string) (Plugin, bool) {
	plugin, ok := p.Plugins[strings.TrimSpace(name)]
	return plugin, ok
}

func Fingerprint(policy Policy) (string, error) {
	plugins := make(map[string]fingerprintPlugin, len(policy.Plugins))
	for name, plugin := range policy.Plugins {
		normalized := strings.TrimSpace(name)
		if normalized == "" {
			return "", fmt.Errorf("fingerprint plugin policy: plugin name is empty")
		}
		if _, ok := plugins[normalized]; ok {
			return "", fmt.Errorf("fingerprint plugin policy: duplicate plugin name %q after normalization", normalized)
		}
		fingerprint, err := newFingerprintPlugin(plugin)
		if err != nil {
			return "", fmt.Errorf("fingerprint plugin policy: plugin %q: %w", normalized, err)
		}
		switch plugin.Engine {
		case EngineAVPCompat, EngineNativeKustomize, EngineExec:
			plugins[normalized] = fingerprint
		default:
			return "", fmt.Errorf("fingerprint plugin policy: plugin %q has unsupported engine %q", normalized, plugin.Engine)
		}
	}

	input := fingerprintPolicy{
		APIVersion: apiVersion,
		Kind:       kind,
		Plugins:    plugins,
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("fingerprint plugin policy: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type fingerprintPolicy struct {
	APIVersion string                       `json:"apiVersion"`
	Kind       string                       `json:"kind"`
	Plugins    map[string]fingerprintPlugin `json:"plugins"`
}

type fingerprintPlugin struct {
	Engine Engine                 `json:"engine"`
	Exec   *fingerprintExecConfig `json:"exec,omitempty"`
}

type fingerprintExecConfig struct {
	Workdir       string               `json:"workdir"`
	Init          *fingerprintCommand  `json:"init,omitempty"`
	Generate      fingerprintCommand   `json:"generate"`
	PostRenderers []fingerprintCommand `json:"postRenderers,omitempty"`
	Env           ExecEnv              `json:"env"`
	Output        ExecOutput           `json:"output"`
}

type fingerprintCommand struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout"`
}

func newFingerprintPlugin(plugin Plugin) (fingerprintPlugin, error) {
	out := fingerprintPlugin{Engine: plugin.Engine}
	if plugin.Engine != EngineExec {
		return out, nil
	}
	if plugin.Exec == nil {
		return fingerprintPlugin{}, fmt.Errorf("exec config is required")
	}
	out.Exec = &fingerprintExecConfig{
		Workdir: plugin.Exec.Workdir,
		Generate: fingerprintCommand{
			Command: append([]string(nil), plugin.Exec.Generate.Command...),
			Timeout: plugin.Exec.Generate.Timeout.String(),
		},
		Env: ExecEnv{
			Allow: append([]string(nil), plugin.Exec.Env.Allow...),
		},
		Output: plugin.Exec.Output,
	}
	if plugin.Exec.Init != nil {
		out.Exec.Init = &fingerprintCommand{
			Command: append([]string(nil), plugin.Exec.Init.Command...),
			Timeout: plugin.Exec.Init.Timeout.String(),
		}
	}
	if len(plugin.Exec.PostRenderers) > 0 {
		out.Exec.PostRenderers = make([]fingerprintCommand, 0, len(plugin.Exec.PostRenderers))
		for _, command := range plugin.Exec.PostRenderers {
			out.Exec.PostRenderers = append(out.Exec.PostRenderers, fingerprintCommand{
				Command: append([]string(nil), command.Command...),
				Timeout: command.Timeout.String(),
			})
		}
	}
	return out, nil
}

func decodeDocuments(data []byte) ([]yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []yaml.Node
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return documents, nil
		}
		if err != nil {
			return nil, err
		}
		documents = append(documents, doc)
	}
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind != yaml.DocumentNode {
		return doc
	}
	if len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

func parsePolicy(root *yaml.Node, path string) (Policy, error) {
	if root.Kind != yaml.MappingNode {
		return Policy{}, fmt.Errorf("parse plugin policy %s: root must be a mapping", path)
	}

	fields := map[string]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		value := root.Content[i+1]
		name, err := stringKey(key, "$")
		if err != nil {
			return Policy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		switch name {
		case "apiVersion", "kind", "plugins":
			fields[name] = value
		default:
			return Policy{}, fmt.Errorf("parse plugin policy %s: unknown top-level field %q", path, name)
		}
	}

	if got, err := requiredString(fields, "apiVersion", "$.apiVersion"); err != nil {
		return Policy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	} else if got != apiVersion {
		return Policy{}, fmt.Errorf("parse plugin policy %s: apiVersion must be %q", path, apiVersion)
	}
	if got, err := requiredString(fields, "kind", "$.kind"); err != nil {
		return Policy{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	} else if got != kind {
		return Policy{}, fmt.Errorf("parse plugin policy %s: kind must be %q", path, kind)
	}

	policy := Policy{Plugins: map[string]Plugin{}}
	pluginsNode := fields["plugins"]
	if pluginsNode == nil || isNullNode(pluginsNode) {
		return policy, nil
	}
	if pluginsNode.Kind != yaml.MappingNode {
		return Policy{}, fmt.Errorf("parse plugin policy %s: $.plugins must be a mapping", path)
	}
	plugins, err := parsePlugins(pluginsNode, path)
	if err != nil {
		return Policy{}, err
	}
	policy.Plugins = plugins
	return policy, nil
}

func parsePlugins(node *yaml.Node, path string) (map[string]Plugin, error) {
	plugins := make(map[string]Plugin, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		rawName, err := stringKey(key, "$.plugins")
		if err != nil {
			return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, fmt.Errorf("parse plugin policy %s: plugin name must not be empty", path)
		}
		if _, ok := plugins[name]; ok {
			return nil, fmt.Errorf("parse plugin policy %s: duplicate plugin name %q after normalization", path, name)
		}
		plugin, err := parsePlugin(value, path, "$.plugins."+name)
		if err != nil {
			return nil, err
		}
		plugins[name] = plugin
	}
	return plugins, nil
}

func parsePlugin(node *yaml.Node, path, pointer string) (Plugin, error) {
	if node.Kind != yaml.MappingNode {
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		name, err := stringKey(key, pointer)
		if err != nil {
			return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = value
	}

	engineValue, err := requiredString(fields, "engine", pointer+".engine")
	if err != nil {
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	engine := Engine(engineValue)
	switch engine {
	case EngineAVPCompat, EngineNativeKustomize:
		if err := rejectUnknownFields(fields, map[string]bool{"engine": true}, pointer); err != nil {
			return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		return Plugin{Engine: engine}, nil
	case EngineExec:
		execConfig, err := parseExecConfig(fields, path, pointer)
		if err != nil {
			return Plugin{}, err
		}
		return Plugin{Engine: engine, Exec: &execConfig}, nil
	default:
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %s.engine has unsupported engine %q", path, pointer, engineValue)
	}
}

func rejectUnknownFields(fields map[string]*yaml.Node, allowed map[string]bool, pointer string) error {
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("unknown field %s.%s", pointer, name)
		}
	}
	return nil
}

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

func requiredString(fields map[string]*yaml.Node, name, pointer string) (string, error) {
	node := fields[name]
	if node == nil {
		return "", fmt.Errorf("missing required field %s", pointer)
	}
	return stringValue(node, pointer)
}

func stringValue(node *yaml.Node, pointer string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s must be a string", pointer)
	}
	return node.Value, nil
}

func durationValue(node *yaml.Node, pointer string) (time.Duration, error) {
	value, err := stringValue(node, pointer)
	if err != nil {
		return 0, err
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", pointer, err)
	}
	return duration, nil
}

func int64Value(node *yaml.Node, pointer string) (int64, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, fmt.Errorf("%s must be an integer", pointer)
	}
	value, err := strconv.ParseInt(node.Value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", pointer, err)
	}
	return value, nil
}

func stringSequence(node *yaml.Node, pointer string) ([]string, error) {
	if node == nil || isNullNode(node) {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s must be a sequence", pointer)
	}
	out := make([]string, 0, len(node.Content))
	for index, child := range node.Content {
		value, err := stringValue(child, fmt.Sprintf("%s[%d]", pointer, index))
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
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

func stringKey(node *yaml.Node, pointer string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s mapping keys must be strings", pointer)
	}
	return node.Value, nil
}

func validateYAMLTree(node *yaml.Node, pointer string) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("%s aliases are not allowed", pointer)
	}
	if err := validateTag(node, pointer); err != nil {
		return err
	}
	switch node.Kind {
	case yaml.AliasNode:
		return fmt.Errorf("%s aliases are not allowed", pointer)
	case yaml.DocumentNode:
		return validateYAMLChildren(node.Content, pointer)
	case yaml.MappingNode:
		return validateYAMLMapping(node, pointer)
	case yaml.SequenceNode:
		return validateYAMLSequence(node, pointer)
	case yaml.ScalarNode:
		return nil
	case yaml.StreamNode:
		return fmt.Errorf("%s stream nodes are not allowed", pointer)
	default:
		return fmt.Errorf("%s has unsupported YAML node kind %d", pointer, node.Kind)
	}
}

func validateYAMLChildren(nodes []*yaml.Node, pointer string) error {
	for _, child := range nodes {
		if err := validateYAMLTree(child, pointer); err != nil {
			return err
		}
	}
	return nil
}

func validateYAMLMapping(node *yaml.Node, pointer string) error {
	seen := map[string]struct{}{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if key.Kind == yaml.ScalarNode && key.Value == "<<" {
			return fmt.Errorf("%s merge keys are not allowed", pointer)
		}
		identity := mappingKeyIdentity(key)
		if _, ok := seen[identity]; ok {
			return fmt.Errorf("%s duplicate mapping key %q", pointer, key.Value)
		}
		seen[identity] = struct{}{}
		if err := validateYAMLTree(key, pointer+" key"); err != nil {
			return err
		}
		if err := validateYAMLTree(value, childPointer(pointer, key)); err != nil {
			return err
		}
	}
	return nil
}

func validateYAMLSequence(node *yaml.Node, pointer string) error {
	for i, child := range node.Content {
		if err := validateYAMLTree(child, fmt.Sprintf("%s[%d]", pointer, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateTag(node *yaml.Node, pointer string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		return nil
	case yaml.MappingNode:
		if node.Tag == "" || node.Tag == "!!map" {
			return nil
		}
	case yaml.SequenceNode:
		if node.Tag == "" || node.Tag == "!!seq" {
			return nil
		}
	case yaml.ScalarNode:
		if isAllowedScalarTag(node.Tag) {
			return nil
		}
	case yaml.AliasNode:
		return nil
	case yaml.StreamNode:
		return nil
	}
	return fmt.Errorf("%s custom YAML tag %q is not allowed", pointer, node.Tag)
}

func isAllowedScalarTag(tag string) bool {
	switch tag {
	case "", "!!str", "!!bool", "!!int", "!!float", "!!null", "!!timestamp", "!!binary":
		return true
	default:
		return false
	}
}

func isNullNode(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

func mappingKeyIdentity(node *yaml.Node) string {
	return fmt.Sprintf("%d\x00%s\x00%s", node.Kind, node.Tag, node.Value)
}

func childPointer(parent string, key *yaml.Node) string {
	if key.Kind != yaml.ScalarNode {
		return parent + ".<key>"
	}
	return parent + "." + key.Value
}
