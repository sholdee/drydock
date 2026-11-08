package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/luahealth"
	"github.com/sholdee/drydock/internal/manifest"
	"github.com/sholdee/drydock/internal/ociartifact"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/plugincontainer"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/project"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

const (
	DiscoveryModeFleet  = "fleet"
	DiscoveryModeStatic = "static"

	DefaultMaxDiscoveryDepth = 4
)

type BuildRequest struct {
	Path                   string
	Strict                 bool
	ProjectDiagnosticsMode diagnostic.ProjectDiagnosticsMode
	// StatusOnly renders Applications for validation without retaining manifests.
	StatusOnly bool
	DiscoveryOptions
	ValidateLuaHealth bool
	AcquisitionOptions
	RenderCacheOptions
	PluginOptions
	ExecutionOptions
	FilterOptions
	CapabilityOptions
	PluginRenderer render.PluginRenderer
	Applications   []argoappv1.Application
	ApplicationSetOptions
	CRDScopeOptions
	StatusCallback          ApplicationStatusCallback
	renderCache             *applicationRenderCache
	renderSettingsSignature string
	discovered              *discovery.Result
	snapshotSession         *acquisition.SnapshotSession
	persistentRenderCache   *persistentRenderCache
	// symlinkBounds keeps the out-of-bounds symlink check of each tree for
	// the run, shared by every provider the run builds.
	symlinkBounds *symlinkBoundsMemo
	rootRevision  string
	// rootRepo is the Git repository that the rootRevision snapshot came
	// from. It is set with rootRevision.
	rootRepo string
	// rootSnapshot describes the submodules of the rootRevision snapshot.
	rootSnapshot         snapshotSubmodules
	selfRepo             selfRepoRefs
	discoveryPathMemo    *sync.Map
	appsetGenerationMemo *sync.Map
	settingsInstallMemo  *sync.Map
	// appsetGeneration, when set, holds the argocd-cmd-params-cm values
	// that change ApplicationSet generation for a restarted discovery run
	// (discoverRepositoryWithOCI).
	appsetGeneration *appsetGenerationSettings
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
	// ParentKey (applicationKey) and RenderedDir carry what rendered a
	// discovered Application (discovery.ApplicationFile), so changed-only
	// selection can select it with its origin. They are selection-only and
	// never key a cache.
	ParentKey   string
	RenderedDir string
	// ApplicationSet is the namespace/name of the ApplicationSet that
	// generated the Application, or "". Project validation names it
	// (generatedApplicationSets).
	ApplicationSet string
	// GitGeneratorProject is the AppProject that a Git generator of that
	// ApplicationSet looked up, or "".
	GitGeneratorProject string
}

