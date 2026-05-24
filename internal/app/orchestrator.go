package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/project"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

type BuildRequest struct {
	Path                           string
	Strict                         bool
	Offline                        bool
	RefreshCharts                  bool
	ChartCacheDir                  string
	ChartCredentials               chart.ChartCredentials
	RepoMaps                       []sourcepkg.RepoMap
	AllowNetwork                   bool
	GitCacheDir                    string
	RefreshGit                     bool
	GitCredentials                 sourcepkg.GitCredentials
	RefreshRemoteResources         bool
	RemoteResourceCacheDir         string
	RemoteResourceForbiddenRoots   []string
	RemoteResourceCredentials      remote.Credentials
	RemoteResourceGitCredentials   remote.GitCredentials
	PluginTimeout                  time.Duration
	Parallelism                    int
	SkipKinds                      []string
	SkipCRDs                       bool
	SkipSecrets                    bool
	PluginRenderer                 render.PluginRenderer
	Applications                   []argoappv1.Application
	ApplicationSetProviderFixtures []string
	ApplicationSetProviderData     appset.ProviderData
	RecordCacheEvents              bool
}

type BuildAppRequest struct {
	BuildRequest
	Name string
}

type ApplicationManifest struct {
	Application argoappv1.Application
	Manifest    render.Manifest
}

type ApplicationSelectionInput struct {
	Application argoappv1.Application
	Paths       []string
}

const (
	ApplicationStatusPass    = "PASS"
	ApplicationStatusFail    = "FAIL"
	ApplicationStatusSkipped = "SKIPPED"
)

type ApplicationStatus struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	Name      string `json:"name" yaml:"name"`
	Status    string `json:"status" yaml:"status"`
	Message   string `json:"message,omitempty" yaml:"message,omitempty"`
}

type BuildResult struct {
	Applications         []argoappv1.Application
	ApplicationInputs    []ApplicationSelectionInput
	Projects             []argoappv1.AppProject
	Manifests            []render.Manifest
	ApplicationManifests []ApplicationManifest
	Diagnostics          []diagnostic.Diagnostic
	Settings             config.ArgoSettings
	Statuses             []ApplicationStatus
	CacheEvents          []cacheevent.Event
}

type DiagRequest = BuildRequest

type DiagResult struct {
	Applications []argoappv1.Application
	Diagnostics  []diagnostic.Diagnostic
	Settings     config.ArgoSettings
	CacheEvents  []cacheevent.Event
}

type Orchestrator struct {
	ChartAcquirer          chart.Acquirer
	GitAcquirer            sourcepkg.GitAcquirer
	RemoteResourceAcquirer remote.Acquirer
	PluginRenderer         render.PluginRenderer
}

func (o Orchestrator) Diag(ctx context.Context, request DiagRequest) (DiagResult, error) {
	result, err := o.Build(ctx, request)
	diagResult := DiagResult{
		Applications: result.Applications,
		Diagnostics:  result.Diagnostics,
		Settings:     result.Settings,
		CacheEvents:  result.CacheEvents,
	}
	if err != nil {
		return diagResult, err
	}
	if err := diagnosticFailure(result.Diagnostics, request.Strict); err != nil {
		return diagResult, err
	}
	return diagResult, nil
}

func (o Orchestrator) ListApplications(_ context.Context, request BuildRequest) (BuildResult, error) {
	root := request.Path
	if root == "" {
		root = "."
	}

	discovered, err := discovery.Scan(root, discovery.Options{})
	if err != nil {
		return BuildResult{}, err
	}

	settings, settingsDiags, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		return BuildResult{}, err
	}

	var result BuildResult
	result.Settings = settings
	settingsDiags = normalizeDiagnostics(settingsDiags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, settingsDiags...)
	result.Projects = appendDiscoveredProjects(result.Projects, discovered)
	appsetOptions, providerDiags, err := applicationSetOptionsForRequest(request)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, providerDiags...)
		return result, diagnosticsError(providerDiags, err)
	}
	result.Diagnostics = append(result.Diagnostics, normalizeDiagnostics(providerDiags, request.Strict, false)...)

	for _, appFile := range discovered.Applications {
		result.Applications = append(result.Applications, appFile.Application)
		result.ApplicationInputs = append(result.ApplicationInputs, ApplicationSelectionInput{
			Application: appFile.Application,
			Paths:       []string{filepath.ToSlash(appFile.Path)},
		})
	}

	for _, appSetPath := range discovered.ApplicationSetPath {
		data, err := os.ReadFile(filepath.Join(root, appSetPath))
		if err != nil {
			return result, err
		}
		generated, diags, err := appset.GenerateFromYAMLWithOptions(root, appSetPath, data, appsetOptions)
		if err != nil {
			if errors.Is(err, appset.ErrUnsupportedGenerator) && len(diags) > 0 {
				diags = normalizeDiagnostics(diags, request.Strict, true)
				result.Diagnostics = append(result.Diagnostics, diags...)
				if request.Strict {
					return result, diagnosticsError(diags, err)
				}
				continue
			}
			return result, diagnosticsError(diags, err)
		}
		diags = normalizeDiagnostics(diags, request.Strict, false)
		result.Diagnostics = append(result.Diagnostics, diags...)
		if err := diagnosticFailure(diags, request.Strict); err != nil {
			return result, err
		}
		for _, app := range generated {
			result.Applications = append(result.Applications, app.Application)
			result.ApplicationInputs = append(result.ApplicationInputs, ApplicationSelectionInput{
				Application: app.Application,
				Paths:       generatedApplicationInputPaths(appSetPath, app),
			})
		}
	}

	if err := diagnosticFailure(result.Diagnostics, request.Strict); err != nil {
		return result, err
	}
	return result, nil
}

