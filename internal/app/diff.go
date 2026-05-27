package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/change"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
	"github.com/sholdee/drydock/internal/gitref"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	"go.yaml.in/yaml/v4"
)

type DiffRequest struct {
	LeftPath                       string
	RightPath                      string
	Repo                           string
	Ref                            string
	RefOrig                        string
	DiscoverKustomizePaths         []string
	ChangedOnly                    bool
	StrictChangedOnly              bool
	Strict                         bool
	Unified                        int
	StripAttrs                     []string
	ShowIgnoredFields              bool
	Offline                        bool
	RefreshCharts                  bool
	ChartCacheDir                  string
	ChartCredentials               chart.ChartCredentials
	RepoMaps                       []sourcepkg.RepoMap
	GitCacheDir                    string
	RefreshGit                     bool
	GitCredentials                 sourcepkg.GitCredentials
	RefreshRemoteResources         bool
	RemoteResourceCacheDir         string
	RemoteResourceCredentials      remote.Credentials
	RemoteResourceGitCredentials   remote.GitCredentials
	PluginTimeout                  time.Duration
	Parallelism                    int
	SkipKinds                      []string
	SkipCRDs                       bool
	SkipSecrets                    bool
	ApplicationSetProviderFixtures []string
	ApplicationSetProviderData     appset.ProviderData
	RecordCacheEvents              bool

	changedPaths []string
}

type DiffAppRequest struct {
	DiffRequest
	Name string
}

type DiffResult struct {
	Results     []diff.Result
	Diagnostics []diagnostic.Diagnostic
	CacheEvents []cacheevent.Event
}

type ImageDiffResult struct {
	Added       []string
	Removed     []string
	Unchanged   []string
	Diagnostics []diagnostic.Diagnostic
	CacheEvents []cacheevent.Event
}

func (o Orchestrator) DiffApps(ctx context.Context, request DiffRequest) (DiffResult, error) {
	request, cleanup, err := resolveDiffRequestPaths(ctx, request, true)
	if err != nil {
		return DiffResult{}, err
	}
	defer func() {
		// Git ref snapshots live under OS temp; cleanup must not turn a valid diff
		// result into a command failure.
		_ = cleanup()
	}()

	if err := validateDiffPaths(request); err != nil {
		return DiffResult{}, err
	}

	leftBuild, rightBuild, diagnostics, err := o.buildDiffSides(ctx, request)
	cacheEvents := cacheEventsFromBuilds(leftBuild, rightBuild)
	buildErr := err
	if buildErr != nil && !hasRenderedDiffInput(leftBuild, rightBuild) {
		return DiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}

	results, diffErr := diffBuildResults(leftBuild, rightBuild, diff.Options{
		Unified:           request.Unified,
		StripAttrs:        request.StripAttrs,
		ShowIgnoredFields: request.ShowIgnoredFields,
	})
	if err := errors.Join(buildErr, diffErr); err != nil {
		return DiffResult{Results: results, Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}
	return DiffResult{Results: results, Diagnostics: diagnostics, CacheEvents: cacheEvents}, nil
}

func (o Orchestrator) DiffApp(ctx context.Context, request DiffAppRequest) (DiffResult, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return DiffResult{}, fmt.Errorf("application name is required")
	}
	diffRequest, cleanup, err := resolveDiffRequestPaths(ctx, request.DiffRequest, false)
	if err != nil {
		return DiffResult{}, err
	}
	request.DiffRequest = diffRequest
	defer func() {
		// Git ref snapshots live under OS temp; cleanup must not turn a valid diff
		// result into a command failure.
		_ = cleanup()
	}()

	if err := validateDiffPaths(request.DiffRequest); err != nil {
		return DiffResult{}, err
	}

	forbiddenRoots := diffForbiddenRoots(request.DiffRequest)
	if err := validateDiffRemoteCache(request.DiffRequest, forbiddenRoots); err != nil {
		return DiffResult{}, err
	}

	leftBuildRequest := request.buildRequest(request.LeftPath, forbiddenRoots)
	rightBuildRequest := request.buildRequest(request.RightPath, forbiddenRoots)

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
	leftErr := err
	rightBuild, err := o.Build(ctx, rightBuildRequest)
	diagnostics = append(diagnostics, rightBuild.Diagnostics...)
	rightErr := err
	cacheEvents := cacheEventsFromBuilds(leftBuild, rightBuild)
	if err := errors.Join(leftErr, rightErr); err != nil {
		return DiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}

	results, err := diffBuildResults(leftBuild, rightBuild, diff.Options{
		Unified:           request.Unified,
		StripAttrs:        request.StripAttrs,
		ShowIgnoredFields: request.ShowIgnoredFields,
	})
	if err != nil {
		return DiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}
	return DiffResult{Results: results, Diagnostics: diagnostics, CacheEvents: cacheEvents}, nil
}

