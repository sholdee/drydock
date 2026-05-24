package diff

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"github.com/pmezard/go-difflib/difflib"
	"go.yaml.in/yaml/v4"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

type KnownTypeField struct {
	Field string
	Type  string
}

type Normalization struct {
	JSONPointers          []string
	JQPathExpressions     []string
	ManagedFieldsManagers []string
	KnownTypeFields       []KnownTypeField
	CompareOptions        CompareOptions
}

type Document struct {
	Parent        Parent        `json:"parent" yaml:"parent"`
	Resource      Resource      `json:"resource" yaml:"resource"`
	Body          string        `json:"body" yaml:"body"`
	Normalization Normalization `json:"-" yaml:"-"`
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
	Unified    int
	StripAttrs []string
}

var errEmptyDiffBody = errors.New("empty diff body")
var errJSONPointerRemovedRoot = errors.New("json pointer removed document root")

func Run(left, right []Document, opts Options) ([]Result, error) {
	leftByKey := documentsByKey(left)
	rightByKey := documentsByKey(right)
	keys := sortedKeys(leftByKey, rightByKey)

	results := make([]Result, 0)
	for _, key := range keys {
		result, include, err := diffResultForKey(leftByKey, rightByKey, key, opts)
		if err != nil {
			return nil, err
		}
		if include {
			results = append(results, result)
		}
	}

	return results, nil
}

func diffResultForKey(leftByKey, rightByKey map[string]Document, key string, opts Options) (Result, bool, error) {
	left, hasLeft := leftByKey[key]
	right, hasRight := rightByKey[key]
	left, right = documentsWithSharedNormalization(left, right, hasLeft, hasRight)
	leftBody, rightBody, err := normalizedDocumentBodies(left, right, hasLeft, hasRight, opts)
	if err != nil {
		return Result{}, false, err
	}
	doc, change, include := changedDocument(left, right, hasLeft, hasRight, leftBody, rightBody)
	if !include {
		return Result{}, false, nil
	}
	result, err := resultFor(doc, change, leftBody, rightBody, opts)
	return result, true, err
}

func documentsWithSharedNormalization(left, right Document, hasLeft, hasRight bool) (Document, Document) {
	normalization := Normalization{}
	var compareOptions CompareOptions
	var hasCompareOptions bool
	if hasLeft {
		normalization = appendUniqueNormalization(normalization, left.Normalization)
		compareOptions = left.Normalization.CompareOptions
		hasCompareOptions = true
	}
	if hasRight {
		normalization = appendUniqueNormalization(normalization, right.Normalization)
		if hasCompareOptions {
			compareOptions = mergeCompareOptions(compareOptions, right.Normalization.CompareOptions)
		} else {
			compareOptions = right.Normalization.CompareOptions
		}
	}
	normalization.CompareOptions = compareOptions
	if hasLeft {
		left.Normalization = cloneNormalization(normalization)
	}
	if hasRight {
		right.Normalization = cloneNormalization(normalization)
	}
	return left, right
}

func appendUniqueNormalization(left, right Normalization) Normalization {
	left.JSONPointers = appendUniqueStrings(left.JSONPointers, right.JSONPointers)
	left.JQPathExpressions = appendUniqueStrings(left.JQPathExpressions, right.JQPathExpressions)
	left.ManagedFieldsManagers = appendUniqueStrings(left.ManagedFieldsManagers, right.ManagedFieldsManagers)
	left.KnownTypeFields = appendUniqueKnownTypeFields(left.KnownTypeFields, right.KnownTypeFields)
	return left
}

