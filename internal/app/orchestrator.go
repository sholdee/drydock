package app

import (
	"context"
	"fmt"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/luahealth"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"

	"path/filepath"
	"strings"
)

const (
	DiscoveryModeFleet  = "fleet"
	DiscoveryModeStatic = "static"

	DefaultMaxDiscoveryDepth = 4
)

type BuildRequest struct {
	Path   string
	Strict bool
	// StatusOnly renders Applications for validation without retaining manifests.
	StatusOnly bool
	DiscoveryOptions
	ValidateLuaHealth bool
	AcquisitionOptions
	PluginOptions
	ExecutionOptions
	FilterOptions
	PluginRenderer render.PluginRenderer
	Applications   []argoappv1.Application
	ApplicationSetOptions
	StatusCallback          ApplicationStatusCallback
	renderCache             *applicationRenderCache
	renderSettingsSignature string
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

type ApplicationStatusEvent struct {
	Status    ApplicationStatus
	Completed int
	Total     int
}

type ApplicationStatusCallback func(ApplicationStatusEvent) error

type PluginExecution struct {
	AppNamespace string `json:"appNamespace" yaml:"appNamespace"`
	AppName      string `json:"appName" yaml:"appName"`
	SourceIndex  int    `json:"sourceIndex" yaml:"sourceIndex"`
	SourceName   string `json:"sourceName,omitempty" yaml:"sourceName,omitempty"`
	SourcePath   string `json:"sourcePath,omitempty" yaml:"sourcePath,omitempty"`
	PluginName   string `json:"pluginName" yaml:"pluginName"`
	Engine       string `json:"engine" yaml:"engine"`
	Phase        string `json:"phase" yaml:"phase"`
	Command      string `json:"command" yaml:"command"`
	Duration     string `json:"duration" yaml:"duration"`
}

type BuildResult struct {
	Applications            []argoappv1.Application
	ApplicationInputs       []ApplicationSelectionInput
	Projects                []argoappv1.AppProject
	Manifests               []render.Manifest
	ApplicationManifests    []ApplicationManifest
	Diagnostics             []diagnostic.Diagnostic
	Settings                config.ArgoSettings
	Statuses                []ApplicationStatus
	CacheEvents             []cacheevent.Event
	PluginExecutions        []PluginExecution
	renderCache             *applicationRenderCache
	renderSettingsSignature string
}

type DiagRequest = BuildRequest

type DiagResult struct {
	Applications     []argoappv1.Application
	Diagnostics      []diagnostic.Diagnostic
	Settings         config.ArgoSettings
	CacheEvents      []cacheevent.Event
	PluginExecutions []PluginExecution
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
		Applications:     result.Applications,
		Diagnostics:      result.Diagnostics,
		Settings:         result.Settings,
		CacheEvents:      result.CacheEvents,
		PluginExecutions: result.PluginExecutions,
	}
	if err != nil {
		return diagResult, err
	}
	if err := diagnosticFailure(result.Diagnostics, request.Strict); err != nil {
		return diagResult, err
	}
	return diagResult, nil
}

func (o Orchestrator) ListApplications(ctx context.Context, request BuildRequest) (BuildResult, error) {
	root := request.Path
	if root == "" {
		root = "."
	}

	var result BuildResult
	loadedRequest, policyDiags, cleanup, err := ensureBuildPluginPolicy(ctx, request, root)
	defer cleanup()
	result.Diagnostics = append(result.Diagnostics, policyDiags...)
	if err != nil {
		return result, err
	}
	request = loadedRequest
	discovered, discoveryDiags, cacheEvents, renderCache, renderSettingsSignature, err := o.discoverRepository(ctx, root, request)
	result.renderCache = renderCache
	result.renderSettingsSignature = renderSettingsSignature
	result.CacheEvents = append(result.CacheEvents, cacheEvents...)
	discoveryDiags = normalizeDiagnostics(discoveryDiags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, discoveryDiags...)
	if err != nil {
		return result, diagnosticsError(discoveryDiags, err)
	}

	settings, settingsDiags, err := loadSettingsFromDiscovery(root, discovered)
	if err != nil {
		return result, err
	}

	result.Settings = settings
	settingsDiags = normalizeDiagnostics(settingsDiags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, settingsDiags...)
	result.Projects = appendDiscoveredProjects(result.Projects, discovered)
	if err := appendDiscoveredApplications(discovered, &result); err != nil {
		return result, err
	}

	result.Diagnostics = dedupeDiagnostics(result.Diagnostics)
	if err := diagnosticFailure(result.Diagnostics, request.Strict); err != nil {
		return result, err
	}
	return result, nil
}