func generatedApplicationInputPaths(appSetPath string, app appset.GeneratedApplication) []string {
	paths := []string{filepath.ToSlash(appSetPath)}
	sourcePaths := app.SourcePaths
	if len(sourcePaths) == 0 && app.SourcePath != "" {
		sourcePaths = []string{app.SourcePath}
	}
	for _, sourcePath := range sourcePaths {
		if sourcePath != "" {
			paths = append(paths, filepath.ToSlash(sourcePath))
		}
	}
	return paths
}

func applicationSetOptionsForRequest(request BuildRequest) (appset.Options, []diagnostic.Diagnostic, error) {
	fixtureData, diags, err := appset.LoadProviderFixtures(request.ApplicationSetProviderFixtures)
	if err != nil {
		return appset.Options{}, diags, err
	}
	data, mergeDiags, err := appset.MergeProviderData(fixtureData, request.ApplicationSetProviderData)
	diags = append(diags, mergeDiags...)
	if err != nil {
		return appset.Options{}, diags, err
	}
	return appset.Options{
		Provider: appset.ProviderOptions{
			FixturePaths: append([]string(nil), request.ApplicationSetProviderFixtures...),
			Data:         data,
		},
	}, diags, nil
}

func (o Orchestrator) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	root := request.Path
	if root == "" {
		root = "."
	}
	parallelism, err := normalizeParallelism(request.Parallelism)
	if err != nil {
		return BuildResult{}, err
	}
	cacheRecorder := cacheevent.NewRecorder(request.RecordCacheEvents)

	result, err := o.prepareBuildResult(ctx, request, root)
	if err != nil {
		result.CacheEvents = cacheRecorder.Events()
		return result, err
	}
	projectDiags := project.ValidateApplications(result.Applications, result.Projects, result.Settings)
	projectDiags = normalizeDiagnostics(projectDiags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, projectDiags...)
	if err := diagnosticFailure(projectDiags, request.Strict); err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		result.CacheEvents = cacheRecorder.Events()
		return result, err
	}
	if err := validateBuildNetworkOptions(request); err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		result.CacheEvents = cacheRecorder.Events()
		return result, err
	}
	if len(result.Applications) == 0 {
		result.CacheEvents = cacheRecorder.Events()
		return result, nil
	}

	resourceFilter := request.resourceFilter()
	settingsFilter := manifest.SettingsResourceFilter{
		Exclusions: result.Settings.ResourceExclusions,
		Inclusions: result.Settings.ResourceInclusions,
	}

	acquirer := o.ChartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	gitAcquirer := o.GitAcquirer
	if gitAcquirer == nil {
		gitAcquirer = sourcepkg.DefaultGitAcquirer{}
	}
	forbiddenRoots := append([]string(nil), request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = append(forbiddenRoots, root)
	provider := localProvider{
		repoRoot:                     root,
		sourceResolver:               sourcepkg.NewResolver(sourcepkg.Options{RepoMaps: request.RepoMaps, AllowNetwork: request.AllowNetwork}),
		chartAcquirer:                acquirer,
		gitAcquirer:                  gitAcquirer,
		remoteResourceAcquirer:       o.RemoteResourceAcquirer,
		pluginRenderer:               o.pluginRenderer(request),
		offline:                      request.Offline,
		allowNetwork:                 request.AllowNetwork,
		refreshCharts:                request.RefreshCharts,
		chartCacheDir:                request.ChartCacheDir,
		chartCredentials:             request.ChartCredentials,
		gitCacheDir:                  request.GitCacheDir,
		refreshGit:                   request.RefreshGit,
		gitCredentials:               request.GitCredentials,
		refreshRemoteResources:       request.RefreshRemoteResources,
		remoteResourceCacheDir:       request.RemoteResourceCacheDir,
		remoteResourceForbiddenRoots: forbiddenRoots,
		remoteResourceCredentials:    request.RemoteResourceCredentials,
		remoteResourceGitCredentials: request.RemoteResourceGitCredentials,
		pluginTimeout:                request.PluginTimeout,
		cacheEvents:                  cacheRecorder,
	}
	snapshotRoot, err := os.MkdirTemp("", "drydock-cache-snapshots-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(snapshotRoot)
	provider.cacheLocks = processCacheTargetLocks
	provider.cacheSnapshotRoot = snapshotRoot
	provider.snapshotCacheReads = true

	rendered, renderErr := renderApplications(ctx, renderApplicationsRequest{
		applications:   result.Applications,
		provider:       provider,
		strict:         request.Strict,
		settingsFilter: settingsFilter,
		resourceFilter: resourceFilter,
		recordEvents:   request.RecordCacheEvents,
		parallelism:    parallelism,
	})
	result.Manifests = append(result.Manifests, rendered.manifests...)
	result.ApplicationManifests = append(result.ApplicationManifests, rendered.applicationManifests...)
	result.Diagnostics = append(result.Diagnostics, rendered.diagnostics...)
	result.Statuses = append(result.Statuses, rendered.statuses...)
	result.CacheEvents = append(result.CacheEvents, rendered.cacheEvents...)
	if renderErr != nil {
		return result, renderErr
	}
	if err := buildStatusFailure(result.Statuses); err != nil {
		return result, err
	}
	return result, nil
}

func normalizeParallelism(value int) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("parallelism must be greater than or equal to 0")
	}
	if value == 0 {
		return 1, nil
	}
	return value, nil
}

type renderApplicationsRequest struct {
	applications   []argoappv1.Application
	provider       localProvider
	strict         bool
	settingsFilter manifest.SettingsResourceFilter
	resourceFilter manifest.ResourceFilter
	recordEvents   bool
	parallelism    int
}

type renderApplicationsResult struct {
	manifests            []render.Manifest
	applicationManifests []ApplicationManifest
	diagnostics          []diagnostic.Diagnostic
	statuses             []ApplicationStatus
	cacheEvents          []cacheevent.Event
}

type applicationRenderResult struct {
	renderApplicationsResult
	set bool
	err error
}

