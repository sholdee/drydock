package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sholdee/drydock/internal/change"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func (o Orchestrator) buildDiffSides(ctx context.Context, request DiffRequest) (BuildResult, BuildResult, []diagnostic.Diagnostic, error) {
	forbiddenRoots := diffForbiddenRoots(request)
	if err := validateDiffCacheRoots(request, forbiddenRoots); err != nil {
		return BuildResult{}, BuildResult{}, nil, err
	}

	leftBuildRequest := request.buildRequest(request.LeftPath, forbiddenRoots)
	rightBuildRequest := request.buildRequest(request.RightPath, forbiddenRoots)

	var diagnostics []diagnostic.Diagnostic
	if request.ChangedOnly {
		changedPaths, err := filteredChangedOnlyPaths(request)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		if len(changedPaths) == 0 {
			return BuildResult{}, BuildResult{}, diagnostics, nil
		}

		leftList, err := o.ListApplications(ctx, leftBuildRequest)
		diagnostics = append(diagnostics, leftList.Diagnostics...)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		leftBuildRequest.renderCache = leftList.renderCache
		leftBuildRequest.renderSettingsSignature = leftList.renderSettingsSignature
		rightList, err := o.ListApplications(ctx, rightBuildRequest)
		diagnostics = append(diagnostics, rightList.Diagnostics...)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		rightBuildRequest.renderCache = rightList.renderCache
		rightBuildRequest.renderSettingsSignature = rightList.renderSettingsSignature

		leftSelected, leftUnowned := SelectChangedApplicationInputs(leftList.ApplicationInputs, changedPaths)
		rightSelected, rightUnowned := SelectChangedApplicationInputs(rightList.ApplicationInputs, changedPaths)
		unowned := unownedByNeitherSide(leftUnowned, rightUnowned)
		if len(unowned) > 0 {
			diag := diagnostic.Diagnostic{
				Severity: diagnostic.SeverityWarning,
				Category: "changed-only",
				Message:  fmt.Sprintf("changed-only could not map %d changed path(s); rendering all Applications: %s", len(unowned), strings.Join(unowned, ", ")),
			}
			diagnostics = append(diagnostics, diag)
			if request.StrictChangedOnly || request.Strict {
				diagnostics[len(diagnostics)-1].Severity = diagnostic.SeverityError
				return BuildResult{}, BuildResult{}, diagnostics, fmt.Errorf("changed-only input ownership incomplete")
			}
			leftBuildRequest.Applications = leftList.Applications
			rightBuildRequest.Applications = rightList.Applications
		} else {
			leftBuildRequest.Applications = leftSelected
			rightBuildRequest.Applications = rightSelected
		}
	}

	leftBuild, err := o.Build(ctx, leftBuildRequest)
	diagnostics = append(diagnostics, leftBuild.Diagnostics...)
	leftErr := err
	rightBuild, err := o.Build(ctx, rightBuildRequest)
	diagnostics = append(diagnostics, rightBuild.Diagnostics...)
	rightErr := err
	if err := errors.Join(leftErr, rightErr); err != nil {
		return leftBuild, rightBuild, diagnostics, err
	}
	return leftBuild, rightBuild, diagnostics, nil
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
