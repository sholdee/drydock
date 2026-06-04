package pluginpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v4"
)

const (
	apiVersion = "drydock.sholdee.dev/v1alpha1"
	kind       = "PluginPolicy"

	NoPolicyFingerprint = ""

	ExecWorkdirSource          = "source"
	ExecCopyScopeSource        = "source"
	ExecCopyScopeRepository    = "repository"
	DefaultInitTimeout         = 10 * time.Second
	DefaultGenerateTimeout     = 60 * time.Second
	DefaultPostRendererTimeout = 30 * time.Second
	DefaultMaxStdoutBytes      = int64(10 * 1024 * 1024)
	DefaultMaxStderrBytes      = int64(64 * 1024)
	DefaultContainerRuntime    = ContainerRuntimeDocker
	DefaultContainerNetwork    = ContainerNetworkNone

	maxEnvAllowCount = 64

	maxExecParameterAllowCount = 64
	maxExecCopyIncludeCount    = 64
)

type Engine string

const (
	EngineAVPCompat       Engine = "avp-compat"
	EngineNativeKustomize Engine = "native-kustomize"
	EngineExec            Engine = "exec"
	EngineContainer       Engine = "container"
)

type Policy struct {
	Bootstrap Bootstrap
	Plugins   map[string]Plugin
}

type Bootstrap struct {
	Entrypoints []BootstrapEntrypoint
}

type BootstrapEntrypoint struct {
	Name       string
	Plugin     string
	SourcePath string
	Parameters []BootstrapParameter
}

type BootstrapParameter struct {
	Name   string
	String *string
	Array  *BootstrapParameterArray
	Map    *BootstrapParameterMap
}

type BootstrapParameterArray struct {
	Values []string
}

type BootstrapParameterMap struct {
	Values map[string]string
}

type Plugin struct {
	Engine                 Engine
	Match                  *PluginMatch
	ConfigManagementPlugin *ConfigManagementPluginSeed
	Exec                   *ExecConfig
	Container              *ContainerConfig
}

type PluginMatch struct {
	Discover PluginDiscoverMatch
}

type PluginDiscoverMatch struct {
	FileName string
	FindGlob string
}

type ConfigManagementPluginSeed struct {
	Discover *PluginDiscoverMatch
	Generate *ConfigManagementPluginGenerate
}

type ConfigManagementPluginGenerate struct {
	Command []string
	Args    []string
}

type ExecConfig struct {
	Workdir       string
	Copy          ExecCopy
	Init          *ExecCommand
	Generate      ExecCommand
	PostRenderers []ExecCommand
	Env           ExecEnv
	Parameters    ExecParameters
	Output        ExecOutput
}

type ExecCopy struct {
	Scope   string
	Include []string
}

type ExecCommand struct {
	Command []string
	Timeout time.Duration
}

type ExecEnv struct {
	Allow []string
}

type ExecParameterType string

const (
	ExecParameterTypeString ExecParameterType = "string"
	ExecParameterTypeArray  ExecParameterType = "array"
	ExecParameterTypeMap    ExecParameterType = "map"
)

type ExecParameters struct {
	Allow []ExecParameter
}

type ExecParameter struct {
	Name     string
	Type     ExecParameterType
	Required bool
	Path     *ExecParameterPath
}

type ExecParameterPath struct {
	Base  string
	Allow []string
}

const (
	ExecParameterPathBaseSource     = "source"
	ExecParameterPathBaseRepository = "repository"
)

type ExecOutput struct {
	MaxStdoutBytes int64
	MaxStderrBytes int64
}

type ContainerRuntime string

const (
	ContainerRuntimeDocker ContainerRuntime = "docker"
)

type ContainerNetwork string

const (
	ContainerNetworkNone    ContainerNetwork = "none"
	ContainerNetworkDefault ContainerNetwork = "default"
)