func (o Orchestrator) DiffImages(ctx context.Context, request DiffRequest) (ImageDiffResult, error) {
	request, cleanup, err := resolveDiffRequestPaths(ctx, request, true)
	if err != nil {
		return ImageDiffResult{}, err
	}
	defer func() {
		// Git ref snapshots live under OS temp; cleanup must not turn a valid diff
		// result into a command failure.
		_ = cleanup()
	}()

	if err := validateDiffPaths(request); err != nil {
		return ImageDiffResult{}, err
	}

	leftBuild, rightBuild, diagnostics, err := o.buildDiffSides(ctx, request)
	cacheEvents := cacheEventsFromBuilds(leftBuild, rightBuild)
	buildErr := err
	if buildErr != nil && !hasRenderedDiffInput(leftBuild, rightBuild) {
		return ImageDiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}

	leftDocs, err := diffDocuments(leftBuild)
	if err != nil {
		return ImageDiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}
	rightDocs, err := diffDocuments(rightBuild)
	if err != nil {
		return ImageDiffResult{Diagnostics: diagnostics, CacheEvents: cacheEvents}, err
	}

	added, removed, unchanged := compareStringSets(diff.ExtractImages(leftDocs), diff.ExtractImages(rightDocs))
	return ImageDiffResult{
		Added:       added,
		Removed:     removed,
		Unchanged:   unchanged,
		Diagnostics: diagnostics,
		CacheEvents: cacheEvents,
	}, buildErr
}

