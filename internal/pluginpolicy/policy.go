package pluginpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v4"
)

const (
	apiVersion = "drydock.sholdee.dev/v1alpha1"
	kind       = "PluginPolicy"

	NoPolicyFingerprint = ""
)

type Engine string

const (
	EngineAVPCompat       Engine = "avp-compat"
	EngineNativeKustomize Engine = "native-kustomize"
)

type Policy struct {
	Plugins map[string]Plugin
}

type Plugin struct {
	Engine Engine
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
		switch plugin.Engine {
		case EngineAVPCompat, EngineNativeKustomize:
			plugins[normalized] = fingerprintPlugin(plugin)
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
	Engine Engine `json:"engine"`
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
		if name != "engine" {
			return Plugin{}, fmt.Errorf("parse plugin policy %s: unknown field %s.%s", path, pointer, name)
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
		return Plugin{Engine: engine}, nil
	default:
		return Plugin{}, fmt.Errorf("parse plugin policy %s: %s.engine has unsupported engine %q", path, pointer, engineValue)
	}
}

func requiredString(fields map[string]*yaml.Node, name, pointer string) (string, error) {
	node := fields[name]
	if node == nil {
		return "", fmt.Errorf("missing required field %s", pointer)
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s must be a string", pointer)
	}
	return node.Value, nil
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
	case yaml.DocumentNode:
		return validateYAMLChildren(node.Content, pointer)
	case yaml.MappingNode:
		return validateYAMLMapping(node, pointer)
	case yaml.SequenceNode:
		return validateYAMLSequence(node, pointer)
	case yaml.ScalarNode:
		return nil
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
