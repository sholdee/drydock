package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/change"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/project"
	"github.com/sholdee/drydock/internal/render"
)

func (o Orchestrator) buildDiffSides(ctx context.Context, request DiffRequest) (BuildResult, BuildResult, []diagnostic.Diagnostic, error) {
	forbiddenRoots := diffForbiddenRoots(request)
	if err := validateDiffCacheRoots(request, forbiddenRoots); err != nil {
		return BuildResult{}, BuildResult{}, nil, err
	}

	leftBuildRequest := request.buildRequest(request.LeftPath, forbiddenRoots)
	rightBuildRequest := request.buildRequest(request.RightPath, forbiddenRoots)
	leftBuildRequest, rightBuildRequest = request.withSideRoots(leftBuildRequest, rightBuildRequest)
	parallelism, err := normalizeParallelism(request.Parallelism)
	if err != nil {
		return BuildResult{}, BuildResult{}, nil, err
	}
	leftParallelism, rightParallelism, concurrent := splitSideParallelism(parallelism)
	leftBuildRequest.Parallelism = leftParallelism
	rightBuildRequest.Parallelism = rightParallelism
	snapshotSession, err := acquisition.NewSnapshotSession("drydock-cache-snapshots-*")
	if err != nil {
		return BuildResult{}, BuildResult{}, nil, err
	}
	defer snapshotSession.Close()
	leftBuildRequest.snapshotSession = snapshotSession
	rightBuildRequest.snapshotSession = snapshotSession

	var diagnostics []diagnostic.Diagnostic
	var leftList, rightList diffSideOutcome
	if request.ChangedOnly || selectsByLabels(request.LabelSelector) {
		var changedPaths []string
		if request.ChangedOnly {
			changedPaths, err = filteredChangedOnlyPaths(request)
			if err != nil {
				return BuildResult{}, BuildResult{}, diagnostics, err
			}
			if len(changedPaths) == 0 {
				return BuildResult{}, BuildResult{}, diagnostics, nil
			}
		}

		leftList, rightList = runDiffSidePair(ctx, concurrent, o.ListApplications, leftBuildRequest, rightBuildRequest)
		diagnostics = append(diagnostics, leftList.result.Diagnostics...)
		diagnostics = append(diagnostics, rightList.result.Diagnostics...)
		if err := errors.Join(leftList.err, rightList.err); err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		leftBuildRequest.PluginOptions = leftList.result.pluginOptions
		leftBuildRequest.renderCache = leftList.result.renderCache
		leftBuildRequest.renderSettingsSignature = leftList.result.renderSettingsSignature
		leftBuildRequest.discovered = leftList.result.discovered
		rightBuildRequest.PluginOptions = rightList.result.pluginOptions
		rightBuildRequest.renderCache = rightList.result.renderCache
		rightBuildRequest.renderSettingsSignature = rightList.result.renderSettingsSignature
		rightBuildRequest.discovered = rightList.result.discovered

		leftApplications, rightApplications, selectionDiags, err := o.diffSideApplications(ctx, request, leftBuildRequest, rightBuildRequest, leftList.result, rightList.result, changedPaths)
		diagnostics = append(diagnostics, selectionDiags...)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		leftBuildRequest.Applications = leftApplications
		rightBuildRequest.Applications = rightApplications
	}

	leftBuild, rightBuild := runDiffSidePair(ctx, concurrent, o.Build, leftBuildRequest, rightBuildRequest)
	leftBuild.result.CacheEvents = append(append([]cacheevent.Event(nil), leftList.result.CacheEvents...), leftBuild.result.CacheEvents...)
	rightBuild.result.CacheEvents = append(append([]cacheevent.Event(nil), rightList.result.CacheEvents...), rightBuild.result.CacheEvents...)
	diagnostics = append(diagnostics, leftBuild.result.Diagnostics...)
	diagnostics = append(diagnostics, rightBuild.result.Diagnostics...)
	diagnostics = append(diagnostics, request.hydratorDryChangeDiagnostics(ctx, leftBuildRequest, rightBuildRequest, leftBuild.result, rightBuild.result)...)
	if err := errors.Join(leftBuild.err, rightBuild.err); err != nil {
		return leftBuild.result, rightBuild.result, diagnostics, err
	}
	return leftBuild.result, rightBuild.result, diagnostics, nil
}

