package appset

import (
	"encoding/json"
	"fmt"
	"strconv"

	"dario.cat/mergo"
	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func setGeneratorTemplate(paramSets []generatorParamSet, template argoappv1.ApplicationSetTemplate) []generatorParamSet {
	for i := range paramSets {
		paramSets[i].Template = template
	}
	return paramSets
}

func mergeGeneratorTemplate(base, override argoappv1.ApplicationSetTemplate) (argoappv1.ApplicationSetTemplate, error) {
	dest := override.DeepCopy()
	if err := mergo.Merge(dest, base); err != nil {
		return argoappv1.ApplicationSetTemplate{}, err
	}
	return *dest, nil
}

func applyGeneratorSelector(manifestPath string, selectorSpec *metav1.LabelSelector, paramSets []generatorParamSet) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if selectorSpec == nil {
		return paramSets, nil, nil
	}
	selector, err := appsetutils.LabelSelectorAsSelector(selectorSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("parse generator selector: %w", err)
	}
	filtered := make([]generatorParamSet, 0, len(paramSets))
	for _, paramSet := range paramSets {
		flat := map[string]string{}
		flattenSelectorParams("", paramSet.Params, flat)
		if selector.Matches(labels.Set(flat)) {
			filtered = append(filtered, paramSet)
		}
	}
	return filtered, nil, nil
}

func applyProviderGeneratorSelector(manifestPath, kind string, selectorSpec *metav1.LabelSelector, paramSets []generatorParamSet) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	filtered, diags, err := applyGeneratorSelector(manifestPath, selectorSpec, paramSets)
	if err != nil {
		diags = append(diags, providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("%s selector: %v", kind, err)))
		return nil, diags, nil
	}
	if selectorSpec != nil && len(paramSets) > 0 && len(filtered) == 0 {
		diags = append(diags, providerNoMatchDiagnostic(manifestPath, kind))
	}
	return filtered, diags, nil
}

func nestedGeneratorsContainProvider(nestedGenerators []argoappv1.ApplicationSetNestedGenerator) bool {
	for _, nested := range nestedGenerators {
		generator, err := generatorFromNested(nested)
		if err != nil {
			continue
		}
		if generatorContainsProvider(generator) {
			return true
		}
	}
	return false
}

func generatorContainsProvider(generator argoappv1.ApplicationSetGenerator) bool {
	switch {
	case generator.Clusters != nil ||
		generator.ClusterDecisionResource != nil ||
		generator.SCMProvider != nil ||
		generator.PullRequest != nil ||
		generator.Plugin != nil:
		return true
	case generator.Matrix != nil:
		return nestedGeneratorsContainProvider(generator.Matrix.Generators)
	case generator.Merge != nil:
		return nestedGeneratorsContainProvider(generator.Merge.Generators)
	default:
		return false
	}
}

func flattenSelectorParams(prefix string, value any, out map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			flatKey := key
			if prefix != "" {
				flatKey = prefix + "." + key
			}
			flattenSelectorParams(flatKey, nested, out)
		}
	case map[string]string:
		for key, nested := range typed {
			flatKey := key
			if prefix != "" {
				flatKey = prefix + "." + key
			}
			out[flatKey] = nested
		}
	case []any:
		for i, nested := range typed {
			flattenSelectorParams(prefix+"."+strconv.Itoa(i), nested, out)
		}
	case []string:
		for i, nested := range typed {
			out[prefix+"."+strconv.Itoa(i)] = nested
		}
	default:
		if prefix != "" {
			out[prefix] = fmt.Sprint(typed)
		}
	}
}

func cloneTemplateApp(app argoappv1.Application) (argoappv1.Application, error) {
	data, err := json.Marshal(app)
	if err != nil {
		return argoappv1.Application{}, err
	}
	var out argoappv1.Application
	if err := json.Unmarshal(data, &out); err != nil {
		return argoappv1.Application{}, err
	}
	return out, nil
}
