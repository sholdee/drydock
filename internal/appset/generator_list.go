package appset

import (
	"encoding/json"
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v3"
)

func evaluateListGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.List.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := listGeneratorParamSets(ctx.ManifestPath, generator.List, ctx.AppSet.Spec.GoTemplate)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyGeneratorSelector(ctx.ManifestPath, generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func listGeneratorParamSets(manifestPath string, list *argoappv1.ListGenerator, useGoTemplate bool) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	out := make([]generatorParamSet, 0, len(list.Elements))
	var diags []diagnostic.Diagnostic
	for i, element := range list.Elements {
		params := map[string]any{}
		if len(element.Raw) > 0 {
			if err := json.Unmarshal(element.Raw, &params); err != nil {
				return nil, nil, fmt.Errorf("list generator element %d must be a mapping: %w", i, err)
			}
		}
		if !useGoTemplate {
			normalized, err := normalizeListElementParams(i, params)
			if err != nil {
				return nil, nil, err
			}
			params = normalized
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "list",
		})
	}
	if strings.TrimSpace(list.ElementsYaml) == "" {
		return out, diags, nil
	}
	var elements []any
	if err := yaml.Unmarshal([]byte(list.ElementsYaml), &elements); err != nil {
		diags = append(diags, appsetDiagnostic(manifestPath, fmt.Sprintf("list generator elementsYaml is not valid YAML: %v", err)))
		return out, diags, nil
	}
	for i, element := range elements {
		params, ok := element.(map[string]any)
		if !ok {
			diags = append(diags, appsetDiagnostic(manifestPath, fmt.Sprintf("list generator elementsYaml entries must be mappings: entry %d", i)))
			continue
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "list",
		})
	}
	return out, diags, nil
}

func normalizeListElementParams(index int, element map[string]any) (map[string]any, error) {
	params := map[string]any{}
	for key, value := range element {
		if key == "values" {
			values, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("list generator element %d values must be a mapping of strings", index)
			}
			for valueKey, nestedValue := range values {
				rendered, ok := nestedValue.(string)
				if !ok {
					return nil, fmt.Errorf("list generator element %d values.%s must be a string", index, valueKey)
				}
				params["values."+valueKey] = rendered
			}
			continue
		}
		rendered, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("list generator element %d field %q must be a string", index, key)
		}
		params[key] = rendered
	}
	return params, nil
}
