package appset

import (
	"maps"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func evaluatePluginGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Plugin.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := pluginParamSets(ctx.ManifestPath, generator.Plugin, ctx.Options.Provider.Data.Plugins, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "plugin", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func pluginParamSets(manifestPath string, generator *argoappv1.PluginGenerator, inputs []PluginInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "plugin")}, nil
	}

	configMapName := generator.ConfigMapRef.Name
	matched := false
	var out []generatorParamSet
	for _, input := range inputs {
		if input.ConfigMapRef != configMapName {
			continue
		}
		matched = true
		for _, output := range input.Outputs {
			outputMap, ok := output.(map[string]any)
			if !ok || outputMap == nil {
				return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, "plugin: fixture output must be a mapping object")}, nil
			}
			params, err := pluginParams(outputMap, generator.Input.Parameters, generator.Values, useGoTemplate, goTemplateOptions)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, generatorParamSet{
				Params:    params,
				Generator: "plugin",
			})
		}
	}
	if len(out) == 0 {
		if matched {
			return nil, nil, nil
		}
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "plugin")}, nil
	}
	return out, nil, nil
}

func pluginParams(output map[string]any, inputParameters argoappv1.PluginParameters, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	params := map[string]any{}
	if useGoTemplate {
		maps.Copy(params, output)
	} else {
		flattenParams("", output, params)
	}
	params["generator"] = map[string]any{
		"input": map[string]argoappv1.PluginParameters{
			"parameters": inputParameters,
		},
	}
	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}