func renderApplications(ctx context.Context, request renderApplicationsRequest) (renderApplicationsResult, error) {
	if request.parallelism <= 1 || len(request.applications) <= 1 {
		return renderApplicationsSequential(ctx, request)
	}
	return renderApplicationsParallel(ctx, request)
}

func renderApplicationsSequential(ctx context.Context, request renderApplicationsRequest) (renderApplicationsResult, error) {
	var out renderApplicationsResult
	for _, application := range request.applications {
		result := renderOneApplication(ctx, application, request)
		appendApplicationRenderResult(&out, result)
		if result.err != nil {
			return out, result.err
		}
	}
	return out, nil
}

func renderApplicationsParallel(ctx context.Context, request renderApplicationsRequest) (renderApplicationsResult, error) {
	workerCount := request.parallelism
	if workerCount > len(request.applications) {
		workerCount = len(request.applications)
	}

	jobs := make(chan int)
	results := make([]applicationRenderResult, len(request.applications))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index] = renderOneApplication(ctx, request.applications[index], request)
			}
		}()
	}

	var scheduleErr error
schedule:
	for index := range request.applications {
		select {
		case <-ctx.Done():
			scheduleErr = ctx.Err()
			break schedule
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()

	var out renderApplicationsResult
	var renderErr error
	for _, result := range results {
		if !result.set {
			continue
		}
		appendApplicationRenderResult(&out, result)
		if result.err != nil && renderErr == nil {
			renderErr = result.err
		}
	}
	if scheduleErr != nil {
		return out, scheduleErr
	}
	if renderErr != nil {
		return out, renderErr
	}
	return out, nil
}

func appendApplicationRenderResult(out *renderApplicationsResult, result applicationRenderResult) {
	out.manifests = append(out.manifests, result.manifests...)
	out.applicationManifests = append(out.applicationManifests, result.applicationManifests...)
	out.diagnostics = append(out.diagnostics, result.diagnostics...)
	out.statuses = append(out.statuses, result.statuses...)
	out.cacheEvents = append(out.cacheEvents, result.cacheEvents...)
}

func renderOneApplication(ctx context.Context, application argoappv1.Application, request renderApplicationsRequest) applicationRenderResult {
	provider := request.provider
	recorder := cacheevent.NewRecorder(request.recordEvents)
	provider.cacheEvents = recorder
	out := applicationRenderResult{set: true}

	rendered, err := RenderApplication(ctx, application, provider)
	if err != nil {
		out.diagnostics = append(out.diagnostics, normalizeDiagnostics(rendered.Diagnostics, request.strict, false)...)
		out.diagnostics = append(out.diagnostics, renderFailureDiagnostic(application, err))
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		if ctxErr := ctx.Err(); ctxErr != nil {
			out.err = ctxErr
		}
		return out
	}

	rendered.Diagnostics = normalizeDiagnostics(rendered.Diagnostics, request.strict, false)
	out.diagnostics = append(out.diagnostics, rendered.Diagnostics...)
	if err := diagnosticFailure(rendered.Diagnostics, request.strict); err != nil {
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		return out
	}

	cluster := applicationDestinationCluster(application)
	for _, renderedManifest := range rendered.Manifests {
		id := manifest.IdentityOf(renderedManifest.Object)
		if request.settingsFilter.Drop(id, cluster) {
			continue
		}
		if request.resourceFilter.Drop(renderedManifest.Object) {
			continue
		}
		out.manifests = append(out.manifests, renderedManifest)
		out.applicationManifests = append(out.applicationManifests, ApplicationManifest{
			Application: application,
			Manifest:    renderedManifest,
		})
	}
	out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusPass, ""))
	out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
	return out
}

func (o Orchestrator) pluginRenderer(request BuildRequest) render.PluginRenderer {
	if request.PluginRenderer != nil {
		return request.PluginRenderer
	}
	return o.PluginRenderer
}

func (o Orchestrator) prepareBuildResult(ctx context.Context, request BuildRequest, root string) (BuildResult, error) {
	if request.Applications == nil {
		listResult, err := o.ListApplications(ctx, request)
		if err != nil {
			listResult.Statuses = skippedApplicationStatuses(listResult.Applications, err)
			return listResult, err
		}
		return listResult, nil
	}

	var result BuildResult
	result.Applications = append(result.Applications, request.Applications...)
	discovered, err := discovery.Scan(root, discovery.Options{})
	if err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, err
	}
	result.Projects = appendDiscoveredProjects(result.Projects, discovered)
	settings, diags, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, err
	}
	result.Settings = settings
	diags = normalizeDiagnostics(diags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, diags...)
	if err := diagnosticFailure(diags, request.Strict); err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, err
	}
	return result, nil
}

func appendDiscoveredProjects(projects []argoappv1.AppProject, discovered discovery.Result) []argoappv1.AppProject {
	for _, projectFile := range discovered.Projects {
		projects = append(projects, projectFile.Project)
	}
	return projects
}

func loadSettingsFromDiscovery(root string, discovered discovery.Result) (config.ArgoSettings, []diagnostic.Diagnostic, error) {
	var candidates []config.ArgoSettings
	var diags []diagnostic.Diagnostic
	for _, candidate := range discovered.SettingsCandidates {
		path := filepath.Join(root, candidate.Path)
		var (
			settings  config.ArgoSettings
			nextDiags []diagnostic.Diagnostic
			err       error
		)
		switch candidate.Kind {
		case "argocd-cm":
			settings, nextDiags, err = config.LoadFromConfigMap(path)
		case "argocd-values":
			settings, nextDiags, err = config.LoadFromHelmValues(path)
		case "repository-secret":
			settings, nextDiags, err = config.LoadRepositorySecret(path)
		default:
			continue
		}
		if err != nil {
			return config.DefaultSettings(), diags, err
		}
		candidates = append(candidates, settings)
		diags = append(diags, nextDiags...)
	}
	merged, mergeDiags := config.MergeDiscovered(candidates)
	diags = append(diags, mergeDiags...)
	return merged, diags, nil
}