// diffSideApplications returns the Applications each side of a diff renders
// after both sides are listed: the changed-only selection
// (changedOnlyApplications) when request.ChangedOnly is set, or every listed
// Application, narrowed to the label selection (selectDiffSidesByLabels) when
// request.LabelSelector is set. A selector that matches no listed
// Application adds a SelectorNoMatchCode warning.
func (o Orchestrator) diffSideApplications(ctx context.Context, request DiffRequest, leftBuildRequest, rightBuildRequest BuildRequest, leftList, rightList BuildResult, changedPaths []string) ([]argoappv1.Application, []argoappv1.Application, []diagnostic.Diagnostic, error) {
	leftApplications, rightApplications := leftList.Applications, rightList.Applications
	var diagnostics []diagnostic.Diagnostic
	if request.ChangedOnly {
		var err error
		leftApplications, rightApplications, diagnostics, err = changedOnlyApplications(ctx, request, leftBuildRequest, rightBuildRequest, leftList, rightList, changedPaths, o.pluginRenderer(leftBuildRequest) != nil)
		if err != nil {
			return nil, nil, diagnostics, err
		}
	}
	if !selectsByLabels(request.LabelSelector) {
		return leftApplications, rightApplications, diagnostics, nil
	}
	diagnostics = selectedRenderAllDiagnostics(diagnostics)
	leftApplications, rightApplications, matched := selectDiffSidesByLabels(request.LabelSelector, leftList.Applications, rightList.Applications, leftApplications, rightApplications)
	if !matched {
		diagnostics = append(diagnostics, SelectorNoMatchDiagnostic(request.LabelSelector))
	}
	return leftApplications, rightApplications, diagnostics, nil
}

// renderAllApplications ends the message of each changed-only render-all
// warning. Under a label selector, the render-all renders only the selected
// Applications, so selectedRenderAllDiagnostics replaces it with
// renderAllSelectedApplications.
const (
	renderAllApplications         = "rendering all Applications"
	renderAllSelectedApplications = "rendering all selected Applications"
)

// selectedRenderAllDiagnostics returns diagnostics with each changed-only
// render-all message changed to say that only the selected Applications
// render.
func selectedRenderAllDiagnostics(diagnostics []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	out := make([]diagnostic.Diagnostic, 0, len(diagnostics))
	for _, diag := range diagnostics {
		if diag.Category == "changed-only" {
			diag.Message = strings.Replace(diag.Message, renderAllApplications, renderAllSelectedApplications, 1)
		}
		out = append(out, diag)
	}
	return out
}

// changedOnlySettingsCode identifies the changed-only render-all an Argo CD
// settings change forces. It is strict-exempt (strictExemptDiagnostic):
// rendering every Application is the complete answer, not an ownership gap,
// so neither --strict nor --strict-changed-only fails on it.
const changedOnlySettingsCode = "diff.changed-only-settings"

func changedOnlySettingsDiagnostic() diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     changedOnlySettingsCode,
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  "Argo CD settings changed; " + renderAllApplications,
	}
}

// changedOnlySettingsScopedCode identifies a changed-only selection that a
// repository or cluster settings change widened to the Applications it
// reaches (settingsDelta.selects). It is strict-exempt
// (strictExemptDiagnostic): the selection is complete, not an ownership gap.
// No severity below warning exists.
const changedOnlySettingsScopedCode = "diff.changed-only-settings-scoped"

func changedOnlySettingsScopedDiagnostic(selected int) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     changedOnlySettingsScopedCode,
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  fmt.Sprintf("Argo CD repository or cluster settings changed; selecting %d Application(s) that use them", selected),
	}
}

