package appset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"dario.cat/mergo"
	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type GeneratedApplication struct {
	Application argoappv1.Application
	SourcePath  string
	SourcePaths []string
	Generator   string
}

var ErrUnsupportedGenerator = errors.New("unsupported ApplicationSet generator")

type Options struct {
	Provider ProviderOptions
}

func GenerateFromYAML(repoRoot, manifestPath string, data []byte) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	return GenerateFromYAMLWithOptions(repoRoot, manifestPath, data, Options{})
}

func GenerateFromYAMLWithOptions(repoRoot, manifestPath string, data []byte, options Options) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse ApplicationSet %s: %w", manifestPath, err)
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ApplicationSet %s: %w", manifestPath, err)
	}
	var appset argoappv1.ApplicationSet
	if err := json.Unmarshal(normalized, &appset); err != nil {
		return nil, nil, fmt.Errorf("parse ApplicationSet %s: %w", manifestPath, err)
	}
	return GenerateWithOptions(repoRoot, manifestPath, appset, options)
}

func Generate(repoRoot, manifestPath string, appset argoappv1.ApplicationSet) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	return GenerateWithOptions(repoRoot, manifestPath, appset, Options{})
}

func GenerateWithOptions(repoRoot, manifestPath string, appset argoappv1.ApplicationSet, options Options) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	var out []GeneratedApplication
	var diags []diagnostic.Diagnostic
	unsupportedCount := 0
	if len(appset.Spec.Generators) == 0 {
		diags := unsupportedGeneratorDiagnostic(manifestPath)
		return nil, diags, fmt.Errorf("%w in %s", ErrUnsupportedGenerator, manifestPath)
	}

	ctx := generatorContext{
		RepoRoot:     repoRoot,
		ManifestPath: manifestPath,
		AppSet:       appset,
		BaseTemplate: appset.Spec.Template,
		Options:      options,
	}
	for _, generator := range appset.Spec.Generators {
		paramSets, generatorDiags, supported, err := evaluateGenerator(ctx, generator)
		if err != nil {
			return out, append(diags, generatorDiags...), err
		}
		diags = append(diags, generatorDiags...)
		if !supported {
			unsupportedCount++
			continue
		}
		for _, paramSet := range paramSets {
			rendered, err := renderApplicationTemplateWithTemplate(appset, paramSet.Template, paramSet.Params)
			if err != nil {
				return out, diags, fmt.Errorf("%s render %s: %w", manifestPath, paramSet.SourcePath, err)
			}
			out = append(out, generatedApplication(appset, rendered, paramSet))
		}
	}
	if len(out) == 0 && unsupportedCount > 0 {
		return nil, diags, fmt.Errorf("%w in %s", ErrUnsupportedGenerator, manifestPath)
	}
	return out, diags, nil
}

type generatorParamSet struct {
	Params      map[string]any
	SourcePath  string
	SourcePaths []string
	Generator   string
	Template    argoappv1.ApplicationSetTemplate
}

type generatorContext struct {
	RepoRoot     string
	ManifestPath string
	AppSet       argoappv1.ApplicationSet
	BaseTemplate argoappv1.ApplicationSetTemplate
	Options      Options
}

func evaluateGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if generator.List != nil {
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

	if generator.Matrix != nil {
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

	if generator.Merge != nil {
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

	if generator.Clusters != nil {
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

	if generator.ClusterDecisionResource != nil {
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

	if generator.SCMProvider != nil {
		if !ctx.Options.Provider.Supplied() {
			return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
		}
		template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.SCMProvider.Template)
		if err != nil {
			return nil, nil, true, err
		}
		paramSets, diags, err := scmProviderParamSets(ctx.ManifestPath, generator.SCMProvider, ctx.Options.Provider.Data.SCMRepositories, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
		if err != nil {
			return nil, diags, true, err
		}
		paramSets = setGeneratorTemplate(paramSets, template)
		paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "scmProvider", generator.Selector, paramSets)
		diags = append(diags, selectorDiags...)
		return paramSets, diags, true, err
	}

	if generator.PullRequest != nil {
		if !ctx.Options.Provider.Supplied() {
			return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
		}
		template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.PullRequest.Template)
		if err != nil {
			return nil, nil, true, err
		}
		paramSets, diags, err := pullRequestParamSets(ctx.ManifestPath, generator.PullRequest, ctx.Options.Provider.Data.PullRequests, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
		if err != nil {
			return nil, diags, true, err
		}
		paramSets = setGeneratorTemplate(paramSets, template)
		paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "pullRequest", generator.Selector, paramSets)
		diags = append(diags, selectorDiags...)
		return paramSets, diags, true, err
	}

	if generator.Plugin != nil {
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

	if generator.Git == nil {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Git.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, supported, err := gitGeneratorParamSets(ctx.RepoRoot, ctx.ManifestPath, generator.Git, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil || !supported {
		return paramSets, diags, supported, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyGeneratorSelector(ctx.ManifestPath, generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, supported, err
}

func evaluateNestedGenerator(ctx generatorContext, nested argoappv1.ApplicationSetNestedGenerator, inheritedParams map[string]any) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	generator, err := generatorFromNested(nested)
	if err != nil {
		return nil, nil, true, err
	}
	supported, err := supportedGeneratorTree(generator, ctx.Options.Provider.Supplied())
	if err != nil {
		return nil, nil, true, err
	}
	if !supported {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	if len(inheritedParams) != 0 {
		rendered, err := (&appsetutils.Render{}).RenderGeneratorParams(&generator, inheritedParams, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
		if err != nil {
			return nil, nil, true, fmt.Errorf("interpolate nested generator: %w", err)
		}
		generator = *rendered
	}
	if generator.Merge != nil {
		return mergeGeneratorParamSets(ctx, generator.Merge, generator.Selector)
	}
	return evaluateGenerator(ctx, generator)
}

func supportedGeneratorTree(generator argoappv1.ApplicationSetGenerator, providerSupplied bool) (bool, error) {
	switch {
	case generator.List != nil || generator.Git != nil:
		return true, nil
	case generator.Clusters != nil || generator.ClusterDecisionResource != nil || generator.SCMProvider != nil || generator.PullRequest != nil || generator.Plugin != nil:
		return providerSupplied, nil
	case generator.Matrix != nil:
		if err := validateMatrixGenerator(generator.Matrix); err != nil {
			return true, err
		}
		return supportedNestedGeneratorTrees(generator.Matrix.Generators, providerSupplied)
	case generator.Merge != nil:
		if err := validateMergeGenerator(generator.Merge); err != nil {
			return true, err
		}
		return supportedNestedGeneratorTrees(generator.Merge.Generators, providerSupplied)
	default:
		return false, nil
	}
}

func supportedNestedGeneratorTrees(nestedGenerators []argoappv1.ApplicationSetNestedGenerator, providerSupplied bool) (bool, error) {
	for _, nested := range nestedGenerators {
		generator, err := generatorFromNested(nested)
		if err != nil {
			return false, err
		}
		supported, err := supportedGeneratorTree(generator, providerSupplied)
		if err != nil || !supported {
			return supported, err
		}
	}
	return true, nil
}

func generatorFromNested(nested argoappv1.ApplicationSetNestedGenerator) (argoappv1.ApplicationSetGenerator, error) {
	matrix, err := argoappv1.ToNestedMatrixGenerator(nested.Matrix)
	if err != nil {
		return argoappv1.ApplicationSetGenerator{}, fmt.Errorf("convert nested matrix generator: %w", err)
	}
	merge, err := argoappv1.ToNestedMergeGenerator(nested.Merge)
	if err != nil {
		return argoappv1.ApplicationSetGenerator{}, fmt.Errorf("convert nested merge generator: %w", err)
	}
	var matrixGenerator *argoappv1.MatrixGenerator
	if matrix != nil {
		matrixGenerator = matrix.ToMatrixGenerator()
	}
	var mergeGenerator *argoappv1.MergeGenerator
	if merge != nil {
		mergeGenerator = merge.ToMergeGenerator()
	}
	return argoappv1.ApplicationSetGenerator{
		List:                    nested.List,
		Clusters:                nested.Clusters,
		Git:                     nested.Git,
		SCMProvider:             nested.SCMProvider,
		ClusterDecisionResource: nested.ClusterDecisionResource,
		PullRequest:             nested.PullRequest,
		Matrix:                  matrixGenerator,
		Merge:                   mergeGenerator,
		Selector:                nested.Selector,
		Plugin:                  nested.Plugin,
	}, nil
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

	var allDiags []diagnostic.Diagnostic
	allSets := make([][]generatorParamSet, 0, len(merge.Generators))
	for _, generator := range merge.Generators {
		paramSets, diags, supported, err := evaluateNestedGenerator(ctx, generator, nil)
		allDiags = append(allDiags, diags...)
		if err != nil || !supported {
			return nil, allDiags, supported, err
		}
		allSets = append(allSets, paramSets)
	}

	baseByKey := map[string]int{}
	out := make([]generatorParamSet, len(allSets[0]))
	for i, paramSet := range allSets[0] {
		key, err := mergeParamSetKey(paramSet.Params, merge.MergeKeys)
		if err != nil {
			return nil, allDiags, true, err
		}
		if _, exists := baseByKey[key]; exists {
			return nil, allDiags, true, fmt.Errorf("parameters from a generator were not unique by merge keys; duplicate key was %s", key)
		}
		baseByKey[key] = i
		out[i] = generatorParamSet{
			Params:      cloneParams(paramSet.Params),
			SourcePath:  paramSet.SourcePath,
			SourcePaths: mergeSourcePaths(paramSet),
			Generator:   "merge",
		}
	}

	for _, paramSets := range allSets[1:] {
		seen := map[string]struct{}{}
		for _, paramSet := range paramSets {
			key, err := mergeParamSetKey(paramSet.Params, merge.MergeKeys)
			if err != nil {
				return nil, allDiags, true, err
			}
			if _, exists := seen[key]; exists {
				return nil, allDiags, true, fmt.Errorf("parameters from a generator were not unique by merge keys; duplicate key was %s", key)
			}
			seen[key] = struct{}{}
			baseIndex, exists := baseByKey[key]
			if !exists {
				continue
			}
			merged, err := mergeParams(out[baseIndex].Params, paramSet.Params, ctx.AppSet.Spec.GoTemplate)
			if err != nil {
				return nil, allDiags, true, err
			}
			out[baseIndex].Params = merged
			out[baseIndex].SourcePaths = mergeSourcePaths(out[baseIndex], paramSet)
			out[baseIndex].SourcePath = primarySourcePath(out[baseIndex].SourcePaths)
		}
	}

	var selectorDiags []diagnostic.Diagnostic
	var err error
	if ctx.Options.Provider.Supplied() && nestedGeneratorsContainProvider(merge.Generators) {
		out, selectorDiags, err = applyProviderGeneratorSelector(ctx.ManifestPath, "merge", selector, out)
	} else {
		out, selectorDiags, err = applyGeneratorSelector(ctx.ManifestPath, selector, out)
	}
	allDiags = append(allDiags, selectorDiags...)
	return out, allDiags, true, err
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
	for key, value := range override {
		out[key] = value
	}
	return out, nil
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

func clusterDecisionResourceParamSets(manifestPath string, generator *argoappv1.DuckTypeGenerator, data ProviderData, useGoTemplate bool, _ []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(data.ClusterDecisions) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}
	hasLabelSelector := len(generator.LabelSelector.MatchLabels) > 0 || len(generator.LabelSelector.MatchExpressions) > 0
	switch {
	case generator.Name == "" && !hasLabelSelector:
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, "clusterDecisionResource must set exactly one of name or labelSelector with provider fixtures")}, nil
	case generator.Name != "" && hasLabelSelector:
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, "clusterDecisionResource cannot combine name and labelSelector with provider fixtures")}, nil
	}

	clustersByName := map[string]ClusterInput{}
	for _, cluster := range data.Clusters {
		clustersByName[cluster.Name] = cluster
	}

	var out []generatorParamSet
	for _, input := range data.ClusterDecisions {
		matched, err := clusterDecisionResourceMatches(generator, input)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, err.Error())}, nil
		}
		if !matched {
			continue
		}
		if input.StatusListKey != defaultClusterDecisionStatusListKey {
			continue
		}
		matchKey := input.MatchKey
		if matchKey == "" {
			continue
		}
		for _, decision := range input.Decisions {
			matchValue, ok := decision[matchKey]
			if !ok || fmt.Sprint(matchValue) == "" {
				continue
			}
			cluster, ok := clustersByName[fmt.Sprint(matchValue)]
			if !ok {
				continue
			}
			params := map[string]any{
				"name":   cluster.Name,
				"server": cluster.Server,
			}
			for key, value := range decision {
				params[key] = fmt.Sprint(value)
			}
			appendRawValues(params, generator.Values, useGoTemplate)
			out = append(out, generatorParamSet{
				Params:    params,
				Generator: "clusterDecisionResource",
			})
		}
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}
	return out, nil, nil
}

const defaultClusterDecisionStatusListKey = "clusters"

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

func scmProviderParamSets(manifestPath string, generator *argoappv1.SCMProviderGenerator, inputs []SCMRepositoryInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "scmProvider")}, nil
	}
	if err := scmProviderOfflinePreflight(generator); err != nil {
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("scmProvider: %v", err))}, nil
	}

	var scoped []SCMRepositoryInput
	for _, input := range inputs {
		if scmProviderMatchesScope(generator, input) {
			scoped = append(scoped, input)
		}
	}
	if len(scoped) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "scmProvider")}, nil
	}

	var out []generatorParamSet
	for _, input := range scoped {
		matches, err := scmRepositoryMatchesProviderFilters(input, generator)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("scmProvider: %v", err))}, nil
		}
		if !matches {
			continue
		}
		matches, err = scmRepositoryMatchesFilters(input, generator.Filters)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("scmProvider: %v", err))}, nil
		}
		if !matches {
			continue
		}
		params, err := scmRepositoryParams(input, generator.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "scmProvider",
		})
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "scmProvider")}, nil
	}
	return out, nil, nil
}

func scmProviderMatchesScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	switch {
	case generator.Github != nil:
		return providerMatches(input.Provider, "github") && scopeEqual(input.Organization, generator.Github.Organization)
	case generator.Gitlab != nil:
		return providerMatches(input.Provider, "gitlab") && gitLabGroupScopeEqual(input.Organization, generator.Gitlab.Group, generator.Gitlab.IncludeSubgroups)
	case generator.Gitea != nil:
		return providerMatches(input.Provider, "gitea") && scopeEqual(input.Organization, generator.Gitea.Owner)
	case generator.Bitbucket != nil:
		return providerMatches(input.Provider, "bitbucket") && scopeEqual(input.Organization, generator.Bitbucket.Owner)
	case generator.BitbucketServer != nil:
		return providerMatches(input.Provider, "bitbucketServer") && scopeEqual(providerProject(input), generator.BitbucketServer.Project)
	case generator.AzureDevOps != nil:
		return providerMatches(input.Provider, "azureDevOps") &&
			scopeEqual(input.Organization, generator.AzureDevOps.Organization) &&
			scopeEqual(providerProject(input), generator.AzureDevOps.TeamProject)
	case generator.AWSCodeCommit != nil:
		return providerMatches(input.Provider, "awsCodeCommit") && optionalScopeEqual(input.Region, generator.AWSCodeCommit.Region)
	default:
		return false
	}
}

