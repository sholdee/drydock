package appset

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

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

	// Argo CD v3.6.1 wraps the child errors (matrix.go:55-58 and :80-82).
	first, firstDiags, supported, err := evaluateNestedGenerator(ctx, matrix.Generators[0], nil)
	if err != nil {
		return nil, firstDiags, true, wrapGeneratorError(err, "error failed to get params for first generator in matrix generator: %w")
	}
	if !supported {
		return nil, firstDiags, false, nil
	}

	var out []generatorParamSet
	var diags []diagnostic.Diagnostic
	diags = append(diags, firstDiags...)
	for _, firstSet := range first {
		second, secondDiags, supported, err := evaluateNestedGenerator(ctx, matrix.Generators[1], firstSet.Params)
		diags = append(diags, secondDiags...)
		if err != nil {
			return nil, diags, true, wrapGeneratorError(err, "failed to get params for second generator in the matrix generator: %w")
		}
		if !supported {
			return nil, diags, false, nil
		}
		for _, secondSet := range second {
			params, err := combineMatrixParams(ctx.AppSet.Name, firstSet.Params, secondSet.Params, ctx.AppSet.Spec.GoTemplate)
			if err != nil {
				return nil, diags, true, paramsError(err)
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

func combineMatrixParams(appSetName string, first, second map[string]any, useGoTemplate bool) (map[string]any, error) {
	if useGoTemplate {
		out := cloneParams(second)
		if err := mergeParamsWithOverride(appSetName, "matrix", "applicationset/generators/matrix.go:95", &out, cloneParams(first)); err != nil {
			return nil, err
		}
		return out, nil
	}
	out, err := appsetutils.CombineStringMaps(first, second)
	if err != nil {
		return nil, fmt.Errorf("failed to combine string maps with merging params for the matrix generator: %w", err)
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

// cloneParamValue copies the param shapes that generators produce, so a
// merge never writes into another param set. Generators keep Argo CD's Go
// types, such as map[string]string for cluster metadata and values, so the
// copy keeps them too.
func cloneParamValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if typed == nil {
			return typed
		}
		return cloneParams(typed)
	case map[string]string:
		return maps.Clone(typed)
	case []any:
		// Clone keeps a nil list nil, as Argo CD v3.6.1 deepCopyParamValue
		// does (applicationset/generators/matrix.go:205-212). A nil list
		// and an empty one print differently with toJson.
		out := slices.Clone(typed)
		for i, value := range out {
			out[i] = cloneParamValue(value)
		}
		return out
	case []map[string]any:
		if typed == nil {
			return typed
		}
		out := make([]map[string]any, len(typed))
		for i, value := range typed {
			out[i] = cloneParams(value)
		}
		return out
	case []string:
		return slices.Clone(typed)
	default:
		return typed
	}
}

func mergeGeneratorParamSets(ctx generatorContext, merge *argoappv1.MergeGenerator, selector *metav1.LabelSelector) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if len(merge.Generators) < 2 {
		return nil, nil, true, &generatorParamsError{err: errors.New("found less than two generators, Merge requires two or more")}
	}

	allSets, allDiags, supported, err := mergeGeneratorChildSets(ctx, merge)
	if err != nil {
		return nil, allDiags, true, wrapGeneratorError(err, "error getting param sets from generators: %w")
	}
	if !supported {
		return nil, allDiags, false, nil
	}
	// Argo CD v3.6.1 checks the merge keys after it ran every child
	// (merge.go:62-76 and :106-109).
	if len(merge.MergeKeys) == 0 {
		return nil, allDiags, true, &generatorParamsError{err: errors.New("error getting param sets by merge key: no merge keys were specified, Merge requires at least one")}
	}
	out, baseByKey, err := baseMergeParamSets(allSets[0], merge.MergeKeys)
	if err != nil {
		return nil, allDiags, true, paramsError(fmt.Errorf("error getting param sets by merge key: %w", err))
	}
	for _, paramSets := range allSets[1:] {
		if err := applyMergeParamSets(ctx.AppSet.Name, out, baseByKey, paramSets, merge.MergeKeys, ctx.AppSet.Spec.GoTemplate); err != nil {
			return nil, allDiags, true, paramsError(err)
		}
	}

	var selectorDiags []diagnostic.Diagnostic
	out, selectorDiags, err = applyMergeGeneratorSelector(ctx, merge, selector, out)
	allDiags = append(allDiags, selectorDiags...)
	return out, allDiags, true, err
}

// mergeGeneratorChildSets runs every merge child in order, as Argo CD
// v3.6.1 getParamSetsForAllGenerators does (merge.go:39-50), and returns
// the first error with its wrap. A child that drydock cannot evaluate does
// not stop the later ones, so that an error in a later child still fails
// the ApplicationSet.
func mergeGeneratorChildSets(ctx generatorContext, merge *argoappv1.MergeGenerator) ([][]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	allSets := make([][]generatorParamSet, 0, len(merge.Generators))
	var allDiags []diagnostic.Diagnostic
	allSupported := true
	for i, generator := range merge.Generators {
		paramSets, diags, supported, err := evaluateNestedGenerator(ctx, generator, nil)
		allDiags = append(allDiags, diags...)
		if err != nil {
			return nil, allDiags, true, wrapGeneratorError(err, fmt.Sprintf("error getting params from generator %d of %d: %%w", i+1, len(merge.Generators)))
		}
		if !supported {
			allSupported = false
			continue
		}
		allSets = append(allSets, paramSets)
	}
	if !allSupported {
		return nil, allDiags, false, nil
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
			return nil, nil, fmt.Errorf("the parameters from a generator were not unique by the given mergeKeys, Merge requires all param sets to be unique. Duplicate key was %s", key)
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

func applyMergeParamSets(appSetName string, out []generatorParamSet, baseByKey map[string]int, paramSets []generatorParamSet, mergeKeys []string, useGoTemplate bool) error {
	seen := map[string]struct{}{}
	for _, paramSet := range paramSets {
		if err := applyMergeParamSet(appSetName, out, baseByKey, seen, paramSet, mergeKeys, useGoTemplate); err != nil {
			return err
		}
	}
	return nil
}

func applyMergeParamSet(appSetName string, out []generatorParamSet, baseByKey map[string]int, seen map[string]struct{}, paramSet generatorParamSet, mergeKeys []string, useGoTemplate bool) error {
	key, err := mergeParamSetKey(paramSet.Params, mergeKeys)
	if err != nil {
		return err
	}
	if _, exists := seen[key]; exists {
		return fmt.Errorf("error getting param sets by merge key: the parameters from a generator were not unique by the given mergeKeys, Merge requires all param sets to be unique. Duplicate key was %s", key)
	}
	seen[key] = struct{}{}
	baseIndex, exists := baseByKey[key]
	if !exists {
		return nil
	}
	merged, err := mergeParams(appSetName, out[baseIndex].Params, paramSet.Params, useGoTemplate)
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

// validateMatrixGenerator mirrors the child count check of the Argo CD
// v3.6.1 matrix generator (applicationset/generators/matrix.go:45-51).
func validateMatrixGenerator(matrix *argoappv1.MatrixGenerator) error {
	switch {
	case len(matrix.Generators) < 2:
		return &generatorParamsError{err: errors.New("found less than two generators, Matrix support only two")}
	case len(matrix.Generators) > 2:
		return &generatorParamsError{err: errors.New("found more than two generators, Matrix support only two")}
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

func mergeParams(appSetName string, base, override map[string]any, useGoTemplate bool) (map[string]any, error) {
	out := cloneParams(base)
	if useGoTemplate {
		if err := mergeParamsWithOverride(appSetName, "merge", "applicationset/generators/merge.go:81", &out, cloneParams(override)); err != nil {
			return nil, err
		}
		return out, nil
	}
	maps.Copy(out, override)
	return out, nil
}

// mergeParamsWithOverride runs the Go-template param merge of the Argo CD
// v3.6.1 matrix and merge generators: mergo.Merge with mergo.WithOverride
// (applicationset/generators/matrix.go:95 and merge.go:81).
//
// mergo writes map entries through reflect. Generators keep Argo CD's Go
// types, so two child generators can set the same key with different map
// types. For example, cluster generator values are a map[string]string,
// and a list element field named "values" is a map[string]any. When the
// merge writes a map[string]any entry into a map[string]string, reflect
// panics. Argo CD panics in the same call. Its ApplicationSet controller
// recovers the panic in Reconcile
// (applicationset/controllers/applicationset_controller.go:123-133) and
// returns before it generates Applications or sets a status condition, so
// the ApplicationSet creates no Applications.
//
// drydock recovers only this merge call and returns a
// generatorTemplateError. GenerateWithOptions then warns
// appset.template-render-failed, and only this ApplicationSet produces no
// Applications.
func mergeParamsWithOverride(appSetName, generatorName, upstreamCite string, dst *map[string]any, src map[string]any) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &generatorTemplateError{
				err: fmt.Errorf(
					"ApplicationSet %s: the %s generator cannot merge the params of its child generators: %v; this happens when two child generators set the same key with different Go types, such as a list element field named \"values\" and generator values; Argo CD v3.6.1 panics in the same merge (%s)",
					appSetName, generatorName, recovered, upstreamCite),
				upstreamPanic: true,
			}
		}
	}()
	if err := mergo.Merge(dst, src, mergo.WithOverride); err != nil {
		return fmt.Errorf("merge %s params: %w", generatorName, err)
	}
	return nil
}
