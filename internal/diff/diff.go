package diff

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"go.yaml.in/yaml/v4"
)

type Parent struct {
	Namespace   string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Name        string `json:"name" yaml:"name"`
	SourceIndex int    `json:"sourceIndex" yaml:"sourceIndex"`
	SourceName  string `json:"sourceName,omitempty" yaml:"sourceName,omitempty"`
	SourcePath  string `json:"sourcePath,omitempty" yaml:"sourcePath,omitempty"`
}

type Resource struct {
	Group     string `json:"group,omitempty" yaml:"group,omitempty"`
	Kind      string `json:"kind" yaml:"kind"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Name      string `json:"name" yaml:"name"`
}

type Document struct {
	Parent   Parent   `json:"parent" yaml:"parent"`
	Resource Resource `json:"resource" yaml:"resource"`
	Body     string   `json:"body" yaml:"body"`
}

type Change string

const (
	ChangeAdded    Change = "added"
	ChangeRemoved  Change = "removed"
	ChangeModified Change = "modified"
)

type Result struct {
	Parent   Parent   `json:"parent" yaml:"parent"`
	Resource Resource `json:"resource" yaml:"resource"`
	Change   Change   `json:"change" yaml:"change"`
	Diff     string   `json:"diff" yaml:"diff"`
}

type Options struct {
	Unified int
}

func Run(left, right []Document, opts Options) ([]Result, error) {
	leftByKey := documentsByKey(left)
	rightByKey := documentsByKey(right)
	keys := sortedKeys(leftByKey, rightByKey)

	var results []Result
	for _, key := range keys {
		l, hasLeft := leftByKey[key]
		r, hasRight := rightByKey[key]

		switch {
		case hasLeft && hasRight && l.Body == r.Body:
			continue
		case hasLeft && hasRight:
			result, err := resultFor(r, ChangeModified, l.Body, r.Body, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case hasLeft:
			result, err := resultFor(l, ChangeRemoved, l.Body, "", opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case hasRight:
			result, err := resultFor(r, ChangeAdded, "", r.Body, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
	}

	return results, nil
}

func keyOf(doc Document) string {
	return strings.Join([]string{
		parentKind(doc.Parent),
		doc.Parent.Namespace,
		doc.Parent.Name,
		doc.Resource.Group,
		doc.Resource.Kind,
		doc.Resource.Namespace,
		doc.Resource.Name,
	}, "\x00")
}

func documentsByKey(docs []Document) map[string]Document {
	out := make(map[string]Document, len(docs))
	for _, doc := range docs {
		out[keyOf(doc)] = doc
	}
	return out
}

func sortedKeys(left, right map[string]Document) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
	}
	for key := range right {
		seen[key] = struct{}{}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resultFor(doc Document, change Change, from, to string, opts Options) (Result, error) {
	diff, err := unified(doc, from, to, opts)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Parent:   doc.Parent,
		Resource: doc.Resource,
		Change:   change,
		Diff:     diff,
	}, nil
}

func unified(doc Document, from, to string, opts Options) (string, error) {
	header := headerOf(doc)
	displayFrom, displayTo, err := displayBodies(doc, from, to)
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", header, err)
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(displayFrom),
		B:        difflib.SplitLines(displayTo),
		FromFile: header,
		ToFile:   header,
		Context:  opts.Unified,
	})
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", header, err)
	}
	return diff, nil
}

func displayBodies(doc Document, from, to string) (string, string, error) {
	if doc.Resource.Kind != "Secret" {
		return from, to, nil
	}
	return redactedSecretBodies(from, to)
}

func redactedSecretBodies(from, to string) (string, string, error) {
	fromObject, err := decodeDiffYAML(from)
	if err != nil {
		return "", "", fmt.Errorf("redact Secret before body: %w", err)
	}
	toObject, err := decodeDiffYAML(to)
	if err != nil {
		return "", "", fmt.Errorf("redact Secret after body: %w", err)
	}

	redactSecretFieldPair(fromObject, toObject, "data")
	redactSecretFieldPair(fromObject, toObject, "stringData")

	redactedFrom, err := encodeDiffYAML(fromObject)
	if err != nil {
		return "", "", fmt.Errorf("encode redacted Secret before body: %w", err)
	}
	redactedTo, err := encodeDiffYAML(toObject)
	if err != nil {
		return "", "", fmt.Errorf("encode redacted Secret after body: %w", err)
	}
	return redactedFrom, redactedTo, nil
}

func decodeDiffYAML(body string) (map[string]any, error) {
	if body == "" {
		return nil, nil
	}
	var object map[string]any
	if err := yaml.Unmarshal([]byte(body), &object); err != nil {
		return nil, err
	}
	return object, nil
}

func encodeDiffYAML(object map[string]any) (string, error) {
	if object == nil {
		return "", nil
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(object); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func redactSecretFieldPair(fromObject, toObject map[string]any, field string) {
	fromValues := stringMapField(fromObject, field)
	toValues := stringMapField(toObject, field)
	if fromValues == nil && toValues == nil {
		return
	}

	keys := mapKeys(fromValues, toValues)
	for _, key := range keys {
		fromValue, hasFrom := fromValues[key]
		toValue, hasTo := toValues[key]
		switch {
		case hasFrom && hasTo && reflect.DeepEqual(fromValue, toValue):
			fromValues[key] = "<redacted>"
			toValues[key] = "<redacted>"
		case hasFrom && hasTo:
			fromValues[key] = "<redacted-before>"
			toValues[key] = "<redacted-after>"
		case hasFrom:
			fromValues[key] = "<redacted-removed>"
		case hasTo:
			toValues[key] = "<redacted-added>"
		}
	}
}

func stringMapField(object map[string]any, field string) map[string]any {
	if object == nil {
		return nil
	}
	values, ok := object[field]
	if !ok {
		return nil
	}
	switch typed := values.(type) {
	case map[string]any:
		return typed
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				continue
			}
			converted[stringKey] = value
		}
		object[field] = converted
		return converted
	default:
		return nil
	}
}

func mapKeys(left, right map[string]any) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
	}
	for key := range right {
		seen[key] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func headerOf(doc Document) string {
	parts := []string{
		fmt.Sprintf("%s: %s", parentKind(doc.Parent), parentName(doc.Parent)),
		fmt.Sprintf("Source: %d", doc.Parent.SourceIndex),
	}
	if doc.Parent.SourceName != "" {
		parts = append(parts, fmt.Sprintf("name=%q", doc.Parent.SourceName))
	}
	if doc.Parent.SourcePath != "" {
		parts = append(parts, doc.Parent.SourcePath)
	}
	parts = append(parts, resourceName(doc.Resource))
	return strings.Join(parts, " ")
}

func parentKind(parent Parent) string {
	return "Application"
}

func parentName(parent Parent) string {
	if parent.Namespace == "" {
		return parent.Name
	}
	return parent.Namespace + "/" + parent.Name
}

func resourceName(resource Resource) string {
	kind := resource.Kind
	if resource.Group != "" {
		kind = resource.Group + "/" + kind
	}
	name := resource.Name
	if resource.Namespace != "" {
		name = resource.Namespace + "/" + name
	}
	return kind + ": " + name
}