func scmRepositoryMatchesProviderFilters(input SCMRepositoryInput, generator *argoappv1.SCMProviderGenerator) (bool, error) {
	if generator.Gitlab != nil && generator.Gitlab.Topic != "" {
		if input.Labels == nil {
			return false, errors.New("gitlab.topic requires fixture labels")
		}
		if !stringSliceContains(input.Labels, generator.Gitlab.Topic) {
			return false, nil
		}
	}
	if generator.AWSCodeCommit != nil && len(generator.AWSCodeCommit.TagFilters) != 0 {
		if input.Tags == nil {
			return false, errors.New("awsCodeCommit.tagFilters require fixture tags")
		}
		if !scmRepositoryMatchesAWSTagFilters(input.Tags, generator.AWSCodeCommit.TagFilters) {
			return false, nil
		}
	}
	return true, nil
}

func scmProviderOfflinePreflight(generator *argoappv1.SCMProviderGenerator) error {
	if generator.AWSCodeCommit != nil && generator.AWSCodeCommit.Region == "" {
		return errors.New("awsCodeCommit.region must be explicit for offline fixture matching")
	}
	return nil
}

func scmRepositoryMatchesAWSTagFilters(tags map[string]string, filters []*argoappv1.TagFilter) bool {
	grouped := map[string][]string{}
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		grouped[filter.Key] = append(grouped[filter.Key], filter.Value)
	}
	for key, values := range grouped {
		actual, ok := tags[key]
		if !ok {
			return false
		}
		values = nonEmptyStrings(values)
		if len(values) == 0 {
			continue
		}
		if !stringSliceContains(values, actual) {
			return false
		}
	}
	return true
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func scmRepositoryMatchesFilters(input SCMRepositoryInput, filters []argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if len(filters) == 0 {
		return true, nil
	}

	repoFilters := make([]argoappv1.SCMProviderGeneratorFilter, 0, len(filters))
	branchFilters := make([]argoappv1.SCMProviderGeneratorFilter, 0, len(filters))
	for _, filter := range filters {
		switch scmFilterType(filter) {
		case "repo":
			repoFilters = append(repoFilters, filter)
		case "branch":
			branchFilters = append(branchFilters, filter)
		}
	}

	if len(repoFilters) != 0 {
		matchedRepo := false
		for _, filter := range repoFilters {
			matches, err := scmRepositoryMatchesFilter(input, filter)
			if err != nil {
				return false, err
			}
			if matches {
				matchedRepo = true
				break
			}
		}
		if !matchedRepo {
			return false, nil
		}
	}

	if len(branchFilters) == 0 {
		return true, nil
	}

	for _, filter := range branchFilters {
		matches, err := scmRepositoryMatchesFilter(input, filter)
		if err != nil {
			return false, err
		}
		if matches {
			return true, nil
		}
	}

	return false, nil
}

func scmFilterType(filter argoappv1.SCMProviderGeneratorFilter) string {
	switch {
	case filter.BranchMatch != nil || len(filter.PathsExist) != 0 || len(filter.PathsDoNotExist) != 0:
		return "branch"
	case filter.RepositoryMatch != nil || filter.LabelMatch != nil:
		return "repo"
	default:
		return ""
	}
}

func scmRepositoryMatchesFilter(input SCMRepositoryInput, filter argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if filter.RepositoryMatch != nil {
		matches, err := regexp.MatchString(*filter.RepositoryMatch, input.Repository)
		if err != nil {
			return false, fmt.Errorf("repositoryMatch %q: %w", *filter.RepositoryMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	if filter.BranchMatch != nil {
		matches, err := regexp.MatchString(*filter.BranchMatch, input.Branch)
		if err != nil {
			return false, fmt.Errorf("branchMatch %q: %w", *filter.BranchMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	if filter.LabelMatch != nil {
		if input.Labels == nil {
			return false, errors.New("labelMatch requires fixture labels")
		}
		labelRE, err := regexp.Compile(*filter.LabelMatch)
		if err != nil {
			return false, fmt.Errorf("labelMatch %q: %w", *filter.LabelMatch, err)
		}
		found := false
		for _, label := range input.Labels {
			if labelRE.MatchString(label) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	if len(filter.PathsExist) != 0 {
		if input.Paths == nil {
			return false, errors.New("pathsExist requires fixture paths")
		}
		for _, requiredPath := range filter.PathsExist {
			if !stringSliceContains(input.Paths, strings.TrimRight(requiredPath, "/")) {
				return false, nil
			}
		}
	}
	if len(filter.PathsDoNotExist) != 0 {
		if input.Paths == nil {
			return false, errors.New("pathsDoNotExist requires fixture paths")
		}
		for _, rejectedPath := range filter.PathsDoNotExist {
			if stringSliceContains(input.Paths, strings.TrimRight(rejectedPath, "/")) {
				return false, nil
			}
		}
	}
	return true, nil
}

func scmRepositoryParams(input SCMRepositoryInput, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	params := map[string]any{
		"organization":     input.Organization,
		"repository":       input.Repository,
		"repository_id":    input.RepositoryID,
		"url":              input.URL,
		"branch":           input.Branch,
		"branchNormalized": appsetutils.SanitizeName(input.Branch),
		"sha":              input.SHA,
		"short_sha":        shortString(input.SHA, 8),
		"short_sha_7":      shortString(input.SHA, 7),
		"labels":           strings.Join(input.Labels, ","),
	}
	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

func pullRequestParamSets(manifestPath string, generator *argoappv1.PullRequestGenerator, inputs []PullRequestInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "pullRequest")}, nil
	}

	var scoped []PullRequestInput
	for _, input := range inputs {
		if pullRequestMatchesScope(generator, input) {
			scoped = append(scoped, input)
		}
	}
	if len(scoped) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "pullRequest")}, nil
	}

	requiredLabels := pullRequestRequiredLabels(generator)
	var out []generatorParamSet
	for _, input := range scoped {
		matches, err := pullRequestMatchesProviderState(input, generator)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("pullRequest: %v", err))}, nil
		}
		if !matches {
			continue
		}
		matches, err = pullRequestMatchesProviderLabels(input, requiredLabels)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("pullRequest: %v", err))}, nil
		}
		if !matches {
			continue
		}
		matches, err = pullRequestMatchesFilters(input, generator.Filters)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("pullRequest: %v", err))}, nil
		}
		if !matches {
			continue
		}
		params, err := pullRequestParams(input, generator.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "pullRequest",
		})
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "pullRequest")}, nil
	}
	return out, nil, nil
}

func pullRequestMatchesScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	switch {
	case generator.Github != nil:
		return providerMatches(input.Provider, "github") &&
			scopeEqual(input.Organization, generator.Github.Owner) &&
			scopeEqual(input.Repository, generator.Github.Repo)
	case generator.GitLab != nil:
		org, repo := splitProviderProject(generator.GitLab.Project)
		return providerMatches(input.Provider, "gitlab") && repositoryProjectScopeEqual(input.Organization, input.Repository, org, repo, generator.GitLab.Project)
	case generator.Gitea != nil:
		return providerMatches(input.Provider, "gitea") &&
			scopeEqual(input.Organization, generator.Gitea.Owner) &&
			scopeEqual(input.Repository, generator.Gitea.Repo)
	case generator.Bitbucket != nil:
		return providerMatches(input.Provider, "bitbucket") &&
			scopeEqual(input.Organization, generator.Bitbucket.Owner) &&
			scopeEqual(input.Repository, generator.Bitbucket.Repo)
	case generator.BitbucketServer != nil:
		return providerMatches(input.Provider, "bitbucketServer") &&
			scopeEqual(providerProjectFromPR(input), generator.BitbucketServer.Project) &&
			scopeEqual(input.Repository, generator.BitbucketServer.Repo)
	case generator.AzureDevOps != nil:
		return providerMatches(input.Provider, "azureDevOps") &&
			scopeEqual(input.Organization, generator.AzureDevOps.Organization) &&
			scopeEqual(providerProjectFromPR(input), generator.AzureDevOps.Project) &&
			scopeEqual(input.Repository, generator.AzureDevOps.Repo)
	default:
		return false
	}
}

func pullRequestRequiredLabels(generator *argoappv1.PullRequestGenerator) []string {
	switch {
	case generator.Github != nil:
		return generator.Github.Labels
	case generator.GitLab != nil:
		return generator.GitLab.Labels
	case generator.Gitea != nil:
		return generator.Gitea.Labels
	case generator.AzureDevOps != nil:
		return generator.AzureDevOps.Labels
	default:
		return nil
	}
}

func pullRequestMatchesProviderLabels(input PullRequestInput, required []string) (bool, error) {
	if len(required) == 0 {
		return true, nil
	}
	if input.Labels == nil {
		return false, errors.New("provider labels require fixture labels")
	}
	for _, label := range required {
		if !stringSliceContains(input.Labels, label) {
			return false, nil
		}
	}
	return true, nil
}

func pullRequestMatchesProviderState(input PullRequestInput, generator *argoappv1.PullRequestGenerator) (bool, error) {
	if generator.GitLab == nil || generator.GitLab.PullRequestState == "" {
		return true, nil
	}
	if input.State == "" {
		return false, errors.New("gitlab.pullRequestState requires fixture state")
	}
	return input.State == generator.GitLab.PullRequestState, nil
}

func pullRequestMatchesFilters(input PullRequestInput, filters []argoappv1.PullRequestGeneratorFilter) (bool, error) {
	if len(filters) == 0 {
		return true, nil
	}
	for _, filter := range filters {
		matches, err := pullRequestMatchesFilter(input, filter)
		if err != nil {
			return false, err
		}
		if matches {
			return true, nil
		}
	}
	return false, nil
}

