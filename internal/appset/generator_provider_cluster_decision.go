package appset

import (
	"errors"
	"fmt"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	paramSets, diags, err := clusterDecisionResourceParamSets(ctx.ManifestPath, generator.ClusterDecisionResource, ctx.Options.Provider.Data, ctx.AppSet.Spec.GoTemplate)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "clusterDecisionResource", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func clusterDecisionResourceParamSets(manifestPath string, generator *argoappv1.DuckTypeGenerator, data ProviderData, useGoTemplate bool) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if err := validateClusterDecisionResourceGenerator(generator); err != nil {
		return nil, nil, &generatorParamsError{err: err}
	}
	if len(data.ClusterDecisions) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}

	clustersByName := clusterInputsByName(data.Clusters)
	out, matchedResource, diag, err := clusterDecisionResourceInputParamSets(manifestPath, generator, data.ClusterDecisions, clustersByName, useGoTemplate)
	if diag != nil {
		return nil, []diagnostic.Diagnostic{*diag}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !matchedResource {
		// Argo CD v3.6.1 fails the ApplicationSet when it lists no
		// decision resource (applicationset/generators/duck_type.go:129-132).
		// The fixture supplies decision resources, so none of them is the
		// one this generator names or selects.
		return nil, nil, &generatorParamsError{err: errors.New("no clusterDecisionResources found")}
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}
	return out, nil, nil
}

// validateClusterDecisionResourceGenerator mirrors the definition check of
// the Argo CD v3.6.1 clusterDecisionResource generator
// (applicationset/generators/duck_type.go:99-104): exactly one of name and
// labelSelector, where a labelSelector counts as set when matchLabels or
// matchExpressions is present, even empty. Argo CD then lists the decision
// resources with the selector. One that does not parse makes the list call
// fail (duck_type.go:113-127).
func validateClusterDecisionResourceGenerator(generator *argoappv1.DuckTypeGenerator) error {
	hasLabelSelector := generator.LabelSelector.MatchLabels != nil || generator.LabelSelector.MatchExpressions != nil
	if (generator.Name == "" && !hasLabelSelector) || (generator.Name != "" && hasLabelSelector) {
		return errors.New("there is a problem with the definition of the ClusterDecisionResource generator")
	}
	if generator.Name == "" {
		if _, err := metav1.LabelSelectorAsSelector(&generator.LabelSelector); err != nil {
			return fmt.Errorf("failed to get dynamic resources: %w", err)
		}
	}
	return nil
}

func clusterInputsByName(clusters []ClusterInput) map[string]ClusterInput {
	clustersByName := map[string]ClusterInput{}
	for _, cluster := range clusters {
		clustersByName[cluster.Name] = cluster
	}
	return clustersByName
}

func clusterDecisionResourceInputParamSets(manifestPath string, generator *argoappv1.DuckTypeGenerator, inputs []ClusterDecisionInput, clustersByName map[string]ClusterInput, useGoTemplate bool) ([]generatorParamSet, bool, *diagnostic.Diagnostic, error) {
	var out []generatorParamSet
	matchedResource := false
	for _, input := range inputs {
		matched, err := clusterDecisionResourceMatches(generator, input)
		if err != nil {
			diag := providerUnsupportedFilterDiagnostic(manifestPath, err.Error())
			return nil, false, &diag, nil
		}
		if !matched {
			continue
		}
		matchedResource = true
		paramSets, err := clusterDecisionResourceDecisionParamSets(generator, input, clustersByName, useGoTemplate)
		if err != nil {
			return nil, true, nil, err
		}
		out = append(out, paramSets...)
	}
	return out, matchedResource, nil, nil
}

func clusterDecisionResourceDecisionParamSets(generator *argoappv1.DuckTypeGenerator, input ClusterDecisionInput, clustersByName map[string]ClusterInput, useGoTemplate bool) ([]generatorParamSet, error) {
	statusListKey := clusterDecisionResourceStatusListKey(input)
	if statusListKey == "" || input.MatchKey == "" {
		return nil, nil
	}
	out := make([]generatorParamSet, 0, len(input.Decisions))
	for _, decision := range input.Decisions {
		paramSet, ok, err := clusterDecisionResourceDecisionParamSet(generator, input.MatchKey, decision, clustersByName, useGoTemplate)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, paramSet)
		}
	}
	return out, nil
}

func clusterDecisionResourceStatusListKey(input ClusterDecisionInput) string {
	if input.StatusListKey == "" {
		return defaultClusterDecisionStatusListKey
	}
	return input.StatusListKey
}

func clusterDecisionResourceDecisionParamSet(generator *argoappv1.DuckTypeGenerator, matchKey string, decision map[string]any, clustersByName map[string]ClusterInput, useGoTemplate bool) (generatorParamSet, bool, error) {
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
	if err := collectClusterDecisionValues(params, generator.Values, useGoTemplate); err != nil {
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

// collectClusterDecisionValues copies the generator values into params as
// Argo CD v3.6.1 does (applicationset/generators/duck_type.go:170-172 and
// collectParams at :222-231). Unlike every other generator, Argo CD does not
// render clusterDecisionResource values as templates: each value is the raw
// string from the ApplicationSet, so '{{region}}' stays literal and a broken
// template in a value is not an error.
//
// In Go-template mode, a decision field named "values" is already a string
// param when collectParams runs, so its map[string]string type assertion
// panics and Argo CD generates no Applications. drydock reports the same
// outcome as a failed ApplicationSet instead of panicking.
func collectClusterDecisionValues(params map[string]any, values map[string]string, useGoTemplate bool) error {
	for key, value := range values {
		if !useGoTemplate {
			params["values."+key] = value
			continue
		}
		if params["values"] == nil {
			params["values"] = map[string]string{}
		}
		collected, ok := params["values"].(map[string]string)
		if !ok {
			return &generatorTemplateError{
				err:           errors.New(`clusterDecisionResource decision field "values" conflicts with generator values; Argo CD v3.6.1 panics on this ApplicationSet (applicationset/generators/duck_type.go:227)`),
				upstreamPanic: true,
			}
		}
		collected[key] = value
	}
	return nil
}
