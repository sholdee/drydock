package appset

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// generatorTemplateError marks a template-execution failure raised while
// evaluating a generator: generator `values` templating, matrix/merge
// child interpolation, and the param failures where Argo CD panics (see
// upstreamPanic). Argo CD reports these generator Transform errors as an
// ErrorOccurred condition and reconciles no Applications for the
// ApplicationSet, so GenerateWithOptions scopes them like template and
// templatePatch failures instead of failing the run. Acquisition and IO
// failures stay fatal because they are drydock-side limitations.
//
// upstreamPanic marks a failure where Argo CD panics instead of returning an
// error. The ApplicationSet controller recovers the panic in Reconcile
// (applicationset/controllers/applicationset_controller.go:123-133) and
// returns before it generates Applications or sets a status condition. The
// result for the ApplicationSet is the same: no Applications.
type generatorTemplateError struct {
	err           error
	upstreamPanic bool
}

func (e *generatorTemplateError) Error() string {
	return e.err.Error()
}

func (e *generatorTemplateError) Unwrap() error {
	return e.err
}

// appsetGeneratorTemplateRenderDiagnostic is the generator-side counterpart
// of appsetTemplateRenderDiagnostic. The "template render failed" substring
// is the stable-code dispatch key (appset.template-render-failed).
func appsetGeneratorTemplateRenderDiagnostic(manifestPath string, err error) diagnostic.Diagnostic {
	argoCDOutcome := "Argo CD reports the same failure as an ErrorOccurred condition and reconciles no Applications"
	if templateErr, ok := errors.AsType[*generatorTemplateError](err); ok && templateErr.upstreamPanic {
		argoCDOutcome = "the Argo CD ApplicationSet controller recovers from the panic and reconciles no Applications without setting an ErrorOccurred condition"
	}
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet template render failed while evaluating a generator: %v; this ApplicationSet contributes no Applications (%s)", err, argoCDOutcome),
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.generators"},
	}
}

// generatorParamsError marks a generator failure that Argo CD v3.6.1
// returns from generators.Transform for a reason other than template
// execution: a list element or elementsYaml that does not decode, a matrix
// or merge with the wrong number of children, a child that names no
// generator or more than one, missing or non-unique merge keys, a matrix
// param clash, or a selector that does not parse.
// template.GenerateApplications returns the first such error with reason
// ApplicationParamsGenerationError, and the controller sets an
// ErrorOccurred condition and reconciles no Applications for the
// ApplicationSet (applicationset/controllers/applicationset_controller.go:207-224).
// Other ApplicationSets are not affected, so GenerateWithOptions scopes the
// failure to this ApplicationSet, like a template render failure.
type generatorParamsError struct {
	err error
}

func (e *generatorParamsError) Error() string {
	return e.err.Error()
}

func (e *generatorParamsError) Unwrap() error {
	return e.err
}

// paramsError wraps err as a generatorParamsError. It keeps an error that
// is already scoped to the ApplicationSet as it is.
func paramsError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*generatorParamsError](err); ok {
		return err
	}
	if _, ok := errors.AsType[*generatorTemplateError](err); ok {
		return err
	}
	return &generatorParamsError{err: err}
}

// appsetGeneratorFailedDiagnostic reports a generatorParamsError. The
// "ApplicationSet generator failed" substring is the stable-code dispatch
// key (appset.generator-failed).
func appsetGeneratorFailedDiagnostic(manifestPath string, err error) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet generator failed: %v; this ApplicationSet contributes no Applications (Argo CD reports the same failure as an ErrorOccurred condition and reconciles no Applications)", err),
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.generators"},
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