func appendUniqueStrings(out []string, values []string) []string {
	seen := make(map[string]struct{}, len(out)+len(values))
	for _, value := range out {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func appendUniqueKnownTypeFields(out []KnownTypeField, values []KnownTypeField) []KnownTypeField {
	seen := make(map[KnownTypeField]struct{}, len(out)+len(values))
	for _, value := range out {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func cloneNormalization(normalization Normalization) Normalization {
	return Normalization{
		JSONPointers:          append([]string(nil), normalization.JSONPointers...),
		JQPathExpressions:     append([]string(nil), normalization.JQPathExpressions...),
		ManagedFieldsManagers: append([]string(nil), normalization.ManagedFieldsManagers...),
		KnownTypeFields:       append([]KnownTypeField(nil), normalization.KnownTypeFields...),
		CompareOptions:        normalization.CompareOptions,
	}
}

func normalizedDocumentBodies(left, right Document, hasLeft, hasRight bool, opts Options) (string, string, error) {
	if hasLeft && hasRight && len(left.Normalization.ManagedFieldsManagers) > 0 {
		return normalizedDocumentPairBodies(left, right, opts)
	}

	var leftBody string
	if hasLeft {
		body, err := normalizeDocumentBody(left, opts)
		if err != nil {
			return "", "", err
		}
		leftBody = body
	}

	var rightBody string
	if hasRight {
		body, err := normalizeDocumentBody(right, opts)
		if err != nil {
			return "", "", err
		}
		rightBody = body
	}

	return leftBody, rightBody, nil
}

func normalizedDocumentPairBodies(left, right Document, opts Options) (string, string, error) {
	leftObject, leftRemoved, err := normalizeDiffObject(left.Body, opts, left.Normalization, left.Resource)
	if err != nil {
		return "", "", fmt.Errorf("normalize %s: %w", headerOf(left), err)
	}
	rightObject, rightRemoved, err := normalizeDiffObject(right.Body, opts, right.Normalization, right.Resource)
	if err != nil {
		return "", "", fmt.Errorf("normalize %s: %w", headerOf(right), err)
	}
	if !leftRemoved && !rightRemoved {
		leftObject, rightObject, err = normalizeManagedFieldsPair(leftObject, rightObject, left.Normalization.ManagedFieldsManagers)
		if err != nil {
			return "", "", fmt.Errorf("normalize %s: %w", headerOf(right), err)
		}
		stripManagedFields(leftObject)
		stripManagedFields(rightObject)
	}

	leftBody, err := encodeDiffYAML(leftObject)
	if err != nil {
		return "", "", fmt.Errorf("normalize %s: %w", headerOf(left), err)
	}
	rightBody, err := encodeDiffYAML(rightObject)
	if err != nil {
		return "", "", fmt.Errorf("normalize %s: %w", headerOf(right), err)
	}
	return leftBody, rightBody, nil
}

func changedDocument(left, right Document, hasLeft, hasRight bool, leftBody, rightBody string) (Document, Change, bool) {
	if leftBody == rightBody {
		return Document{}, "", false
	}
	switch {
	case hasLeft && hasRight:
		return right, ChangeModified, true
	case hasLeft:
		return left, ChangeRemoved, true
	case hasRight:
		return right, ChangeAdded, true
	default:
		return Document{}, "", false
	}
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

func normalizeDocumentBody(doc Document, opts Options) (string, error) {
	body, err := normalizeDiffBodyForResource(doc.Body, opts, doc.Normalization, doc.Resource)
	if err != nil {
		return "", fmt.Errorf("normalize %s: %w", headerOf(doc), err)
	}
	return body, nil
}

func normalizeDiffBody(body string, opts Options, normalization Normalization) (string, error) {
	return normalizeDiffBodyForResource(body, opts, normalization, Resource{})
}

func normalizeDiffBodyForResource(body string, opts Options, normalization Normalization, resource Resource) (string, error) {
	attrs := stripAttrSet(opts.StripAttrs)
	if len(attrs) == 0 &&
		len(normalization.JSONPointers) == 0 &&
		len(normalization.JQPathExpressions) == 0 &&
		len(normalization.KnownTypeFields) == 0 &&
		!compareOptionsRequireObject(resource, normalization.CompareOptions) {
		return body, nil
	}
	object, removedRoot, err := normalizeDiffObject(body, opts, normalization, resource)
	if err != nil {
		return "", err
	}
	if removedRoot {
		return "", nil
	}
	return encodeDiffYAML(object)
}

func normalizeDiffObject(body string, opts Options, normalization Normalization, resource Resource) (map[string]any, bool, error) {
	attrs := stripAttrSet(opts.StripAttrs)
	if body == "" {
		return nil, false, nil
	}
	if err := validateJSONPointers(normalization.JSONPointers); err != nil {
		return nil, false, err
	}
	object, err := decodeDiffYAML(body)
	if err != nil {
		return nil, false, err
	}
	object, err = normalizeKnownTypeFields(object, resource, normalization.KnownTypeFields)
	if err != nil {
		return nil, false, err
	}
	stripMetadataAttrs(object, attrs)
	object, err = removeJSONPointers(object, normalization.JSONPointers)
	if err != nil {
		if errors.Is(err, errJSONPointerRemovedRoot) {
			return nil, true, nil
		}
		return nil, false, err
	}
	object, err = removeJQPathExpressions(object, normalization.JQPathExpressions)
	if err != nil {
		return nil, false, err
	}
	object = normalizeCompareOptionsObject(object, resource, normalization.CompareOptions)
	return object, false, nil
}

func normalizeKnownTypeFields(object map[string]any, resource Resource, fields []KnownTypeField) (map[string]any, error) {
	if len(fields) == 0 {
		return object, nil
	}
	overrideFields := make([]argoappv1.KnownTypeField, 0, len(fields))
	for _, field := range fields {
		overrideFields = append(overrideFields, argoappv1.KnownTypeField{Field: field.Field, Type: field.Type})
	}
	key := resource.Kind
	if resource.Group != "" {
		key = resource.Group + "/" + resource.Kind
	}
	normalizer, err := normalizers.NewKnownTypesNormalizer(map[string]argoappv1.ResourceOverride{
		key: {KnownTypeFields: overrideFields},
	})
	if err != nil {
		return nil, err
	}
	obj := &unstructured.Unstructured{Object: object}
	if err := normalizer.Normalize(obj); err != nil {
		return nil, err
	}
	return obj.Object, nil
}

func stripAttrSet(attrs []string) map[string]struct{} {
	out := make(map[string]struct{}, len(attrs))
	for _, attr := range attrs {
		attr = strings.TrimSpace(attr)
		if attr != "" {
			out[attr] = struct{}{}
		}
	}
	return out
}

func stripMetadataAttrs(object map[string]any, attrs map[string]struct{}) {
	metadata, ok := stringMapField(object, "metadata")
	if !ok {
		return
	}
	stripMetadataAttrMap(metadata, "labels", attrs)
	stripMetadataAttrMap(metadata, "annotations", attrs)
}

func stripMetadataAttrMap(metadata map[string]any, field string, attrs map[string]struct{}) {
	values, ok := stringMapField(metadata, field)
	if !ok {
		return
	}
	for attr := range attrs {
		delete(values, attr)
	}
	if len(values) == 0 {
		delete(metadata, field)
	}
}

func validateJSONPointers(pointers []string) error {
	for _, pointer := range pointers {
		if _, err := jsonPointerTokens(pointer); err != nil {
			return err
		}
	}
	return nil
}

func removeJSONPointers(object map[string]any, pointers []string) (map[string]any, error) {
	var root any = object
	for _, pointer := range pointers {
		nextRoot, removedRoot, err := removeJSONPointer(root, pointer)
		if err != nil {
			return nil, err
		}
		if removedRoot {
			return nil, errJSONPointerRemovedRoot
		}
		root = nextRoot
	}
	typed, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("json pointer normalization produced %T root, want object", root)
	}
	return typed, nil
}

func removeJSONPointer(root any, pointer string) (any, bool, error) {
	tokens, err := jsonPointerTokens(pointer)
	if err != nil {
		return nil, false, err
	}
	if len(tokens) == 0 {
		return root, true, nil
	}
	nextRoot, _, err := removeJSONPointerValue(root, tokens, pointer)
	return nextRoot, false, err
}

func jsonPointerTokens(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q: must be empty or start with /", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		token, err := decodeJSONPointerToken(part, pointer)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, nil
}

func decodeJSONPointerToken(token, pointer string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			out.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("invalid JSON pointer %q: invalid escape in token %q", pointer, token)
		}
		switch token[i+1] {
		case '0':
			out.WriteByte('~')
		case '1':
			out.WriteByte('/')
		default:
			return "", fmt.Errorf("invalid JSON pointer %q: invalid escape in token %q", pointer, token)
		}
		i++
	}
	return out.String(), nil
}

func removeJSONPointerValue(current any, tokens []string, pointer string) (any, bool, error) {
	if len(tokens) == 0 {
		return current, true, nil
	}
	if object, ok := asStringMap(current); ok {
		token := tokens[0]
		if len(tokens) == 1 {
			if _, exists := object[token]; !exists {
				return current, false, nil
			}
			delete(object, token)
			return object, true, nil
		}
		child, exists := object[token]
		if !exists {
			return current, false, nil
		}
		nextChild, removed, err := removeJSONPointerValue(child, tokens[1:], pointer)
		if err != nil {
			return current, false, err
		}
		if removed {
			object[token] = nextChild
		}
		return object, removed, nil
	}
	if values, ok := current.([]any); ok {
		index, err := parseJSONPointerArrayIndex(tokens[0], pointer)
		if err != nil {
			return current, false, err
		}
		if index < 0 || index >= len(values) {
			return current, false, nil
		}
		if len(tokens) == 1 {
			values = append(values[:index], values[index+1:]...)
			return values, true, nil
		}
		nextChild, removed, err := removeJSONPointerValue(values[index], tokens[1:], pointer)
		if err != nil {
			return current, false, err
		}
		if removed {
			values[index] = nextChild
		}
		return values, removed, nil
	}
	return current, false, nil
}

func asStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, false
			}
			converted[stringKey] = value
		}
		return converted, true
	default:
		return nil, false
	}
}

