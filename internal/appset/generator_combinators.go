package appset

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"dario.cat/mergo"
	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func evaluateMatrixGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Matrix.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, supported, err := matrixGeneratorParamSets(ctx, generator.Matrix, generator.Selector)
	if err != nil || !supported {
		return paramSets, diags, supported, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	return paramSets, diags, true, nil
}

func evaluateMergeGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Merge.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, supported, err := mergeGeneratorParamSets(ctx, generator.Merge, generator.Selector)
	if err != nil || !supported {
		return paramSets, diags, supported, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	return paramSets, diags, true, nil
}

func matrixGeneratorParamSets(ctx generatorContext, matrix *argoappv1.MatrixGenerator, selector *metav1.LabelSelector) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if err := validateMatrixGenerator(matrix); err != nil {
		return nil, nil, true, err
	}
	supported, err := supportedNestedGeneratorTrees(matrix.Generators, ctx.Options.Provider.Supplied())
	if err != nil {
		return nil, nil, true, err
	}
	if !supported {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}

	first, firstDiags, supported, err := evaluateNestedGenerator(ctx, matrix.Generators[0], nil)
	if err != nil || !supported {
		return nil, firstDiags, supported, err
	}

	var out []generatorParamSet
	var diags []diagnostic.Diagnostic
	diags = append(diags, firstDiags...)
	for _, firstSet := range first {
		second, secondDiags, supported, err := evaluateNestedGenerator(ctx, matrix.Generators[1], firstSet.Params)
		diags = append(diags, secondDiags...)
		if err != nil || !supported {
			return nil, diags, supported, err
		}
		for _, secondSet := range second {
			params, err := combineMatrixParams(firstSet.Params, secondSet.Params, ctx.AppSet.Spec.GoTemplate)
			if err != nil {
				return nil, diags, true, err
			}
			out = append(out, generatorParamSet{
				Params:      params,
				SourcePath:  primarySourcePath(mergeSourcePaths(firstSet, secondSet)),
				SourcePaths: mergeSourcePaths(firstSet, secondSet),
				Generator:   "matrix",
			})
		}
	}
	var selectorDiags []diagnostic.Diagnostic
	if ctx.Options.Provider.Supplied() && nestedGeneratorsContainProvider(matrix.Generators) {
		out, selectorDiags, err = applyProviderGeneratorSelector(ctx.ManifestPath, "matrix", selector, out)
	} else {
		out, selectorDiags, err = applyGeneratorSelector(ctx.ManifestPath, selector, out)
	}
	diags = append(diags, selectorDiags...)
	return out, diags, true, err
}

func combineMatrixParams(first, second map[string]any, useGoTemplate bool) (map[string]any, error) {
	if useGoTemplate {
		out := cloneParams(second)
		if err := mergo.Merge(&out, cloneParams(first), mergo.WithOverride); err != nil {
			return nil, fmt.Errorf("merge matrix params: %w", err)
		}
		return out, nil
	}
	out, err := appsetutils.CombineStringMaps(first, second)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func primarySourcePath(sourcePaths []string) string {
	if len(sourcePaths) == 0 {
		return ""
	}
	return sourcePaths[0]
}

func mergeSourcePaths(paramSets ...generatorParamSet) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, paramSet := range paramSets {
		paths := paramSet.SourcePaths
		if len(paths) == 0 && paramSet.SourcePath != "" {
			paths = []string{paramSet.SourcePath}
		}
		for _, sourcePath := range paths {
			if sourcePath == "" {
				continue
			}
			if _, ok := seen[sourcePath]; ok {
				continue
			}
			seen[sourcePath] = struct{}{}
			out = append(out, sourcePath)
		}
	}
	return out
}

func cloneParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for key, value := range params {
		out[key] = cloneParamValue(value)
	}
	return out
}

func cloneParamValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneParams(typed)
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			out[i] = cloneParamValue(value)
		}
		return out
	default:
		return typed
	}
}

func mergeGeneratorParamSets(ctx generatorContext, merge *argoappv1.MergeGenerator, selector *metav1.LabelSelector) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if err := validateMergeGenerator(merge); err != nil {
		return nil, nil, true, err
	}

	allSets, allDiags, supported, err := mergeGeneratorChildSets(ctx, merge)
	if err != nil || !supported {
		return nil, allDiags, supported, err
	}
	out, baseByKey, err := baseMergeParamSets(allSets[0], merge.MergeKeys)
	if err != nil {
		return nil, allDiags, true, err
	}
	for _, paramSets := range allSets[1:] {
		if err := applyMergeParamSets(out, baseByKey, paramSets, merge.MergeKeys, ctx.AppSet.Spec.GoTemplate); err != nil {
			return nil, allDiags, true, err
		}
	}

	var selectorDiags []diagnostic.Diagnostic
	out, selectorDiags, err = applyMergeGeneratorSelector(ctx, merge, selector, out)
	allDiags = append(allDiags, selectorDiags...)
	return out, allDiags, true, err
}

