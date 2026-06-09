package appset

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	switch {
	case generator.List != nil:
		return evaluateListGenerator(ctx, generator)
	case generator.Matrix != nil:
		return evaluateMatrixGenerator(ctx, generator)
	case generator.Merge != nil:
		return evaluateMergeGenerator(ctx, generator)
	case generator.Clusters != nil:
		return evaluateClustersGenerator(ctx, generator)
	case generator.ClusterDecisionResource != nil:
		return evaluateClusterDecisionResourceGenerator(ctx, generator)
	case generator.SCMProvider != nil:
		return evaluateSCMProviderGenerator(ctx, generator)
	case generator.PullRequest != nil:
		return evaluatePullRequestGenerator(ctx, generator)
	case generator.Plugin != nil:
		return evaluatePluginGenerator(ctx, generator)
	case generator.Git != nil:
		return evaluateGitGenerator(ctx, generator)
	default:
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
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

const defaultClusterDecisionStatusListKey = "clusters"

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

	renderedParams := map[string]any{}
	for key, value := range values {
		rendered, err := renderer.Replace(value, params, useGoTemplate, goTemplateOptions)
		if err != nil {
			return fmt.Errorf("render value %q: %w", key, err)
		}
		renderedParams["values."+key] = rendered
	}
	maps.Copy(params, renderedParams)
	return nil
}
