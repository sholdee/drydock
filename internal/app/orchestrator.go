package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/home-operations/argocd-local/internal/appset"
	"github.com/home-operations/argocd-local/internal/chart"
	"github.com/home-operations/argocd-local/internal/config"
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/discovery"
	"github.com/home-operations/argocd-local/internal/manifest"
	"github.com/home-operations/argocd-local/internal/remote"
	"github.com/home-operations/argocd-local/internal/render"
	sourcepkg "github.com/home-operations/argocd-local/internal/source"
)

type BuildRequest struct {
	Path                         string
	Strict                       bool
	Offline                      bool
	RefreshCharts                bool
	ChartCacheDir                string
	ChartCredentials             chart.ChartCredentials
	RepoMaps                     []sourcepkg.RepoMap
	AllowNetwork                 bool
	GitCacheDir                  string
	RefreshGit                   bool
	GitCredentials               sourcepkg.GitCredentials
	RefreshRemoteResources       bool
	RemoteResourceCacheDir       string
	RemoteResourceForbiddenRoots []string
	RemoteResourceCredentials    remote.Credentials
	RemoteResourceGitCredentials remote.GitCredentials
	SkipKinds                    []string
	SkipCRDs                     bool
	SkipSecrets                  bool
	Applications                 []argoappv1.Application
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
	Manifests            []render.Manifest
	ApplicationManifests []ApplicationManifest
	Diagnostics          []diagnostic.Diagnostic
	Settings             config.ArgoSettings
	Statuses             []ApplicationStatus
}

type DiagRequest = BuildRequest

type DiagResult struct {
	Applications []argoappv1.Application
	Diagnostics  []diagnostic.Diagnostic
}

type Orchestrator struct {
	ChartAcquirer          chart.Acquirer
	GitAcquirer            sourcepkg.GitAcquirer
	RemoteResourceAcquirer remote.Acquirer
}

func (o Orchestrator) Diag(ctx context.Context, request DiagRequest) (DiagResult, error) {
	result, err := o.Build(ctx, request)
	diagResult := DiagResult{
		Applications: result.Applications,
		Diagnostics:  result.Diagnostics,
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
		generated, diags, err := appset.GenerateFromYAML(root, appSetPath, data)
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

func (o Orchestrator) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	root := request.Path
	if root == "" {
		root = "."
	}

	result, err := o.prepareBuildResult(ctx, request, root)
	if err != nil {
		return result, err
	}
	if err := validateBuildNetworkOptions(request); err != nil {
		result.Statuses = skippedApplicationStatuses(result.Applications, err)
		return result, err
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
	}
	for _, application := range result.Applications {
		rendered, err := RenderApplication(ctx, application, provider)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, renderFailureDiagnostic(application, err))
			result.Statuses = append(result.Statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
			continue
		}
		rendered.Diagnostics = normalizeDiagnostics(rendered.Diagnostics, request.Strict, false)
		result.Diagnostics = append(result.Diagnostics, rendered.Diagnostics...)
		if err := diagnosticFailure(rendered.Diagnostics, request.Strict); err != nil {
			result.Statuses = append(result.Statuses, applicationStatus(application, ApplicationStatusFail, err.Error()))
			continue
		}
		cluster := applicationDestinationCluster(application)
		for _, renderedManifest := range rendered.Manifests {
			id := manifest.IdentityOf(renderedManifest.Object)
			if settingsFilter.Drop(id, cluster) {
				continue
			}
			if resourceFilter.Drop(renderedManifest.Object) {
				continue
			}
			result.Manifests = append(result.Manifests, renderedManifest)
			result.ApplicationManifests = append(result.ApplicationManifests, ApplicationManifest{
				Application: application,
				Manifest:    renderedManifest,
			})
		}
		result.Statuses = append(result.Statuses, applicationStatus(application, ApplicationStatusPass, ""))
	}

	if err := buildStatusFailure(result.Statuses); err != nil {
		return result, err
	}
	return result, nil
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
	settings, diags, err := loadSettingsFromPath(root)
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

func loadSettingsFromPath(root string) (config.ArgoSettings, []diagnostic.Diagnostic, error) {
	discovered, err := discovery.Scan(root, discovery.Options{})
	if err != nil {
		return config.DefaultSettings(), nil, err
	}
	return loadSettingsFromDiscovery(root, discovered)
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
}

func (p localProvider) RenderSource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	sourceRoot, err := p.resolveSourceRoot(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	source.RepoRoot = sourceRoot
	opts.ChartAcquirer = p.chartAcquirer
	opts.ChartCacheDir = p.chartCacheDir
	opts.OfflineCharts = p.offline
	opts.RefreshCharts = p.refreshCharts
	opts.ChartCredentials = p.chartCredentials
	opts.RemoteResourceAcquirer = p.remoteResourceAcquirer
	opts.RemoteResourceCacheDir = p.remoteResourceCacheDir
	opts.OfflineRemoteResources = p.offline
	opts.RefreshRemoteResources = p.refreshRemoteResources
	opts.RemoteResourceForbiddenRoots = p.remoteResourceForbiddenRoots
	opts.RemoteResourceForbiddenRoots = appendUniqueString(opts.RemoteResourceForbiddenRoots, sourceRoot)
	opts.RemoteResourceCredentials = p.remoteResourceCredentials
	opts.RemoteResourceGitCredentials = p.remoteResourceGitCredentials
	anchoredRefRoots, err := anchorLocalRefRoots(sourceRoot, opts.RefRoots)
	if err != nil {
		return nil, nil, err
	}
	refRoots, err := p.resolveRefRoots(ctx, opts.RefSources)
	if err != nil {
		return nil, nil, err
	}
	opts.RefRoots = mergeRefRoots(anchoredRefRoots, refRoots)
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

func (p localProvider) resolveSourceRoot(ctx context.Context, source render.ResolvedSource) (string, error) {
	if source.Path == "" && source.Chart != "" {
		return p.repoRoot, nil
	}
	if p.sourceResolver != nil {
		if mappedPath, ok := p.sourceResolver.MappedPath(source.RepoURL); ok {
			return filepath.Abs(mappedPath)
		}
	}
	if source.Path != "" {
		if exists, err := sourcePathExists(p.repoRoot, source.Path); err != nil {
			return "", err
		} else if exists {
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
		return "", fmt.Errorf("%s", sourcepkg.RedactGitCredentialError(err.Error(), p.gitCredentials))
	}
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

func (p localProvider) renderChartOnlySource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	kind := chart.RepositoryHTTP
	if strings.HasPrefix(strings.TrimSpace(source.RepoURL), "oci://") {
		kind = chart.RepositoryOCI
	}

	acquirer := p.chartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}

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
		return nil, nil, fmt.Errorf("acquire chart %s: %s", source.Chart, redactChartAcquireError(err, source.RepoURL, p.chartCredentials))
	}

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
	for _, secret := range []string{credentials.Password, credentials.BearerToken} {
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