func appendDiscoveredApplications(discovered discovery.Result, result *BuildResult) error {
	for _, appFile := range discovered.Applications {
		result.Applications = append(result.Applications, appFile.Application)
		result.ApplicationInputs = append(result.ApplicationInputs, ApplicationSelectionInput{
			Application: appFile.Application,
			Paths:       discoveredApplicationInputPaths(appFile),
		})
	}
	return nil
}

func discoveredApplicationInputPaths(appFile discovery.ApplicationFile) []string {
	paths := append([]string(nil), appFile.InputPaths...)
	if len(paths) == 0 && appFile.Path != "" {
		paths = []string{appFile.Path}
	}
	for i := range paths {
		paths[i] = filepath.ToSlash(paths[i])
	}
	return paths
}

func generatedApplicationInputPaths(appSetFile discovery.ApplicationSetFile, app appset.GeneratedApplication) []string {
	paths := append([]string(nil), appSetFile.InputPaths...)
	if len(paths) == 0 && appSetFile.Path != "" {
		paths = []string{appSetFile.Path}
	}
	for i := range paths {
		paths[i] = filepath.ToSlash(paths[i])
	}
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

func normalizeDiscoveryMode(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DiscoveryModeFleet, nil
	}
	switch value {
	case DiscoveryModeFleet, DiscoveryModeStatic:
		return value, nil
	default:
		return "", fmt.Errorf("discovery-mode must be %q or %q", DiscoveryModeFleet, DiscoveryModeStatic)
	}
}

func normalizeMaxDiscoveryDepth(value int, explicitlySet bool) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("max-discovery-depth must be greater than or equal to 0")
	}
	if value == 0 && !explicitlySet {
		return DefaultMaxDiscoveryDepth, nil
	}
	return value, nil
}

type renderApplicationsRequest struct {
	applications      []argoappv1.Application
	provider          localProvider
	renderCache       *applicationRenderCache
	settingsSignature string
	request           BuildRequest
	strict            bool
	statusOnly        bool
	settingsFilter    manifest.SettingsResourceFilter
	resourceFilter    manifest.ResourceFilter
	healthEvaluator   *luahealth.Evaluator
	recordEvents      bool
	parallelism       int
	statusCallback    ApplicationStatusCallback
}

type renderApplicationsResult struct {
	manifests            []render.Manifest
	applicationManifests []ApplicationManifest
	diagnostics          []diagnostic.Diagnostic
	statuses             []ApplicationStatus
	cacheEvents          []cacheevent.Event
	pluginExecutions     []PluginExecution
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
		if err := emitApplicationStatusEvent(request.statusCallback, result, len(out.statuses), len(request.applications)); err != nil {
			return out, err
		}
		if result.err != nil {
			return out, result.err
		}
	}
	return out, nil
}

func renderApplicationsParallel(ctx context.Context, request renderApplicationsRequest) (renderApplicationsResult, error) {
	results, completed, parallelErr := runOrderedParallel(ctx, orderedParallelOptions[applicationRenderResult]{
		total:       len(request.applications),
		parallelism: request.parallelism,
		run: func(ctx context.Context, index int) applicationRenderResult {
			return renderOneApplication(ctx, request.applications[index], request)
		},
		onComplete: func(result applicationRenderResult, completed, total int) error {
			return emitApplicationStatusEvent(request.statusCallback, result, completed, total)
		},
	})

	var out renderApplicationsResult
	var renderErr error
	for index, result := range results {
		if !completed[index] || !result.set {
			continue
		}
		appendApplicationRenderResult(&out, result)
		if result.err != nil && renderErr == nil {
			renderErr = result.err
		}
	}
	if parallelErr != nil {
		return out, parallelErr
	}
	if renderErr != nil {
		return out, renderErr
	}
	return out, nil
}

func emitApplicationStatusEvent(callback ApplicationStatusCallback, result applicationRenderResult, completed, total int) error {
	if callback == nil {
		return nil
	}
	for _, status := range result.statuses {
		if err := callback(ApplicationStatusEvent{Status: status, Completed: completed, Total: total}); err != nil {
			return err
		}
	}
	return nil
}

func appendApplicationRenderResult(out *renderApplicationsResult, result applicationRenderResult) {
	out.manifests = append(out.manifests, result.manifests...)
	out.applicationManifests = append(out.applicationManifests, result.applicationManifests...)
	out.diagnostics = append(out.diagnostics, result.diagnostics...)
	out.statuses = append(out.statuses, result.statuses...)
	out.cacheEvents = append(out.cacheEvents, result.cacheEvents...)
	out.pluginExecutions = append(out.pluginExecutions, result.pluginExecutions...)
}

