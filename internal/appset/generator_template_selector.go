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
		if selector.Matches(labels.Set(selectorLabels(paramSet.Params))) {
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

// selectorLabels flattens generator params into the label set that a
// generator selector matches, as Argo CD v3.6.1 flattenParameters does
// (applicationset/generators/generator_spec_processor.go:126-160).
//
// Argo CD flattens with github.com/jeremywohl/flatten, which only walks
// map[string]any and []any. Before that, normalizeMapForFlatten turns each
// map[string]string into a map[string]any, but only where the map sits in
// the top-level params or in nested map[string]any values, not inside a
// []any. Every other value is one leaf, stringified with fmt "%v". So a
// []string such as path.segments or pull request labels is one key whose
// value looks like "[apps alpha]", and a []map[string]any such as the
// cluster flatList is one key too.
func selectorLabels(params map[string]any) map[string]string {
	out := map[string]string{}
	flattenSelectorContainer(true, "", normalizeSelectorParams(params), out)
	return out
}

// normalizeSelectorParams mirrors normalizeMapForFlatten
// (generator_spec_processor.go:146-160).
func normalizeSelectorParams(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		switch typed := value.(type) {
		case map[string]string:
			inner := make(map[string]any, len(typed))
			for innerKey, innerValue := range typed {
				inner[innerKey] = innerValue
			}
			out[key] = inner
		case map[string]any:
			out[key] = normalizeSelectorParams(typed)
		default:
			out[key] = value
		}
	}
	return out
}

// flattenSelectorContainer mirrors flatten and enkey in
// github.com/jeremywohl/flatten (flatten.go:88-127) with DotStyle: a
// top-level key is used as is, and a nested key is prefix + "." + key.
func flattenSelectorContainer(top bool, prefix string, nested any, out map[string]string) {
	switch typed := nested.(type) {
	case map[string]any:
		for key, value := range typed {
			flattenSelectorValue(selectorLabelKey(top, prefix, key), value, out)
		}
	case []any:
		for i, value := range typed {
			flattenSelectorValue(selectorLabelKey(top, prefix, strconv.Itoa(i)), value, out)
		}
	}
}

func flattenSelectorValue(key string, value any, out map[string]string) {
	switch value.(type) {
	case map[string]any, []any:
		flattenSelectorContainer(false, key, value, out)
	default:
		// flattenParameters stringifies each leaf with fmt.Sprintf("%v").
		out[key] = fmt.Sprintf("%v", value)
	}
}

func selectorLabelKey(top bool, prefix, key string) string {
	if top {
		return prefix + key
	}
	return prefix + "." + key
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
