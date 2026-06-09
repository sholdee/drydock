package pluginpolicy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
)

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

func boolValue(node *yaml.Node, pointer string) (bool, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, fmt.Errorf("%s must be a boolean", pointer)
	}
	value, err := strconv.ParseBool(node.Value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", pointer, err)
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