func (request BuildRequest) resourceFilter() manifest.ResourceFilter {
	return manifest.ResourceFilter{
		SkipKinds:   append([]string(nil), request.SkipKinds...),
		SkipCRDs:    request.SkipCRDs,
		SkipSecrets: request.SkipSecrets,
	}
}

func applicationDestinationCluster(application argoappv1.Application) string {
	if application.Spec.Destination.Name != "" {
		return application.Spec.Destination.Name
	}
	return application.Spec.Destination.Server
}

func (o Orchestrator) BuildApp(ctx context.Context, request BuildAppRequest) (BuildResult, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return BuildResult{}, fmt.Errorf("application name is required")
	}

	buildRequest := request.BuildRequest
	listResult, err := o.ListApplications(ctx, buildRequest)
	if err != nil {
		listResult.Statuses = skippedStatusesForRequestedApplication(listResult.Applications, name, err)
		return listResult, err
	}

	selected, err := SelectApplicationByName(listResult.Applications, name)
	if err != nil {
		return listResult, err
	}

	buildRequest.Applications = []argoappv1.Application{selected}
	buildResult, err := o.Build(ctx, buildRequest)
	buildResult.ApplicationInputs = selectApplicationInputsForApplication(listResult.ApplicationInputs, selected)
	buildResult.Diagnostics = append(append([]diagnostic.Diagnostic(nil), listResult.Diagnostics...), buildResult.Diagnostics...)
	return buildResult, err
}

func skippedStatusesForRequestedApplication(applications []argoappv1.Application, name string, cause error) []ApplicationStatus {
	if len(applications) == 0 || cause == nil {
		return nil
	}
	selected, ok, err := SelectOptionalApplicationByName(applications, name)
	if err == nil && ok {
		return []ApplicationStatus{applicationStatus(selected, ApplicationStatusSkipped, cause.Error())}
	}
	return skippedApplicationStatuses(applications, cause)
}

func skippedApplicationStatuses(applications []argoappv1.Application, cause error) []ApplicationStatus {
	if len(applications) == 0 || cause == nil {
		return nil
	}
	statuses := make([]ApplicationStatus, 0, len(applications))
	for _, application := range applications {
		statuses = append(statuses, applicationStatus(application, ApplicationStatusSkipped, cause.Error()))
	}
	return statuses
}

func renderFailureDiagnostic(application argoappv1.Application, err error) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity: diagnostic.SeverityError,
		Category: "render",
		Message:  fmt.Sprintf("Application %s failed to render: %s", applicationDisplayName(application), err),
	}
}

func applicationStatus(application argoappv1.Application, status, message string) ApplicationStatus {
	return ApplicationStatus{
		Namespace: application.Namespace,
		Name:      application.Name,
		Status:    status,
		Message:   message,
	}
}

func buildStatusFailure(statuses []ApplicationStatus) error {
	var failed []ApplicationStatus
	for _, status := range statuses {
		if status.Status == ApplicationStatusFail || status.Status == ApplicationStatusSkipped {
			failed = append(failed, status)
		}
	}
	if len(failed) == 0 {
		return nil
	}

	label := "Applications"
	if len(failed) == 1 {
		label = "Application"
	}
	messages := make([]string, 0, len(failed))
	for _, status := range failed {
		messages = append(messages, fmt.Sprintf("%s: %s", applicationStatusDisplayName(status), status.Message))
	}
	return fmt.Errorf("%d %s failed: %s", len(failed), label, strings.Join(messages, "; "))
}

func applicationStatusDisplayName(status ApplicationStatus) string {
	if status.Namespace == "" {
		return status.Name
	}
	return status.Namespace + "/" + status.Name
}

func selectApplicationInputsForApplication(inputs []ApplicationSelectionInput, selected argoappv1.Application) []ApplicationSelectionInput {
	for _, input := range inputs {
		if input.Application.Namespace == selected.Namespace && input.Application.Name == selected.Name {
			return []ApplicationSelectionInput{cloneApplicationSelectionInput(input)}
		}
	}
	return nil
}

func cloneApplicationSelectionInput(input ApplicationSelectionInput) ApplicationSelectionInput {
	input.Paths = append([]string(nil), input.Paths...)
	return input
}

type localProvider struct {
	repoRoot                     string
	sourceResolver               *sourcepkg.Resolver
	chartAcquirer                chart.Acquirer
	gitAcquirer                  sourcepkg.GitAcquirer
	remoteResourceAcquirer       remote.Acquirer
	pluginRenderer               render.PluginRenderer
	offline                      bool
	allowNetwork                 bool
	refreshCharts                bool
	chartCacheDir                string
	chartCredentials             chart.ChartCredentials
	gitCacheDir                  string
	refreshGit                   bool
	gitCredentials               sourcepkg.GitCredentials
	refreshRemoteResources       bool
	remoteResourceCacheDir       string
	remoteResourceForbiddenRoots []string
	remoteResourceCredentials    remote.Credentials
	remoteResourceGitCredentials remote.GitCredentials
	pluginTimeout                time.Duration
	cacheEvents                  *cacheevent.Recorder
	cacheLocks                   *cacheTargetLocks
	cacheSnapshotRoot            string
	snapshotCacheReads           bool
}

var processCacheTargetLocks = newCacheTargetLocks()

