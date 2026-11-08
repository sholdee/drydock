package appset

import (
	"fmt"
	"maps"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argocommon "github.com/argoproj/argo-cd/v3/common"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func evaluateClustersGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if err := clusterGeneratorSpecError(generator.Clusters); err != nil {
		return nil, nil, true, err
	}
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

// clusterGeneratorSpecError mirrors the selector check of the Argo CD v3.6.1
// cluster generator. It lists the cluster Secrets with
// metav1.LabelSelectorAsSelector, which also checks label values, and fails
// the ApplicationSet when the selector does not parse
// (applicationset/generators/cluster.go:62-66 and :194-198). The check
// needs only the spec, so it runs also without provider fixtures.
func clusterGeneratorSpecError(clusters *argoappv1.ClusterGenerator) error {
	selector := metav1.AddLabelToSelector(clusters.Selector.DeepCopy(), argocommon.LabelKeySecretType, argocommon.LabelValueSecretTypeCluster)
	if _, err := metav1.LabelSelectorAsSelector(selector); err != nil {
		return &generatorParamsError{err: fmt.Errorf("error getting cluster secrets: error converting label selector: %w", err)}
	}
	return nil
}

func clusterGeneratorParamSets(manifestPath string, clusters *argoappv1.ClusterGenerator, inputs []ClusterInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if err := clusterGeneratorSpecError(clusters); err != nil {
		return nil, nil, err
	}
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusters")}, nil
	}

	selector, err := appsetutils.LabelSelectorAsSelector(&clusters.Selector)
	if err != nil {
		return nil, nil, &generatorParamsError{err: fmt.Errorf("error getting cluster secrets: error converting label selector: %w", err)}
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
		// Argo CD v3.6.1 puts the clusters under one key as a
		// []map[string]any (applicationset/generators/cluster.go:119-126).
		// Keep that type.
		clusterList := make([]map[string]any, 0, len(out))
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

	annotations := clusterAnnotationParams(cluster.Annotations)
	if useGoTemplate {
		meta := map[string]any{}
		// Argo CD v3.6.1 passes both maps to the template as
		// map[string]string, not map[string]any
		// (applicationset/generators/cluster.go:172 and :175). Keep that
		// type. Sprig map functions such as hasKey, keys, pick, omit,
		// and values take map[string]any, so they fail on these maps in
		// Argo CD and must fail here too.
		//
		// Argo CD gates the annotations key on the unfiltered map, so the key
		// exists whenever the cluster has annotations, even if all of them
		// are excluded.
		if len(cluster.Annotations) > 0 {
			meta["annotations"] = annotations
		}
		// Argo CD gates labels the same way, but its cluster generator only
		// lists Secrets selected by argocd.argoproj.io/secret-type=cluster,
		// so every cluster it sees has labels and the key is always present.
		// Fixture clusters do not carry that implicit label, so always expose
		// the key (an empty map when the fixture has no labels). The copy
		// keeps generator merges from writing into the fixture data.
		labels := make(map[string]string, len(cluster.Labels))
		maps.Copy(labels, cluster.Labels)
		meta["labels"] = labels
		params["metadata"] = meta
	} else {
		for key, value := range annotations {
			params["metadata.annotations."+key] = value
		}
		for key, value := range cluster.Labels {
			params["metadata.labels."+key] = value
		}
	}

	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

// excludedClusterAnnotations mirrors Argo CD v3.6.1 excludedClusterAnnotations
// (applicationset/generators/cluster.go): annotations Argo CD or kubectl write
// onto cluster Secrets as their own state, or one-shot Application triggers,
// are not exposed as cluster generator parameters.
var excludedClusterAnnotations = map[string]bool{
	argoappv1.AnnotationKeyRefresh:          true,
	argoappv1.AnnotationKeyRefreshTimestamp: true,
	argoappv1.AnnotationKeyHydrate:          true,
	argoappv1.AnnotationKeyHydrateTimestamp: true,
	argocommon.AnnotationKeyAppInstance:     true,
	argocommon.AnnotationInstallationID:     true,
	corev1.LastAppliedConfigAnnotation:      true,
}

func clusterAnnotationParams(annotations map[string]string) map[string]string {
	params := make(map[string]string, len(annotations))
	for key, value := range annotations {
		if !excludedClusterAnnotations[key] {
			params[key] = value
		}
	}
	return params
}
