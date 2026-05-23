package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/change"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/diff"
	"github.com/home-operations/argocd-local/internal/manifest"
	"github.com/home-operations/argocd-local/internal/remote"
	"go.yaml.in/yaml/v4"
)

type DiffRequest struct {
	LeftPath               string
	RightPath              string
	ChangedOnly            bool
	StrictChangedOnly      bool
	Strict                 bool
	Unified                int
	Offline                bool
	RefreshCharts          bool
	ChartCacheDir          string
	RefreshRemoteResources bool
	RemoteResourceCacheDir string
}

type DiffAppRequest struct {
	DiffRequest
	Name string
}

type DiffResult struct {
	Results     []diff.Result
	Diagnostics []diagnostic.Diagnostic
}

type ImageDiffResult struct {
	Added       []string
	Removed     []string
	Unchanged   []string
	Diagnostics []diagnostic.Diagnostic
}

func (o Orchestrator) DiffApps(ctx context.Context, request DiffRequest) (DiffResult, error) {
	if err := validateDiffPaths(request); err != nil {
		return DiffResult{}, err
	}

	leftBuild, rightBuild, diagnostics, err := o.buildDiffSides(ctx, request)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}

	results, err := diffBuildResults(leftBuild, rightBuild, request.Unified)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	return DiffResult{Results: results, Diagnostics: diagnostics}, nil
}

func (o Orchestrator) DiffApp(ctx context.Context, request DiffAppRequest) (DiffResult, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return DiffResult{}, fmt.Errorf("application name is required")
	}
	if err := validateDiffPaths(request.DiffRequest); err != nil {
		return DiffResult{}, err
	}

	forbiddenRoots := []string{request.LeftPath, request.RightPath}
	if err := validateDiffRemoteCache(request.DiffRequest, forbiddenRoots); err != nil {
		return DiffResult{}, err
	}

	leftBuildRequest := request.DiffRequest.buildRequest(request.LeftPath, forbiddenRoots)
	rightBuildRequest := request.DiffRequest.buildRequest(request.RightPath, forbiddenRoots)

	var diagnostics []diagnostic.Diagnostic
	leftList, err := o.ListApplications(ctx, leftBuildRequest)
	diagnostics = append(diagnostics, leftList.Diagnostics...)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	rightList, err := o.ListApplications(ctx, rightBuildRequest)
	diagnostics = append(diagnostics, rightList.Diagnostics...)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}

	leftApp, leftOK, err := SelectOptionalApplicationByName(leftList.Applications, name)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	rightApp, rightOK, err := SelectOptionalApplicationByName(rightList.Applications, name)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	if !leftOK && !rightOK {
		return DiffResult{Diagnostics: diagnostics}, fmt.Errorf("application %q not found in either tree", name)
	}

	leftBuildRequest.Applications = selectedApplications(leftApp, leftOK)
	rightBuildRequest.Applications = selectedApplications(rightApp, rightOK)

	leftBuild, err := o.Build(ctx, leftBuildRequest)
	diagnostics = append(diagnostics, leftBuild.Diagnostics...)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	rightBuild, err := o.Build(ctx, rightBuildRequest)
	diagnostics = append(diagnostics, rightBuild.Diagnostics...)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}

	results, err := diffBuildResults(leftBuild, rightBuild, request.Unified)
	if err != nil {
		return DiffResult{Diagnostics: diagnostics}, err
	}
	return DiffResult{Results: results, Diagnostics: diagnostics}, nil
}

func (o Orchestrator) DiffImages(ctx context.Context, request DiffRequest) (ImageDiffResult, error) {
	if err := validateDiffPaths(request); err != nil {
		return ImageDiffResult{}, err
	}

	leftBuild, rightBuild, diagnostics, err := o.buildDiffSides(ctx, request)
	if err != nil {
		return ImageDiffResult{Diagnostics: diagnostics}, err
	}

	leftDocs, err := diffDocuments(leftBuild)
	if err != nil {
		return ImageDiffResult{Diagnostics: diagnostics}, err
	}
	rightDocs, err := diffDocuments(rightBuild)
	if err != nil {
		return ImageDiffResult{Diagnostics: diagnostics}, err
	}

	added, removed, unchanged := compareStringSets(diff.ExtractImages(leftDocs), diff.ExtractImages(rightDocs))
	return ImageDiffResult{
		Added:       added,
		Removed:     removed,
		Unchanged:   unchanged,
		Diagnostics: diagnostics,
	}, nil
}

func compareStringSets(left, right []string) (added, removed, unchanged []string) {
	leftIndex := 0
	rightIndex := 0
	for leftIndex < len(left) && rightIndex < len(right) {
		switch {
		case left[leftIndex] == right[rightIndex]:
			unchanged = append(unchanged, left[leftIndex])
			leftIndex++
			rightIndex++
		case left[leftIndex] < right[rightIndex]:
			removed = append(removed, left[leftIndex])
			leftIndex++
		default:
			added = append(added, right[rightIndex])
			rightIndex++
		}
	}
	removed = append(removed, left[leftIndex:]...)
	added = append(added, right[rightIndex:]...)
	return added, removed, unchanged
}

func (request DiffRequest) buildRequest(path string, forbiddenRoots []string) BuildRequest {
	return BuildRequest{
		Path:                         path,
		Strict:                       request.Strict,
		Offline:                      request.Offline,
		RefreshCharts:                request.RefreshCharts,
		ChartCacheDir:                request.ChartCacheDir,
		RefreshRemoteResources:       request.RefreshRemoteResources,
		RemoteResourceCacheDir:       request.RemoteResourceCacheDir,
		RemoteResourceForbiddenRoots: forbiddenRoots,
	}
}

func validateDiffPaths(request DiffRequest) error {
	if request.LeftPath == "" {
		return fmt.Errorf("--path-orig is required")
	}
	if request.RightPath == "" {
		return fmt.Errorf("--path is required")
	}
	return nil
}

func validateDiffRemoteCache(request DiffRequest, forbiddenRoots []string) error {
	if request.RemoteResourceCacheDir == "" {
		return nil
	}
	inside, root, err := remote.IsPathInsideAny(request.RemoteResourceCacheDir, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("remote resource cache dir %q must not be inside repository root %q", request.RemoteResourceCacheDir, root)
	}
	return nil
}

func selectedApplications(application argoappv1.Application, ok bool) []argoappv1.Application {
	if !ok {
		return []argoappv1.Application{}
	}
	return []argoappv1.Application{application}
}

func diffBuildResults(leftBuild, rightBuild BuildResult, unified int) ([]diff.Result, error) {
	leftDocs, err := diffDocuments(leftBuild)
	if err != nil {
		return nil, err
	}
	rightDocs, err := diffDocuments(rightBuild)
	if err != nil {
		return nil, err
	}
	return diff.Run(leftDocs, rightDocs, diff.Options{Unified: unified})
}

func (o Orchestrator) buildDiffSides(ctx context.Context, request DiffRequest) (BuildResult, BuildResult, []diagnostic.Diagnostic, error) {
	forbiddenRoots := []string{request.LeftPath, request.RightPath}
	if err := validateDiffRemoteCache(request, forbiddenRoots); err != nil {
		return BuildResult{}, BuildResult{}, nil, err
	}

	leftBuildRequest := request.buildRequest(request.LeftPath, forbiddenRoots)
	rightBuildRequest := request.buildRequest(request.RightPath, forbiddenRoots)

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