// changedOnlyProjectsCode identifies the changed-only render-all a changed
// AppProject population forces: the first AppProject declared, or the last
// one removed, on either side (changedProjectNames' all switch). It is
// strict-exempt (strictExemptDiagnostic) for the same reason as
// changedOnlySettingsCode: rendering every Application is the complete
// answer, not an ownership gap.
const changedOnlyProjectsCode = "diff.changed-only-projects"

func changedOnlyProjectsDiagnostic() diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     changedOnlyProjectsCode,
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  "first AppProject declared or last AppProject removed on one side; " + renderAllApplications,
	}
}

// changedOnlyJsonnetScanCode identifies the Applications that changed-only
// selects because the scan of their Jsonnet imports ran out of budget
// (render.ErrJsonnetImportScanBudget) or failed, or because an import path
// in their extVar or TLA code names an ARGOCD_APP_REVISION variable, whose
// value selection does not know. It is strict-exempt
// (strictExemptDiagnostic): selecting them is the safe answer, not an
// ownership gap.
const changedOnlyJsonnetScanCode = "diff.changed-only-jsonnet-scan"

func changedOnlyJsonnetScanDiagnostic(names []string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     changedOnlyJsonnetScanCode,
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  fmt.Sprintf("the Jsonnet imports of %d Application(s) could not be scanned within the scan budget, could not be read, or name an ARGOCD_APP_REVISION variable in extVar or TLA code, so changed-only renders them on every change: %s", len(names), strings.Join(names, ", ")),
	}
}

// changedOnlySymlinksCode identifies the changed-only render-all that a
// change of the out-of-bounds symlink verdict of the tree under diff forces.
// It is strict-exempt (strictExemptDiagnostic) for the same reason as
// changedOnlySettingsCode.
const changedOnlySymlinksCode = "diff.changed-only-symlinks"

// changedOnlySymlinksUnknownDiagnostic is the changedOnlySymlinksCode
// render-all for a tree that drydock could not walk: each source that renders
// from it then fails on its own, as it does in a full diff.
func changedOnlySymlinksUnknownDiagnostic(err error) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     changedOnlySymlinksCode,
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  fmt.Sprintf("could not check the repository for out-of-bounds symlinks (%v); %s", err, renderAllApplications),
	}
}

func changedOnlySymlinksDiagnostic(left, right string) diagnostic.Diagnostic {
	describe := func(file string) string {
		if file == "" {
			return "none"
		}
		return file
	}
	return diagnostic.Diagnostic{
		Code:     changedOnlySymlinksCode,
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  fmt.Sprintf("the first out-of-bounds symlink of the repository changed (left: %s, right: %s); Argo CD fails every Application that renders from a repository with one, so %s", describe(left), describe(right), renderAllApplications),
	}
}

// outOfBoundsSymlinkVerdicts returns the first out-of-bounds symlink of the
// tree of each side, or "" for a side that has none or allows them.
// A side whose root is a snapshot of a Git ref (rootRevision set) is not a
// user root, so Git ignore rules hide nothing in it (userRootSet).
func outOfBoundsSymlinkVerdicts(ctx context.Context, memo *symlinkBoundsMemo, left, right BuildRequest, leftSettings, rightSettings config.ArgoSettings) (string, string, error) {
	verdict := func(side BuildRequest, settings config.ArgoSettings) (string, error) {
		if allowOutOfBoundsSymlinks(settings) {
			return "", nil
		}
		return memo.findOutOfBoundsSymlink(ctx, side.Path, render.OutOfBoundsSymlinkScopeRepository, strings.TrimSpace(side.rootRevision) == "")
	}
	leftFile, err := verdict(left, leftSettings)
	if err != nil {
		return "", "", err
	}
	rightFile, err := verdict(right, rightSettings)
	return leftFile, rightFile, err
}

