package app

import (
	"sort"
	"strings"

	"github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diff"
	"github.com/sholdee/drydock/internal/format"
	"github.com/sholdee/drydock/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// ignoreDifferencesAnnotationValue mirrors Argo CD's parsed form of the
// argocd.argoproj.io/ignore-differences annotation: only jsonPointers is
// supported.
type ignoreDifferencesAnnotationValue struct {
	JSONPointers []string `json:"jsonPointers"`
}

// annotationIgnoreJSONPointers returns the jsonPointers declared by obj's own
// argocd.argoproj.io/ignore-differences annotation, following Argo CD's
// ExtractIgnoreDifferencesFromAnnotations: the value is YAML decoded with
// sigs.k8s.io/yaml, and an empty value, a parse error, or an empty
// jsonPointers list yields no rule. The rule Argo CD builds from the
// annotation targets the annotated object's own group, kind, name, and
// namespace, so the pointers apply only to that document.
func annotationIgnoreJSONPointers(obj *unstructured.Unstructured) []string {
	if obj == nil {
		return nil
	}
	value, ok := obj.GetAnnotations()[common.AnnotationKeyIgnoreDifferences]
	if !ok || value == "" {
		return nil
	}
	var parsed ignoreDifferencesAnnotationValue
	if err := yaml.Unmarshal([]byte(value), &parsed); err != nil || len(parsed.JSONPointers) == 0 {
		return nil
	}
	return parsed.JSONPointers
}

// normalizationFor returns the diff normalization for one resource. It keeps
// Argo CD v3.6.1's rule order and keeps every copy of a repeated pointer or
// expression: Application spec.ignoreDifferences rules come first, in spec
// order, and resource.customizations rules come after them
// (util/argo/normalizers/diff_normalizer.go:121-176). The caller adds the
// resource's annotation rule, which runs between the two.
func normalizationFor(application argoappv1.Application, id manifest.Identity, settings config.ArgoSettings) diff.Normalization {
	normalization := diff.Normalization{
		CompareOptions: diff.CompareOptions{
			IgnoreAggregatedRoles:     settings.CompareOptions.IgnoreAggregatedRoles,
			IgnoreResourceStatusField: settings.CompareOptions.IgnoreResourceStatusField,
		},
	}
	for _, rule := range application.Spec.IgnoreDifferences {
		if !ignoreRuleMatches(rule, id) {
			continue
		}
		normalization.ApplicationRules = appendIgnoreRule(normalization.ApplicationRules, rule.JSONPointers, rule.JQPathExpressions)
		normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, rule.ManagedFieldsManagers...)
	}
	global := globalNormalizationFor(settings, id)
	normalization.CustomizationRules = global.CustomizationRules
	normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, global.ManagedFieldsManagers...)
	normalization.KnownTypeFields = global.KnownTypeFields
	return normalization
}

// appendIgnoreRule adds one rule when it has pointers or jq expressions, as
// Argo CD builds no patch for a rule without them.
func appendIgnoreRule(rules []diff.IgnoreRule, pointers, expressions []string) []diff.IgnoreRule {
	if len(pointers) == 0 && len(expressions) == 0 {
		return rules
	}
	return append(rules, diff.IgnoreRule{
		JSONPointers:      append([]string(nil), pointers...),
		JQPathExpressions: append([]string(nil), expressions...),
	})
}

// globalNormalizationFor returns the resource.customizations normalization for
// one resource. Argo CD iterates its overrides map in Go map order, which is
// not defined. drydock uses sorted key order so the result is stable.
func globalNormalizationFor(settings config.ArgoSettings, id manifest.Identity) diff.Normalization {
	var normalization diff.Normalization
	keys := make([]string, 0, len(settings.ResourceCustomizations))
	for key := range settings.ResourceCustomizations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		customization := settings.ResourceCustomizations[key]
		group, kind := overrideKeyGroupKind(key)
		if glob.Match(group, id.Group) && glob.Match(kind, id.Kind) {
			normalization.CustomizationRules = appendIgnoreRule(normalization.CustomizationRules,
				customization.IgnoreDifferences.JSONPointers, customization.IgnoreDifferences.JQPathExpressions)
		}
		if resourceCustomizationKeyMatches(key, id) {
			normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, customization.IgnoreDifferences.ManagedFieldsManagers...)
		}
		// The known-types normalizer looks fields up by exact group and kind,
		// with no glob matching (util/argo/normalizers/knowntypes_normalizer.go:42-58).
		if group != id.Group || kind != id.Kind {
			continue
		}
		for _, field := range customization.KnownTypeFields {
			normalization.KnownTypeFields = append(normalization.KnownTypeFields, diff.KnownTypeField{
				Field: field.Field,
				Type:  field.Type,
			})
		}
	}
	return normalization
}

// overrideKeyGroupKind splits a resource.customizations key like Argo CD's
// getGroupKindForOverrideKey (util/argo/normalizers/util.go:8-21): a key with
// one slash is group/kind, and a key with no slash is a kind in the core
// group. Argo CD logs any other key and uses an empty group and kind, which
// matches no resource.
func overrideKeyGroupKind(key string) (string, string) {
	parts := strings.Split(key, "/")
	switch len(parts) {
	case 2:
		return parts[0], parts[1]
	case 1:
		return "", parts[0]
	default:
		return "", ""
	}
}

// resourceCustomizationKeyMatches matches a resource.customizations key for
// managedFieldsManagers. It matches a key with no slash against the kind in
// any group.
func resourceCustomizationKeyMatches(key string, id manifest.Identity) bool {
	if key == "" {
		return false
	}
	if key == "*/*" {
		return true
	}
	group, kind, found := strings.Cut(key, "/")
	if !found {
		return glob.Match(key, id.Kind)
	}
	return glob.Match(group, id.Group) && glob.Match(kind, id.Kind)
}

func ignoreRuleMatches(rule argoappv1.ResourceIgnoreDifferences, id manifest.Identity) bool {
	if !glob.Match(rule.Group, id.Group) || !glob.Match(rule.Kind, id.Kind) {
		return false
	}
	if rule.Name != "" && rule.Name != id.Name {
		return false
	}
	if rule.Namespace != "" && rule.Namespace != id.Namespace {
		return false
	}
	return true
}

func marshalDiffObject(obj map[string]any) (string, error) {
	data, err := format.MarshalYAML(obj)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
