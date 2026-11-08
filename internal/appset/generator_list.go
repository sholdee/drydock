package appset

import (
	"encoding/json"
	"errors"
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	sigsyaml "sigs.k8s.io/yaml"
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

// listGeneratorParamSets mirrors the Argo CD v3.6.1 list generator
// (applicationset/generators/list.go:33-90). Every decode failure fails the
// whole ApplicationSet there, so it is a generatorParamsError here: an
// element that is not a mapping (an element written as null decodes to no
// JSON at all, which does not decode either), a non-string value or a
// non-mapping values field without goTemplate, and an elementsYaml that is
// not a YAML list of mappings. An elementsYaml entry written as null is one
// empty param set.
func listGeneratorParamSets(manifestPath string, list *argoappv1.ListGenerator, useGoTemplate bool) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	out := make([]generatorParamSet, 0, len(list.Elements))
	for _, element := range list.Elements {
		var params map[string]any
		if err := json.Unmarshal(element.Raw, &params); err != nil {
			return nil, nil, &generatorParamsError{err: fmt.Errorf("error unmarshling list element %w", err)}
		}
		if !useGoTemplate {
			normalized, err := normalizeListElementParams(params)
			if err != nil {
				return nil, nil, &generatorParamsError{err: err}
			}
			params = normalized
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "list",
		})
	}
	if list.ElementsYaml == "" {
		return out, nil, nil
	}
	var elements []map[string]any
	if err := sigsyaml.Unmarshal([]byte(list.ElementsYaml), &elements); err != nil {
		return nil, nil, &generatorParamsError{err: fmt.Errorf("error unmarshling decoded ElementsYaml %w", err)}
	}
	for _, params := range elements {
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "list",
		})
	}
	return out, nil, nil
}

// errListValueNotString is the text of the Argo CD v3.6.1 list generator
// error for a non-string value without goTemplate (list.go:70-73). It
// formats a nil error with %w, so the text ends in "%!w(<nil>)".
var errListValueNotString = errors.New("error parsing value as string %!w(<nil>)")

// normalizeListElementParams mirrors the fasttemplate branch of the Argo CD
// v3.6.1 list generator (list.go:55-77), with its error texts.
func normalizeListElementParams(element map[string]any) (map[string]any, error) {
	params := map[string]any{}
	for key, value := range element {
		if key == "values" {
			values, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("error parsing values map")
			}
			for valueKey, nestedValue := range values {
				rendered, ok := nestedValue.(string)
				if !ok {
					return nil, errListValueNotString
				}
				params["values."+valueKey] = rendered
			}
			continue
		}
		rendered, ok := value.(string)
		if !ok {
			return nil, errListValueNotString
		}
		params[key] = rendered
	}
	return params, nil
}