type ContainerConfig struct {
	Runtime              ContainerRuntime
	Image                string
	AllowMutableImageTag bool
	Network              ContainerNetwork
	Lifecycle            ExecConfig
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
		case EngineAVPCompat, EngineNativeKustomize, EngineExec, EngineContainer:
			plugins[normalized] = fingerprint
		default:
			return "", fmt.Errorf("fingerprint plugin policy: plugin %q has unsupported engine %q", normalized, plugin.Engine)
		}
	}
	bootstrap, hasBootstrap, err := newFingerprintBootstrap(policy.Bootstrap, policy.Plugins)
	if err != nil {
		return "", fmt.Errorf("fingerprint plugin policy: %w", err)
	}
	var bootstrapPtr *fingerprintBootstrap
	if hasBootstrap {
		bootstrapPtr = &bootstrap
	}

	input := fingerprintPolicy{
		APIVersion: apiVersion,
		Kind:       kind,
		Bootstrap:  bootstrapPtr,
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
		case "apiVersion", "kind", "bootstrap", "plugins":
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
	if pluginsNode != nil && !isNullNode(pluginsNode) {
		if pluginsNode.Kind != yaml.MappingNode {
			return Policy{}, fmt.Errorf("parse plugin policy %s: $.plugins must be a mapping", path)
		}
		plugins, err := parsePlugins(pluginsNode, path)
		if err != nil {
			return Policy{}, err
		}
		policy.Plugins = plugins
	}
	bootstrap, err := parseBootstrap(fields["bootstrap"], policy.Plugins, path)
	if err != nil {
		return Policy{}, err
	}
	policy.Bootstrap = bootstrap
	return policy, nil
}