func (p localProvider) RenderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	sourceRoot, err := p.resolveSourceRoot(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	source.RepoRoot = sourceRoot
	opts.ChartAcquirer = p.cacheSafeChartAcquirer(p.chartAcquirer)
	opts.ChartCacheDir = p.chartCacheDir
	opts.OfflineCharts = p.offline
	opts.RefreshCharts = p.refreshCharts
	opts.ChartCredentials = p.chartCredentials
	opts.RemoteResourceAcquirer = p.cacheSafeRemoteAcquirer(p.remoteResourceAcquirer)
	opts.RemoteResourceCacheDir = p.remoteResourceCacheDir
	opts.OfflineRemoteResources = p.offline
	opts.RefreshRemoteResources = p.refreshRemoteResources
	opts.RemoteResourceForbiddenRoots = p.remoteResourceForbiddenRoots
	opts.RemoteResourceForbiddenRoots = appendUniqueString(opts.RemoteResourceForbiddenRoots, sourceRoot)
	opts.RemoteResourceCredentials = p.remoteResourceCredentials
	opts.RemoteResourceGitCredentials = p.remoteResourceGitCredentials
	opts.CacheEventRecorder = p.cacheEvents
	anchoredRefRoots, err := anchorLocalRefRoots(sourceRoot, opts.RefRoots)
	if err != nil {
		return nil, nil, err
	}
	refRoots, err := p.resolveRefRoots(ctx, opts.RefSources)
	if err != nil {
		return nil, nil, err
	}
	opts.RefRoots = mergeRefRoots(anchoredRefRoots, refRoots)
	if opts.Plugin != nil {
		return p.renderPluginSource(ctx, source, opts)
	}
	if source.Path != "" {
		renderer, err := selectLocalRenderer(source)
		if err != nil {
			return nil, nil, err
		}
		return renderer.Render(ctx, source, opts)
	}
	if source.Chart != "" {
		return p.renderChartOnlySource(ctx, source, opts)
	}
	return nil, nil, nil
}

func (p localProvider) renderPluginSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	if p.pluginRenderer == nil {
		message := fmt.Sprintf("config management plugin %s is not supported without an injected plugin renderer", pluginDisplayName(opts.Plugin.Name))
		return nil, []diagnostic.Diagnostic{{
			Code:     diagnostic.CodePluginUnsupported,
			Severity: diagnostic.SeverityError,
			Category: "plugin",
			Message:  message,
		}}, fmt.Errorf("%s: %w", message, render.ErrUnsupportedPlugin)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	renderCtx := ctx
	cancel := func() {}
	if p.pluginTimeout > 0 {
		renderCtx, cancel = context.WithTimeout(ctx, p.pluginTimeout)
	}
	defer cancel()
	request := render.PluginRequest{
		AppName:      opts.AppName,
		AppNamespace: opts.AppNamespace,
		Project:      opts.Project,
		Namespace:    opts.Namespace,
		Source:       source,
		Plugin:       *opts.Plugin,
		RefRoots:     cloneStringMap(opts.RefRoots),
		RefSources:   cloneResolvedSourceMap(opts.RefSources),
	}
	manifests, diags, err := p.pluginRenderer.RenderPlugin(renderCtx, request)
	diags = diagnostic.WithStableCodes(diags)
	if err == nil {
		return manifests, diags, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return manifests, diags, ctxErr
	}
	if renderCtx.Err() == context.DeadlineExceeded {
		message := fmt.Sprintf("config management plugin %s timed out", pluginDisplayName(opts.Plugin.Name))
		diags = append(diags, pluginFailedDiagnostic(message))
		return manifests, diags, fmt.Errorf("%s: %w", message, err)
	}
	if errors.Is(err, render.ErrUnsupportedPlugin) || diagnosticsContainCode(diags, diagnostic.CodePluginUnsupported) {
		return manifests, diags, err
	}
	message := fmt.Sprintf("config management plugin %s failed: %s", pluginDisplayName(opts.Plugin.Name), err)
	diags = append(diags, pluginFailedDiagnostic(message))
	return manifests, diags, err
}

func pluginDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "<unnamed>"
	}
	return name
}

func pluginFailedDiagnostic(message string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     diagnostic.CodePluginFailed,
		Severity: diagnostic.SeverityError,
		Category: "plugin",
		Message:  message,
	}
}

func diagnosticsContainCode(diags []diagnostic.Diagnostic, code string) bool {
	for _, diag := range diags {
		if diag.Code == code {
			return true
		}
	}
	return false
}

func (p localProvider) resolveSourceRoot(ctx context.Context, source render.ResolvedSource) (string, error) {
	if source.Path == "" && source.Chart != "" {
		return p.repoRoot, nil
	}
	if p.sourceResolver != nil {
		if mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL); ok {
			p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionMapped, Target: source.RepoURL, Revision: source.TargetRevision})
			return filepath.Abs(mappedPath)
		}
	}
	if source.Path != "" {
		if exists, err := sourcePathExists(p.repoRoot, source.Path); err != nil {
			return "", err
		} else if exists {
			p.recordCacheEvent(cacheevent.Event{Source: cacheevent.SourceGit, Action: cacheevent.ActionLocal, Target: source.RepoURL, Revision: source.TargetRevision})
			return p.repoRoot, nil
		}
	}
	if strings.TrimSpace(source.RepoURL) == "" {
		return p.repoRoot, nil
	}
	if p.sourceResolver == nil {
		return p.repoRoot, nil
	}
	if _, err := p.sourceResolver.Resolve(source.RepoURL, source.TargetRevision); err != nil {
		return "", fmt.Errorf("source path %q is not present under local repository root and %w", source.Path, err)
	}
	if p.offline && p.allowNetwork {
		return "", fmt.Errorf("--offline cannot be combined with --allow-network for Git source fetching")
	}
	acquirer := p.gitAcquirer
	if acquirer == nil {
		acquirer = sourcepkg.DefaultGitAcquirer{}
	}
	acquirer = p.cacheSafeGitAcquirer(acquirer)
	acquired, err := acquirer.Acquire(ctx, sourcepkg.GitRequest{
		URL:      source.RepoURL,
		Revision: source.TargetRevision,
	}, sourcepkg.GitOptions{
		AllowNetwork: p.allowNetwork,
		CacheDir:     p.gitCacheDir,
		Refresh:      p.refreshGit,
		Credentials:  p.gitCredentials,
	})
	if err != nil {
		redactedError := redactGitAcquireError(err, source.RepoURL, p.gitCredentials)
		p.recordCacheEvent(cacheevent.Event{
			Source:          cacheevent.SourceGit,
			Action:          cacheActionForError(err),
			Target:          source.RepoURL,
			Revision:        source.TargetRevision,
			Offline:         p.offline,
			Refresh:         p.refreshGit,
			Error:           err.Error(),
			SensitiveValues: sourceGitSensitiveValues(p.gitCredentials),
		})
		return "", fmt.Errorf("%s", redactedError)
	}
	p.recordCacheEvent(cacheevent.Event{
		Source:   cacheevent.SourceGit,
		Action:   actionForAcquisition(acquired.FromCache, acquired.Network, p.refreshGit),
		Target:   source.RepoURL,
		Revision: acquired.Revision,
		CacheHit: acquired.FromCache,
		Offline:  p.offline,
		Refresh:  p.refreshGit,
	})
	return acquired.Path, nil
}

