package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/home-operations/argocd-local/internal/change"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/diff"
	"github.com/home-operations/argocd-local/internal/manifest"
	"go.yaml.in/yaml/v4"
)

type DiffRequest struct {
	LeftPath          string
	RightPath         string
	ChangedOnly       bool
	StrictChangedOnly bool
	Strict            bool
	Unified           int
	Offline           bool
	RefreshCharts     bool
	ChartCacheDir     string
}

type DiffResult struct {
	Results     []diff.Result
	Diagnostics []diagnostic.Diagnostic
}

func (o Orchestrator) DiffApps(ctx context.Context, request DiffRequest) (DiffResult, error) {
	if request.LeftPath == "" {
		return DiffResult{}, fmt.Errorf("--path-orig is required")
	}
	if request.RightPath == "" {
		return DiffResult{}, fmt.Errorf("--path is required")
	}

	leftBuild, rightBuild, diagnostics, err := o.buildDiffSides(ctx, request)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}

	leftDocs, err := diffDocuments(leftBuild)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	rightDocs, err := diffDocuments(rightBuild)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	results, err := diff.Run(leftDocs, rightDocs, diff.Options{Unified: request.Unified})
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	return DiffResult{Results: results, Diagnostics: diagnostics}, nil
}

func (o Orchestrator) buildDiffSides(ctx context.Context, request DiffRequest) (BuildResult, BuildResult, []diagnostic.Diagnostic, error) {
	leftBuildRequest := BuildRequest{
		Path:          request.LeftPath,
		Strict:        request.Strict,
		Offline:       request.Offline,
		RefreshCharts: request.RefreshCharts,
		ChartCacheDir: request.ChartCacheDir,
	}
	rightBuildRequest := leftBuildRequest
	rightBuildRequest.Path = request.RightPath

	var diagnostics []diagnostic.Diagnostic
	if request.ChangedOnly {
		leftList, err := o.ListApplications(ctx, leftBuildRequest)
		diagnostics = append(diagnostics, leftList.Diagnostics...)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		rightList, err := o.ListApplications(ctx, rightBuildRequest)
		diagnostics = append(diagnostics, rightList.Diagnostics...)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}

		changedPaths, err := change.Detect(request.LeftPath, request.RightPath)
		if err != nil {
			return BuildResult{}, BuildResult{}, diagnostics, err
		}
		leftSelected, _ := SelectChangedApplications(leftList.Applications, changedPaths)
		rightSelected, unowned := SelectChangedApplications(rightList.Applications, changedPaths)
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
	if err != nil {
		return leftBuild, BuildResult{}, diagnostics, err
	}
	rightBuild, err := o.Build(ctx, rightBuildRequest)
	diagnostics = append(diagnostics, rightBuild.Diagnostics...)
	if err != nil {
		return leftBuild, rightBuild, diagnostics, err
	}
	return leftBuild, rightBuild, diagnostics, nil
}

func diffDocuments(build BuildResult) ([]diff.Document, error) {
	docs := make([]diff.Document, 0, len(build.ApplicationManifests))
	for _, item := range build.ApplicationManifests {
		if item.Manifest.Object == nil {
			continue
		}
		id := manifest.IdentityOf(item.Manifest.Object)
		body, err := marshalDiffObject(item.Manifest.Object.Object)
		if err != nil {
			return nil, err
		}
		docs = append(docs, diff.Document{
			Parent: diff.Parent{
				Namespace:   item.Application.Namespace,
				Name:        item.Application.Name,
				SourceIndex: item.Manifest.SourceIndex,
				SourceName:  item.Manifest.SourceName,
				SourcePath:  item.Manifest.Path,
			},
			Resource: diff.Resource{
				Group:     id.Group,
				Kind:      id.Kind,
				Namespace: id.Namespace,
				Name:      id.Name,
			},
			Body: body,
		})
	}
	return docs, nil
}

func marshalDiffObject(obj map[string]any) (string, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(obj); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