func parseBootstrap(node *yaml.Node, plugins map[string]Plugin, policyPath string) (Bootstrap, error) {
	if node == nil {
		return Bootstrap{}, nil
	}
	if isNullNode(node) {
		return Bootstrap{}, fmt.Errorf("parse plugin policy %s: $.bootstrap must be a mapping", policyPath)
	}
	if node.Kind != yaml.MappingNode {
		return Bootstrap{}, fmt.Errorf("parse plugin policy %s: $.bootstrap must be a mapping", policyPath)
	}
	fields, err := mappingFields(node, "$.bootstrap")
	if err != nil {
		return Bootstrap{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"entrypoints": true}, "$.bootstrap"); err != nil {
		return Bootstrap{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	entrypointsNode := fields["entrypoints"]
	if entrypointsNode == nil || isNullNode(entrypointsNode) {
		return Bootstrap{}, fmt.Errorf("parse plugin policy %s: missing required field $.bootstrap.entrypoints", policyPath)
	}
	if entrypointsNode.Kind != yaml.SequenceNode {
		return Bootstrap{}, fmt.Errorf("parse plugin policy %s: $.bootstrap.entrypoints must be a sequence", policyPath)
	}
	if len(entrypointsNode.Content) == 0 {
		return Bootstrap{}, fmt.Errorf("parse plugin policy %s: $.bootstrap.entrypoints must not be empty", policyPath)
	}
	seen := map[string]struct{}{}
	entrypoints := make([]BootstrapEntrypoint, 0, len(entrypointsNode.Content))
	for index, child := range entrypointsNode.Content {
		pointer := fmt.Sprintf("$.bootstrap.entrypoints[%d]", index)
		entrypoint, err := parseBootstrapEntrypoint(child, plugins, policyPath, pointer)
		if err != nil {
			return Bootstrap{}, err
		}
		if _, ok := seen[entrypoint.Name]; ok {
			return Bootstrap{}, fmt.Errorf("parse plugin policy %s: $.bootstrap.entrypoints contains duplicate name %q", policyPath, entrypoint.Name)
		}
		seen[entrypoint.Name] = struct{}{}
		entrypoints = append(entrypoints, entrypoint)
	}
	sort.Slice(entrypoints, func(i, j int) bool {
		return entrypoints[i].Name < entrypoints[j].Name
	})
	return Bootstrap{Entrypoints: entrypoints}, nil
}

func parseBootstrapEntrypoint(node *yaml.Node, plugins map[string]Plugin, policyPath, pointer string) (BootstrapEntrypoint, error) {
	if node.Kind != yaml.MappingNode {
		return BootstrapEntrypoint{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", policyPath, pointer)
	}
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return BootstrapEntrypoint{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"name": true, "plugin": true, "sourcePath": true, "parameters": true}, pointer); err != nil {
		return BootstrapEntrypoint{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	name, err := parseBootstrapEntrypointName(fields, policyPath, pointer)
	if err != nil {
		return BootstrapEntrypoint{}, err
	}
	pluginName, plugin, err := parseBootstrapEntrypointPlugin(fields, plugins, policyPath, pointer)
	if err != nil {
		return BootstrapEntrypoint{}, err
	}
	if _, _, ok := bootstrapPluginDiscoverRule(plugin); !ok {
		return BootstrapEntrypoint{}, fmt.Errorf("parse plugin policy %s: %s.plugin %q must define match.discover or configManagementPlugin.discover", policyPath, pointer, pluginName)
	}
	sourcePath, err := parseBootstrapEntrypointSourcePath(fields, policyPath, pointer)
	if err != nil {
		return BootstrapEntrypoint{}, err
	}
	parameters, err := parseBootstrapParameters(fields["parameters"], policyPath, pointer+".parameters")
	if err != nil {
		return BootstrapEntrypoint{}, err
	}
	return BootstrapEntrypoint{
		Name:       name,
		Plugin:     pluginName,
		SourcePath: sourcePath,
		Parameters: parameters,
	}, nil
}

func parseBootstrapEntrypointName(fields map[string]*yaml.Node, policyPath, pointer string) (string, error) {
	value, err := requiredString(fields, "name", pointer+".name")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	name := strings.TrimSpace(value)
	if name == "" {
		return "", fmt.Errorf("parse plugin policy %s: %s.name must not be empty", policyPath, pointer)
	}
	if len(name) > 63 || !bootstrapEntrypointNamePattern.MatchString(name) {
		return "", fmt.Errorf("parse plugin policy %s: %s.name %q is invalid", policyPath, pointer, name)
	}
	return name, nil
}

func parseBootstrapEntrypointPlugin(fields map[string]*yaml.Node, plugins map[string]Plugin, policyPath, pointer string) (string, Plugin, error) {
	value, err := requiredString(fields, "plugin", pointer+".plugin")
	if err != nil {
		return "", Plugin{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	name := strings.TrimSpace(value)
	if name == "" {
		return "", Plugin{}, fmt.Errorf("parse plugin policy %s: %s.plugin must not be empty", policyPath, pointer)
	}
	plugin, ok := plugins[name]
	if !ok {
		return "", Plugin{}, fmt.Errorf("parse plugin policy %s: %s.plugin references unknown plugin %q", policyPath, pointer, name)
	}
	return name, plugin, nil
}

func parseBootstrapEntrypointSourcePath(fields map[string]*yaml.Node, policyPath, pointer string) (string, error) {
	value, err := requiredString(fields, "sourcePath", pointer+".sourcePath")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	sourcePath, err := cleanBootstrapSourcePath(value)
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %s.sourcePath %q is invalid: %w", policyPath, pointer, value, err)
	}
	return sourcePath, nil
}

func cleanBootstrapSourcePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("empty paths are not allowed")
	}
	if strings.Contains(value, `\`) {
		return "", fmt.Errorf("backslashes are not allowed")
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	for part := range strings.SplitSeq(value, "/") {
		switch part {
		case "":
			return "", fmt.Errorf("empty path components are not allowed")
		case ".":
			if value != "." {
				return "", fmt.Errorf("dot path components are not allowed")
			}
		case "..":
			return "", fmt.Errorf("parent directory segments are not allowed")
		case ".git":
			return "", fmt.Errorf(".git paths are not allowed")
		}
	}
	clean := path.Clean(strings.Trim(value, "/"))
	if clean == "." {
		return ".", nil
	}
	return clean, nil
}

func bootstrapPluginDiscoverRule(plugin Plugin) (PluginDiscoverMatch, string, bool) {
	if plugin.Match != nil {
		return plugin.Match.Discover, "match.discover", true
	}
	if plugin.ConfigManagementPlugin != nil && plugin.ConfigManagementPlugin.Discover != nil {
		return *plugin.ConfigManagementPlugin.Discover, "configManagementPlugin.discover", true
	}
	return PluginDiscoverMatch{}, "", false
}

func parseBootstrapParameters(node *yaml.Node, policyPath, pointer string) ([]BootstrapParameter, error) {
	if node == nil || isNullNode(node) {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("parse plugin policy %s: %s must be a sequence", policyPath, pointer)
	}
	seen := map[string]struct{}{}
	parameters := make([]BootstrapParameter, 0, len(node.Content))
	for index, child := range node.Content {
		parameter, err := parseBootstrapParameter(child, policyPath, fmt.Sprintf("%s[%d]", pointer, index))
		if err != nil {
			return nil, err
		}
		if _, ok := seen[parameter.Name]; ok {
			return nil, fmt.Errorf("parse plugin policy %s: %s contains duplicate parameter name %q", policyPath, pointer, parameter.Name)
		}
		seen[parameter.Name] = struct{}{}
		parameters = append(parameters, parameter)
	}
	sort.Slice(parameters, func(i, j int) bool {
		return parameters[i].Name < parameters[j].Name
	})
	return parameters, nil
}

func parseBootstrapParameter(node *yaml.Node, policyPath, pointer string) (BootstrapParameter, error) {
	if node.Kind != yaml.MappingNode {
		return BootstrapParameter{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", policyPath, pointer)
	}
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return BootstrapParameter{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"name": true, "string": true, "array": true, "map": true}, pointer); err != nil {
		return BootstrapParameter{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	name, err := parseBootstrapParameterName(fields, policyPath, pointer)
	if err != nil {
		return BootstrapParameter{}, err
	}
	count := 0
	var out BootstrapParameter
	out.Name = name
	if fields["string"] != nil {
		value, err := stringValue(fields["string"], pointer+".string")
		if err != nil {
			return BootstrapParameter{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
		}
		out.String = &value
		count++
	}
	if fields["array"] != nil {
		values, err := stringSequence(fields["array"], pointer+".array")
		if err != nil {
			return BootstrapParameter{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
		}
		out.Array = &BootstrapParameterArray{Values: values}
		count++
	}
	if fields["map"] != nil {
		values, err := stringMapValue(fields["map"], pointer+".map")
		if err != nil {
			return BootstrapParameter{}, fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
		}
		out.Map = &BootstrapParameterMap{Values: values}
		count++
	}
	if count != 1 {
		return BootstrapParameter{}, fmt.Errorf("parse plugin policy %s: %s must contain exactly one of string, array, or map", policyPath, pointer)
	}
	return out, nil
}

func parseBootstrapParameterName(fields map[string]*yaml.Node, policyPath, pointer string) (string, error) {
	value, err := requiredString(fields, "name", pointer+".name")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", policyPath, err)
	}
	name := strings.TrimSpace(value)
	if name == "" {
		return "", fmt.Errorf("parse plugin policy %s: %s.name must not be empty", policyPath, pointer)
	}
	if !parameterNamePattern.MatchString(name) {
		return "", fmt.Errorf("parse plugin policy %s: %s.name %q is invalid", policyPath, pointer, name)
	}
	return name, nil
}

func stringMapValue(node *yaml.Node, pointer string) (map[string]string, error) {
	if node == nil || isNullNode(node) {
		return map[string]string{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", pointer)
	}
	values := map[string]string{}
	for i := 0; i < len(node.Content); i += 2 {
		key, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return nil, err
		}
		value, err := stringValue(node.Content[i+1], pointer+"."+key)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
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
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}

	engineValue, err := requiredString(fields, "engine", pointer+".engine")
	if err != nil {
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	engine := Engine(engineValue)
	switch engine {
	case EngineAVPCompat, EngineNativeKustomize:
		return parseStaticPlugin(engine, fields, path, pointer)
	case EngineExec:
		return parseExecPlugin(fields, path, pointer)
	case EngineContainer:
		return parseContainerPlugin(fields, path, pointer)
	default:
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %s.engine has unsupported engine %q", path, pointer, engineValue)
	}
}

type parsedPluginOptionalFields struct {
	match    PluginMatch
	hasMatch bool
	seed     ConfigManagementPluginSeed
	hasSeed  bool
}

func (fields *parsedPluginOptionalFields) matchPtr() *PluginMatch {
	if !fields.hasMatch {
		return nil
	}
	return &fields.match
}

func (fields *parsedPluginOptionalFields) seedPtr() *ConfigManagementPluginSeed {
	if !fields.hasSeed {
		return nil
	}
	return &fields.seed
}

func parseStaticPlugin(engine Engine, fields map[string]*yaml.Node, path, pointer string) (Plugin, error) {
	if err := rejectUnknownFields(fields, map[string]bool{"engine": true, "match": true, "configManagementPlugin": true}, pointer); err != nil {
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	optional, err := parsePluginOptionalFields(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	return Plugin{Engine: engine, Match: optional.matchPtr(), ConfigManagementPlugin: optional.seedPtr()}, nil
}

func parseExecPlugin(fields map[string]*yaml.Node, path, pointer string) (Plugin, error) {
	if err := rejectUnknownFields(fields, execPluginAllowedFields(), pointer); err != nil {
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	optional, err := parsePluginOptionalFields(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	execConfig, err := parseExecConfig(fields, path, pointer)
	if err != nil {
		return Plugin{}, err
	}
	return Plugin{
		Engine:                 EngineExec,
		Match:                  optional.matchPtr(),
		ConfigManagementPlugin: optional.seedPtr(),
		Exec:                   &execConfig,
	}, nil
}

func parsePluginOptionalFields(fields map[string]*yaml.Node, path, pointer string) (parsedPluginOptionalFields, error) {
	var out parsedPluginOptionalFields
	match, hasMatch, err := parsePluginMatch(fields["match"], path, pointer+".match")
	if err != nil {
		return parsedPluginOptionalFields{}, err
	}
	if hasMatch {
		out.match = match
		out.hasMatch = true
	}
	seed, hasSeed, err := parseConfigManagementPluginSeed(fields["configManagementPlugin"], path, pointer+".configManagementPlugin")
	if err != nil {
		return parsedPluginOptionalFields{}, err
	}
	if hasSeed {
		out.seed = seed
		out.hasSeed = true
	}
	if err := validateConfigManagementPluginSeedDiscover(out.matchPtr(), out.seedPtr(), path, pointer); err != nil {
		return parsedPluginOptionalFields{}, err
	}
	return out, nil
}

func execPluginAllowedFields() map[string]bool {
	fields := execLifecycleAllowedFields()
	fields["engine"] = true
	fields["match"] = true
	fields["configManagementPlugin"] = true
	return fields
}

func execLifecycleAllowedFields() map[string]bool {
	return map[string]bool{
		"workdir":       true,
		"copy":          true,
		"init":          true,
		"generate":      true,
		"postRenderers": true,
		"env":           true,
		"parameters":    true,
		"output":        true,
	}
}

func parseConfigManagementPluginSeed(node *yaml.Node, path, pointer string) (ConfigManagementPluginSeed, bool, error) {
	if node == nil {
		return ConfigManagementPluginSeed{}, false, nil
	}
	if isNullNode(node) {
		return ConfigManagementPluginSeed{}, false, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	if node.Kind != yaml.MappingNode {
		return ConfigManagementPluginSeed{}, false, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return ConfigManagementPluginSeed{}, false, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"discover": true, "generate": true}, pointer); err != nil {
		return ConfigManagementPluginSeed{}, false, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if fields["discover"] == nil && fields["generate"] == nil {
		return ConfigManagementPluginSeed{}, false, fmt.Errorf("parse plugin policy %s: %s must contain discover or generate", path, pointer)
	}
	var out ConfigManagementPluginSeed
	if fields["discover"] != nil {
		if isNullNode(fields["discover"]) {
			return ConfigManagementPluginSeed{}, false, fmt.Errorf("parse plugin policy %s: %s.discover must not be null", path, pointer)
		}
		discover, err := parsePluginDiscoverMatch(fields["discover"], path, pointer+".discover")
		if err != nil {
			return ConfigManagementPluginSeed{}, false, err
		}
		out.Discover = &discover
	}
	if fields["generate"] != nil {
		generate, err := parseConfigManagementPluginGenerate(fields["generate"], path, pointer+".generate")
		if err != nil {
			return ConfigManagementPluginSeed{}, false, err
		}
		out.Generate = generate
	}
	return out, true, nil
}

func parseConfigManagementPluginGenerate(node *yaml.Node, path, pointer string) (*ConfigManagementPluginGenerate, error) {
	if isNullNode(node) {
		return nil, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
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
	if err := rejectUnknownFields(fields, map[string]bool{"command": true, "args": true}, pointer); err != nil {
		return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	command, err := seedArgvSequence(fields["command"], pointer+".command", true)
	if err != nil {
		return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	args, err := seedArgvSequence(fields["args"], pointer+".args", false)
	if err != nil {
		return nil, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if fields["command"] == nil && fields["args"] == nil {
		return nil, fmt.Errorf("parse plugin policy %s: %s must contain command or args", path, pointer)
	}
	return &ConfigManagementPluginGenerate{Command: command, Args: args}, nil
}

func seedArgvSequence(node *yaml.Node, pointer string, requireNonEmpty bool) ([]string, error) {
	if node == nil || isNullNode(node) {
		return nil, nil
	}
	values, err := stringSequence(node, pointer)
	if err != nil {
		return nil, err
	}
	if requireNonEmpty && len(values) == 0 {
		return nil, fmt.Errorf("%s must not be empty", pointer)
	}
	for index, token := range values {
		if strings.TrimSpace(token) == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", pointer, index)
		}
	}
	return values, nil
}

func validateConfigManagementPluginSeedDiscover(match *PluginMatch, seed *ConfigManagementPluginSeed, path, pointer string) error {
	if match == nil || seed == nil || seed.Discover == nil {
		return nil
	}
	if match.Discover.FileName != "" {
		if seed.Discover.FileName == match.Discover.FileName {
			return nil
		}
		return fmt.Errorf("parse plugin policy %s: %s.configManagementPlugin.discover must match %s.match.discover", path, pointer, pointer)
	}
	if match.Discover.FindGlob != "" {
		if seed.Discover.FindGlob == match.Discover.FindGlob {
			return nil
		}
		return fmt.Errorf("parse plugin policy %s: %s.configManagementPlugin.discover must match %s.match.discover", path, pointer, pointer)
	}
	return nil
}

func parsePluginMatch(node *yaml.Node, path, pointer string) (PluginMatch, bool, error) {
	if node == nil {
		return PluginMatch{}, false, nil
	}
	if isNullNode(node) {
		return PluginMatch{}, false, fmt.Errorf("parse plugin policy %s: %s must contain exactly one static discover rule", path, pointer)
	}
	if node.Kind != yaml.MappingNode {
		return PluginMatch{}, false, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields, err := mappingFields(node, pointer)
	if err != nil {
		return PluginMatch{}, false, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if err := rejectUnknownFields(fields, map[string]bool{"discover": true}, pointer); err != nil {
		return PluginMatch{}, false, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if fields["discover"] == nil || isNullNode(fields["discover"]) {
		return PluginMatch{}, false, fmt.Errorf("parse plugin policy %s: %s.discover must contain exactly one static rule", path, pointer)
	}
	discover, err := parsePluginDiscoverMatch(fields["discover"], path, pointer+".discover")
	if err != nil {
		return PluginMatch{}, false, err
	}
	return PluginMatch{Discover: discover}, true, nil
}

func parsePluginDiscoverMatch(node *yaml.Node, path, pointer string) (PluginDiscoverMatch, error) {
	if node.Kind != yaml.MappingNode {
		return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = node.Content[i+1]
	}
	if err := rejectUnknownFields(fields, map[string]bool{"fileName": true, "find": true}, pointer); err != nil {
		return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
	}

	var out PluginDiscoverMatch
	ruleCount := 0
	if fields["fileName"] != nil {
		if isNullNode(fields["fileName"]) {
			return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %s.fileName must not be null", path, pointer)
		}
		fileName, err := stringValue(fields["fileName"], pointer+".fileName")
		if err != nil {
			return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fileName, err = normalizeMatchPattern(fileName, path, pointer+".fileName")
		if err != nil {
			return PluginDiscoverMatch{}, err
		}
		if _, err := filepath.Match(fileName, ""); err != nil {
			return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %s must be a valid filepath glob: %w", path, pointer+".fileName", err)
		}
		out.FileName = fileName
		ruleCount++
	}
	if fields["find"] != nil {
		if isNullNode(fields["find"]) {
			return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %s.find must not be null", path, pointer)
		}
		findGlob, err := parsePluginDiscoverFindGlob(fields["find"], path, pointer+".find")
		if err != nil {
			return PluginDiscoverMatch{}, err
		}
		out.FindGlob = findGlob
		ruleCount++
	}
	if ruleCount != 1 {
		return PluginDiscoverMatch{}, fmt.Errorf("parse plugin policy %s: %s must contain exactly one static rule", path, pointer)
	}
	return out, nil
}

func parsePluginDiscoverFindGlob(node *yaml.Node, path, pointer string) (string, error) {
	if node.Kind != yaml.MappingNode {
		return "", fmt.Errorf("parse plugin policy %s: %s must be a mapping", path, pointer)
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
		}
		fields[name] = node.Content[i+1]
	}
	if err := rejectUnknownFields(fields, map[string]bool{"glob": true}, pointer); err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	if fields["glob"] == nil || isNullNode(fields["glob"]) {
		return "", fmt.Errorf("parse plugin policy %s: missing required field %s.glob", path, pointer)
	}
	glob, err := stringValue(fields["glob"], pointer+".glob")
	if err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %w", path, err)
	}
	glob, err = normalizeMatchPattern(glob, path, pointer+".glob")
	if err != nil {
		return "", err
	}
	if !doublestar.ValidatePattern(glob) {
		return "", fmt.Errorf("parse plugin policy %s: %s must be a valid doublestar glob", path, pointer+".glob")
	}
	return glob, nil
}

func normalizeMatchPattern(raw, path, pointer string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, `\`) {
		return "", fmt.Errorf("parse plugin policy %s: %s is invalid: backslashes are not allowed", path, pointer)
	}
	pattern := filepath.ToSlash(raw)
	if pattern == "" {
		return "", fmt.Errorf("parse plugin policy %s: %s must not be empty", path, pointer)
	}
	if err := validateRepositoryRelativePattern(pattern); err != nil {
		return "", fmt.Errorf("parse plugin policy %s: %s is invalid: %w", path, pointer, err)
	}
	return pattern, nil
}

func rejectUnknownFields(fields map[string]*yaml.Node, allowed map[string]bool, pointer string) error {
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("unknown field %s.%s", pointer, name)
		}
	}
	return nil
}

func mappingFields(node *yaml.Node, pointer string) (map[string]*yaml.Node, error) {
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name, err := stringKey(node.Content[i], pointer)
		if err != nil {
			return nil, err
		}
		fields[name] = node.Content[i+1]
	}
	return fields, nil
}
