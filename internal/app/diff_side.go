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
)

func (o Orchestrator) buildDiffSides(ctx context.Context, request DiffRequest) (BuildResult, BuildResult, []diagnostic.Diagnostic, error) {
	forbiddenRoots := diffForbiddenRoots(request)
	if err := validateDiffCacheRoots(request, forbiddenRoots); err != nil {
		return BuildResult{}, BuildResult{}, nil, err
	}

	leftBuildRequest := request.buildRequest(request.LeftPath, forbiddenRoots)
	rightBuildRequest := request.buildRequest(request.RightPath, forbiddenRoots)
	leftBuildRequest.rootRevision = request.leftPathRevision
	rightBuildRequest.rootRevision = request.rightPathRevision
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
	if request.ChangedOnly {
		changedPaths, err := filteredChangedOnlyPaths(request)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		if len(changedPaths) == 0 {
			return BuildResult{}, BuildResult{}, diagnostics, nil
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

		leftApplications, rightApplications, selectionDiags, err := changedOnlyApplications(ctx, request, leftBuildRequest, rightBuildRequest, leftList.result, rightList.result, changedPaths)
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
	if err := errors.Join(leftBuild.err, rightBuild.err); err != nil {
		return leftBuild.result, rightBuild.result, diagnostics, err
	}
	return leftBuild.result, rightBuild.result, diagnostics, nil
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
		Message:  "Argo CD settings changed; rendering all Applications",
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
		Message:  "first AppProject declared or last AppProject removed on one side; rendering all Applications",
	}
}

// changedOnlyApplications returns the Applications each side of a
// changed-only diff renders: those the changed paths or changed AppProjects
// select, or all of them when the Argo CD settings changed, either side
// declares no AppProject while the other does, or a changed path is owned by
// no Application (an error under --strict-changed-only or --strict).
func changedOnlyApplications(ctx context.Context, request DiffRequest, leftBuildRequest, rightBuildRequest BuildRequest, leftList, rightList BuildResult, changedPaths []string) ([]argoappv1.Application, []argoappv1.Application, []diagnostic.Diagnostic, error) {
	// A settings change reaches every render, whichever Application owns the
	// file that carries it (a self-managed argocd Application, a sibling's
	// Kustomize graph): selecting only that owner would drop the rest.
	settingsChanged, err := argoSettingsChanged(leftList.Settings, rightList.Settings)
	if err != nil {
		return nil, nil, nil, err
	}
	if settingsChanged {
		return leftList.Applications, rightList.Applications, []diagnostic.Diagnostic{changedOnlySettingsDiagnostic()}, nil
	}

	// An AppProject change reaches every Application in the project through
	// project validation, whichever Application owns the file that carries
	// it. With project diagnostics off it reaches nothing a diff reports.
	var projectNames map[string]struct{}
	if request.ProjectDiagnosticsMode.Normalize() != diagnostic.ProjectDiagnosticsModeOff {
		var all bool
		projectNames, all = changedProjectNames(leftList.Projects, rightList.Projects)
		if all {
			return leftList.Applications, rightList.Applications, []diagnostic.Diagnostic{changedOnlyProjectsDiagnostic()}, nil
		}
	}

	// Each side owns the selection-only inputs of its own tree, so a base
	// file added or deleted by the change is owned by the side that has it.
	// Each side's self-repo facts decide which $ref roots are local.
	leftSide := withSelectionOnlyPaths(ctx, leftBuildRequest.Path, leftBuildRequest.RepoMaps, leftBuildRequest.selfRepo, leftList.ApplicationInputs)
	rightSide := withSelectionOnlyPaths(ctx, rightBuildRequest.Path, rightBuildRequest.RepoMaps, rightBuildRequest.selfRepo, rightList.ApplicationInputs)
	leftSelected, rightSelected, unowned := selectChangedDiffSides(leftSide, rightSide, changedPaths, projectNames)
	if len(unowned) == 0 {
		return leftSelected, rightSelected, nil, nil
	}
	diag := diagnostic.Diagnostic{
		Severity: diagnostic.SeverityWarning,
		Category: "changed-only",
		Message:  fmt.Sprintf("changed-only could not map %d changed path(s); rendering all Applications: %s", len(unowned), strings.Join(unowned, ", ")),
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
// goes unchecked (project.ValidateApplications), so every one is affected.
func changedProjectNames(left, right []argoappv1.AppProject) (names map[string]struct{}, all bool) {
	if (len(left) == 0) != (len(right) == 0) {
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

// argoSettingsChanged reports whether the two sides resolved different Argo
// CD settings (changedOnlySettingsSignature).
func argoSettingsChanged(left, right config.ArgoSettings) (bool, error) {
	leftSig, err := changedOnlySettingsSignature(left)
	if err != nil {
		return false, err
	}
	rightSig, err := changedOnlySettingsSignature(right)
	if err != nil {
		return false, err
	}
	return leftSig != rightSig, nil
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
