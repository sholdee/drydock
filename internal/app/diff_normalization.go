package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
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
		if managedFieldsKeyMatches(key, id) {
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

// managedFieldsKeyMatches matches a resource.customizations key for
// managedFieldsManagers. Argo CD looks the key up in the overrides map by the
// exact text "group/kind", then by "*/*" (util/argo/diff/ignore.go:52-64).
// It uses no glob. A core resource has the lookup key "/Kind", so a key with
// no slash, such as Service, never matches.
func managedFieldsKeyMatches(key string, id manifest.Identity) bool {
	return key == id.Group+"/"+id.Kind || key == "*/*"
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

// Stable codes of the diff normalization diagnostics.
const (
	// diffIgnorePointerInvalidCode is the warning for an ignore-rule
	// jsonPointer that is not a valid JSON pointer. Argo CD applies it with
	// JSON-patch path rules and skips it where the patch fails, so drydock
	// does the same and warns.
	diffIgnorePointerInvalidCode = "diff.ignore-pointer-invalid"
	// diffIgnoreRuleInvalidCode is the error for a jq expression that Argo
	// CD cannot build. Argo CD fails the comparison of the Application.
	diffIgnoreRuleInvalidCode = "diff.ignore-rule-invalid"
)

// diffApplicationKey names one Application in a diff.
type diffApplicationKey struct {
	Namespace string
	Name      string
}

func (key diffApplicationKey) String() string {
	if key.Namespace == "" {
		return key.Name
	}
	return key.Namespace + "/" + key.Name
}

// targetApplications returns each Application that renders manifests, with
// the build that holds the settings it is compared under: the current side
// when the Application exists there, and the baseline side otherwise. Argo
// CD compares under the rules and settings of the live Application, which
// are the current side's after the change.
func targetApplications(leftBuild, rightBuild BuildResult) ([]diffApplicationKey, map[diffApplicationKey]argoappv1.Application, map[diffApplicationKey]config.ArgoSettings) {
	apps := map[diffApplicationKey]argoappv1.Application{}
	settings := map[diffApplicationKey]config.ArgoSettings{}
	for _, build := range []BuildResult{rightBuild, leftBuild} {
		for _, item := range build.ApplicationManifests {
			key := diffApplicationKey{Namespace: item.Application.Namespace, Name: item.Application.Name}
			if _, ok := apps[key]; ok {
				continue
			}
			apps[key] = item.Application
			settings[key] = build.Settings
		}
	}
	keys := make([]diffApplicationKey, 0, len(apps))
	for key := range apps {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys, apps, settings
}

// invalidIgnoreRuleDiagnostics returns an error diagnostic for each
// Application whose ignore rules hold a jq expression that does not parse or
// compile. Argo CD v3.6.1 builds the jq patches of every Application rule and
// every resource.customizations entry before it matches any resource, and a
// failure fails the comparison of that Application with a ComparisonError
// condition (controller/state.go:975-983). So the rule fails the
// Application even when it matches no resource.
func invalidIgnoreRuleDiagnostics(leftBuild, rightBuild BuildResult) (map[diffApplicationKey]struct{}, []diagnostic.Diagnostic) {
	keys, apps, settings := targetApplications(leftBuild, rightBuild)
	failed := map[diffApplicationKey]struct{}{}
	var diags []diagnostic.Diagnostic
	for _, key := range keys {
		message := invalidIgnoreRuleMessage(apps[key], settings[key])
		if message == "" {
			continue
		}
		failed[key] = struct{}{}
		diags = append(diags, diagnostic.Diagnostic{
			Code:     diffIgnoreRuleInvalidCode,
			Severity: diagnostic.SeverityError,
			Category: "diff",
			Message:  fmt.Sprintf("Application %s: failed to compare desired states, as Argo CD fails the comparison: %s", key, message),
		})
	}
	return failed, diags
}

func invalidIgnoreRuleMessage(application argoappv1.Application, settings config.ArgoSettings) string {
	for i, rule := range application.Spec.IgnoreDifferences {
		for _, expression := range rule.JQPathExpressions {
			if err := diff.ValidateJQPathExpression(expression); err != nil {
				return fmt.Sprintf("spec.ignoreDifferences[%d]: %v", i, err)
			}
		}
	}
	keys := make([]string, 0, len(settings.ResourceCustomizations))
	for key := range settings.ResourceCustomizations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, expression := range settings.ResourceCustomizations[key].IgnoreDifferences.JQPathExpressions {
			if err := diff.ValidateJQPathExpression(expression); err != nil {
				return fmt.Sprintf("resource.customizations %s: %v", key, err)
			}
		}
	}
	return ""
}

// invalidIgnorePointerDiagnostics warns once per Application and pointer
// about an Application or resource.customizations jsonPointer that is not a
// valid JSON pointer and that a rendered resource uses. drydock applies it
// with Argo CD's JSON-patch path rules. It reads the rules each resource is
// compared under: the current side's, or the baseline side's for a resource
// that only the baseline has.
func invalidIgnorePointerDiagnostics(leftDocs, rightDocs []diff.Document) []diagnostic.Diagnostic {
	current := make(map[string]struct{}, len(rightDocs))
	for _, doc := range rightDocs {
		current[diffDocumentKey(doc)] = struct{}{}
	}
	docs := append([]diff.Document(nil), rightDocs...)
	for _, doc := range leftDocs {
		if _, ok := current[diffDocumentKey(doc)]; !ok {
			docs = append(docs, doc)
		}
	}
	seen := map[string]struct{}{}
	var diags []diagnostic.Diagnostic
	for _, doc := range docs {
		app := diffApplicationKey{Namespace: doc.Parent.Namespace, Name: doc.Parent.Name}
		for _, source := range []struct {
			name  string
			rules []diff.IgnoreRule
		}{
			{"spec.ignoreDifferences", doc.Normalization.ApplicationRules},
			{"resource.customizations", doc.Normalization.CustomizationRules},
		} {
			for _, rule := range source.rules {
				for _, pointer := range rule.JSONPointers {
					reason := diff.InvalidJSONPointerReason(pointer)
					if reason == "" {
						continue
					}
					id := app.String() + "\x00" + source.name + "\x00" + pointer
					if _, ok := seen[id]; ok {
						continue
					}
					seen[id] = struct{}{}
					diags = append(diags, diagnostic.Diagnostic{
						Code:     diffIgnorePointerInvalidCode,
						Severity: diagnostic.SeverityWarning,
						Category: "diff",
						Message: fmt.Sprintf("Application %s: %s jsonPointer %q is not a valid JSON pointer (%s); like Argo CD, drydock applies it as a JSON-patch path and skips it where the patch fails",
							app, source.name, pointer, reason),
					})
				}
			}
		}
	}
	return diags
}

func diffDocumentKey(doc diff.Document) string {
	return strings.Join([]string{doc.Parent.Namespace, doc.Parent.Name, doc.Resource.Group, doc.Resource.Kind, doc.Resource.Namespace, doc.Resource.Name}, "\x00")
}

func withoutApplications(docs []diff.Document, failed map[diffApplicationKey]struct{}) []diff.Document {
	if len(failed) == 0 {
		return docs
	}
	out := make([]diff.Document, 0, len(docs))
	for _, doc := range docs {
		if _, ok := failed[diffApplicationKey{Namespace: doc.Parent.Namespace, Name: doc.Parent.Name}]; ok {
			continue
		}
		out = append(out, doc)
	}
	return out
}
