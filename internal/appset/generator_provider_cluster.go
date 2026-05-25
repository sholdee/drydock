package appset

import (
	"fmt"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"k8s.io/apimachinery/pkg/labels"
)

func evaluateClustersGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Clusters.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := clusterGeneratorParamSets(ctx.ManifestPath, generator.Clusters, ctx.Options.Provider.Data.Clusters, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "clusters", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func clusterGeneratorParamSets(manifestPath string, clusters *argoappv1.ClusterGenerator, inputs []ClusterInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusters")}, nil
	}

	selector, err := appsetutils.LabelSelectorAsSelector(&clusters.Selector)
	if err != nil {
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("clusters selector: %v", err))}, nil
	}

	var out []generatorParamSet
	for _, cluster := range inputs {
		if !selector.Matches(labels.Set(cluster.Labels)) {
			continue
		}
		params, err := clusterParams(cluster, clusters.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "clusters",
		})
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusters")}, nil
	}
	if clusters.FlatList {
		clusterList := make([]any, 0, len(out))
		for _, paramSet := range out {
			clusterList = append(clusterList, paramSet.Params)
		}
		return []generatorParamSet{{
			Params:    map[string]any{"clusters": clusterList},
			Generator: "clusters",
		}}, nil, nil
	}
	return out, nil, nil
}

func clusterParams(cluster ClusterInput, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	project := cluster.Project
	params := map[string]any{
		"name":           cluster.Name,
		"nameNormalized": appsetutils.SanitizeName(cluster.Name),
		"server":         cluster.Server,
		"project":        project,
	}

	if useGoTemplate {
		params["metadata"] = map[string]any{
			"labels":      stringMapAny(cluster.Labels),
			"annotations": stringMapAny(cluster.Annotations),
		}
	} else {
		for key, value := range cluster.Labels {
			params["metadata.labels."+key] = value
		}
		for key, value := range cluster.Annotations {
			params["metadata.annotations."+key] = value
		}
	}

	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}
