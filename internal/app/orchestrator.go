package app

import (
	"context"
	"errors"
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"

	"path/filepath"
	"strings"
	"sync"
	"time"
)

type BuildRequest struct {
	Path   string
	Strict bool
	// StatusOnly renders Applications for validation without retaining manifests.
	StatusOnly                     bool
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

	for _, appSetFile := range discovered.ApplicationSets {
		generated, diags, err := appset.GenerateWithOptions(root, appSetFile.Path, appSetFile.ApplicationSet, appsetOptions)
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
				Paths:       generatedApplicationInputPaths(appSetFile.Path, app),
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
	session, err := newBuildSession(o, request)
	if err != nil {
		return BuildResult{}, err
	}
	return session.Build(ctx)
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
	statusOnly     bool
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
	if request.statusOnly {
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusPass, ""))
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
	seen := make(map[string]struct{}, len(discovered.SettingsCandidates))
	for _, candidate := range discovered.SettingsCandidates {
		key := fmt.Sprintf("%s\x00%s\x00%d", candidate.Kind, candidate.Path, candidate.DocumentIndex)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		path := filepath.Join(root, candidate.Path)
		var (
			settings  config.ArgoSettings
			nextDiags []diagnostic.Diagnostic
			err       error
		)
		switch candidate.Kind {
		case "argocd-cm":
			settings, nextDiags, err = config.LoadFromConfigMapDocument(path, candidate.DocumentIndex)
		case "argocd-values":
			settings, nextDiags, err = config.LoadFromHelmValuesDocument(path, candidate.DocumentIndex)
		case "repository-secret":
			settings, nextDiags, err = config.LoadRepositorySecretDocument(path, candidate.DocumentIndex)
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
