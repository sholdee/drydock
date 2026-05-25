package appset

import (
	"errors"
	"fmt"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"k8s.io/apimachinery/pkg/labels"
)

func evaluateClusterDecisionResourceGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.ClusterDecisionResource.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := clusterDecisionResourceParamSets(ctx.ManifestPath, generator.ClusterDecisionResource, ctx.Options.Provider.Data, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "clusterDecisionResource", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func clusterDecisionResourceParamSets(manifestPath string, generator *argoappv1.DuckTypeGenerator, data ProviderData, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(data.ClusterDecisions) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}
	if err := validateClusterDecisionResourceGenerator(generator); err != nil {
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, err.Error())}, nil
	}

	clustersByName := clusterInputsByName(data.Clusters)
	out, diag, err := clusterDecisionResourceInputParamSets(manifestPath, generator, data.ClusterDecisions, clustersByName, useGoTemplate, goTemplateOptions)
	if diag != nil {
		return nil, []diagnostic.Diagnostic{*diag}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}
	return out, nil, nil
}

func validateClusterDecisionResourceGenerator(generator *argoappv1.DuckTypeGenerator) error {
	hasLabelSelector := len(generator.LabelSelector.MatchLabels) > 0 || len(generator.LabelSelector.MatchExpressions) > 0
	switch {
	case generator.Name == "" && !hasLabelSelector:
		return errors.New("clusterDecisionResource must set exactly one of name or labelSelector with provider fixtures")
	case generator.Name != "" && hasLabelSelector:
		return errors.New("clusterDecisionResource cannot combine name and labelSelector with provider fixtures")
	default:
		return nil
	}
}

func clusterInputsByName(clusters []ClusterInput) map[string]ClusterInput {
	clustersByName := map[string]ClusterInput{}
	for _, cluster := range clusters {
		clustersByName[cluster.Name] = cluster
	}
	return clustersByName
}

func clusterDecisionResourceInputParamSets(manifestPath string, generator *argoappv1.DuckTypeGenerator, inputs []ClusterDecisionInput, clustersByName map[string]ClusterInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, *diagnostic.Diagnostic, error) {
	var out []generatorParamSet
	for _, input := range inputs {
		matched, err := clusterDecisionResourceMatches(generator, input)
		if err != nil {
			diag := providerUnsupportedFilterDiagnostic(manifestPath, err.Error())
			return nil, &diag, nil
		}
		if !matched {
			continue
		}
		paramSets, err := clusterDecisionResourceDecisionParamSets(generator, input, clustersByName, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, paramSets...)
	}
	return out, nil, nil
}

func clusterDecisionResourceDecisionParamSets(generator *argoappv1.DuckTypeGenerator, input ClusterDecisionInput, clustersByName map[string]ClusterInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, error) {
	if input.StatusListKey != defaultClusterDecisionStatusListKey || input.MatchKey == "" {
		return nil, nil
	}
	out := make([]generatorParamSet, 0, len(input.Decisions))
	for _, decision := range input.Decisions {
		paramSet, ok, err := clusterDecisionResourceDecisionParamSet(generator, input.MatchKey, decision, clustersByName, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, paramSet)
		}
	}
	return out, nil
}

func clusterDecisionResourceDecisionParamSet(generator *argoappv1.DuckTypeGenerator, matchKey string, decision map[string]any, clustersByName map[string]ClusterInput, useGoTemplate bool, goTemplateOptions []string) (generatorParamSet, bool, error) {
	matchValue, ok := decision[matchKey]
	if !ok || fmt.Sprint(matchValue) == "" {
		return generatorParamSet{}, false, nil
	}
	cluster, ok := clustersByName[fmt.Sprint(matchValue)]
	if !ok {
		return generatorParamSet{}, false, nil
	}
	params := map[string]any{
		"name":   cluster.Name,
		"server": cluster.Server,
	}
	for key, value := range decision {
		params[key] = fmt.Sprint(value)
	}
	if err := appendTemplatedValues(params, generator.Values, useGoTemplate, goTemplateOptions); err != nil {
		return generatorParamSet{}, false, err
	}
	return generatorParamSet{
		Params:    params,
		Generator: "clusterDecisionResource",
	}, true, nil
}

func clusterDecisionResourceMatches(generator *argoappv1.DuckTypeGenerator, input ClusterDecisionInput) (bool, error) {
	if generator.ConfigMapRef != input.ConfigMapRef {
		return false, nil
	}
	if generator.Name != "" && generator.Name != input.ResourceName {
		return false, nil
	}
	if len(generator.LabelSelector.MatchLabels) == 0 && len(generator.LabelSelector.MatchExpressions) == 0 {
		return true, nil
	}
	selector, err := appsetutils.LabelSelectorAsSelector(&generator.LabelSelector)
	if err != nil {
		return false, fmt.Errorf("clusterDecisionResource labelSelector: %w", err)
	}
	return selector.Matches(labels.Set(input.Labels)), nil
}