func (p localProvider) resolveRefRoots(ctx context.Context, refSources map[string]render.ResolvedSource) (map[string]string, error) {
	if len(refSources) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(refSources))
	for refKey, refSource := range refSources {
		root, err := p.resolveSourceRoot(ctx, refSource)
		if err != nil {
			return nil, fmt.Errorf("ref root %s: %w", refKey, err)
		}
		out[refKey] = root
	}
	return out, nil
}

func sourcePathExists(repoRoot, sourcePath string) (bool, error) {
	clean, err := cleanLocalSourcePath(sourcePath)
	if err != nil {
		return false, err
	}
	return localPathExists(filepath.Join(repoRoot, clean))
}

func mergeRefRoots(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneResolvedSourceMap(in map[string]render.ResolvedSource) map[string]render.ResolvedSource {
	if len(in) == 0 {
		return map[string]render.ResolvedSource{}
	}
	out := make(map[string]render.ResolvedSource, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (p localProvider) recordCacheEvent(event cacheevent.Event) {
	if p.cacheEvents != nil {
		p.cacheEvents.Record(event)
	}
}

func (p localProvider) cacheSafeGitAcquirer(delegate sourcepkg.GitAcquirer) sourcepkg.GitAcquirer {
	if delegate == nil {
		delegate = sourcepkg.DefaultGitAcquirer{}
	}
	if p.cacheLocks == nil {
		return delegate
	}
	return cacheSafeGitAcquirer{
		delegate:     delegate,
		locks:        p.cacheLocks,
		snapshotRoot: p.cacheSnapshotRoot,
		snapshot:     p.snapshotCacheReads,
	}
}

func (p localProvider) cacheSafeChartAcquirer(delegate chart.Acquirer) chart.Acquirer {
	if delegate == nil {
		delegate = chart.DefaultAcquirer{}
	}
	if p.cacheLocks == nil {
		return delegate
	}
	return cacheSafeChartAcquirer{
		delegate:     delegate,
		locks:        p.cacheLocks,
		snapshotRoot: p.cacheSnapshotRoot,
		snapshot:     p.snapshotCacheReads,
	}
}

func (p localProvider) cacheSafeRemoteAcquirer(delegate remote.Acquirer) remote.Acquirer {
	if delegate == nil {
		delegate = remote.DefaultAcquirer{}
	}
	if p.cacheLocks == nil {
		return delegate
	}
	return cacheSafeRemoteAcquirer{
		delegate:     delegate,
		locks:        p.cacheLocks,
		snapshotRoot: p.cacheSnapshotRoot,
		snapshot:     p.snapshotCacheReads,
	}
}

type cacheTargetLocks struct {
	mu    sync.Mutex
	locks map[string]*cacheTargetLock
}

type cacheTargetLock struct {
	mu   sync.Mutex
	refs int
}

func newCacheTargetLocks() *cacheTargetLocks {
	return &cacheTargetLocks{locks: map[string]*cacheTargetLock{}}
}

func (locks *cacheTargetLocks) lock(key string) func() {
	if locks == nil || key == "" {
		return func() {}
	}
	locks.mu.Lock()
	targetLock, ok := locks.locks[key]
	if !ok {
		targetLock = &cacheTargetLock{}
		locks.locks[key] = targetLock
	}
	targetLock.refs++
	locks.mu.Unlock()
	targetLock.mu.Lock()
	return func() {
		targetLock.mu.Unlock()
		locks.mu.Lock()
		targetLock.refs--
		if targetLock.refs == 0 {
			delete(locks.locks, key)
		}
		locks.mu.Unlock()
	}
}

type cacheSafeGitAcquirer struct {
	delegate     sourcepkg.GitAcquirer
	locks        *cacheTargetLocks
	snapshotRoot string
	snapshot     bool
}

func (acquirer cacheSafeGitAcquirer) Acquire(ctx context.Context, request sourcepkg.GitRequest, opts sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	key, keyErr := gitCacheLockKey(request, opts)
	if keyErr != nil {
		return acquirer.delegate.Acquire(ctx, request, opts)
	}
	unlock := acquirer.locks.lock(key)
	defer unlock()

	result, err := acquirer.delegate.Acquire(ctx, request, opts)
	if err != nil || !acquirer.snapshot {
		return result, err
	}
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "git", result.Path)
	if err != nil {
		return sourcepkg.GitResult{}, err
	}
	result.Path = snapshot
	return result, nil
}

type cacheSafeChartAcquirer struct {
	delegate     chart.Acquirer
	locks        *cacheTargetLocks
	snapshotRoot string
	snapshot     bool
}

func (acquirer cacheSafeChartAcquirer) Acquire(ctx context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	key, keyErr := chartCacheLockKey(request, opts)
	if keyErr != nil {
		return acquirer.delegate.Acquire(ctx, request, opts)
	}
	unlock := acquirer.locks.lock(key)
	defer unlock()

	result, err := acquirer.delegate.Acquire(ctx, request, opts)
	if err != nil || !acquirer.snapshot {
		return result, err
	}
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "chart", result.ChartDir)
	if err != nil {
		return chart.Result{}, err
	}
	result.ChartDir = snapshot
	return result, nil
}

type cacheSafeRemoteAcquirer struct {
	delegate     remote.Acquirer
	locks        *cacheTargetLocks
	snapshotRoot string
	snapshot     bool
}

func (acquirer cacheSafeRemoteAcquirer) Acquire(ctx context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	key, keyErr := remoteCacheLockKey(request, opts)
	if keyErr != nil {
		return acquirer.delegate.Acquire(ctx, request, opts)
	}
	unlock := acquirer.locks.lock(key)
	defer unlock()

	result, err := acquirer.delegate.Acquire(ctx, request, opts)
	if err != nil || !acquirer.snapshot {
		return result, err
	}
	snapshot, err := snapshotCachePath(acquirer.snapshotRoot, "remote", result.Path)
	if err != nil {
		return remote.Result{}, err
	}
	result.Path = snapshot
	return result, nil
}

func gitCacheLockKey(request sourcepkg.GitRequest, opts sourcepkg.GitOptions) (string, error) {
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = sourcepkg.DefaultGitCacheDir()
		if err != nil {
			return "", err
		}
	}
	return absoluteCacheLockKey("git", filepath.Join(cacheDir, sourcepkg.GitCacheKey(request.URL, request.Revision)))
}