// validateGeneratedApplications mirrors the name checks in Argo CD v3.6.1
// ApplicationSetReconciler.validateGeneratedApplications
// (applicationset/controllers/applicationset_controller.go). A generated
// Application with an empty metadata.name (including a generateName-only
// templatePatch) is rejected first; a name that repeats an earlier generated
// Application is rejected next. Upstream keys errors by QualifiedName and
// keeps only Applications whose key has no error, so every Application
// sharing a duplicated name is dropped, not just the later ones. The
// remaining Applications still generate. An empty spec.project fails the
// AppProject lookup that follows. A named AppProject that the repository
// does not declare is checked after discovery, when every AppProject is
// known (pruneMissingProjectApplications in internal/app). The destination
// check needs live clusters and stays with the project diagnostics.
func validateGeneratedApplications(manifestPath string, appset argoappv1.ApplicationSet, apps []GeneratedApplication) ([]GeneratedApplication, []diagnostic.Diagnostic) {
	errorsByApp := map[string]generatedApplicationError{}
	namesSet := map[string]bool{}
	for i := range apps {
		app := &apps[i].Application
		if app.Name == "" {
			errorsByApp[app.QualifiedName()] = generatedApplicationError{fmt.Sprintf("ApplicationSet %s contains an application with no name; a name must be set in the template's metadata.name or via a templatePatch (generateName is not supported)", appset.Name), templateNamePointer}
			continue
		}
		if namesSet[app.Name] {
			errorsByApp[app.QualifiedName()] = generatedApplicationError{fmt.Sprintf("ApplicationSet %s contains applications with duplicate name: %s", appset.Name, app.Name), templateNamePointer}
			continue
		}
		namesSet[app.Name] = true
		// Upstream then looks the AppProject up by name in the Argo CD
		// namespace. An empty spec.project names no object, so the
		// controller-runtime cache reports it as not found
		// (applicationset_controller.go:627-631), and the Application is
		// not created. The ApplicationSet controller does not read an empty
		// project as "default", unlike the application controller.
		if app.Spec.Project == "" {
			errorsByApp[app.QualifiedName()] = generatedApplicationError{fmt.Sprintf("application references project %s which does not exist", app.Spec.Project), "spec.template.spec.project"}
		}
	}
	if len(errorsByApp) == 0 {
		return apps, nil
	}

	valid := make([]GeneratedApplication, 0, len(apps))
	for _, app := range apps {
		if _, failed := errorsByApp[app.Application.QualifiedName()]; !failed {
			valid = append(valid, app)
		}
	}
	keys := slices.Sorted(maps.Keys(errorsByApp))
	diags := make([]diagnostic.Diagnostic, 0, len(keys))
	for _, key := range keys {
		diags = append(diags, generatedApplicationValidationDiagnostic(manifestPath, errorsByApp[key]))
	}
	return valid, diags
}

// MissingProjectDiagnostic reports a generated Application whose AppProject
// does not exist. The Argo CD v3.6.1 ApplicationSet controller looks the
// AppProject up for each generated Application, and when it is not found it
// does not create or update that Application and sets an
// ApplicationValidationError condition with this message
// (applicationset/controllers/applicationset_controller.go:627-633). The
// other Applications of the ApplicationSet still generate.
func MissingProjectDiagnostic(manifestPath, projectName string) diagnostic.Diagnostic {
	return generatedApplicationValidationDiagnostic(manifestPath, generatedApplicationError{fmt.Sprintf("application references project %s which does not exist", projectName), "spec.template.spec.project"})
}

// generatedApplicationValidationDiagnostic carries the upstream validation
// message verbatim. The "generated Application validation failed" substring
// is the stable-code dispatch key (appset.application-validation-failed).
// templateNamePointer is the pointer of a name validation failure.
const templateNamePointer = "spec.template.metadata.name"

// generatedApplicationError is one upstream validation failure of a
// generated Application, with the template field that causes it.
type generatedApplicationError struct {
	message string
	pointer string
}

func generatedApplicationValidationDiagnostic(manifestPath string, failure generatedApplicationError) diagnostic.Diagnostic {
	upstreamMessage := failure.message
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("generated Application validation failed: %s; Argo CD reports an ApplicationValidationError condition and does not create or update the affected Applications, so drydock omits them while the ApplicationSet's other Applications still generate", upstreamMessage),
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: failure.pointer},
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

// rolloutStrategyDiagnostics lints spec.strategy.rollingSync the way Argo CD
// v3.6.1 does (applicationset/progressivesync progressive_sync.go and
// validation_issues.go). With progressive syncs off, Argo CD ignores the
// strategy (applicationset_controller.go:254-295), so drydock reports only
// that. With them on, an invalid config becomes an InvalidRolloutConfig
// condition that never blocks Applications, so the findings are warnings
// and generation output is unchanged. An empty steps list never reaches
// here (rolloutStepsEmpty). generated is every generated Application
// (upstream evaluates steps before dropping validation failures); valid
// stands in for the live Applications whose status entries drive the
// maxUpdate check, assuming a steady state where the cluster holds exactly
// the valid desired set.
func rolloutStrategyDiagnostics(manifestPath string, appset argoappv1.ApplicationSet, generated, valid []GeneratedApplication, options Options) []diagnostic.Diagnostic {
	if !isRollingSyncStrategy(appset) {
		return nil
	}
	if !options.ProgressiveSyncs {
		return []diagnostic.Diagnostic{rolloutStrategyIgnoredDiagnostic(manifestPath, appset)}
	}
	issues := rolloutValidationIssues(appset.Spec.Strategy.RollingSync.Steps, generated, valid)
	var diags []diagnostic.Diagnostic
	for _, message := range issues.messages() {
		diags = append(diags, invalidRolloutConfigDiagnostic(manifestPath, appset, message))
	}
	return diags
}