func mergeGeneratorChildSets(ctx generatorContext, merge *argoappv1.MergeGenerator) ([][]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	allSets := make([][]generatorParamSet, 0, len(merge.Generators))
	var allDiags []diagnostic.Diagnostic
	for _, generator := range merge.Generators {
		paramSets, diags, supported, err := evaluateNestedGenerator(ctx, generator, nil)
		allDiags = append(allDiags, diags...)
		if err != nil || !supported {
			return nil, allDiags, supported, err
		}
		allSets = append(allSets, paramSets)
	}
	return allSets, allDiags, true, nil
}

func baseMergeParamSets(paramSets []generatorParamSet, mergeKeys []string) ([]generatorParamSet, map[string]int, error) {
	baseByKey := map[string]int{}
	out := make([]generatorParamSet, len(paramSets))
	for i, paramSet := range paramSets {
		key, err := mergeParamSetKey(paramSet.Params, mergeKeys)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := baseByKey[key]; exists {
			return nil, nil, fmt.Errorf("parameters from a generator were not unique by merge keys; duplicate key was %s", key)
		}
		baseByKey[key] = i
		out[i] = generatorParamSet{
			Params:      cloneParams(paramSet.Params),
			SourcePath:  paramSet.SourcePath,
			SourcePaths: mergeSourcePaths(paramSet),
			Generator:   "merge",
		}
	}
	return out, baseByKey, nil
}

func applyMergeParamSets(out []generatorParamSet, baseByKey map[string]int, paramSets []generatorParamSet, mergeKeys []string, useGoTemplate bool) error {
	seen := map[string]struct{}{}
	for _, paramSet := range paramSets {
		if err := applyMergeParamSet(out, baseByKey, seen, paramSet, mergeKeys, useGoTemplate); err != nil {
			return err
		}
	}
	return nil
}

func applyMergeParamSet(out []generatorParamSet, baseByKey map[string]int, seen map[string]struct{}, paramSet generatorParamSet, mergeKeys []string, useGoTemplate bool) error {
	key, err := mergeParamSetKey(paramSet.Params, mergeKeys)
	if err != nil {
		return err
	}
	if _, exists := seen[key]; exists {
		return fmt.Errorf("parameters from a generator were not unique by merge keys; duplicate key was %s", key)
	}
	seen[key] = struct{}{}
	baseIndex, exists := baseByKey[key]
	if !exists {
		return nil
	}
	merged, err := mergeParams(out[baseIndex].Params, paramSet.Params, useGoTemplate)
	if err != nil {
		return err
	}
	out[baseIndex].Params = merged
	out[baseIndex].SourcePaths = mergeSourcePaths(out[baseIndex], paramSet)
	out[baseIndex].SourcePath = primarySourcePath(out[baseIndex].SourcePaths)
	return nil
}

func applyMergeGeneratorSelector(ctx generatorContext, merge *argoappv1.MergeGenerator, selector *metav1.LabelSelector, paramSets []generatorParamSet) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if ctx.Options.Provider.Supplied() && nestedGeneratorsContainProvider(merge.Generators) {
		return applyProviderGeneratorSelector(ctx.ManifestPath, "merge", selector, paramSets)
	}
	return applyGeneratorSelector(ctx.ManifestPath, selector, paramSets)
}

func validateMatrixGenerator(matrix *argoappv1.MatrixGenerator) error {
	if len(matrix.Generators) != 2 {
		return fmt.Errorf("matrix support only two child generators, found %d", len(matrix.Generators))
	}
	return nil
}

func validateMergeGenerator(merge *argoappv1.MergeGenerator) error {
	if len(merge.Generators) < 2 {
		return fmt.Errorf("merge requires two or more child generators, found %d", len(merge.Generators))
	}
	if len(merge.MergeKeys) == 0 {
		return errors.New("merge requires at least one merge key")
	}
	return nil
}

func mergeParamSetKey(params map[string]any, mergeKeys []string) (string, error) {
	key := make(map[string]any, len(mergeKeys))
	for _, mergeKey := range mergeKeys {
		key[mergeKey] = params[mergeKey]
	}
	data, err := json.Marshal(key)
	if err != nil {
		return "", fmt.Errorf("marshal merge key: %w", err)
	}
	return string(data), nil
}

func mergeParams(base, override map[string]any, useGoTemplate bool) (map[string]any, error) {
	out := cloneParams(base)
	if useGoTemplate {
		if err := mergo.Merge(&out, cloneParams(override), mergo.WithOverride); err != nil {
			return nil, fmt.Errorf("merge params: %w", err)
		}
		return out, nil
	}
	maps.Copy(out, override)
	return out, nil
}
