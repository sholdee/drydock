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
	// GitGeneratorProject, when set, is the AppProject that a Git generator
	// of the ApplicationSet looked up before it listed the repository
	// (lookUpGitGeneratorProject). Argo CD fails the whole ApplicationSet
	// when that AppProject does not exist, so the caller warns when it does
	// not see it. Every Application of the ApplicationSet carries the same
	// value.
	GitGeneratorProject *string
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
	// NewGitFileGlobbing is the parsed argocd-cmd-params-cm key
	// applicationsetcontroller.enable.new.git.file.globbing. Without it, the
	// Git files generator matches its patterns as git pathspecs, as `git
	// ls-files` does; with it, as doublestar globs.
	NewGitFileGlobbing bool `json:"newGitFileGlobbing,omitempty"`
	// IncludeHiddenDirectories is the parsed argocd-cmd-params-cm key
	// reposerver.include.hidden.directories. With it, the Git and OCI
	// directory generators also see hidden directories.
	IncludeHiddenDirectories bool `json:"includeHiddenDirectories,omitempty"`
	// SubmodulesDisabled is the parsed argocd-cmd-params-cm key
	// applicationsetcontroller.enable.git.submodule turned off. The Git
	// generator then sees each gitlink as an empty directory, as the
	// repo-server's checkout holds it without submodule content.
	SubmodulesDisabled bool `json:"submodulesDisabled,omitempty"`
	// Worktree, when set, filters the repository of a working-tree run: the
	// Git generator skips every path it reports. drydock sets it to the
	// paths that Git ignores and does not track, which no commit can hold.
	// It is excluded from JSON: one discovery run has one repository root,
	// and the memo is per run.
	Worktree WorktreeFilter `json:"-"`
}

// WorktreeFilter reports the paths of a working tree that no Argo CD
// checkout holds. rel is relative to the repository root, with forward
// slashes.
type WorktreeFilter interface {
	Skip(rel string, isDir bool) bool
}

// GitlinkLister is implemented by a WorktreeFilter that knows the gitlinks
// (submodules) of the repository: the index of a working tree, or the
// commit of a snapshot. Gitlinks returns them relative to the repository
// root, with forward slashes.
type GitlinkLister interface {
	Gitlinks() []string
}

// RepositoryGenerators names the repository generators an ApplicationSet
// uses, at any depth of matrix and merge generators.
type RepositoryGenerators struct {
	// GitFiles is a Git files generator.
	GitFiles bool
	// Directories is a Git or OCI directories generator.
	Directories bool
	// Git is any Git generator.
	Git bool
}

// RepositoryGeneratorsOf returns the repository generators of appset. A
// nested generator that does not decode counts as none.
func RepositoryGeneratorsOf(appset argoappv1.ApplicationSet) RepositoryGenerators {
	var uses RepositoryGenerators
	for _, generator := range topLevelGenerators(appset.Spec.Generators) {
		uses.add(generator)
	}
	return uses
}

func (u *RepositoryGenerators) add(generator argoappv1.ApplicationSetGenerator) {
	switch {
	case generator.Git != nil:
		u.Git = true
		if len(generator.Git.Directories) > 0 {
			u.Directories = true
		} else if len(generator.Git.Files) > 0 {
			u.GitFiles = true
		}
	case generator.Oci != nil:
		if len(generator.Oci.Directories) > 0 {
			u.Directories = true
		}
	case generator.Matrix != nil:
		u.addNested(generator.Matrix.Generators)
	case generator.Merge != nil:
		u.addNested(generator.Merge.Generators)
	}
}