// IsRollingSyncStrategy reports an ApplicationSet whose strategy the
// applicationsetcontroller.enable.progressive.syncs flag changes.
func IsRollingSyncStrategy(appset argoappv1.ApplicationSet) bool {
	return isRollingSyncStrategy(appset)
}

// isRollingSyncStrategy mirrors progressivesync.IsRollingSyncStrategy.
func isRollingSyncStrategy(appset argoappv1.ApplicationSet) bool {
	return appset.Spec.Strategy != nil && appset.Spec.Strategy.Type == "RollingSync" && appset.Spec.Strategy.RollingSync != nil
}

// rolloutStepsEmpty reports an ApplicationSet whose RollingSync strategy has
// an empty steps list while progressive syncs are on. Argo CD v3.6.1
// (progressivesync.IsStepsEmpty) then reconciles none of its Applications.
func rolloutStepsEmpty(appset argoappv1.ApplicationSet, options Options) bool {
	return options.ProgressiveSyncs && isRollingSyncStrategy(appset) && len(appset.Spec.Strategy.RollingSync.Steps) == 0
}

// disableRollingSyncAutomatedSync mirrors SyncDesiredApplications and
// disableAutomatedSync (Argo CD v3.6.1
// applicationset/progressivesync/progressive_sync.go:997-1013), which the
// controller calls with progressive syncs on for a RollingSync strategy that
// has steps (applicationset_controller.go:335-343). Every Application with
// an automated sync policy gets automated.enabled false, because the
// ApplicationSet controller starts the syncs itself. The sync operations it
// then adds are live state and are not modeled.
func disableRollingSyncAutomatedSync(appset argoappv1.ApplicationSet, options Options, apps []GeneratedApplication) {
	if !options.ProgressiveSyncs || !isRollingSyncStrategy(appset) || len(appset.Spec.Strategy.RollingSync.Steps) == 0 {
		return
	}
	for i := range apps {
		policy := apps[i].Application.Spec.SyncPolicy
		if policy == nil || policy.Automated == nil {
			continue
		}
		policy = policy.DeepCopy()
		policy.Automated.Enabled = new(false)
		apps[i].Application.Spec.SyncPolicy = policy
	}
}

// rolloutStrategyIgnoredDiagnostic reports a RollingSync strategy that Argo
// CD ignores because progressive syncs are off. The "RollingSync strategy is
// not used" substring is the stable-code dispatch key
// (appset.rollout-strategy-ignored).
func rolloutStrategyIgnoredDiagnostic(manifestPath string, appset argoappv1.ApplicationSet) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet %s: the RollingSync strategy is not used, because applicationsetcontroller.enable.progressive.syncs is not true in argocd-cmd-params-cm", appsetDisplayName(appset)),
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.strategy"},
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

// rolloutNoStepsDiagnostic carries upstream's "No steps defined for rollout"
// condition message, which is also the stable-code dispatch key
// (appset.rollout-no-steps).
func rolloutNoStepsDiagnostic(manifestPath string, appset argoappv1.ApplicationSet) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet %s uses the RollingSync strategy with an empty steps list: No steps defined for rollout; Argo CD reports an ErrorOccurred condition (ApplicationSetRolloutError) and creates, updates, or deletes none of its Applications, so this ApplicationSet contributes no Applications", appsetDisplayName(appset)),
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.strategy.rollingSync.steps"},
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

// invalidRolloutConfigDiagnostic carries one upstream InvalidRolloutConfig
// message. The "invalid RollingSync rollout config" substring is the
// stable-code dispatch key (appset.invalid-rollout-config).
func invalidRolloutConfigDiagnostic(manifestPath string, appset argoappv1.ApplicationSet, upstreamMessage string) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "appset",
		Message:    fmt.Sprintf("ApplicationSet %s has an invalid RollingSync rollout config: %s; Argo CD reports an InvalidRolloutConfig condition and still creates and updates the Applications", appsetDisplayName(appset), upstreamMessage),
		Provenance: diagnostic.Provenance{Path: manifestPath, Pointer: "spec.strategy.rollingSync.steps"},
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