func pullRequestMatchesFilter(input PullRequestInput, filter argoappv1.PullRequestGeneratorFilter) (bool, error) {
	if filter.BranchMatch != nil {
		matches, err := regexp.MatchString(*filter.BranchMatch, input.Branch)
		if err != nil {
			return false, fmt.Errorf("branchMatch %q: %w", *filter.BranchMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	if filter.TargetBranchMatch != nil {
		matches, err := regexp.MatchString(*filter.TargetBranchMatch, input.TargetBranch)
		if err != nil {
			return false, fmt.Errorf("targetBranchMatch %q: %w", *filter.TargetBranchMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	if filter.TitleMatch != nil {
		matches, err := regexp.MatchString(*filter.TitleMatch, input.Title)
		if err != nil {
			return false, fmt.Errorf("titleMatch %q: %w", *filter.TitleMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

func pullRequestParams(input PullRequestInput, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	params := map[string]any{
		"number":             strconv.Itoa(input.Number),
		"title":              input.Title,
		"branch":             input.Branch,
		"branch_slug":        appsetutils.SlugifyName(50, false, input.Branch),
		"target_branch":      input.TargetBranch,
		"target_branch_slug": appsetutils.SlugifyName(50, false, input.TargetBranch),
		"head_sha":           input.HeadSHA,
		"head_short_sha":     shortString(input.HeadSHA, 8),
		"head_short_sha_7":   shortString(input.HeadSHA, 7),
		"author":             input.Author,
	}
	if useGoTemplate {
		params["labels"] = append([]string(nil), input.Labels...)
	}
	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
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

func providerMatches(actual, expected string) bool {
	return normalizeProvider(actual) == normalizeProvider(expected)
}

func normalizeProvider(value string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func scopeEqual(actual, expected string) bool {
	return expected != "" && actual == expected
}

func optionalScopeEqual(actual, expected string) bool {
	return expected == "" || actual == expected
}

func repositoryProjectScopeEqual(actualOrg, actualRepo, scopeOrg, scopeRepo, fullProject string) bool {
	if scopeRepo == "" {
		return actualRepo == fullProject || actualOrg == fullProject
	}
	return actualOrg == scopeOrg && actualRepo == scopeRepo
}

func splitProviderProject(project string) (string, string) {
	i := strings.LastIndex(project, "/")
	if i < 0 {
		return "", project
	}
	return project[:i], project[i+1:]
}

func gitLabGroupScopeEqual(actual, expected string, includeSubgroups bool) bool {
	if expected == "" {
		return false
	}
	if actual == expected {
		return true
	}
	return includeSubgroups && strings.HasPrefix(actual, expected+"/")
}

func providerProject(input SCMRepositoryInput) string {
	if input.Project != "" {
		return input.Project
	}
	return input.Organization
}

func providerProjectFromPR(input PullRequestInput) string {
	if input.Project != "" {
		return input.Project
	}
	return input.Organization
}

func stringSliceContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func shortString(value string, limit int) string {
	if len(value) < limit {
		return value
	}
	return value[:limit]
}

func stringMapAny(input map[string]string) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func appendRawValues(params map[string]any, values map[string]string, useGoTemplate bool) {
	if len(values) == 0 {
		return
	}
	if useGoTemplate {
		params["values"] = values
		return
	}
	for key, value := range values {
		params["values."+key] = value
	}
}

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

func gitGeneratorParamSets(repoRoot, manifestPath string, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	var out []generatorParamSet
	var diags []diagnostic.Diagnostic
	if len(git.Directories) == 0 && len(git.Files) == 0 {
		return nil, unsupportedGeneratorDiagnostic(manifestPath), false, nil
	}
	if len(git.Directories) > 0 {
		directorySets, err := gitDirectoryParamSets(repoRoot, manifestPath, git, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, true, err
		}
		out = append(out, directorySets...)
	}
	if len(git.Files) > 0 {
		fileSets, fileDiags, err := gitFileParamSets(repoRoot, manifestPath, git, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, fileDiags, true, err
		}
		diags = append(diags, fileDiags...)
		out = append(out, fileSets...)
	}
	return out, diags, true, nil
}

func unsupportedGeneratorDiagnostic(manifestPath string) []diagnostic.Diagnostic {
	return []diagnostic.Diagnostic{appsetDiagnostic(manifestPath, "unsupported ApplicationSet generator; supported generators are git directories, git files, list, matrix, and merge")}
}

func providerNoMatchDiagnostic(manifestPath, kind string) diagnostic.Diagnostic {
	diag := appsetDiagnostic(manifestPath, fmt.Sprintf("provider fixture supplied but no entries match %s generator", kind))
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

func providerUnsupportedFilterDiagnostic(manifestPath, detail string) diagnostic.Diagnostic {
	diag := appsetDiagnostic(manifestPath, fmt.Sprintf("provider filter cannot be evaluated from fixture data: %s", detail))
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

func appsetDiagnostic(manifestPath, message string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    message,
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.generators"},
	}
}

func generatedApplication(appset argoappv1.ApplicationSet, rendered argoappv1.Application, paramSet generatorParamSet) GeneratedApplication {
	rendered.Namespace = appset.Namespace
	rendered.TypeMeta = metav1.TypeMeta{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
	}
	return GeneratedApplication{
		Application: rendered,
		SourcePath:  paramSet.SourcePath,
		SourcePaths: mergeSourcePaths(paramSet),
		Generator:   paramSet.Generator,
	}
}

func gitDirectoryParamSets(repoRoot, manifestPath string, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, error) {
	includes, excludes := splitDirectoryPatterns(git.Directories)
	matches, err := matchDirectories(repoRoot, includes, excludes)
	if err != nil {
		return nil, err
	}

	out := make([]generatorParamSet, 0, len(matches))
	for _, match := range matches {
		params, err := pathParams(match, git.PathParamPrefix, git.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, fmt.Errorf("%s render %s: %w", manifestPath, match, err)
		}
		out = append(out, generatorParamSet{
			Params:      params,
			SourcePath:  match,
			SourcePaths: []string{match},
			Generator:   "git-directories",
		})
	}
	return out, nil
}

func splitDirectoryPatterns(dirs []argoappv1.GitDirectoryGeneratorItem) ([]string, []string) {
	var includes []string
	var excludes []string
	for _, dir := range dirs {
		cleaned := cleanPathPattern(dir.Path)
		if dir.Exclude {
			excludes = append(excludes, cleaned)
			continue
		}
		includes = append(includes, cleaned)
	}
	return includes, excludes
}

func matchDirectories(repoRoot string, includes, excludes []string) ([]string, error) {
	var candidates []string
	err := filepath.WalkDir(repoRoot, func(abs string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") && abs != repoRoot {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(repoRoot, abs)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		candidates = append(candidates, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, rel := range candidates {
		included, err := matchesAny(includes, rel)
		if err != nil {
			return nil, err
		}
		excluded, err := matchesAny(excludes, rel)
		if err != nil {
			return nil, err
		}
		if included && !excluded {
			matches = append(matches, rel)
		}
	}
	sort.Strings(matches)
	return matches, nil
}

func gitFileParamSets(repoRoot, manifestPath string, git *argoappv1.GitGenerator, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	includes, excludes, diags := splitGitFilePatterns(manifestPath, git.Files)
	matches, matchDiags, err := matchGitFiles(repoRoot, includes, excludes, manifestPath)
	diags = append(diags, matchDiags...)
	if err != nil {
		return nil, diags, err
	}

	out := make([]generatorParamSet, 0, len(matches))
	for _, match := range matches {
		params, decodeDiags, err := gitFileParams(repoRoot, match, git.PathParamPrefix, git.Values, useGoTemplate, goTemplateOptions, manifestPath)
		diags = append(diags, decodeDiags...)
		if err != nil {
			return nil, diags, err
		}
		if params == nil {
			continue
		}
		out = append(out, generatorParamSet{
			Params:      params,
			SourcePath:  match,
			SourcePaths: []string{match},
			Generator:   "git-files",
		})
	}
	return out, diags, nil
}

func splitGitFilePatterns(manifestPath string, files []argoappv1.GitFileGeneratorItem) ([]string, []string, []diagnostic.Diagnostic) {
	var includes []string
	var excludes []string
	var diags []diagnostic.Diagnostic
	for _, file := range files {
		cleaned, err := cleanGitFilePattern(file.Path)
		if err != nil {
			diags = append(diags, appsetDiagnostic(manifestPath, err.Error()))
			continue
		}
		if file.Exclude {
			excludes = append(excludes, cleaned)
			continue
		}
		includes = append(includes, cleaned)
	}
	return includes, excludes, diags
}

func cleanGitFilePattern(pattern string) (string, error) {
	if filepath.IsAbs(pattern) {
		return "", fmt.Errorf("git files pattern %q must be relative", pattern)
	}
	cleaned := path.Clean(filepath.ToSlash(strings.TrimSpace(pattern)))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("git files pattern %q must name files", pattern)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("git files pattern %q escapes repository root", pattern)
	}
	return cleaned, nil
}

func matchGitFiles(repoRoot string, includes, excludes []string, manifestPath string) ([]string, []diagnostic.Diagnostic, error) {
	var candidates []string
	var diags []diagnostic.Diagnostic
	err := filepath.WalkDir(repoRoot, func(abs string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if abs == repoRoot {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, abs)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			diags = append(diags, appsetDiagnostic(manifestPath, fmt.Sprintf("git files path %q is a symlink and was skipped", rel)))
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		candidates = append(candidates, rel)
		return nil
	})
	if err != nil {
		return nil, diags, err
	}
	sort.Strings(candidates)

	var matches []string
	for _, rel := range candidates {
		included, err := matchesAnyGlob(includes, rel)
		if err != nil {
			return nil, diags, fmt.Errorf("match git files include pattern: %w", err)
		}
		excluded, err := matchesAnyGlob(excludes, rel)
		if err != nil {
			return nil, diags, fmt.Errorf("match git files exclude pattern: %w", err)
		}
		if included && !excluded {
			matches = append(matches, rel)
		}
	}
	return matches, diags, nil
}

func matchesAnyGlob(patterns []string, rel string) (bool, error) {
	for _, pattern := range patterns {
		ok, err := doublestar.Match(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("pattern %q: %w", pattern, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func matchesAny(patterns []string, rel string) (bool, error) {
	for _, pattern := range patterns {
		ok, err := path.Match(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("match directory pattern %q: %w", pattern, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func gitFileParams(repoRoot, rel, prefix string, values map[string]string, useGoTemplate bool, goTemplateOptions []string, manifestPath string) (map[string]any, []diagnostic.Diagnostic, error) {
	fileValues, diag, err := decodeGitFileParams(filepath.Join(repoRoot, filepath.FromSlash(rel)), rel, manifestPath)
	if err != nil || diag != nil {
		if diag == nil {
			return nil, nil, err
		}
		return nil, []diagnostic.Diagnostic{*diag}, nil
	}

	params := map[string]any{}
	if useGoTemplate {
		for key, value := range fileValues {
			params[key] = value
		}
		addGoTemplateFilePathParams(params, rel, prefix)
	} else {
		flattenParams("", fileValues, params)
		addFlatFilePathParams(params, rel, prefix)
	}

	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, nil, err
	}
	return params, nil, nil
}

func decodeGitFileParams(absPath, rel, manifestPath string) (map[string]any, *diagnostic.Diagnostic, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, nil, err
	}

	var decoded any
	if strings.EqualFold(path.Ext(rel), ".json") {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			diag := appsetDiagnostic(manifestPath, fmt.Sprintf("git files match %q is not valid JSON: %v", rel, err))
			return nil, &diag, nil
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			diag := appsetDiagnostic(manifestPath, fmt.Sprintf("git files match %q contains multiple JSON documents", rel))
			return nil, &diag, nil
		}
	} else if err := yaml.Unmarshal(data, &decoded); err != nil {
		diag := appsetDiagnostic(manifestPath, fmt.Sprintf("git files match %q is not valid YAML: %v", rel, err))
		return nil, &diag, nil
	}
	mapping, ok := decoded.(map[string]any)
	if !ok || len(mapping) == 0 {
		diag := appsetDiagnostic(manifestPath, fmt.Sprintf("git files match %q must decode to a non-empty mapping document", rel))
		return nil, &diag, nil
	}
	return mapping, nil, nil
}

func pathParams(rel, prefix string, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	segments := strings.Split(rel, "/")
	base := path.Base(rel)

	params := map[string]any{}
	pathParamName := "path"
	if prefix != "" {
		pathParamName = prefix + "." + pathParamName
	}

	if useGoTemplate {
		pathFields := map[string]any{
			"path":               rel,
			"basename":           base,
			"basenameNormalized": appsetutils.SanitizeName(base),
			"segments":           segments,
		}
		if prefix == "" {
			params["path"] = pathFields
		} else {
			params[prefix] = map[string]any{"path": pathFields}
		}
	} else {
		params[pathParamName] = rel
		params[pathParamName+".basename"] = base
		params[pathParamName+".basenameNormalized"] = appsetutils.SanitizeName(base)
		for k, v := range segments {
			if v != "" {
				params[pathParamName+"["+strconv.Itoa(k)+"]"] = v
			}
		}
	}

	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

func addGoTemplateFilePathParams(params map[string]any, rel, prefix string) {
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	segments := splitPathSegments(dir)
	filename := path.Base(rel)
	pathFields := map[string]any{
		"path":               dir,
		"basename":           path.Base(dir),
		"basenameNormalized": appsetutils.SanitizeName(path.Base(dir)),
		"filename":           filename,
		"filenameNormalized": appsetutils.SanitizeName(filename),
		"segments":           segments,
	}
	if prefix == "" {
		params["path"] = pathFields
		return
	}
	params[prefix] = map[string]any{"path": pathFields}
}

func addFlatFilePathParams(params map[string]any, rel, prefix string) {
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	pathParamName := "path"
	if prefix != "" {
		pathParamName = prefix + "." + pathParamName
	}
	filename := path.Base(rel)
	params[pathParamName] = dir
	params[pathParamName+".basename"] = path.Base(dir)
	params[pathParamName+".basenameNormalized"] = appsetutils.SanitizeName(path.Base(dir))
	params[pathParamName+".filename"] = filename
	params[pathParamName+".filenameNormalized"] = appsetutils.SanitizeName(filename)
	for k, v := range splitPathSegments(dir) {
		if v != "" {
			params[pathParamName+"["+strconv.Itoa(k)+"]"] = v
		}
	}
}

func splitPathSegments(rel string) []string {
	if rel == "" {
		return []string{}
	}
	return strings.Split(rel, "/")
}

func flattenParams(prefix string, input map[string]any, out map[string]any) {
	for key, value := range input {
		flatKey := key
		if prefix != "" {
			flatKey = prefix + "." + key
		}
		flattenValue(flatKey, value, out)
	}
}

func flattenValue(key string, value any, out map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		flattenParams(key, typed, out)
	case []any:
		for i, item := range typed {
			flattenValue(key+"."+strconv.Itoa(i), item, out)
		}
	default:
		out[key] = fmt.Sprint(value)
	}
}

func appendTemplatedValues(params map[string]any, values map[string]string, useGoTemplate bool, goTemplateOptions []string) error {
	if len(values) == 0 {
		return nil
	}

	renderer := &appsetutils.Render{}
	if useGoTemplate {
		renderedValues := map[string]any{}
		for key, value := range values {
			rendered, err := renderer.Replace(value, params, useGoTemplate, goTemplateOptions)
			if err != nil {
				return fmt.Errorf("render value %q: %w", key, err)
			}
			renderedValues[key] = rendered
		}
		params["values"] = renderedValues
		return nil
	}

	for key, value := range values {
		rendered, err := renderer.Replace(value, params, useGoTemplate, goTemplateOptions)
		if err != nil {
			return fmt.Errorf("render value %q: %w", key, err)
		}
		params["values."+key] = rendered
	}
	return nil
}

func cleanPathPattern(pattern string) string {
	return path.Clean(filepath.ToSlash(pattern))
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