func parseJSONPointerArrayIndex(token, pointer string) (int, error) {
	if token == "" {
		return 0, fmt.Errorf("invalid JSON pointer %q: empty array index", pointer)
	}
	index, err := strconv.Atoi(token)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("invalid JSON pointer %q: invalid array index %q", pointer, token)
	}
	return index, nil
}

func stringMapField(object map[string]any, field string) (map[string]any, bool) {
	if object == nil {
		return nil, false
	}
	value, ok := object[field]
	if !ok {
		return nil, false
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, false
			}
			converted[stringKey] = value
		}
		object[field] = converted
		return converted, true
	default:
		return nil, false
	}
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
		if !errors.Is(err, errEmptyDiffBody) {
			return "", "", fmt.Errorf("redact Secret before body: %w", err)
		}
	}
	toObject, err := decodeDiffYAML(to)
	if err != nil {
		if !errors.Is(err, errEmptyDiffBody) {
			return "", "", fmt.Errorf("redact Secret after body: %w", err)
		}
	}

	redactSecretFieldPair(fromObject, toObject, "data")
	redactSecretFieldPair(fromObject, toObject, "stringData")
	redactSecretFieldPair(fromObject, toObject, "binaryData")

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
		return nil, errEmptyDiffBody
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
	fromField := secretStringMapField(fromObject, field)
	toField := secretStringMapField(toObject, field)
	if !fromField.present && !toField.present {
		return
	}

	if fromField.malformed || toField.malformed {
		redactMalformedSecretFieldPair(fromObject, toObject, field, fromField, toField)
		return
	}

	keys := mapKeys(fromField.values, toField.values)
	for _, key := range keys {
		fromValue, hasFrom := fromField.values[key]
		toValue, hasTo := toField.values[key]
		switch {
		case hasFrom && hasTo && reflect.DeepEqual(fromValue, toValue):
			fromField.values[key] = "<redacted>"
			toField.values[key] = "<redacted>"
		case hasFrom && hasTo:
			fromField.values[key] = "<redacted-before>"
			toField.values[key] = "<redacted-after>"
		case hasFrom:
			fromField.values[key] = "<redacted-removed>"
		case hasTo:
			toField.values[key] = "<redacted-added>"
		}
	}
}