type ApplicationSelector func([]argoappv1.Application) []argoappv1.Application

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
	Runtime      string `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Image        string `json:"image,omitempty" yaml:"image,omitempty"`
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
	discovered              *discovery.Result
	pluginOptions           PluginOptions
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
	OCIArtifactAcquirer    ociartifact.Acquirer
	PluginRenderer         render.PluginRenderer
	PluginExecRunner       pluginexec.Runner
	PluginContainerRunner  plugincontainer.Runner

	// renderObserver is a test seam invoked immediately before local
	// render-engine invocations. It is nil in production.
	renderObserver func(render.ResolvedSource)
	// discoveryRunObserver is a test seam invoked at the start of each
	// discovery run (discoverRepositoryRun). It is nil in production.
	discoveryRunObserver func()
}

// ensureBuildSelfRepoRefs populates request.selfRepo for single-tree
// build/list surfaces so sources naming the local checkout's own repository
// at ""/HEAD or a symref-derived default-branch name resolve to the local
// tree (the #207 diff behavior extended to render surfaces). It runs in
// exactly the two leaves every caller reaches — ListApplications and Build —
// never at delegating entries. Populating only when urlKeys is empty is THE
// diff-path protection: per-side diff builds re-enter these leaves carrying
// refs cloned from the diff request, and re-detection here would replace
// them with side-local remotes. The inert edge — empty urlKeys with
// non-empty revisions re-detects to zero — is harmless because isSelfRepoRef
// short-circuits on empty urlKeys.
func ensureBuildSelfRepoRefs(request BuildRequest) BuildRequest {
	if len(request.selfRepo.urlKeys) > 0 {
		return request
	}
	root := request.Path
	if root == "" {
		root = "."
	}
	request.selfRepo = detectBuildSelfRepoRefs(root)
	return request
}

// resolveBuildRequestRoot resolves a build root that is itself a symlink
// (pathsafety.ResolveSymlinkedRoot), so discovery, rendering, the symlink
// check, and the cache keys all read the tree it points to.
func resolveBuildRequestRoot(request BuildRequest) BuildRequest {
	request.Path = pathsafety.ResolveSymlinkedRoot(request.Path)
	request.RepoMaps = resolveRepoMapRoots(request.RepoMaps)
	return request
}

// resolveRepoMapRoots returns a copy of repoMaps whose roots that are
// themselves symlinks are resolved, so discovery, the source resolver, and
// every other reader of a mapped root see the tree it points to.
func resolveRepoMapRoots(repoMaps []sourcepkg.RepoMap) []sourcepkg.RepoMap {
	if len(repoMaps) == 0 {
		return repoMaps
	}
	out := make([]sourcepkg.RepoMap, len(repoMaps))
	for i, repoMap := range repoMaps {
		out[i] = sourcepkg.RepoMap{URL: repoMap.URL, Path: pathsafety.ResolveSymlinkedRoot(repoMap.Path)}
	}
	return out
}

func ensureSnapshotSession(request BuildRequest) (BuildRequest, func(), error) {
	if request.snapshotSession != nil {
		return request, func() {}, nil
	}
	session, err := acquisition.NewSnapshotSession("drydock-cache-snapshots-*")
	if err != nil {
		return request, func() {}, err
	}
	request.snapshotSession = session
	return request, session.Close, nil
}

// Diag renders the Applications and reports the diagnostics of the run.
// When selectApps is not nil, it renders only the Applications that
// selectApps keeps (BuildSelection). Discovery, settings, and plugin policy
// diagnostics belong to the repository, so they are reported either way.
func (o Orchestrator) Diag(ctx context.Context, request DiagRequest, selectApps ApplicationSelector) (DiagResult, error) {
	var result BuildResult
	var err error
	if selectApps != nil {
		result, err = o.BuildSelection(ctx, request, selectApps)
	} else {
		result, err = o.Build(ctx, request)
	}
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

// DiagStatic reports repository diagnostics without rendering: static
// discovery, settings metadata, and AppProject validation of the discovered
// Applications (source repositories, destinations, source namespaces, and the
// project metadata checks). Rendered-resource policy needs manifests and
// stays with Diag. ListApplications is left as a pure listing primitive
// because get, plugin-policy, the public client, and the selection paths all
// rely on it reporting nothing beyond discovery. When selectApps is not nil,
// only the Applications that it keeps are validated and returned.
func (o Orchestrator) DiagStatic(ctx context.Context, request DiagRequest, selectApps ApplicationSelector) (DiagResult, error) {
	request.DiscoveryMode = DiscoveryModeStatic
	request.MaxDiscoveryDepth = 0
	request.MaxDiscoveryDepthSet = true
	result, err := o.ListApplications(ctx, request)
	diagResult := DiagResult{
		Applications: result.Applications,
		Diagnostics:  result.Diagnostics,
		Settings:     result.Settings,
		CacheEvents:  result.CacheEvents,
	}
	if err != nil {
		return diagResult, err
	}
	if selectApps != nil {
		diagResult.Applications = selectApps(result.Applications)
	}
	projectDiags := project.ValidateGeneratedApplications(diagResult.Applications, result.Projects, result.Settings, generatedApplicationSets(result.ApplicationInputs))
	projectDiags = request.normalizeDiagnostics(projectDiags, false)
	diagResult.Diagnostics = dedupeDiagnostics(append(diagResult.Diagnostics, projectDiags...))
	if err := diagnosticFailure(projectDiags, request.Strict); err != nil {
		return diagResult, err
	}
	return diagResult, nil
}

func (o Orchestrator) ListApplications(ctx context.Context, request BuildRequest) (result BuildResult, err error) {
	if err := request.validateKubeVersion(); err != nil {
		return BuildResult{}, err
	}
	request = resolveBuildRequestRoot(request)
	request, releaseSnapshots, err := ensureSnapshotSession(request)
	if err != nil {
		return BuildResult{}, err
	}
	defer releaseSnapshots()

	if err := validateBuildRenderCacheRoot(request); err != nil {
		return BuildResult{}, err
	}
	request, releaseRenderCache := ensurePersistentRenderCache(request)
	defer func() {
		result.CacheEvents = append(result.CacheEvents, releaseRenderCache()...)
	}()
	request = ensureBuildSelfRepoRefs(request)

	if err := request.ProjectDiagnosticsMode.Validate(); err != nil {
		return BuildResult{}, err
	}
	root := request.Path
	if root == "" {
		root = "."
	}

	loadedRequest, policyDiags, cleanup, err := ensureBuildPluginPolicy(ctx, request, root)
	defer cleanup()
	policyDiags = request.filterProjectDiagnostics(policyDiags)
	result.Diagnostics = append(result.Diagnostics, policyDiags...)
	if err != nil {
		result.Diagnostics = request.filterProjectDiagnostics(result.Diagnostics)
		return result, err
	}
	request = loadedRequest
	result.pluginOptions = request.PluginOptions
	discovered, discoveryDiags, err := o.loadBuildSideDiscovery(ctx, root, request, &result)
	if err != nil {
		return result, diagnosticsError(discoveryDiags, err)
	}
	_, err = loadBuildSideSettings(root, request, discovered, &result)
	if err != nil {
		return result, err
	}

	result.Projects = appendDiscoveredProjects(result.Projects, discovered)
	if err := appendDiscoveredApplications(discovered, &result); err != nil {
		return result, err
	}

	result.Diagnostics = request.filterProjectDiagnostics(dedupeDiagnostics(result.Diagnostics))
	if err := diagnosticFailure(result.Diagnostics, request.Strict); err != nil {
		return result, err
	}
	return result, nil
}

func appendDiscoveredApplications(discovered discovery.Result, result *BuildResult) error {
	for _, appFile := range discovered.Applications {
		result.Applications = append(result.Applications, appFile.Application)
		result.ApplicationInputs = append(result.ApplicationInputs, discoveredApplicationSelectionInput(appFile))
	}
	return nil
}

func discoveredApplicationSelectionInputs(discovered discovery.Result) []ApplicationSelectionInput {
	inputs := make([]ApplicationSelectionInput, 0, len(discovered.Applications))
	for _, appFile := range discovered.Applications {
		inputs = append(inputs, discoveredApplicationSelectionInput(appFile))
	}
	return inputs
}

func discoveredApplicationSelectionInput(appFile discovery.ApplicationFile) ApplicationSelectionInput {
	return ApplicationSelectionInput{
		Application:         appFile.Application,
		Paths:               discoveredApplicationInputPaths(appFile),
		ParentKey:           appFile.ParentKey,
		RenderedDir:         appFile.RenderedDir,
		ApplicationSet:      applicationSetDisplayKey(appFile.ApplicationSetKey),
		GitGeneratorProject: stringValue(appFile.GitGeneratorProject),
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// applicationSetDisplayKey turns an ApplicationFile.ApplicationSetKey into
// namespace/name.
func applicationSetDisplayKey(key string) string {
	if key == "" {
		return ""
	}
	namespace, name, _ := strings.Cut(key, "\x00")
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// generatedApplicationSets returns the ApplicationSet that generated an
// Application, from the selection inputs, for project validation.
func generatedApplicationSets(inputs []ApplicationSelectionInput) project.GeneratedByLookup {
	byApplication := make(map[string]project.GeneratedBy, len(inputs))
	for _, input := range inputs {
		if input.ApplicationSet != "" {
			byApplication[applicationKey(input.Application)] = project.GeneratedBy{ApplicationSet: input.ApplicationSet, GitGeneratorProject: input.GitGeneratorProject}
		}
	}
	return func(application argoappv1.Application) project.GeneratedBy {
		return byApplication[applicationKey(application)]
	}
}

func discoveredApplicationInputPaths(appFile discovery.ApplicationFile) []string {
	paths := append([]string(nil), appFile.InputPaths...)
	if len(paths) == 0 && appFile.Path != "" && appFile.Tier != discovery.SourceTierExternalManifest {
		paths = []string{appFile.Path}
	}
	for i := range paths {
		paths[i] = filepath.ToSlash(paths[i])
	}
	return paths
}

func generatedApplicationInputPaths(appSetFile discovery.ApplicationSetFile, app appset.GeneratedApplication) []string {
	paths := append([]string(nil), appSetFile.InputPaths...)
	if len(paths) == 0 && appSetFile.Path != "" && appSetFile.Tier != discovery.SourceTierExternalManifest {
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

func (o Orchestrator) Build(ctx context.Context, request BuildRequest) (result BuildResult, err error) {
	request = resolveBuildRequestRoot(request)
	request, releaseSnapshots, err := ensureSnapshotSession(request)
	if err != nil {
		return BuildResult{}, err
	}
	defer releaseSnapshots()

	if err := validateBuildRenderCacheRoot(request); err != nil {
		return BuildResult{}, err
	}
	request, releaseRenderCache := ensurePersistentRenderCache(request)
	defer func() {
		result.CacheEvents = append(result.CacheEvents, releaseRenderCache()...)
	}()
	request = ensureBuildSelfRepoRefs(request)

	session, err := newBuildSession(o, request)
	if err != nil {
		return BuildResult{}, err
	}
	result, err = session.Build(ctx)
	return result, err
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
	applicationInputs map[string]applicationInputPaths
	provider          localProvider
	renderCache       *applicationRenderCache
	settingsSignature string
	trackingOptions   TrackingOptions
	request           BuildRequest
	projects          []argoappv1.AppProject
	// generatedBy names the ApplicationSet that generated an Application
	// (generatedApplicationSets), for the project.missing text.
	generatedBy     project.GeneratedByLookup
	settings        config.ArgoSettings
	strict          bool
	statusOnly      bool
	settingsFilter  manifest.SettingsResourceFilter
	resourceFilter  manifest.ResourceFilter
	healthEvaluator *luahealth.Evaluator
	recordEvents    bool
	parallelism     int
	statusCallback  ApplicationStatusCallback
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

	inputPaths, inputsDuplicate := applicationInputPathsForRender(request.applicationInputs, application)
	rendered, err := renderApplicationCached(renderContext{
		context:                    ctx,
		provider:                   provider,
		cache:                      request.renderCache,
		settingsSignature:          request.settingsSignature,
		trackingOptions:            request.trackingOptions,
		request:                    request.request,
		applicationInputPaths:      inputPaths,
		applicationInputsKnown:     request.applicationInputs != nil,
		applicationInputsDuplicate: inputsDuplicate,
	}, application)
	if err != nil {
		out.pluginExecutions = append(out.pluginExecutions, pluginExecutions...)
		out.diagnostics = append(out.diagnostics, request.request.normalizeDiagnostics(rendered.Diagnostics, false)...)
		out.diagnostics = append(out.diagnostics, renderFailureDiagnostic(application, err))
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		if ctxErr := ctx.Err(); ctxErr != nil {
			out.err = ctxErr
		}
		return out
	}

	rendered.Diagnostics = request.request.normalizeDiagnostics(rendered.Diagnostics, false)
	out.pluginExecutions = append(out.pluginExecutions, pluginExecutions...)
	out.diagnostics = append(out.diagnostics, rendered.Diagnostics...)
	if err := diagnosticFailure(rendered.Diagnostics, request.strict); err != nil {
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		return out
	}
	settingsDrop := settingsFilterDrop(application, request.settingsFilter, request.settings)
	filteredManifests := make([]render.Manifest, 0, len(rendered.Manifests))
	policyResources := make([]project.RenderedResource, 0, len(rendered.Manifests)+len(rendered.Hooks))
	for _, renderedManifest := range rendered.Manifests {
		id := manifest.IdentityOf(renderedManifest.Object)
		if settingsDrop(id) {
			continue
		}
		policyResources = append(policyResources, project.RenderedResource{
			Object:                       renderedManifest.Object,
			NamespaceBeforeNormalization: renderedManifest.NamespaceBeforeNormalization,
		})
		if request.resourceFilter.Drop(renderedManifest.Object) {
			continue
		}
		filteredManifests = append(filteredManifests, renderedManifest)
	}
	// Hooks never reach the manifests view, but Argo CD applies the same
	// AppProject permission check to every hook task at sync time and, from
	// v3.5.4, to PreDelete/PostDelete hooks at deletion.
	for _, hook := range rendered.Hooks {
		if settingsDrop(manifest.IdentityOf(hook.Object)) {
			continue
		}
		policyResources = append(policyResources, project.RenderedResource{
			Object:                       hook.Object,
			NamespaceBeforeNormalization: hook.NamespaceBeforeNormalization,
			Hook:                         true,
		})
	}

	selectorDiags := resourceSelectorMismatchDiagnostics(application, policyResources, request.settingsFilter, request.settings)
	selectorDiags = request.request.normalizeDiagnostics(selectorDiags, false)
	out.diagnostics = append(out.diagnostics, selectorDiags...)
	if err := diagnosticFailure(selectorDiags, request.strict); err != nil {
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		return out
	}

	if request.healthEvaluator != nil {
		healthDiags := request.healthEvaluator.Validate(ctx, luahealth.Request{
			Application: luahealth.ApplicationRef{
				Namespace: application.Namespace,
				Name:      application.Name,
			},
			Manifests: filteredManifests,
		})
		healthDiags = request.request.normalizeDiagnostics(healthDiags, false)
		out.diagnostics = append(out.diagnostics, healthDiags...)
		if err := diagnosticFailure(healthDiags, request.strict); err != nil {
			out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
			out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
			return out
		}
	}

	// A CRD shipped as a PreSync hook still defines the scope of the custom
	// resources the main sync applies, so hooks feed the scope registry too.
	scopeInputs := make([]render.Manifest, 0, len(rendered.Manifests)+len(rendered.Hooks))
	scopeInputs = append(scopeInputs, rendered.Manifests...)
	scopeInputs = append(scopeInputs, rendered.Hooks...)
	appScopeRegistry := manifest.BuildCRDScopeRegistry(manifestObjectPtrs(scopeInputs))
	resourcePolicyDiags := project.ValidateRenderedResourcePolicyResourcesWithRegistry(application, policyResources, request.projects, request.settings, appScopeRegistry, request.generatedBy)
	resourcePolicyDiags = request.request.normalizeDiagnostics(resourcePolicyDiags, false)
	out.diagnostics = append(out.diagnostics, resourcePolicyDiags...)
	if err := diagnosticFailure(resourcePolicyDiags, request.strict); err != nil {
		out.statuses = append(out.statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
		out.cacheEvents = append(out.cacheEvents, recorder.Events()...)
		return out
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
	if request.discovered != nil {
		discovered := *request.discovered
		result.renderCache = request.renderCache
		result.renderSettingsSignature = request.renderSettingsSignature
		result.discovered = request.discovered
		result.pluginOptions = request.PluginOptions
		result.Projects = appendDiscoveredProjects(result.Projects, discovered)
		result.ApplicationInputs = selectApplicationInputsForApplications(discoveredApplicationSelectionInputs(discovered), result.Applications)
		diags, err := loadBuildSideSettings(root, request, discovered, &result)
		if err != nil {
			result.Statuses = skippedApplicationStatuses(result.Applications, err)
			return result, err
		}
		if err := diagnosticFailure(request.filterProjectDiagnostics(diags), request.Strict); err != nil {
			result.Statuses = skippedApplicationStatuses(result.Applications, err)
			return result, err
		}
		return result, nil
	}
	discovered, discoveryDiags, err := o.loadBuildSideDiscovery(ctx, root, request, &result)
	if err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, diagnosticsError(discoveryDiags, err)
	}
	result.Projects = appendDiscoveredProjects(result.Projects, discovered)
	diags, err := loadBuildSideSettings(root, request, discovered, &result)
	if err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, err
	}
	if err := diagnosticFailure(request.filterProjectDiagnostics(diags), request.Strict); err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, err
	}
	return result, nil
}

// settingsBuildOptions returns the kustomize.buildOptions words of a found
// argocd-cm (config.ArgoSettings.EffectiveKustomizeBuildOptions), and none
// without one: the renderer then assumes --enable-helm itself
// (settingsKustomizeCaller).
func settingsBuildOptions(settings config.ArgoSettings) []string {
	if !settings.ArgoCDConfigMapFound {
		return nil
	}
	return settings.EffectiveKustomizeBuildOptions()
}

// settingsKustomizeCaller says whether settingsBuildOptions holds the
// kustomize.buildOptions of a found argocd-cm.
func settingsKustomizeCaller(settings config.ArgoSettings) render.KustomizeBuildCaller {
	if settings.ArgoCDConfigMapFound {
		return render.KustomizeCallerArgoCD
	}
	return render.KustomizeCallerArgoCDAssumed
}

// kustomizeVersionError is the error of a Kustomize source whose
// spec.source.kustomize.version argocd-cm does not register. The Argo CD
// v3.6.1 repo-server looks the version up before it reads the kube
// version of the source (GenerateManifests, reposerver/repository/
// repository.go:1776-1785), so this error comes first.
func kustomizeVersionError(settings config.ArgoSettings, kustomize *argoappv1.ApplicationSourceKustomize) error {
	if kustomize == nil || kustomize.Version == "" || settings.KustomizeVersionRegistered(kustomize.Version) {
		return nil
	}
	return fmt.Errorf("error getting kustomize binary path: kustomize version %s is not registered", kustomize.Version)
}

func settingsHelmValueFileSchemes(settings config.ArgoSettings) []string {
	out := make([]string, 0, len(settings.HelmValuesFileSchemes))
	for _, scheme := range settings.HelmValuesFileSchemes {
		out = append(out, scheme.Value)
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

// settingsFilterDrop returns the argocd-cm resource.exclusions and
// resource.inclusions check for application. Argo CD matches the rule
// cluster globs against the resolved destination server
// (controller/state.go CompareAppState calls IsExcludedResource with
// destCluster.Server at v3.6.1), so the destination is resolved first, as the
// resource.selectors check resolves it (project.ResolveDestinationServer).
// When drydock cannot resolve the server offline, a resource is dropped only
// when Argo CD would drop it for every possible server
// (SettingsResourceFilter.DropUnresolved).
func settingsFilterDrop(application argoappv1.Application, filter manifest.SettingsResourceFilter, settings config.ArgoSettings) func(manifest.Identity) bool {
	server, ok := project.ResolveDestinationServer(application.Spec.Destination, settings)
	if !ok {
		return filter.DropUnresolved
	}
	return func(id manifest.Identity) bool {
		return filter.Drop(id, server)
	}
}

func (o Orchestrator) BuildApp(ctx context.Context, request BuildAppRequest) (BuildResult, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return BuildResult{}, fmt.Errorf("application name is required")
	}

	buildRequest := resolveBuildRequestRoot(request.BuildRequest)
	buildRequest, releaseSnapshots, err := ensureSnapshotSession(buildRequest)
	if err != nil {
		return BuildResult{}, err
	}
	defer releaseSnapshots()

	if err := validateBuildRenderCacheRoot(buildRequest); err != nil {
		return BuildResult{}, err
	}
	buildRequest, releaseRenderCache := ensurePersistentRenderCache(buildRequest)

	listResult, err := o.ListApplications(ctx, buildRequest)
	if err != nil {
		listResult.Statuses = skippedStatusesForRequestedApplication(listResult.Applications, name, err)
		listResult.CacheEvents = append(listResult.CacheEvents, releaseRenderCache()...)
		return listResult, err
	}

	selected, err := SelectApplicationByName(listResult.Applications, name)
	if err != nil {
		listResult.CacheEvents = append(listResult.CacheEvents, releaseRenderCache()...)
		return listResult, err
	}

	buildRequest.Applications = []argoappv1.Application{selected}
	buildRequest.PluginOptions = listResult.pluginOptions
	buildRequest.renderCache = listResult.renderCache
	buildRequest.renderSettingsSignature = listResult.renderSettingsSignature
	buildRequest.discovered = listResult.discovered
	buildResult, err := o.Build(ctx, buildRequest)
	buildResult.ApplicationInputs = selectApplicationInputsForApplication(listResult.ApplicationInputs, selected)
	buildResult.Diagnostics = append(append([]diagnostic.Diagnostic(nil), listResult.Diagnostics...), buildResult.Diagnostics...)
	buildResult.Diagnostics = buildRequest.filterProjectDiagnostics(dedupeDiagnostics(buildResult.Diagnostics))
	buildResult.CacheEvents = append(append([]cacheevent.Event(nil), listResult.CacheEvents...), buildResult.CacheEvents...)
	buildResult.CacheEvents = append(buildResult.CacheEvents, releaseRenderCache()...)
	return buildResult, err
}

// BuildSelection lists Applications, narrows them with selectApps, and builds
// the selection while reusing the list phase's discovery and render caches.
func (o Orchestrator) BuildSelection(ctx context.Context, request BuildRequest, selectApps ApplicationSelector) (BuildResult, error) {
	request = resolveBuildRequestRoot(request)
	request, releaseSnapshots, err := ensureSnapshotSession(request)
	if err != nil {
		return BuildResult{}, err
	}
	defer releaseSnapshots()

	if err := validateBuildRenderCacheRoot(request); err != nil {
		return BuildResult{}, err
	}
	request, releaseRenderCache := ensurePersistentRenderCache(request)

	listResult, err := o.ListApplications(ctx, request)
	if err != nil {
		listResult.Statuses = skippedApplicationStatuses(listResult.Applications, err)
		listResult.CacheEvents = append(listResult.CacheEvents, releaseRenderCache()...)
		return listResult, err
	}

	buildRequest := request
	buildRequest.PluginOptions = listResult.pluginOptions
	if selectApps != nil {
		buildRequest.Applications = selectApps(listResult.Applications)
	} else {
		buildRequest.Applications = append([]argoappv1.Application(nil), listResult.Applications...)
	}
	if buildRequest.Applications == nil {
		buildRequest.Applications = []argoappv1.Application{}
	}
	buildRequest.renderCache = listResult.renderCache
	buildRequest.renderSettingsSignature = listResult.renderSettingsSignature
	buildRequest.discovered = listResult.discovered
	buildResult, err := o.Build(ctx, buildRequest)
	buildResult.ApplicationInputs = selectApplicationInputsForApplications(listResult.ApplicationInputs, buildRequest.Applications)
	buildResult.Diagnostics = append(append([]diagnostic.Diagnostic(nil), listResult.Diagnostics...), buildResult.Diagnostics...)
	buildResult.Diagnostics = buildRequest.filterProjectDiagnostics(dedupeDiagnostics(buildResult.Diagnostics))
	buildResult.CacheEvents = append(append([]cacheevent.Event(nil), listResult.CacheEvents...), buildResult.CacheEvents...)
	buildResult.CacheEvents = append(buildResult.CacheEvents, releaseRenderCache()...)
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

func selectApplicationInputsForApplications(inputs []ApplicationSelectionInput, selected []argoappv1.Application) []ApplicationSelectionInput {
	if len(inputs) == 0 || len(selected) == 0 {
		return nil
	}
	selectedByKey := make(map[string]struct{}, len(selected))
	for _, application := range selected {
		selectedByKey[applicationKey(application)] = struct{}{}
	}
	out := make([]ApplicationSelectionInput, 0, len(selected))
	for _, input := range inputs {
		if _, ok := selectedByKey[applicationKey(input.Application)]; ok {
			out = append(out, cloneApplicationSelectionInput(input))
		}
	}
	return out
}

// applicationInputPaths carries one Application's discovery input paths plus
// whether its namespace/name key collided with another Application (in which
// case neither path set is authoritative and the app is cache-ineligible
// with a distinct reason).
type applicationInputPaths struct {
	Paths     []string
	Duplicate bool
}

func applicationInputPathsByKey(inputs []ApplicationSelectionInput) map[string]applicationInputPaths {
	if len(inputs) == 0 {
		return nil
	}
	out := make(map[string]applicationInputPaths, len(inputs))
	for _, input := range inputs {
		key := applicationKey(input.Application)
		if existing, ok := out[key]; ok {
			existing.Paths = nil
			existing.Duplicate = true
			out[key] = existing
			continue
		}
		out[key] = applicationInputPaths{Paths: append([]string(nil), input.Paths...)}
	}
	return out
}

func applicationInputPathsForRender(inputs map[string]applicationInputPaths, application argoappv1.Application) ([]string, bool) {
	if inputs == nil {
		return nil, false
	}
	entry := inputs[applicationKey(application)]
	return append([]string(nil), entry.Paths...), entry.Duplicate
}

func applicationKey(application argoappv1.Application) string {
	return application.Namespace + "\x00" + application.Name
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
		if strict && !strictExemptDiagnostic(out[i]) {
			out[i].Severity = diagnostic.SeverityError
		}
	}
	return out
}

// strictExemptDiagnostic reports whether a diagnostic is advisory-only:
// --strict must neither escalate its severity nor fail the application on it.
// The self-repo near-miss is a --repo-map remediation hint whose primary
// audience is fork-topology checkouts running strict CI renders and diffs —
// escalating it would convert exactly those working runs into hard failures.
// Rendering stays unchanged when the hint fires (remote acquisition still
// happens), so there is nothing for --strict to protect against.
// The plugin-policy env-ignored warning has the same shape: the managed
// env.allow entry is dropped whether or not the warning is shown, and the
// policy is often read from the diff baseline or --plugin-policy-ref where the
// PR branch cannot fix it, so diag/get --strict must accept the same policies
// build/diff --strict accept.
// The changed-only settings render-all is the complete answer, not an
// ownership gap: every Application is rendered. The changed-only AppProject
// render-all (the first AppProject declared or the last removed) has the
// same shape, and so does the selection a repository or cluster settings
// change widens: it selects every Application the change reaches.
// The oci-chart-ignored advisory shares the shape too: the render is
// identical to Argo CD's whether or not it fires, because Argo CD dispatches
// every oci:// repoURL as an OCI artifact source and never reads chart there
// (repository.go:358-364 at v3.5.4). Argo CD accepts the shape silently, so
// failing --strict on it would make drydock stricter than the system it
// mirrors on Applications Argo CD runs today; a stale chart that meant a
// different artifact gives --strict nothing to protect — the pull of
// repoURL:targetRevision either fails (already an error, warning alongside)
// or renders Argo-identical output.
// The source hydrator advisory is the same kind: drydock renders the sync
// source that the Argo CD controller compares, so the output matches Argo CD
// whether or not the advisory fires.
// The namespace-not-enabled warning is the same kind: Argo CD never
// reconciles an Application or ApplicationSet outside the enabled
// namespaces and reports nothing, and drydock skips it the same way.
// The known-type-skipped advisory is the same kind: Argo CD only logs a
// knownTypeFields error and compares the field as written, and drydock does
// the same, so the diff matches Argo CD whether or not the advisory fires.
// The ignoreResourceUpdates advisory is the same kind: Argo CD uses those
// rules only to skip reconciles on live updates, so desired output and
// desired-vs-desired diffs are identical whether or not they are set, and
// the argo-cd Helm chart sets eight of them by default.
// The ApplicationSet invalid-rollout-config lint is the same kind of advisory:
// with progressive syncs on, Argo CD v3.6.1 reports InvalidRolloutConfig as a
// condition that never stops Applications from being created or updated
// (applicationset_controller.go). Generation output is identical either way,
// and an ordinary staged rollout (a prod step with no Applications yet) must
// not fail --strict CI. So is rollout-strategy-ignored: with progressive
// syncs off, Argo CD ignores the RollingSync strategy and reconciles the
// Applications as usual. The empty-steps lint (appset.rollout-no-steps)
// stays promotable: with progressive syncs on it aborts reconciliation of
// the whole ApplicationSet, which then contributes no Applications.
// selector.no-match is advisory too: the command did what the -l selector
// asked, and an empty selection is a valid answer, so --strict must not
// fail a run that the user narrowed on purpose.
func strictExemptDiagnostic(diag diagnostic.Diagnostic) bool {
	switch diag.Code {
	case selfRepoNearMissCode, ociChartIgnoredCode, sourceHydratorCode, namespaceNotEnabledCode, diffKnownTypeSkippedCode, diagnostic.CodeSettingsIgnoreResourceUpdates, diagnostic.CodePluginPolicyEnvIgnored, changedOnlySettingsCode, changedOnlySettingsScopedCode, changedOnlyProjectsCode, changedOnlySymlinksCode, changedOnlyJsonnetScanCode, changedOnlyAppRevisionCode, diagnostic.CodeAppSetInvalidRolloutConfig, diagnostic.CodeAppSetRolloutStrategyIgnored, render.DirectoryExtensionFilterDisabledCode, SelectorNoMatchCode:
		return true
	}
	return false
}

func diagnosticFailure(diags []diagnostic.Diagnostic, strict bool) error {
	for _, diag := range diags {
		if diag.Severity == diagnostic.SeverityError || (strict && !strictExemptDiagnostic(diag)) {
			return fmt.Errorf("diagnostic %s: %s", diag.Category, diag.Message)
		}
	}
	return nil
}