// changedOnlySymlinksRenderAll reports whether a changed-only diff renders
// every Application because the out-of-bounds symlink verdict of the tree
// under diff differs between the sides, or cannot be read. A tree that
// cannot be walked does not stop the diff: each source that renders from it
// then fails on its own, as in a full diff. Only a canceled context is an
// error.
func changedOnlySymlinksRenderAll(ctx context.Context, memo *symlinkBoundsMemo, leftRequest, rightRequest BuildRequest, leftSettings, rightSettings config.ArgoSettings) (bool, diagnostic.Diagnostic, error) {
	left, right, err := outOfBoundsSymlinkVerdicts(ctx, memo, leftRequest, rightRequest, leftSettings, rightSettings)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, diagnostic.Diagnostic{}, ctxErr
		}
		return true, changedOnlySymlinksUnknownDiagnostic(err), nil
	}
	if left != right {
		return true, changedOnlySymlinksDiagnostic(left, right), nil
	}
	return false, diagnostic.Diagnostic{}, nil
}

// changedOnlyApplications returns the Applications each side of a
// changed-only diff renders: those the changed paths, changed AppProjects, or
// changed repository and cluster settings select, or all of them when a
// render-affecting Argo CD setting changed, either side declares no
// AppProject while the other does, or a changed path is owned by no
// Application (an error under --strict-changed-only or --strict).
func changedOnlyApplications(ctx context.Context, request DiffRequest, leftBuildRequest, rightBuildRequest BuildRequest, leftList, rightList BuildResult, changedPaths []string, injectedPlugins bool) ([]argoappv1.Application, []argoappv1.Application, []diagnostic.Diagnostic, error) {
	// A render-affecting settings change reaches every render, whichever
	// Application owns the file that carries it (a self-managed argocd
	// Application, a sibling's Kustomize graph): selecting only that owner
	// would drop the rest. A repository or cluster entry reaches only the
	// Applications that use it, selected below.
	settings, err := classifySettingsChange(leftList.Settings, rightList.Settings)
	if err != nil {
		return nil, nil, nil, err
	}
	if settings.renderAll {
		return leftList.Applications, rightList.Applications, []diagnostic.Diagnostic{changedOnlySettingsDiagnostic()}, nil
	}

	// An out-of-bounds symlink anywhere in the tree fails every Application
	// that renders from it, whichever path holds it, and a changed-only
	// ignore glob may hide that path. A change of the first such symlink
	// reaches nearly every Application, so render them all.
	if renderAll, diag, err := changedOnlySymlinksRenderAll(ctx, leftBuildRequest.symlinkBounds, leftBuildRequest, rightBuildRequest, leftList.Settings, rightList.Settings); err != nil {
		return nil, nil, nil, err
	} else if renderAll {
		return leftList.Applications, rightList.Applications, []diagnostic.Diagnostic{diag}, nil
	}

	// An AppProject change reaches every Application in the project through
	// project validation, whichever Application owns the file that carries
	// it. With project diagnostics off it reaches nothing a diff reports.
	var projectNames map[string]struct{}
	if request.ProjectDiagnosticsMode.Normalize() != diagnostic.ProjectDiagnosticsModeOff {
		var all bool
		projectNames, all = changedProjectNames(leftList.Projects, rightList.Projects, leftList.projectsDeclared, rightList.projectsDeclared)
		if all {
			return leftList.Applications, rightList.Applications, []diagnostic.Diagnostic{changedOnlyProjectsDiagnostic()}, nil
		}
	}

	// Each side owns the selection-only inputs of its own tree, so a base
	// file added or deleted by the change is owned by the side that has it.
	// Each side's self-repo facts decide which $ref roots are local.
	leftSide := withSelectionOnlyPathsIn(ctx, leftBuildRequest.Path, leftBuildRequest.RepoMaps, leftBuildRequest.selfRepo, sourceTypeGatesFromSettings(leftList.Settings), leftList.Settings.ControllerNamespace(""), leftList.ApplicationInputs)
	rightSide := withSelectionOnlyPathsIn(ctx, rightBuildRequest.Path, rightBuildRequest.RepoMaps, rightBuildRequest.selfRepo, sourceTypeGatesFromSettings(rightList.Settings), rightList.Settings.ControllerNamespace(""), rightList.ApplicationInputs)
	leftSelected, rightSelected, unowned := selectChangedDiffSides(leftSide, rightSide, changedPaths, projectNames)
	var scanDiags []diagnostic.Diagnostic
	var unscanned []string
	for _, application := range append(append([]argoappv1.Application(nil), leftSide.unscanned...), rightSide.unscanned...) {
		unscanned = append(unscanned, applicationDisplayName(application))
	}
	if unscanned = dedupeSortedStrings(unscanned); len(unscanned) > 0 {
		scanDiags = append(scanDiags, changedOnlyJsonnetScanDiagnostic(unscanned))
	}
	if len(unowned) == 0 {
		leftSelected, rightSelected, reached := withSettingsSelection(leftSide, rightSide, leftSelected, rightSelected, settings)
		if reached > 0 {
			scanDiags = append(scanDiags, changedOnlySettingsScopedDiagnostic(reached))
		}
		leftSelected, rightSelected, readers := withAppRevisionSelection(leftBuildRequest, rightBuildRequest, leftSide, rightSide, leftList.Settings, rightList.Settings, leftSelected, rightSelected, injectedPlugins)
		if readers > 0 {
			scanDiags = append(scanDiags, changedOnlyAppRevisionDiagnostic(readers))
		}
		return leftSelected, rightSelected, scanDiags, nil
	}
	diag := diagnostic.Diagnostic{
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  fmt.Sprintf("changed-only could not map %d changed path(s); %s: %s", len(unowned), renderAllApplications, strings.Join(unowned, ", ")),
	}
	if request.StrictChangedOnly || request.Strict {
		diag.Severity = diagnostic.SeverityError
		return nil, nil, []diagnostic.Diagnostic{diag}, fmt.Errorf("changed-only input ownership incomplete")
	}
	return leftList.Applications, rightList.Applications, []diagnostic.Diagnostic{diag}, nil
}