func appsetDisplayName(appset argoappv1.ApplicationSet) string {
	if appset.Namespace == "" {
		return appset.Name
	}
	return appset.Namespace + "/" + appset.Name
}

// rolloutIssues mirrors progressivesync.ValidationIssues.
type rolloutIssues struct {
	invalidMatchExpressions []rolloutInvalidMatchExpression
	duplicateAppSelections  map[string][]int
	emptySteps              []int
	invalidMaxUpdates       []rolloutInvalidMaxUpdate
}

type rolloutInvalidMatchExpression struct {
	stepIndex int
	operator  string
}

type rolloutInvalidMaxUpdate struct {
	stepIndex int
	maxUpdate *intstr.IntOrString
}

// rolloutValidationIssues mirrors buildAppDependencyList plus the maxUpdate
// collection in UpdateApplicationSetApplicationStatusProgress.
func rolloutValidationIssues(steps []argoappv1.ApplicationSetRolloutStep, generated, valid []GeneratedApplication) rolloutIssues {
	var issues rolloutIssues
	appDependencyList := make([][]string, len(steps))
	appStepMap := map[string]int{}

	for _, generatedApp := range generated {
		app := generatedApp.Application
		for i, step := range steps {
			selected := true
			for _, matchExpression := range step.MatchExpressions {
				if val, ok := app.Labels[matchExpression.Key]; ok {
					valueMatched, operatorErr := rolloutLabelMatchedExpression(val, matchExpression)
					if operatorErr && !issues.invalidMatchExpressionRecorded(i, matchExpression.Operator) {
						issues.invalidMatchExpressions = append(issues.invalidMatchExpressions, rolloutInvalidMatchExpression{
							stepIndex: i,
							operator:  matchExpression.Operator,
						})
						selected = false
						break
					}
					if !valueMatched {
						selected = false
						break
					}
				} else if matchExpression.Operator == "In" {
					selected = false
					break
				}
			}

			if !selected {
				continue
			}
			appDependencyList[i] = append(appDependencyList[i], app.Name)
			if previous, ok := appStepMap[app.Name]; ok {
				if issues.duplicateAppSelections == nil {
					issues.duplicateAppSelections = map[string][]int{}
				}
				if _, found := issues.duplicateAppSelections[app.Name]; !found {
					issues.duplicateAppSelections[app.Name] = []int{previous}
				}
				issues.duplicateAppSelections[app.Name] = append(issues.duplicateAppSelections[app.Name], i)
			} else {
				appStepMap[app.Name] = i
			}
		}
	}

	for stepIdx, apps := range appDependencyList {
		if len(apps) == 0 {
			issues.emptySteps = append(issues.emptySteps, stepIdx)
		}
	}

	issues.invalidMaxUpdates = rolloutInvalidMaxUpdates(steps, appStepMap, valid)
	return issues
}

// rolloutLabelMatchedExpression mirrors progressivesync.labelMatchedExpression;
// the bool result reports the upstream operator error.
func rolloutLabelMatchedExpression(val string, matchExpression argoappv1.ApplicationMatchExpression) (bool, bool) {
	if matchExpression.Operator != "In" && matchExpression.Operator != "NotIn" {
		return false, true
	}
	if slices.Contains(matchExpression.Values, val) {
		return matchExpression.Operator == "In", false
	}
	return matchExpression.Operator == "NotIn", false
}

// rolloutInvalidMaxUpdates mirrors the maxUpdate check in
// UpdateApplicationSetApplicationStatusProgress. Upstream walks the
// ApplicationSet's application status entries (one per live Application, in
// API list order, which is name order) and resolves each to its step through
// appStepMap, where an Application no step selects falls back to step 0.
// Each step is recorded once.
func rolloutInvalidMaxUpdates(steps []argoappv1.ApplicationSetRolloutStep, appStepMap map[string]int, valid []GeneratedApplication) []rolloutInvalidMaxUpdate {
	names := make([]string, 0, len(valid))
	seen := map[string]bool{}
	for _, app := range valid {
		if !seen[app.Application.Name] {
			seen[app.Application.Name] = true
			names = append(names, app.Application.Name)
		}
	}
	sort.Strings(names)

	totalCountMap := make([]int, len(steps))
	for _, name := range names {
		totalCountMap[appStepMap[name]]++
	}

	var out []rolloutInvalidMaxUpdate
	recorded := map[int]bool{}
	for _, name := range names {
		stepIdx := appStepMap[name]
		maxUpdate := steps[stepIdx].MaxUpdate
		if maxUpdate == nil || recorded[stepIdx] {
			continue
		}
		if _, err := intstr.GetScaledValueFromIntOrPercent(maxUpdate, totalCountMap[stepIdx], false); err != nil {
			recorded[stepIdx] = true
			out = append(out, rolloutInvalidMaxUpdate{stepIndex: stepIdx, maxUpdate: maxUpdate})
		}
	}
	return out
}

