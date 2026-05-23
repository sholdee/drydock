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
	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/discovery"
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
	RepoMaps                     []sourcepkg.RepoMap
	AllowNetwork                 bool
	GitCacheDir                  string
	RefreshGit                   bool
	RefreshRemoteResources       bool
	RemoteResourceCacheDir       string
	RemoteResourceForbiddenRoots []string
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

type BuildResult struct {
	Applications         []argoappv1.Application
	ApplicationInputs    []ApplicationSelectionInput
	Manifests            []render.Manifest
	ApplicationManifests []ApplicationManifest
	Diagnostics          []diagnostic.Diagnostic
}

type Orchestrator struct {
	ChartAcquirer          chart.Acquirer
	GitAcquirer            sourcepkg.GitAcquirer
	RemoteResourceAcquirer remote.Acquirer
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

	var result BuildResult
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
			paths := []string{filepath.ToSlash(appSetPath)}
			if app.SourcePath != "" {
				paths = append(paths, filepath.ToSlash(app.SourcePath))
			}
			result.ApplicationInputs = append(result.ApplicationInputs, ApplicationSelectionInput{
				Application: app.Application,
				Paths:       paths,
			})
		}
	}

	return result, nil
}

func (o Orchestrator) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if err := validateBuildNetworkOptions(request); err != nil {
		return BuildResult{}, err
	}
	var result BuildResult
	if request.Applications != nil {
		result.Applications = append(result.Applications, request.Applications...)
	} else {
		listResult, err := o.ListApplications(ctx, request)
		if err != nil {
			return listResult, err
		}
		result = listResult
	}

	root := request.Path
	if root == "" {
		root = "."
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
		gitCacheDir:                  request.GitCacheDir,
		refreshGit:                   request.RefreshGit,
		refreshRemoteResources:       request.RefreshRemoteResources,
		remoteResourceCacheDir:       request.RemoteResourceCacheDir,
		remoteResourceForbiddenRoots: forbiddenRoots,
	}
	for _, application := range result.Applications {
		rendered, err := RenderApplication(ctx, application, provider)
		if err != nil {
			return result, err
		}
		rendered.Diagnostics = normalizeDiagnostics(rendered.Diagnostics, request.Strict, false)
		result.Diagnostics = append(result.Diagnostics, rendered.Diagnostics...)
		if err := diagnosticFailure(rendered.Diagnostics, request.Strict); err != nil {
			return result, err
		}
		for _, renderedManifest := range rendered.Manifests {
			result.Manifests = append(result.Manifests, renderedManifest)
			result.ApplicationManifests = append(result.ApplicationManifests, ApplicationManifest{
				Application: application,
				Manifest:    renderedManifest,
			})
		}
	}

	return result, nil
}

func (o Orchestrator) BuildApp(ctx context.Context, request BuildAppRequest) (BuildResult, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return BuildResult{}, fmt.Errorf("application name is required")
	}

	buildRequest := request.BuildRequest
	listResult, err := o.ListApplications(ctx, buildRequest)
	if err != nil {
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
	gitCacheDir                  string
	refreshGit                   bool
	refreshRemoteResources       bool
	remoteResourceCacheDir       string
	remoteResourceForbiddenRoots []string
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
	opts.RemoteResourceAcquirer = p.remoteResourceAcquirer
	opts.RemoteResourceCacheDir = p.remoteResourceCacheDir
	opts.OfflineRemoteResources = p.offline
	opts.RefreshRemoteResources = p.refreshRemoteResources
	opts.RemoteResourceForbiddenRoots = p.remoteResourceForbiddenRoots
	opts.RemoteResourceForbiddenRoots = appendUniqueString(opts.RemoteResourceForbiddenRoots, sourceRoot)
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
	})
	if err != nil {
		return "", err
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
		CacheDir: p.chartCacheDir,
		Offline:  p.offline,
		Refresh:  p.refreshCharts,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("acquire chart %s: %s", source.Chart, redactChartAcquireError(err, source.RepoURL))
	}

	return (render.HelmRenderer{}).Render(ctx, render.ResolvedSource{
		RepoRoot:       acquired.ChartDir,
		Path:           ".",
		Chart:          source.Chart,
		RepoURL:        source.RepoURL,
		TargetRevision: source.TargetRevision,
	}, opts)
}

func redactChartAcquireError(err error, repository string) string {
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
	if request.GitCacheDir == "" {
		return nil
	}
	forbiddenRoots := append([]string(nil), request.RemoteResourceForbiddenRoots...)
	forbiddenRoots = append(forbiddenRoots, request.Path)
	inside, root, err := remote.IsPathInsideAny(request.GitCacheDir, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("git cache dir %q must not be inside repository root %q", request.GitCacheDir, root)
	}
	return nil
}