func cacheEventsFromBuilds(leftBuild, rightBuild BuildResult) []cacheevent.Event {
	out := make([]cacheevent.Event, 0, len(leftBuild.CacheEvents)+len(rightBuild.CacheEvents))
	out = append(out, leftBuild.CacheEvents...)
	out = append(out, rightBuild.CacheEvents...)
	return out
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

func resolveDiffRequestPaths(ctx context.Context, request DiffRequest, computeChangedPaths bool) (DiffRequest, func() error, error) {
	var cleanups []func() error
	cleanup := func() error {
		var err error
		for i := len(cleanups) - 1; i >= 0; i-- {
			err = errors.Join(err, cleanups[i]())
		}
		return err
	}

	repoPath := strings.TrimSpace(request.Repo)
	if repoPath == "" {
		repoPath = request.RightPath
	}
	hasRef := strings.TrimSpace(request.Ref) != "" || strings.TrimSpace(request.RefOrig) != ""
	if err := validateDiffRefOptions(request, hasRef); err != nil {
		return request, cleanup, err
	}
	if hasRef {
		request.Repo = repoPath
	}

	if computeChangedPaths && request.ChangedOnly {
		changedPaths, ok, err := gitRefChangedPaths(ctx, request, repoPath)
		if err != nil {
			return request, cleanup, err
		}
		if ok {
			request.changedPaths = changedPaths
		}
	}

	forbiddenRoots := []string{request.LeftPath, request.RightPath, repoPath}
	if strings.TrimSpace(request.RefOrig) != "" {
		result, err := gitref.Snapshot(ctx, gitref.Request{
			Repo:           repoPath,
			Ref:            request.RefOrig,
			ForbiddenRoots: forbiddenRoots,
		})
		if err != nil {
			return request, cleanup, err
		}
		request.LeftPath = result.Path
		cleanups = append(cleanups, result.Cleanup)
		forbiddenRoots = append(forbiddenRoots, result.Path)
	}
	if strings.TrimSpace(request.Ref) != "" {
		result, err := gitref.Snapshot(ctx, gitref.Request{
			Repo:           repoPath,
			Ref:            request.Ref,
			ForbiddenRoots: forbiddenRoots,
		})
		if err != nil {
			return request, cleanup, errors.Join(err, cleanup())
		}
		request.RightPath = result.Path
		cleanups = append(cleanups, result.Cleanup)
	}
	return request, cleanup, nil
}

func validateDiffRefOptions(request DiffRequest, hasRef bool) error {
	if strings.TrimSpace(request.Repo) != "" && !hasRef {
		return fmt.Errorf("--repo requires --ref or --ref-orig")
	}
	if strings.TrimSpace(request.RefOrig) != "" && strings.TrimSpace(request.LeftPath) != "" {
		return fmt.Errorf("--ref-orig cannot be combined with --path-orig")
	}
	return nil
}

func gitRefChangedPaths(ctx context.Context, request DiffRequest, repoPath string) ([]string, bool, error) {
	refOrig := strings.TrimSpace(request.RefOrig)
	if refOrig == "" {
		return nil, false, nil
	}
	ref := strings.TrimSpace(request.Ref)
	if ref != "" {
		changedPaths, err := gitref.ChangedPathsBetweenRefs(ctx, repoPath, refOrig, ref)
		return changedPaths, true, err
	}
	if !sameLocalPath(repoPath, request.RightPath) {
		return nil, false, nil
	}
	changedPaths, err := gitref.ChangedPathsFromRefToWorktree(ctx, repoPath, refOrig)
	return changedPaths, true, err
}

func sameLocalPath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(leftAbs); err == nil {
		leftAbs = resolved
	}
	if resolved, err := filepath.EvalSymlinks(rightAbs); err == nil {
		rightAbs = resolved
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func (request DiffRequest) buildRequest(path string, forbiddenRoots []string) BuildRequest {
	return BuildRequest{
		Path:                           path,
		Strict:                         request.Strict,
		DiscoverKustomizePaths:         append([]string(nil), request.DiscoverKustomizePaths...),
		Offline:                        request.Offline,
		RefreshCharts:                  request.RefreshCharts,
		ChartCacheDir:                  request.ChartCacheDir,
		ChartCredentials:               request.ChartCredentials,
		RepoMaps:                       request.RepoMaps,
		GitCacheDir:                    request.GitCacheDir,
		RefreshGit:                     request.RefreshGit,
		GitCredentials:                 request.GitCredentials,
		RefreshRemoteResources:         request.RefreshRemoteResources,
		RemoteResourceCacheDir:         request.RemoteResourceCacheDir,
		RemoteResourceCredentials:      request.RemoteResourceCredentials,
		RemoteResourceGitCredentials:   request.RemoteResourceGitCredentials,
		RemoteResourceForbiddenRoots:   forbiddenRoots,
		PluginTimeout:                  request.PluginTimeout,
		Parallelism:                    request.Parallelism,
		SkipKinds:                      append([]string(nil), request.SkipKinds...),
		SkipCRDs:                       request.SkipCRDs,
		SkipSecrets:                    request.SkipSecrets,
		ApplicationSetProviderFixtures: append([]string(nil), request.ApplicationSetProviderFixtures...),
		ApplicationSetProviderData:     request.ApplicationSetProviderData,
		RecordCacheEvents:              request.RecordCacheEvents,
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
	inside, root, err := pathsafety.IsInsideAny(request.RemoteResourceCacheDir, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("remote resource cache dir %q must not be inside repository root %q", request.RemoteResourceCacheDir, root)
	}
	return nil
}

func diffForbiddenRoots(request DiffRequest) []string {
	forbiddenRoots := []string{request.LeftPath, request.RightPath}
	return appendUniqueString(forbiddenRoots, request.Repo)
}

func selectedApplications(application argoappv1.Application, ok bool) []argoappv1.Application {
	if !ok {
		return []argoappv1.Application{}
	}
	return []argoappv1.Application{application}
}

func diffBuildResults(leftBuild, rightBuild BuildResult, opts diff.Options) ([]diff.Result, error) {
	leftDocs, err := diffDocuments(leftBuild)
	if err != nil {
		return nil, err
	}
	rightDocs, err := diffDocuments(rightBuild)
	if err != nil {
		return nil, err
	}
	return diff.Run(leftDocs, rightDocs, opts)
}

func hasRenderedDiffInput(leftBuild, rightBuild BuildResult) bool {
	return len(leftBuild.ApplicationManifests) > 0 || len(rightBuild.ApplicationManifests) > 0
}

func (o Orchestrator) buildDiffSides(ctx context.Context, request DiffRequest) (BuildResult, BuildResult, []diagnostic.Diagnostic, error) {
	forbiddenRoots := diffForbiddenRoots(request)
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

		changedPaths := request.changedPaths
		if changedPaths == nil {
			var err error
			changedPaths, err = change.Detect(request.LeftPath, request.RightPath)
			if err != nil {
				return BuildResult{}, BuildResult{}, diagnostics, err
			}
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
	leftErr := err
	rightBuild, err := o.Build(ctx, rightBuildRequest)
	diagnostics = append(diagnostics, rightBuild.Diagnostics...)
	rightErr := err
	if err := errors.Join(leftErr, rightErr); err != nil {
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
			Body:          body,
			Normalization: normalizationFor(item.Application, id, build.Settings),
		})
	}
	return docs, nil
}

func normalizationFor(application argoappv1.Application, id manifest.Identity, settings config.ArgoSettings) diff.Normalization {
	normalization := diff.Normalization{
		CompareOptions: diff.CompareOptions{
			IgnoreAggregatedRoles:     settings.CompareOptions.IgnoreAggregatedRoles,
			IgnoreResourceStatusField: settings.CompareOptions.IgnoreResourceStatusField,
		},
	}
	for _, rule := range application.Spec.IgnoreDifferences {
		if !ignoreRuleMatches(rule, id) {
			continue
		}
		normalization.JSONPointers = append(normalization.JSONPointers, rule.JSONPointers...)
		normalization.JQPathExpressions = append(normalization.JQPathExpressions, rule.JQPathExpressions...)
		normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, rule.ManagedFieldsManagers...)
	}
	global := globalNormalizationFor(settings, id)
	normalization.JSONPointers = append(normalization.JSONPointers, global.JSONPointers...)
	normalization.JQPathExpressions = append(normalization.JQPathExpressions, global.JQPathExpressions...)
	normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, global.ManagedFieldsManagers...)
	normalization.KnownTypeFields = append(normalization.KnownTypeFields, global.KnownTypeFields...)
	return normalization
}

