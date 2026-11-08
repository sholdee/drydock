package appset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifestyaml"
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
	// OCI acquires the artifacts oci generators name. It is excluded from
	// JSON so Options stays usable as a generation memo key. Without it the
	// oci generator is reported as unsupported.
	OCI OCIArtifactFetcher `json:"-"`
	// ProgressiveSyncs is the parsed argocd-cmd-params-cm key
	// applicationsetcontroller.enable.progressive.syncs. Without it, Argo CD
	// ignores a RollingSync strategy. It is part of the generation memo key,
	// because with it an empty RollingSync steps list generates nothing.
	ProgressiveSyncs bool `json:"progressiveSyncs,omitempty"`
}

func GenerateFromYAML(repoRoot, manifestPath string, data []byte) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	return GenerateFromYAMLWithOptions(repoRoot, manifestPath, data, Options{})
}

func GenerateFromYAMLWithOptions(repoRoot, manifestPath string, data []byte, options Options) ([]GeneratedApplication, []diagnostic.Diagnostic, error) {
	raw, err := manifestyaml.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil && !errors.Is(err, io.EOF) {
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
	templateRenderFailed := false
	for _, generator := range topLevelGenerators(appset.Spec.Generators) {
		paramSets, generatorDiags, supported, err := evaluateGenerator(ctx, generator)
		if err != nil {
			if _, ok := errors.AsType[*generatorTemplateError](err); ok {
				templateRenderFailed = true
				diags = append(diags, generatorDiags...)
				diags = append(diags, appsetGeneratorTemplateRenderDiagnostic(manifestPath, err))
				continue
			}
			if _, ok := errors.AsType[*generatorParamsError](err); ok {
				templateRenderFailed = true
				diags = append(diags, generatorDiags...)
				diags = append(diags, appsetGeneratorFailedDiagnostic(manifestPath, err))
				continue
			}
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
				templateRenderFailed = true
				diags = append(diags, appsetTemplateRenderDiagnostic(manifestPath, paramSet.SourcePath, err))
				continue
			}
			out = append(out, generatedApplication(appset, rendered, paramSet))
		}
	}
	if templateRenderFailed {
		return nil, diags, nil
	}
	if len(out) == 0 && unsupportedCount > 0 {
		return nil, diags, fmt.Errorf("%w in %s", ErrUnsupportedGenerator, manifestPath)
	}
	valid, validationDiags := validateGeneratedApplications(manifestPath, appset, out)
	if rolloutStepsEmpty(appset, options) {
		// Argo CD v3.6.1 checks the steps after it validates the generated
		// Applications and returns before it creates, updates, or deletes
		// any of them or sets the ApplicationValidationError condition
		// (applicationset/controllers/applicationset_controller.go:263-277).
		// Like a template render failure, the ApplicationSet contributes no
		// Applications and only a warning.
		return nil, append(diags, rolloutNoStepsDiagnostic(manifestPath, appset)), nil
	}
	diags = append(diags, validationDiags...)
	diags = append(diags, rolloutStrategyDiagnostics(manifestPath, appset, out, valid, options)...)
	disableRollingSyncAutomatedSync(appset, options, valid)
	return valid, diags, nil
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
	case generator.Oci != nil:
		return evaluateOciGenerator(ctx, generator)
	default:
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
}