func (u *RepositoryGenerators) addNested(nestedGenerators []argoappv1.ApplicationSetNestedGenerator) {
	for _, nested := range nestedGenerators {
		generator, err := generatorFromNested(nested)
		if err != nil {
			continue
		}
		for _, single := range splitGenerator(generator) {
			u.add(single)
		}
	}
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
		state:        &generationState{},
	}
	templateRenderFailed := false
	for _, generator := range topLevelGenerators(appset.Spec.Generators) {
		paramSets, generatorDiags, supported, err := evaluateGenerator(ctx, generator)
		if err != nil {
			if unsupported, ok := unsupportedApplicationSet(manifestPath, err); ok {
				// drydock cannot tell what Argo CD generates, so the
				// ApplicationSet is unsupported as a whole.
				return nil, append(append(diags, generatorDiags...), appsetUnsupportedDiagnostic(manifestPath, err)), unsupported
			}
			if _, ok := errors.AsType[*generatorTemplateError](err); ok {
				templateRenderFailed = true
				diags = append(diags, generatorDiags...)
				diags = append(diags, appsetGeneratorTemplateRenderDiagnostic(manifestPath, err))
				continue
			}
			if _, ok := errors.AsType[*generatorParamsError](err); ok {
				// Argo CD runs the later generators too, but it keeps only
				// the first error, and the controller then sets one
				// ErrorOccurred condition and creates no Applications
				// (applicationset_controller.go:199-215). Stopping here
				// gives the same outcome, reports the condition's error,
				// and keeps a later drydock-only fatal error from failing
				// the run.
				templateRenderFailed = true
				diags = append(diags, generatorDiags...)
				diags = append(diags, appsetGeneratorFailedDiagnostic(manifestPath, err))
				break
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
	ctx.state.markGitProject(out)
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
	// state is shared by every generator of one ApplicationSet. It is nil
	// in tests that build a context by hand.
	state *generationState
}

// markGitProject records on every generated Application the AppProject
// that a Git generator looked up, if one did.
func (s *generationState) markGitProject(apps []GeneratedApplication) {
	if s == nil || s.gitProject == nil {
		return
	}
	for i := range apps {
		name := *s.gitProject
		apps[i].GitGeneratorProject = &name
	}
}

// generationState records what the generators of one ApplicationSet did.
type generationState struct {
	// gitProject is the AppProject that a Git generator looked up.
	gitProject *string
}

func evaluateGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	// Transform parses the selector before it runs any generator, so a
	// selector that does not parse fails the ApplicationSet even when
	// drydock cannot evaluate the generator
	// (applicationset/generators/generator_spec_processor.go:34-37).
	if err := parseGeneratorSelector(generator.Selector); err != nil {
		return nil, nil, true, err
	}
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
// parent is "matrix" or "merge", the generator that holds the child.
// Transform parses the child's selector first. It finds no generator in a
// child that sets none, such as `{}` or a matrix nested one level too deep
// (the terminal generator type has no matrix or merge field, so decoding
// drops it). For a child that sets two generators, Transform runs both, and
// getParams reports ErrMoreThenOneInnerGenerators only when neither fails
// (matrix.go:125-135, merge.go:165-175). Every error that Transform
// returns, getParams wraps as "child generator returned an error on
// parameter generation".
func evaluateNestedGenerator(ctx generatorContext, nested argoappv1.ApplicationSetNestedGenerator, inheritedParams map[string]any, parent string) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	generator, err := generatorFromNested(nested)
	if err != nil {
		// The matrix generator wraps a merge child that does not decode
		// (matrix.go:146-149). The merge generator does not
		// (merge.go:142-145), and neither wraps a matrix child.
		if _, ok := errors.AsType[*nestedMergeDecodeError](err); ok && parent == "matrix" {
			err = fmt.Errorf("error retrieving merge generator: %w", err)
		}
		return nil, nil, true, &generatorParamsError{err: err}
	}
	if err := parseGeneratorSelector(generator.Selector); err != nil {
		return nil, nil, true, wrapGeneratorError(err, childGeneratorErrorFormat)
	}
	count := generatorCount(generator)
	if count == 0 {
		return nil, nil, true, &generatorParamsError{err: errors.New("child generator generated no parameters")}
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
			return nil, nil, true, &generatorTemplateError{err: fmt.Errorf(childGeneratorErrorFormat, err)}
		}
		generator = *rendered
		generator.Selector = selector
	}
	if count > 1 {
		for _, single := range splitGenerator(generator) {
			if _, _, _, err := evaluateChildGenerator(ctx, single); err != nil {
				return nil, nil, true, wrapGeneratorError(err, childGeneratorErrorFormat)
			}
		}
		return nil, nil, true, &generatorParamsError{err: errors.New("found more than one generator in matrix.Generators")}
	}
	paramSets, diags, supported, err := evaluateChildGenerator(ctx, generator)
	if err != nil {
		err = wrapGeneratorError(err, childGeneratorErrorFormat)
	}
	return paramSets, diags, supported, err
}