func renderOneApplication(ctx context.Context, application argoappv1.Application, request renderApplicationsRequest) applicationRenderResult {
	provider := request.provider
	recorder := cacheevent.NewRecorder(request.recordEvents)
	var pluginExecutions []PluginExecution
	provider.cacheEvents = recorder
	provider.pluginExecutions = &pluginExecutions
	out := applicationRenderResult{set: true}

	rendered, err := renderApplicationCached(renderContext{
		context:           ctx,
		provider:          provider,
		cache:             request.renderCache,
		settingsSignature: request.settingsSignature,
		request:           request.request,
	}, application)
	if err != nil {
		out.pluginExecutions = append(out.pluginExecutions, pluginExecutions...)
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
	out.pluginExecutions = append(out.pluginExecutions, pluginExecutions...)
	out.diagnostics = append(out.diagnostics, rendered.Diagnostics...)
	if err := diagnosticFailure(rendered.Diagnostics, request.strict); err != nil {
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		return out
	}
	cluster := applicationDestinationCluster(application)
	filteredManifests := make([]render.Manifest, 0, len(rendered.Manifests))
	for _, renderedManifest := range rendered.Manifests {
		id := manifest.IdentityOf(renderedManifest.Object)
		if request.settingsFilter.Drop(id, cluster) {
			continue
		}
		if request.resourceFilter.Drop(renderedManifest.Object) {
			continue
		}
		filteredManifests = append(filteredManifests, renderedManifest)
	}

	if request.healthEvaluator != nil {
		healthDiags := request.healthEvaluator.Validate(ctx, luahealth.Request{
			Application: luahealth.ApplicationRef{
				Namespace: application.Namespace,
				Name:      application.Name,
			},
			Manifests: filteredManifests,
		})
		healthDiags = normalizeDiagnostics(healthDiags, request.strict, false)
		out.diagnostics = append(out.diagnostics, healthDiags...)
		if err := diagnosticFailure(healthDiags, request.strict); err != nil {
			out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
			out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
			return out
		}
	}

	if request.statusOnly {
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusPass, ""))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		return out
	}

	for _, renderedManifest := range filteredManifests {
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
	discovered, discoveryDiags, cacheEvents, renderCache, renderSettingsSignature, err := o.discoverRepository(ctx, root, request)
	result.renderCache = renderCache
	result.renderSettingsSignature = renderSettingsSignature
	result.CacheEvents = append(result.CacheEvents, cacheEvents...)
	discoveryDiags = normalizeDiagnostics(discoveryDiags, request.Strict, false)
	result.Diagnostics = append(result.Diagnostics, discoveryDiags...)
	if err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, diagnosticsError(discoveryDiags, err)
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
			if candidate.Object != nil {
				settings, nextDiags, err = config.LoadFromConfigMapObject(candidate.Path, candidate.Object)
			} else {
				settings, nextDiags, err = config.LoadFromConfigMapDocument(path, candidate.DocumentIndex)
			}
		case "argocd-cmp-cm":
			if candidate.Object != nil {
				settings, nextDiags, err = config.LoadConfigManagementPluginConfigMapObject(candidate.Path, candidate.Object)
			} else {
				settings, nextDiags, err = config.LoadConfigManagementPluginConfigMapDocument(path, candidate.DocumentIndex)
			}
		case "argocd-values":
			if candidate.Object != nil {
				settings, nextDiags, err = config.LoadFromHelmValuesObject(candidate.Path, candidate.Object)
			} else {
				settings, nextDiags, err = config.LoadFromHelmValuesDocument(path, candidate.DocumentIndex)
			}
		case "repository-secret":
			if candidate.Object != nil {
				settings, nextDiags, err = config.LoadRepositorySecretObject(candidate.Path, candidate.Object)
			} else {
				settings, nextDiags, err = config.LoadRepositorySecretDocument(path, candidate.DocumentIndex)
			}
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

func settingsBuildOptions(settings config.ArgoSettings) []string {
	out := make([]string, 0, len(settings.KustomizeBuildOptions))
	for _, option := range settings.KustomizeBuildOptions {
		out = append(out, option.Value)
	}
	return out
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
	buildRequest.renderCache = listResult.renderCache
	buildRequest.renderSettingsSignature = listResult.renderSettingsSignature
	buildResult, err := o.Build(ctx, buildRequest)
	buildResult.ApplicationInputs = selectApplicationInputsForApplication(listResult.ApplicationInputs, selected)
	buildResult.Diagnostics = append(append([]diagnostic.Diagnostic(nil), listResult.Diagnostics...), buildResult.Diagnostics...)
	buildResult.Diagnostics = dedupeDiagnostics(buildResult.Diagnostics)
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