type secretField struct {
	present   bool
	malformed bool
	values    map[string]any
	raw       any
}

func secretStringMapField(object map[string]any, field string) secretField {
	if object == nil {
		return secretField{}
	}
	values, ok := object[field]
	if !ok {
		return secretField{}
	}

	switch typed := values.(type) {
	case map[string]any:
		if !allStringValues(typed) {
			return secretField{present: true, malformed: true, raw: values}
		}
		return secretField{present: true, values: typed, raw: values}
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return secretField{present: true, malformed: true, raw: values}
			}
			if _, ok := value.(string); !ok {
				return secretField{present: true, malformed: true, raw: values}
			}
			converted[stringKey] = value
		}
		object[field] = converted
		return secretField{present: true, values: converted, raw: values}
	default:
		return secretField{present: true, malformed: true, raw: values}
	}
}

func allStringValues(values map[string]any) bool {
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}

func redactMalformedSecretFieldPair(fromObject, toObject map[string]any, field string, fromField, toField secretField) {
	if fromField.present && toField.present {
		if fromField.malformed && toField.malformed && reflect.DeepEqual(fromField.raw, toField.raw) {
			fromObject[field] = "<redacted-malformed>"
			toObject[field] = "<redacted-malformed>"
			return
		}
		fromObject[field] = "<redacted-malformed-before>"
		toObject[field] = "<redacted-malformed-after>"
		return
	}
	if fromField.present {
		fromObject[field] = "<redacted-malformed-removed>"
		return
	}
	toObject[field] = "<redacted-malformed-added>"
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