func chartCacheLockKey(request chart.Request, opts chart.Options) (string, error) {
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = chart.DefaultCacheDir()
		if err != nil {
			return "", err
		}
	}
	key, err := chart.NewCacheKey(request)
	if err != nil {
		return "", err
	}
	return absoluteCacheLockKey("chart", filepath.Join(cacheDir, string(request.Kind), key))
}

func remoteCacheLockKey(request remote.Request, opts remote.Options) (string, error) {
	cacheDir, err := remote.ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return "", err
	}
	key, err := remote.NewCacheKey(request)
	if err != nil {
		return "", err
	}
	if request.Kind == remote.RequestGitRepo {
		return absoluteCacheLockKey("remote-git", filepath.Join(cacheDir, key, "repo"))
	}
	return absoluteCacheLockKey("remote", remote.CachePath(cacheDir, key))
}

func absoluteCacheLockKey(prefix, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return prefix + ":" + filepath.Clean(abs), nil
}

func snapshotCachePath(root, prefix, sourcePath string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(sourcePath) == "" {
		return sourcePath, nil
	}
	snapshotRoot, err := os.MkdirTemp(root, prefix+"-*")
	if err != nil {
		return "", err
	}
	snapshotPath := filepath.Join(snapshotRoot, filepath.Base(sourcePath))
	if err := copyCachePath(sourcePath, snapshotPath); err != nil {
		_ = os.RemoveAll(snapshotRoot)
		return "", err
	}
	return snapshotPath, nil
}

func copyCachePath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyCachePath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyRegularCacheFile(src, dst, info.Mode().Perm())
	default:
		return fmt.Errorf("cache path %q is not a regular file, directory, or symlink", src)
	}
}

func copyRegularCacheFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func actionForAcquisition(fromCache bool, network bool, refresh bool) cacheevent.Action {
	if fromCache {
		return cacheevent.ActionHit
	}
	if network && refresh {
		return cacheevent.ActionRefresh
	}
	return cacheevent.ActionFetch
}

func cacheActionForError(err error) cacheevent.Action {
	if err != nil && strings.Contains(err.Error(), "offline cache miss") {
		return cacheevent.ActionMiss
	}
	return cacheevent.ActionError
}

func redactGitAcquireError(err error, repoURL string, credentials sourcepkg.GitCredentials) string {
	if err == nil {
		return ""
	}
	return cacheevent.RedactEventError(
		sourcepkg.RedactGitCredentialError(err.Error(), credentials),
		cacheevent.RedactTarget(repoURL),
		[]string{repoURL},
		sourceGitSensitiveValues(credentials)...,
	)
}

func sourceGitSensitiveValues(credentials sourcepkg.GitCredentials) []string {
	return compactSensitiveValues(
		credentials.Username,
		credentials.Password,
		credentials.BearerToken,
		credentials.SSHPrivateKey,
		credentials.SSHPassphrase,
	)
}

func chartSensitiveValues(credentials chart.ChartCredentials) []string {
	return compactSensitiveValues(credentials.Username, credentials.Password, credentials.BearerToken)
}

func compactSensitiveValues(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func (p localProvider) renderChartOnlySource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	kind := chart.RepositoryHTTP
	if strings.HasPrefix(strings.TrimSpace(source.RepoURL), "oci://") {
		kind = chart.RepositoryOCI
	}

	acquirer := p.chartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	acquirer = p.cacheSafeChartAcquirer(acquirer)

	acquired, err := acquirer.Acquire(ctx, chart.Request{
		Repository: source.RepoURL,
		Name:       source.Chart,
		Version:    source.TargetRevision,
		Kind:       kind,
	}, chart.Options{
		CacheDir:    p.chartCacheDir,
		Offline:     p.offline,
		Refresh:     p.refreshCharts,
		Credentials: p.chartCredentials,
	})
	if err != nil {
		p.recordCacheEvent(cacheevent.Event{
			Source:          cacheevent.SourceChart,
			Action:          cacheActionForError(err),
			Target:          source.RepoURL,
			Revision:        source.TargetRevision,
			Offline:         p.offline,
			Refresh:         p.refreshCharts,
			Error:           err.Error(),
			SensitiveValues: chartSensitiveValues(p.chartCredentials),
		})
		return nil, nil, fmt.Errorf("acquire chart %s: %s", source.Chart, redactChartAcquireError(err, source.RepoURL, p.chartCredentials))
	}
	p.recordCacheEvent(cacheevent.Event{
		Source:   cacheevent.SourceChart,
		Action:   actionForAcquisition(acquired.FromCache, !acquired.FromCache, p.refreshCharts),
		Target:   source.RepoURL,
		Revision: acquired.Version,
		CacheHit: acquired.FromCache,
		Offline:  p.offline,
		Refresh:  p.refreshCharts,
	})

	return (render.HelmRenderer{}).Render(ctx, render.ResolvedSource{
		RepoRoot:       acquired.ChartDir,
		Path:           ".",
		Chart:          source.Chart,
		RepoURL:        source.RepoURL,
		TargetRevision: source.TargetRevision,
	}, opts)
}