// changedProjectNames returns the names of the AppProjects that validate
// differently on the two sides: added, removed, or with a changed namespace
// or spec, indexed as validation indexes them (project.ByName). all reports
// that exactly one side declares no AppProject: there every Application
// validates against the implicit default project and repository metadata
// goes unchecked (project.ValidateDeclaredApplications), so every one is
// affected. A side declares AppProjects also when --argocd-namespace skips
// all of them (BuildResult.projectsDeclared).
func changedProjectNames(left, right []argoappv1.AppProject, leftDeclared, rightDeclared bool) (names map[string]struct{}, all bool) {
	if (leftDeclared || len(left) > 0) != (rightDeclared || len(right) > 0) {
		return nil, true
	}
	leftFingerprints := projectFingerprints(left)
	rightFingerprints := projectFingerprints(right)
	names = map[string]struct{}{}
	for name, fingerprint := range leftFingerprints {
		if rightFingerprints[name] != fingerprint {
			names[name] = struct{}{}
		}
	}
	for name := range rightFingerprints {
		if _, ok := leftFingerprints[name]; !ok {
			names[name] = struct{}{}
		}
	}
	return names, false
}

// projectFingerprints fingerprints, by name, what validation reads of each
// AppProject: its namespace (the controller namespace for source namespace
// checks) and its spec.
func projectFingerprints(projects []argoappv1.AppProject) map[string]string {
	index := project.ByName(projects)
	fingerprints := make(map[string]string, len(index))
	for name, proj := range index {
		fingerprints[name] = objectContentFingerprint(name, struct {
			Namespace string
			Spec      argoappv1.AppProjectSpec
		}{proj.Namespace, proj.Spec})
	}
	return fingerprints
}

func filteredChangedOnlyPaths(request DiffRequest) ([]string, error) {
	filter, err := changedOnlyPathFilter(request)
	if err != nil {
		return nil, err
	}
	changedPaths := request.changedPaths
	if changedPaths == nil {
		changedPaths, err = change.Detect(request.LeftPath, request.RightPath)
		if err != nil {
			return nil, err
		}
	}
	return filter.Apply(changedPaths).Paths, nil
}

