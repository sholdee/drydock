package diff

import (
	"errors"
	"fmt"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"github.com/sholdee/drydock/internal/format"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

var errEmptyDiffBody = errors.New("empty diff body")

func normalizedDocumentBodies(left, right Document, hasLeft, hasRight bool, opts Options) (string, string, error) {
	if hasLeft && hasRight && len(left.Normalization.ManagedFieldsManagers) > 0 {
		leftBody, rightBody, err := normalizedDocumentPairBodies(left, right, opts)
		if err != nil {
			return "", "", err
		}
		return summarizeConfigMapBinaryDataDocumentBodies(leftBody, rightBody, left, right, hasLeft, hasRight)
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

	return summarizeConfigMapBinaryDataDocumentBodies(leftBody, rightBody, left, right, hasLeft, hasRight)
}
func normalizedDocumentPairBodies(left, right Document, opts Options) (string, string, error) {
	leftObject, err := normalizeDiffObject(left.Body, opts, left.Normalization, left.Resource)
	if err != nil {
		return "", "", fmt.Errorf("normalize %s: %w", headerOf(left), err)
	}
	rightObject, err := normalizeDiffObject(right.Body, opts, right.Normalization, right.Resource)
	if err != nil {
		return "", "", fmt.Errorf("normalize %s: %w", headerOf(right), err)
	}
	leftObject, rightObject, err = normalizeManagedFieldsPair(leftObject, rightObject, left.Normalization.ManagedFieldsManagers)
	if err != nil {
		return "", "", fmt.Errorf("normalize %s: %w", headerOf(right), err)
	}
	stripManagedFields(leftObject)
	stripManagedFields(rightObject)

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
		len(normalization.ApplicationRules) == 0 &&
		len(normalization.AnnotationJSONPointers) == 0 &&
		len(normalization.CustomizationRules) == 0 &&
		!knownTypeFieldsApply(resource, normalization.KnownTypeFields) &&
		!compareOptionsRequireObject(resource, normalization.CompareOptions) {
		return body, nil
	}
	object, err := normalizeDiffObject(body, opts, normalization, resource)
	if err != nil {
		return "", err
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

func normalizeDiffObject(body string, opts Options, normalization Normalization, resource Resource) (map[string]any, error) {
	attrs := stripAttrSet(opts.StripAttrs)
	if body == "" {
		// An empty body is a missing document, which encodes as "".
		return nil, nil //nolint:nilnil // nil means no document, not an error.
	}
	object, err := decodeDiffYAML(body)
	if err != nil {
		return nil, err
	}
	stripMetadataAttrs(object, attrs)
	if !opts.ShowIgnoredFields {
		stripDefaultIgnoredFields(object)
	}
	object, err = applyIgnoreRules(object, normalization)
	if err != nil {
		return nil, err
	}
	object = normalizeCompareOptionsObject(object, resource, normalization.CompareOptions)
	// Argo CD runs the known-types normalizer after the ignore normalizer
	// (util/argo/diff/normalize.go:46-57). The order matters: a jq select can
	// match a value before remarshaling changes it, and an ignore rule can
	// remove a field that would fail to remarshal.
	object, err = normalizeKnownTypeFields(object, resource, normalization.KnownTypeFields)
	if err != nil {
		return nil, err
	}
	return object, nil
}

// applyIgnoreRules applies the ignore rules in Argo CD's order: Application
// rules, then the resource's annotation rule, then resource.customizations
// rules. Each rule applies its JSON pointers and then its jq expressions
// (util/argo/normalizers/diff_normalizer.go:142-176). Argo CD applies every
// pointer, from any of these sources, as its own JSON-patch remove operation
// and skips one that fails, so all pointers use the same JSON-patch path
// rules here.
func applyIgnoreRules(object map[string]any, normalization Normalization) (map[string]any, error) {
	apply := func(rule IgnoreRule) error {
		var err error
		object = removeJSONPatchPointers(object, rule.JSONPointers)
		object, err = removeJQPathExpressions(object, rule.JQPathExpressions)
		return err
	}
	for _, rule := range normalization.ApplicationRules {
		if err := apply(rule); err != nil {
			return nil, err
		}
	}
	object = removeJSONPatchPointers(object, normalization.AnnotationJSONPointers)
	for _, rule := range normalization.CustomizationRules {
		if err := apply(rule); err != nil {
			return nil, err
		}
	}
	return object, nil
}

// rolloutGroup and rolloutKind name the Argo Rollouts kind that Argo CD's
// known-types normalizer always covers. Unless a resource.customizations
// entry sets knownTypeFields for it, Argo CD normalizes spec.template.spec as
// a PodSpec (util/argo/normalizers/knowntypes_normalizer.go:56-70).
const (
	rolloutGroup = "argoproj.io"
	rolloutKind  = "Rollout"
)

// knownTypeFieldsApply reports whether the known-types normalizer can change
// the resource.
func knownTypeFieldsApply(resource Resource, fields []KnownTypeField) bool {
	return len(fields) > 0 || (resource.Group == rolloutGroup && resource.Kind == rolloutKind)
}

// normalizeKnownTypeFields runs Argo CD's known-types normalizer. fields must
// come only from resource.customizations keys whose group and kind equal the
// resource's group and kind, because Argo CD looks the fields up by exact
// group and kind.
func normalizeKnownTypeFields(object map[string]any, resource Resource, fields []KnownTypeField) (map[string]any, error) {
	if !knownTypeFieldsApply(resource, fields) {
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
	overrides := map[string]argoappv1.ResourceOverride{}
	if len(overrideFields) > 0 {
		overrides[key] = argoappv1.ResourceOverride{KnownTypeFields: overrideFields}
	}
	normalizer, err := normalizers.NewKnownTypesNormalizer(overrides)
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
	data, err := format.MarshalYAML(object)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func cloneIgnoreRule(rule IgnoreRule) IgnoreRule {
	return IgnoreRule{
		JSONPointers:      append([]string(nil), rule.JSONPointers...),
		JQPathExpressions: append([]string(nil), rule.JQPathExpressions...),
	}
}

func cloneIgnoreRules(rules []IgnoreRule) []IgnoreRule {
	if rules == nil {
		return nil
	}
	out := make([]IgnoreRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, cloneIgnoreRule(rule))
	}
	return out
}

func cloneNormalization(normalization Normalization) Normalization {
	return Normalization{
		ApplicationRules:       cloneIgnoreRules(normalization.ApplicationRules),
		CustomizationRules:     cloneIgnoreRules(normalization.CustomizationRules),
		ManagedFieldsManagers:  append([]string(nil), normalization.ManagedFieldsManagers...),
		KnownTypeFields:        append([]KnownTypeField(nil), normalization.KnownTypeFields...),
		CompareOptions:         normalization.CompareOptions,
		AnnotationJSONPointers: append([]string(nil), normalization.AnnotationJSONPointers...),
	}
}
