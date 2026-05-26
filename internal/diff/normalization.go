package diff

import (
	"bytes"
	"errors"
	"fmt"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"go.yaml.in/yaml/v4"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

var errEmptyDiffBody = errors.New("empty diff body")

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
	if !requiresDefaultIgnoredFieldNormalization(body, opts) &&
		len(attrs) == 0 &&
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

func requiresDefaultIgnoredFieldNormalization(body string, opts Options) bool {
	return !opts.ShowIgnoredFields &&
		(strings.Contains(body, "helm.sh/chart") ||
			strings.Contains(body, "chart:") ||
			strings.Contains(body, `"chart":`) ||
			strings.Contains(body, `'chart':`) ||
			strings.Contains(body, "app.kubernetes.io/version") ||
			strings.Contains(body, "checksum/"))
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
	if !opts.ShowIgnoredFields {
		stripDefaultIgnoredFields(object)
	}
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

func stripDefaultIgnoredFields(object map[string]any) {
	stripDefaultIgnoredMetadataAttrs(object, []string{"metadata"}, "labels")
	stripDefaultIgnoredMetadataAttrs(object, []string{"spec", "template", "metadata"}, "labels")
	stripChecksumAnnotations(object)
	deleteEmptyNestedMap(object, "spec", "template", "metadata")
}

func stripDefaultIgnoredMetadataAttrs(object map[string]any, path []string, field string) {
	metadata, ok := nestedStringMapField(object, path...)
	if !ok {
		return
	}
	values, ok := stringMapField(metadata, field)
	if !ok {
		return
	}
	for _, attr := range []string{"helm.sh/chart", "chart", "app.kubernetes.io/version"} {
		delete(values, attr)
	}
	if len(values) == 0 {
		delete(metadata, field)
	}
}

func stripChecksumAnnotations(object map[string]any) {
	metadata, ok := nestedStringMapField(object, "spec", "template", "metadata")
	if !ok {
		return
	}
	annotations, ok := stringMapField(metadata, "annotations")
	if !ok {
		return
	}
	for key := range annotations {
		if strings.HasPrefix(key, "checksum/") {
			delete(annotations, key)
		}
	}
	if len(annotations) == 0 {
		delete(metadata, "annotations")
	}
}

func nestedStringMapField(object map[string]any, fields ...string) (map[string]any, bool) {
	current := object
	for _, field := range fields {
		next, ok := stringMapField(current, field)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func deleteEmptyNestedMap(object map[string]any, fields ...string) {
	if len(fields) == 0 {
		return
	}
	parent := object
	for _, field := range fields[:len(fields)-1] {
		next, ok := stringMapField(parent, field)
		if !ok {
			return
		}
		parent = next
	}
	child, ok := stringMapField(parent, fields[len(fields)-1])
	if ok && len(child) == 0 {
		delete(parent, fields[len(fields)-1])
	}
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
