package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	sigsyaml "sigs.k8s.io/yaml"
)

// Keys under configs.cm that the argo-helm argo-cd chart uses itself and
// does not copy into argocd-cm.
const (
	helmCMCreateKey                       = "create"
	helmCMAnnotationsKey                  = "annotations"
	helmCMResourceExclusionsAdditionalKey = "resourceExclusionsAdditional"
	helmCMResourceExclusionsKey           = "resource.exclusions"
)

// helmArgoCDConfigMapData returns the argocd-cm data that the argo-helm
// argo-cd chart renders from the configs.cm values of one values file. It
// follows the chart at version 10.14.0:
//
//   - templates/argocd-configs/argocd-cm.yaml renders argocd-cm only when
//     configs.cm.create is true. When a values file sets create to a false
//     value, the chart renders no argocd-cm, and rendered is false.
//   - The "argo-cd.config.cm" helper in templates/_helpers.tpl leaves out
//     create, annotations, and resourceExclusionsAdditional. When
//     resourceExclusionsAdditional is set, the helper adds it as YAML to the
//     end of resource.exclusions.
//   - The helper turns each value into text with toString and leaves out
//     every key whose text is empty.
//
// Helm reads values files with sigs.k8s.io/yaml, so every number is a
// float64. toString then writes 100000000 as "1e+08", a map as
// "map[a:1]", and a list as "[1 a]". drydock converts the values the same
// way.
//
// drydock does not read the chart's default values. A null value removes a
// chart default in Helm, so drydock leaves out every key whose value is
// null. Helm renders "<nil>" only for a null key that the chart does not
// define, which drydock cannot tell apart. For the same reason, the
// resourceExclusionsAdditional text is added only to a resource.exclusions
// value that the values file sets.
func helmArgoCDConfigMapData(cm map[string]any, path string) (data map[string]string, rendered bool, diags []diagnostic.Diagnostic) {
	if len(cm) == 0 {
		return nil, true, nil
	}
	values, err := helmValuesFromYAML(cm)
	if err != nil {
		return nil, true, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    fmt.Sprintf("invalid configs.cm Helm values: %v", err),
			Provenance: diagnostic.Provenance{Path: path, Pointer: "configs.cm"},
		}}
	}
	if create, ok := values[helmCMCreateKey]; ok && !helmTruthy(create) {
		return nil, false, nil
	}

	config := make(map[string]any, len(values))
	for key, value := range values {
		switch key {
		case helmCMCreateKey, helmCMAnnotationsKey, helmCMResourceExclusionsAdditionalKey:
			continue
		}
		config[key] = value
	}
	if additional := values[helmCMResourceExclusionsAdditionalKey]; helmTruthy(additional) {
		exclusions, diag := helmResourceExclusionsWithAdditional(config[helmCMResourceExclusionsKey], additional, path)
		if diag != nil {
			diags = append(diags, *diag)
		} else {
			config[helmCMResourceExclusionsKey] = exclusions
		}
	}

	data = make(map[string]string, len(config))
	for key, value := range config {
		if value == nil {
			continue
		}
		if text := helmToString(value); text != "" {
			data[key] = text
		}
	}
	return data, true, diags
}

// helmResourceExclusionsWithAdditional builds resource.exclusions as the
// chart helper does:
//
//	$existing := get $merged "resource.exclusions" | default "" | trimSuffix "\n"
//	$additional := .Values.configs.cm.resourceExclusionsAdditional | toYaml
//	list $existing $additional | compact | join "\n"
//
// trimSuffix accepts only text, so Helm fails when resource.exclusions is a
// value other than text that default does not replace.
func helmResourceExclusionsWithAdditional(existing, additional any, path string) (string, *diagnostic.Diagnostic) {
	existingText := ""
	if helmTruthy(existing) {
		text, ok := existing.(string)
		if !ok {
			return "", &diagnostic.Diagnostic{
				Severity: diagnostic.SeverityError,
				Category: "settings",
				Message:  "configs.cm.resource.exclusions must be text when configs.cm.resourceExclusionsAdditional is set; the argo-cd Helm chart fails to render otherwise",
				Provenance: diagnostic.Provenance{
					Path:    path,
					Pointer: "configs.cm." + helmCMResourceExclusionsKey,
				},
			}
		}
		existingText = strings.TrimSuffix(text, "\n")
	}
	var parts []string
	for _, part := range []string{existingText, helmToYAML(additional)} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// helmValuesFromYAML converts decoded YAML values into the types Helm uses
// after sigs.k8s.io/yaml reads a values file: maps, lists, text, booleans,
// float64 numbers, and nil.
func helmValuesFromYAML(values map[string]any) (map[string]any, error) {
	data, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// helmToString is Sprig's toString for Helm values: text stays as it is,
// and every other value is written with fmt's %v.
func helmToString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", value)
}

// helmToYAML is Helm's toYaml template function (pkg/engine/funcs.go).
func helmToYAML(value any) string {
	data, err := sigsyaml.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(string(data), "\n")
}

// helmTruthy reports whether a Go template treats a Helm value as true, as
// text/template's IsTrue does. value is one of the types that
// helmValuesFromYAML returns.
func helmTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case map[string]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return true
	}
}
