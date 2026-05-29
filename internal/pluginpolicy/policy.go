package pluginpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