func changedOnlyPathFilter(request DiffRequest) (change.PathFilter, error) {
	return change.NewPathFilter(change.PathFilterConfig{
		Includes: request.ChangedOnlyIncludeGlobs,
		Ignores:  request.ChangedOnlyIgnoreGlobs,
	})
}

// selectChangedDiffSides selects, on each side, every Application that either
// side's inputs select, plus the changed paths neither side owns. Ownership
// can differ per tree — a file the change deletes is only in the left tree's
// Kustomize graph — and rendering an Application on one side only would
// report a false all-added or all-deleted diff instead of the real change or
// render failure. A rendered directory whose Kustomize graph a changed path
// intersects owns that path on its side and selects every Application it
// declared, on either side. Every Application a selected one renders is
// selected too, transitively: whatever changes the parent's render can
// rewrite the child's spec. Unowned paths are unaffected: every path a child
// reaches through its parent is owned by that parent already. Every
// Application in one of changedProjects is selected last, on either side:
// the AppProject reaches its validation, not its render or its children's.
func selectChangedDiffSides(left, right selectionSide, changedPaths []string, changedProjects map[string]struct{}) ([]argoappv1.Application, []argoappv1.Application, []string) {
	selectedKeys := map[string]struct{}{}
	changedDirs := map[string]struct{}{}
	var unowned [2][]string
	for i, side := range []selectionSide{left, right} {
		selected, sideUnowned := SelectChangedApplicationInputs(side.inputs, changedPaths)
		for _, application := range selected {
			selectedKeys[applicationKey(application)] = struct{}{}
		}
		unowned[i] = side.selectChangedRenderedDirs(changedDirs, changedPaths, sideUnowned)
	}
	for _, inputs := range [][]ApplicationSelectionInput{left.inputs, right.inputs} {
		for _, input := range inputs {
			if _, ok := changedDirs[input.RenderedDir]; ok {
				selectedKeys[applicationKey(input.Application)] = struct{}{}
			}
		}
	}
	// An Application whose Jsonnet imports could not be scanned can read
	// any changed path, so a change selects it. It is selected here, not
	// through its Paths, so that changed paths it does not own still count
	// as unowned.
	if len(changedPaths) > 0 {
		for _, application := range append(append([]argoappv1.Application(nil), left.unscanned...), right.unscanned...) {
			selectedKeys[applicationKey(application)] = struct{}{}
		}
	}
	selectRenderedDescendants(selectedKeys, left.inputs, right.inputs)
	for _, inputs := range [][]ApplicationSelectionInput{left.inputs, right.inputs} {
		for _, input := range inputs {
			if _, ok := changedProjects[input.Application.Spec.GetProject()]; ok {
				selectedKeys[applicationKey(input.Application)] = struct{}{}
			}
		}
	}
	return selectedByKey(left.inputs, selectedKeys), selectedByKey(right.inputs, selectedKeys), unownedByNeitherSide(unowned[0], unowned[1])
}

// selectChangedRenderedDirs adds to dirs every rendered directory whose
// Kustomize graph intersects a changed path, and returns unowned without the
// paths those graphs own.
func (s selectionSide) selectChangedRenderedDirs(dirs map[string]struct{}, changedPaths, unowned []string) []string {
	owned := map[string]struct{}{}
	for _, changedPath := range changedPaths {
		normalizedChanged := normalizeSelectPath(changedPath)
		for dir, graph := range s.renderedDirs {
			if slices.ContainsFunc(graph, func(graphPath string) bool {
				return pathIntersects(normalizeSelectPath(graphPath), normalizedChanged)
			}) {
				dirs[dir] = struct{}{}
				owned[normalizedChanged] = struct{}{}
			}
		}
	}
	remaining := make([]string, 0, len(unowned))
	for _, changedPath := range unowned {
		if _, ok := owned[changedPath]; !ok {
			remaining = append(remaining, changedPath)
		}
	}
	return remaining
}

