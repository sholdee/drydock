package appset

import (
	"encoding/json"
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
)

func evaluateListGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.List.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags := listGeneratorParamSets(ctx.ManifestPath, generator.List)
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyGeneratorSelector(ctx.ManifestPath, generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func listGeneratorParamSets(manifestPath string, list *argoappv1.ListGenerator) ([]generatorParamSet, []diagnostic.Diagnostic) {
	out := make([]generatorParamSet, 0, len(list.Elements))
	var diags []diagnostic.Diagnostic
	for i, element := range list.Elements {
		params := map[string]any{}
		if len(element.Raw) > 0 {
			if err := json.Unmarshal(element.Raw, &params); err != nil {
				diags = append(diags, appsetDiagnostic(manifestPath, fmt.Sprintf("list generator element %d must be a mapping: %v", i, err)))
				continue
			}
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "list",
		})
	}
	if strings.TrimSpace(list.ElementsYaml) == "" {
		return out, diags
	}
	var elements []any
	if err := yaml.Unmarshal([]byte(list.ElementsYaml), &elements); err != nil {
		diags = append(diags, appsetDiagnostic(manifestPath, fmt.Sprintf("list generator elementsYaml is not valid YAML: %v", err)))
		return out, diags
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
	return out, diags
}