// evaluateNestedGenerator evaluates one matrix or merge child the way the
// Argo CD v3.6.1 matrix and merge getParams do: they build the child
// generator (nestedGeneratorSpec) and run generators.Transform on it
// (applicationset/generators/matrix.go:117-139 and merge.go:137-177).
// Transform finds no generator in a child that sets none, such as `{}` or a
// matrix nested one level too deep (the terminal generator type has no
// matrix or merge field, so decoding drops it). It finds two in a child that
// sets two. getParams then fails with "child generator generated no
// parameters" or ErrMoreThenOneInnerGenerators, and the whole
// ApplicationSet fails.
func evaluateNestedGenerator(ctx generatorContext, nested argoappv1.ApplicationSetNestedGenerator, inheritedParams map[string]any) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	generator, err := generatorFromNested(nested)
	if err != nil {
		return nil, nil, true, &generatorParamsError{err: err}
	}
	switch generatorCount(generator) {
	case 0:
		return nil, nil, true, &generatorParamsError{err: errors.New("child generator generated no parameters")}
	case 1:
	default:
		return nil, nil, true, &generatorParamsError{err: errors.New("found more than one generator in matrix.Generators")}
	}
	if !supportedGeneratorTree(generator, ctx.Options) {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	if len(inheritedParams) != 0 {
		// Transform interpolates the child generator with the parameters
		// of the first matrix child, but it filters the child's params with
		// the selector of the generator it was given, before interpolation
		// (applicationset/generators/generator_spec_processor.go:34 and
		// :89). So a template in the child's own selector stays literal.
		// The selectors of generators inside the child are interpolated,
		// because the child evaluates them from the interpolated spec.
		selector := generator.Selector
		rendered, err := (&appsetutils.Render{}).RenderGeneratorParams(&generator, inheritedParams, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
		if err != nil {
			return nil, nil, true, &generatorTemplateError{err: fmt.Errorf("interpolate nested generator: %w", err)}
		}
		generator = *rendered
		generator.Selector = selector
	}
	if generator.Merge != nil {
		return mergeGeneratorParamSets(ctx, generator.Merge, generator.Selector)
	}
	return evaluateGenerator(ctx, generator)
}

// supportedGeneratorTree reports whether drydock can evaluate every
// generator in the tree. A malformed tree counts as supported, so that
// evaluation reports the error that Argo CD reports.
func supportedGeneratorTree(generator argoappv1.ApplicationSetGenerator, options Options) bool {
	switch {
	case generator.List != nil || generator.Git != nil:
		return true
	case generator.Clusters != nil || generator.ClusterDecisionResource != nil || generator.SCMProvider != nil || generator.PullRequest != nil || generator.Plugin != nil:
		return options.Provider.Supplied()
	case generator.Oci != nil:
		return options.OCI != nil
	case generator.Matrix != nil:
		return supportedNestedGeneratorTrees(generator.Matrix.Generators, options)
	case generator.Merge != nil:
		return supportedNestedGeneratorTrees(generator.Merge.Generators, options)
	default:
		// A generator that sets nothing fails in Argo CD when it is a
		// matrix or merge child (evaluateNestedGenerator).
		return true
	}
}

func supportedNestedGeneratorTrees(nestedGenerators []argoappv1.ApplicationSetNestedGenerator, options Options) bool {
	for _, nested := range nestedGenerators {
		generator, err := generatorFromNested(nested)
		if err != nil {
			continue
		}
		if !supportedGeneratorTree(generator, options) {
			return false
		}
	}
	return true
}

// generatorCount counts the generator fields that a generator sets, as
// Argo CD v3.6.1 GetRelevantGenerators does: every non-nil field except the
// selector (applicationset/generators/generator_spec_processor.go:104-124).
func generatorCount(generator argoappv1.ApplicationSetGenerator) int {
	return len(splitGenerator(generator))
}

// splitGenerator returns one generator for each generator field that
// generator sets, in the field order of ApplicationSetGenerator, each with
// the shared selector. Argo CD v3.6.1 Transform runs each one in that order
// (GetRelevantGenerators), so a top-level generator that sets both list and
// clusters generates from both.
func splitGenerator(generator argoappv1.ApplicationSetGenerator) []argoappv1.ApplicationSetGenerator {
	var out []argoappv1.ApplicationSetGenerator
	for _, single := range []argoappv1.ApplicationSetGenerator{
		{List: generator.List},
		{Clusters: generator.Clusters},
		{Git: generator.Git},
		{SCMProvider: generator.SCMProvider},
		{ClusterDecisionResource: generator.ClusterDecisionResource},
		{PullRequest: generator.PullRequest},
		{Matrix: generator.Matrix},
		{Merge: generator.Merge},
		{Plugin: generator.Plugin},
		{Oci: generator.Oci},
	} {
		if single == (argoappv1.ApplicationSetGenerator{}) {
			continue
		}
		single.Selector = generator.Selector
		out = append(out, single)
	}
	return out
}

// topLevelGenerators expands each spec.generators entry into its generator
// fields (splitGenerator). An entry that sets none stays, so that it is
// reported as unsupported. Argo CD skips it (Transform finds no generator)
// and logs CheckInvalidGenerators.
func topLevelGenerators(generators []argoappv1.ApplicationSetGenerator) []argoappv1.ApplicationSetGenerator {
	out := make([]argoappv1.ApplicationSetGenerator, 0, len(generators))
	for _, generator := range generators {
		split := splitGenerator(generator)
		if len(split) == 0 {
			out = append(out, generator)
			continue
		}
		out = append(out, split...)
	}
	return out
}

func generatorFromNested(nested argoappv1.ApplicationSetNestedGenerator) (argoappv1.ApplicationSetGenerator, error) {
	// Argo CD v3.6.1 getMatrixGenerator and getMergeGenerator
	// (applicationset/generators/utils.go).
	matrix, err := argoappv1.ToNestedMatrixGenerator(nested.Matrix)
	if err != nil {
		return argoappv1.ApplicationSetGenerator{}, fmt.Errorf("convert nested matrix generator: %w", err)
	}
	merge, err := argoappv1.ToNestedMergeGenerator(nested.Merge)
	if err != nil {
		return argoappv1.ApplicationSetGenerator{}, fmt.Errorf("error converting to nested merge generator: %w", err)
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
		Oci:                     nested.Oci,
	}, nil
}

const defaultClusterDecisionStatusListKey = "clusters"

// supportedGeneratorsSummary names every generator drydock evaluates. Keep it
// in sync with evaluateGenerator and supportedGeneratorTree.
const supportedGeneratorsSummary = "supported generators are git directories, git files, oci directories, oci files, list, matrix, and merge, plus clusters, clusterDecisionResource, scmProvider, pullRequest, and plugin from --appset-provider-fixture data"

func unsupportedGeneratorDiagnostic(manifestPath string) []diagnostic.Diagnostic {
	return []diagnostic.Diagnostic{appsetDiagnostic(manifestPath, "unsupported ApplicationSet generator; "+supportedGeneratorsSummary)}
}

// unsupportedOciGeneratorDiagnostic reports an oci generator evaluated
// without an OCIArtifactFetcher (direct library callers); the drydock
// discovery pipeline always configures one.
func unsupportedOciGeneratorDiagnostic(manifestPath string) []diagnostic.Diagnostic {
	return []diagnostic.Diagnostic{appsetDiagnostic(manifestPath, "unsupported ApplicationSet generator: the oci generator needs OCI artifact acquisition, which this caller did not configure; "+supportedGeneratorsSummary)}
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

// appsetTemplateRenderDiagnostic scopes template-execution failures (template
// and templatePatch) to the failing ApplicationSet, matching the Argo CD
// controller: it records the render error as an ErrorOccurred condition and
// reconciles no Applications for that ApplicationSet. Generator-side
// template failures (values templating, nested matrix/merge interpolation)
// are scoped the same way through generatorTemplateError and
// appsetGeneratorTemplateRenderDiagnostic. Other evaluateGenerator failures
// stay fatal because acquisition/IO failures are drydock-side limitations
// where silently skipping would hide unknown desired state. The "template render failed"
// message substring is the stable-code dispatch key
// (appset.template-render-failed); keep it in sync with
// internal/diagnostic.appSetCode.
func appsetTemplateRenderDiagnostic(manifestPath, sourcePath string, err error) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet template render failed for %q: %v; this ApplicationSet contributes no Applications (Argo CD reports the same failure as an ErrorOccurred condition and reconciles no Applications)", sourcePath, err),
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.template"},
	}
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
		// Argo CD v3.6.1 stores rendered values as map[string]string
		// (applicationset/generators/value_interpolation.go:20-24). Keep
		// that type so templates that call Sprig map functions on .values
		// fail the same way.
		renderedValues := map[string]string{}
		for key, value := range values {
			rendered, err := renderer.Replace(value, params, useGoTemplate, goTemplateOptions)
			if err != nil {
				return &generatorTemplateError{err: fmt.Errorf("render value %q: %w", key, err)}
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
			return &generatorTemplateError{err: fmt.Errorf("render value %q: %w", key, err)}
		}
		renderedParams["values."+key] = rendered
	}
	maps.Copy(params, renderedParams)
	return nil
}