func globalNormalizationFor(settings config.ArgoSettings, id manifest.Identity) diff.Normalization {
	var normalization diff.Normalization
	keys := make([]string, 0, len(settings.ResourceCustomizations))
	for key := range settings.ResourceCustomizations {
		if resourceCustomizationKeyMatches(key, id) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		customization := settings.ResourceCustomizations[key]
		normalization.JSONPointers = append(normalization.JSONPointers, customization.IgnoreDifferences.JSONPointers...)
		normalization.JQPathExpressions = append(normalization.JQPathExpressions, customization.IgnoreDifferences.JQPathExpressions...)
		normalization.ManagedFieldsManagers = append(normalization.ManagedFieldsManagers, customization.IgnoreDifferences.ManagedFieldsManagers...)
		for _, field := range customization.KnownTypeFields {
			normalization.KnownTypeFields = append(normalization.KnownTypeFields, diff.KnownTypeField{
				Field: field.Field,
				Type:  field.Type,
			})
		}
	}
	return normalization
}

func resourceCustomizationKeyMatches(key string, id manifest.Identity) bool {
	if key == "" {
		return false
	}
	if key == "*/*" {
		return true
	}
	group, kind, found := strings.Cut(key, "/")
	if !found {
		return glob.Match(key, id.Kind)
	}
	return glob.Match(group, id.Group) && glob.Match(kind, id.Kind)
}

func ignoreRuleMatches(rule argoappv1.ResourceIgnoreDifferences, id manifest.Identity) bool {
	if !glob.Match(rule.Group, id.Group) || !glob.Match(rule.Kind, id.Kind) {
		return false
	}
	if rule.Name != "" && rule.Name != id.Name {
		return false
	}
	if rule.Namespace != "" && rule.Namespace != id.Namespace {
		return false
	}
	return true
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