func redactChartAcquireError(err error, repository string, credentials chart.ChartCredentials) string {
	message := err.Error()
	redacted := sourcepkg.RedactURL(repository)
	raw := strings.TrimSpace(repository)
	if raw == "" {
		return message
	}

	replacements := []string{raw}
	if parsed, parseErr := url.Parse(raw); parseErr == nil && parsed.Scheme != "" {
		withoutFragment := *parsed
		withoutFragment.Fragment = ""
		replacements = append(replacements, withoutFragment.String())

		withoutQueryFragment := withoutFragment
		withoutQueryFragment.RawQuery = ""
		withoutQueryFragment.ForceQuery = false
		replacements = append(replacements, withoutQueryFragment.String())

		withoutUser := *parsed
		withoutUser.User = nil
		replacements = append(replacements, withoutUser.String())

		if parsed.User != nil {
			replacements = append(replacements, parsed.User.String()+"@")
		}
		if parsed.RawQuery != "" {
			replacements = append(replacements, parsed.RawQuery)
		}
		if parsed.Fragment != "" {
			replacements = append(replacements, parsed.Fragment)
		}
	}

	for _, replacement := range replacements {
		if replacement == "" || replacement == redacted {
			continue
		}
		message = strings.ReplaceAll(message, replacement, redacted)
	}
	return redactChartCredentialValues(message, credentials)
}

func redactChartCredentialValues(message string, credentials chart.ChartCredentials) string {
	for _, secret := range []string{credentials.Username, credentials.Password, credentials.BearerToken} {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return message
}

func anchorLocalRefRoots(repoRoot string, refRoots map[string]string) (map[string]string, error) {
	if len(refRoots) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(refRoots))
	for key, root := range refRoots {
		if filepath.IsAbs(root) {
			return nil, fmt.Errorf("absolute ref root %s %q must be supplied through repo-map or source resolution", key, root)
		}
		clean, err := cleanLocalSourcePath(root)
		if err != nil {
			return nil, fmt.Errorf("ref root %s %q: %w", key, root, err)
		}
		out[key] = filepath.Join(repoRoot, clean)
	}
	return out, nil
}

func selectLocalRenderer(source render.ResolvedSource) (render.Renderer, error) {
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return nil, err
	}
	if err := rejectLocalSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return nil, err
	}

	root := filepath.Join(source.RepoRoot, sourcePath)
	if exists, err := localPathExists(filepath.Join(root, "Chart.yaml")); err != nil {
		return nil, err
	} else if exists {
		return render.HelmRenderer{}, nil
	}
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if exists, err := localPathExists(filepath.Join(root, name)); err != nil {
			return nil, err
		} else if exists {
			return render.KustomizeRenderer{}, nil
		}
	}
	return render.DirectoryRenderer{}, nil
}

func cleanLocalSourcePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("source path %q must be relative", path)
	}

	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source path %q escapes repository root", path)
	}
	return clean, nil
}

func rejectLocalSymlinkComponents(repoRoot, sourcePath string) error {
	if sourcePath == "." {
		return nil
	}

	current := filepath.Clean(repoRoot)
	for _, component := range strings.Split(sourcePath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q includes symlink component %q", sourcePath, component)
		}
	}
	return nil
}

func localPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func diagnosticsError(diags []diagnostic.Diagnostic, err error) error {
	if len(diags) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, diags[0].Message)
}

func normalizeDiagnostics(diags []diagnostic.Diagnostic, strict, forceWarning bool) []diagnostic.Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	out := make([]diagnostic.Diagnostic, len(diags))
	copy(out, diags)
	for i := range out {
		if forceWarning {
			out[i].Severity = diagnostic.SeverityWarning
		}
		if strict {
			out[i].Severity = diagnostic.SeverityError
		}
	}
	return out
}

func diagnosticFailure(diags []diagnostic.Diagnostic, strict bool) error {
	for _, diag := range diags {
		if strict || diag.Severity == diagnostic.SeverityError {
			return fmt.Errorf("diagnostic %s: %s", diag.Category, diag.Message)
		}
	}
	return nil
}

func validateBuildNetworkOptions(request BuildRequest) error {
	if request.Offline && request.AllowNetwork {
		return fmt.Errorf("--offline cannot be combined with --allow-network for Git source fetching")
	}
	if request.GitCacheDir == "" && !request.AllowNetwork {
		return nil
	}
	gitCacheDir := request.GitCacheDir
	if gitCacheDir == "" {
		defaultDir, err := sourcepkg.DefaultGitCacheDir()
		if err != nil {
			return err
		}
		gitCacheDir = defaultDir
	}
	root := request.Path
	if root == "" {
		root = "."
	}
	forbiddenRoots := append([]string(nil), request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = append(forbiddenRoots, root)
	for _, repoMap := range request.RepoMaps {
		if strings.TrimSpace(repoMap.Path) != "" {
			forbiddenRoots = append(forbiddenRoots, repoMap.Path)
		}
	}
	inside, matchedRoot, err := remote.IsPathInsideAny(gitCacheDir, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("git cache dir %q must not be inside repository root %q", gitCacheDir, matchedRoot)
	}
	return nil
}
