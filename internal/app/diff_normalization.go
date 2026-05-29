package app

import (
	"bytes"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diff"
	"github.com/sholdee/drydock/internal/manifest"
	"go.yaml.in/yaml/v4"
)

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
		normalization.JSONPointers = append(normalization.JSONPointers, rule.JSONPointers...)
		normalization.JQPathExpressions = append(normalization.JQPathExpressions, rule.JQPathExpressions...)
		normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, rule.ManagedFieldsManagers...)
	}
	global := globalNormalizationFor(settings, id)
	normalization.JSONPointers = append(normalization.JSONPointers, global.JSONPointers...)
	normalization.JQPathExpressions = append(normalization.JQPathExpressions, global.JQPathExpressions...)
	normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, global.ManagedFieldsManagers...)
	normalization.KnownTypeFields = append(normalization.KnownTypeFields, global.KnownTypeFields...)
	return normalization
}

func globalNormalizationFor(settings config.ArgoSettings, id manifest.Identity) diff.Normalization {
	var normalization diff.Normalization
	keys := make([]string, 0, len(settings.ResourceCustomizations))
	for key := range settings.ResourceCustomizations {
		if resourceCustomizationKeyMatches(key, id) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		customization := settings.ResourceCustomizations[key]
		normalization.JSONPointers = append(normalization.JSONPointers, customization.IgnoreDifferences.JSONPointers...)
		normalization.JQPathExpressions = append(normalization.JQPathExpressions, customization.IgnoreDifferences.JQPathExpressions...)
		normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, customization.IgnoreDifferences.ManagedFieldsManagers...)
		for _, field := range customization.KnownTypeFields {
			normalization.KnownTypeFields = append(normalization.KnownTypeFields, diff.KnownTypeField{
				Field: field.Field,
				Type:  field.Type,
			})
		}
	}
	return normalization
}

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
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(obj); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