// selectRenderedDescendants adds to keys every Application whose recorded
// parent (ParentKey) is in keys, on either side, until none is left: a
// nested app-of-apps selects its grandchildren, and a cycle or self-parent
// ends because each key is added once.
func selectRenderedDescendants(keys map[string]struct{}, sides ...[]ApplicationSelectionInput) {
	children := map[string][]string{}
	for _, inputs := range sides {
		// A child is rendered by an Application of its own side.
		parents := newParentResolver(inputs)
		for _, input := range inputs {
			if input.ParentKey == "" {
				continue
			}
			child := applicationKey(input.Application)
			for _, parent := range parents.resolve(input.ParentKey) {
				children[parent] = append(children[parent], child)
			}
		}
	}
	pending := slices.Collect(maps.Keys(keys))
	for len(pending) > 0 {
		key := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, child := range children[key] {
			if _, ok := keys[child]; !ok {
				keys[child] = struct{}{}
				pending = append(pending, child)
			}
		}
	}
}

// parentResolver resolves a recorded ParentKey to the Applications of one
// side it can name.
type parentResolver struct {
	keys map[string]struct{}
	// namespaces lists the namespaces each Application name appears in.
	namespaces map[string][]string
}

func newParentResolver(inputs []ApplicationSelectionInput) parentResolver {
	resolver := parentResolver{keys: map[string]struct{}{}, namespaces: map[string][]string{}}
	for _, input := range inputs {
		key := applicationKey(input.Application)
		if _, ok := resolver.keys[key]; ok {
			continue
		}
		resolver.keys[key] = struct{}{}
		resolver.namespaces[input.Application.Name] = append(resolver.namespaces[input.Application.Name], input.Application.Namespace)
	}
	return resolver
}

// resolve returns parentKey when an input carries it. Otherwise it mirrors
// namespaceDefaultedConflict: a discovery merge can replace the parent with a
// twin whose namespace was defaulted, so it returns every same-name
// Application where exactly one of the two namespaces is empty. When several
// match it returns them all: selecting too much is safe.
func (r parentResolver) resolve(parentKey string) []string {
	if _, ok := r.keys[parentKey]; ok {
		return []string{parentKey}
	}
	// applicationKey is namespace + "\x00" + name.
	namespace, name, ok := strings.Cut(parentKey, "\x00")
	if !ok {
		return nil
	}
	var keys []string
	for _, candidate := range r.namespaces[name] {
		if candidate != namespace && (candidate == "" || namespace == "") {
			keys = append(keys, applicationKey(argoappv1.Application{Namespace: candidate, Name: name}))
		}
	}
	return keys
}

func selectedByKey(inputs []ApplicationSelectionInput, keys map[string]struct{}) []argoappv1.Application {
	selected := make([]argoappv1.Application, 0, len(keys))
	for _, input := range inputs {
		if _, ok := keys[applicationKey(input.Application)]; ok {
			selected = append(selected, input.Application)
		}
	}
	return selected
}

func unownedByNeitherSide(leftUnowned, rightUnowned []string) []string {
	left := make(map[string]struct{}, len(leftUnowned))
	for _, changedPath := range leftUnowned {
		left[changedPath] = struct{}{}
	}

	var unowned []string
	for _, changedPath := range rightUnowned {
		if _, ok := left[changedPath]; ok {
			unowned = append(unowned, changedPath)
		}
	}
	return unowned
}

// withSideRoots records on each side's request the tree it renders: the
// snapshot revision of a side that renders a Git ref, and the repository
// the snapshot came from.
func (request DiffRequest) withSideRoots(left, right BuildRequest) (BuildRequest, BuildRequest) {
	left.rootRevision = request.leftPathRevision
	right.rootRevision = request.rightPathRevision
	left.rootRepo = request.Repo
	right.rootRepo = request.Repo
	left.rootSnapshot = request.leftSnapshot
	right.rootSnapshot = request.rightSnapshot
	return left, right
}