// evaluateChildGenerator runs the one generator of a matrix or merge child.
// A merge child uses no template of its own (Transform gets an empty base
// template from getParams).
func evaluateChildGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if generator.Merge != nil {
		return mergeGeneratorParamSets(ctx, generator.Merge, generator.Selector)
	}
	return evaluateGenerator(ctx, generator)
}

// childGeneratorErrorFormat is how Argo CD v3.6.1 getParams wraps a Transform
// error of a matrix or merge child (matrix.go:125-127, merge.go:165-167).
const childGeneratorErrorFormat = "child generator returned an error on parameter generation: %w"

// parseGeneratorSelector parses a generator selector as Argo CD v3.6.1
// Transform does (applicationset/generators/generator_spec_processor.go:34-37).
func parseGeneratorSelector(selector *metav1.LabelSelector) error {
	if _, err := appsetutils.LabelSelectorAsSelector(selector); err != nil {
		return &generatorParamsError{err: fmt.Errorf("error parsing label selector: %w", err)}
	}
	return nil
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

// nestedMergeDecodeError is a nested merge generator that does not decode,
// with the text of Argo CD v3.6.1 getMergeGenerator (merge.go:216-225).
type nestedMergeDecodeError struct {
	err error
}

func (e *nestedMergeDecodeError) Error() string {
	return e.err.Error()
}

func (e *nestedMergeDecodeError) Unwrap() error {
	return e.err
}

func generatorFromNested(nested argoappv1.ApplicationSetNestedGenerator) (argoappv1.ApplicationSetGenerator, error) {
	// Argo CD v3.6.1 getMatrixGenerator and getMergeGenerator
	// (applicationset/generators/utils.go).
	matrix, err := argoappv1.ToNestedMatrixGenerator(nested.Matrix)
	if err != nil {
		// getMatrixGenerator returns the decode error as it is
		// (matrix.go:308-317).
		return argoappv1.ApplicationSetGenerator{}, err
	}
	merge, err := argoappv1.ToNestedMergeGenerator(nested.Merge)
	if err != nil {
		return argoappv1.ApplicationSetGenerator{}, &nestedMergeDecodeError{err: fmt.Errorf("error converting to nested merge generator: %w", err)}
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
// in sync with evaluateGenerator.
const supportedGeneratorsSummary = "supported generators are git directories, git files, oci directories, oci files, list, matrix, and merge, plus clusters, clusterDecisionResource, scmProvider, pullRequest, and plugin from --appset-provider-fixture data"

// generatorUnsupportedError marks a generator input that drydock does not
// evaluate, where Argo CD generates normally, such as a pathspec with attr
// magic. drydock cannot tell what the ApplicationSet generates, so
// GenerateWithOptions reports the whole ApplicationSet as unsupported, as
// it reports an unsupported generator.
type generatorUnsupportedError struct {
	err error
}

func (e *generatorUnsupportedError) Error() string {
	return e.err.Error()
}

func (e *generatorUnsupportedError) Unwrap() error {
	return e.err
}

// unsupportedApplicationSet returns the ErrUnsupportedGenerator error of an
// ApplicationSet whose generator failed with a generatorUnsupportedError.
func unsupportedApplicationSet(manifestPath string, err error) (error, bool) {
	if _, ok := errors.AsType[*generatorUnsupportedError](err); !ok {
		return nil, false
	}
	return fmt.Errorf("%w in %s", ErrUnsupportedGenerator, manifestPath), true
}

// appsetUnsupportedDiagnostic reports a generatorUnsupportedError. The
// "unsupported ApplicationSet generator" substring is the stable-code
// dispatch key (appset.unsupported-generator).
func appsetUnsupportedDiagnostic(manifestPath string, err error) diagnostic.Diagnostic {
	return appsetDiagnostic(manifestPath, fmt.Sprintf("unsupported ApplicationSet generator input: %v; drydock cannot tell what Argo CD generates, so this ApplicationSet contributes no Applications", err))
}

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