func (v *rolloutIssues) invalidMatchExpressionRecorded(stepIndex int, operator string) bool {
	for _, issue := range v.invalidMatchExpressions {
		if issue.stepIndex == stepIndex && issue.operator == operator {
			return true
		}
	}
	return false
}

// messages returns one upstream-formatted message per issue class in
// upstream's priority order. Argo CD's InvalidRolloutConfig condition shows
// only the first; drydock reports each class.
func (v *rolloutIssues) messages() []string {
	var out []string
	if len(v.invalidMatchExpressions) > 0 {
		out = append(out, v.formatInvalidMatchExpressionMessage())
	}
	if len(v.duplicateAppSelections) > 0 {
		out = append(out, v.formatDuplicateAppSelectionMessage())
	}
	if len(v.invalidMaxUpdates) > 0 {
		out = append(out, v.formatInvalidMaxUpdateMessage())
	}
	if len(v.emptySteps) > 0 {
		out = append(out, v.formatEmptyStepsMessage())
	}
	return out
}

func (v *rolloutIssues) formatInvalidMatchExpressionMessage() string {
	stepNums := make([]string, len(v.invalidMatchExpressions))
	operators := make([]string, len(v.invalidMatchExpressions))
	for i, issue := range v.invalidMatchExpressions {
		stepNums[i] = strconv.Itoa(issue.stepIndex + 1)
		operators[i] = issue.operator
	}
	if len(stepNums) == 1 {
		return fmt.Sprintf("Step %s has invalid matchExpression operators: %s. Supported Operators are 'In' and 'NotIn'", stepNums[0], strings.Join(operators, ", "))
	}
	return fmt.Sprintf("Steps %s have invalid matchExpression operators: %s. Supported Operators are 'In' and 'NotIn'", strings.Join(stepNums, ", "), strings.Join(operators, ", "))
}

func (v *rolloutIssues) formatDuplicateAppSelectionMessage() string {
	keys := slices.Sorted(maps.Keys(v.duplicateAppSelections))
	stepNums := make([]string, 0, len(keys))
	for _, key := range keys {
		steps := v.duplicateAppSelections[key]
		stepStrings := make([]string, len(steps))
		for i, step := range steps {
			stepStrings[i] = strconv.Itoa(step + 1)
		}
		stepNums = append(stepNums, strings.Join(stepStrings, "-"))
	}
	if len(keys) == 1 {
		return fmt.Sprintf("Application '%s' is selected by multiple steps: (%s)", keys[0], strings.Join(stepNums, ", "))
	}
	return fmt.Sprintf("Applications '%s' are selected by multiple steps: [(%s)]", strings.Join(keys, "', '"), strings.Join(stepNums, "), ("))
}

func (v *rolloutIssues) formatInvalidMaxUpdateMessage() string {
	stepNums := make([]string, len(v.invalidMaxUpdates))
	values := make([]string, len(v.invalidMaxUpdates))
	for i, issue := range v.invalidMaxUpdates {
		stepNums[i] = strconv.Itoa(issue.stepIndex + 1)
		values[i] = issue.maxUpdate.String()
	}
	if len(stepNums) == 1 {
		return fmt.Sprintf("Step %v has an invalid maxUpdate value: %v", strings.Join(stepNums, ", "), strings.Join(values, ", "))
	}
	return fmt.Sprintf("Steps %v have invalid maxUpdate values: [%v]", strings.Join(stepNums, ", "), strings.Join(values, ", "))
}

func (v *rolloutIssues) formatEmptyStepsMessage() string {
	stepNums := make([]string, len(v.emptySteps))
	for i, step := range v.emptySteps {
		stepNums[i] = strconv.Itoa(step + 1)
	}
	return fmt.Sprintf("Steps %v have no applications matching their criteria", stepNums)
}
