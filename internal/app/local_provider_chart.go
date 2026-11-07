package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

func chartSensitiveValues(credentials chart.ChartCredentials) []string {
	return cacheevent.CompactSensitiveValues(credentials.Username, credentials.Password, credentials.BearerToken)
}

// renderChartOnlySource acquires a chart from a chart repository and
// renders it. The Argo CD v3.6.1 repo-server extracts the chart and passes
// its directory to GenerateManifests as the source path
// (reposerver/repository/repository.go:451-470), so the source type is
// picked there as for a path source: with gates, argocd-cm helm.enable
// "false" renders the chart directory as a plain directory. A nil gates is
// for a config management plugin compatibility path, which runs helm
// template whatever argocd-cm says.
func (p localProvider) renderChartOnlySource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions, gates *sourceTypeGates) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	kind := render.ChartRepositoryKind(source.RepoURL, opts.OCIChartRepositories)

	acquirer := p.chartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	acquirer = p.acquisition.ChartAcquirer(acquirer)

	request := chart.Request{
		Repository:    source.RepoURL,
		Name:          source.Chart,
		Version:       source.TargetRevision,
		Kind:          kind,
		VersionPolicy: chart.VersionArgoTargetRevision,
	}
	acquired, err := acquirer.Acquire(ctx, request, chart.Options{
		CacheDir:        p.chartCacheDir,
		Offline:         p.offline,
		Refresh:         p.refreshCharts,
		MaxAge:          p.chartCacheMaxAge,
		ForbiddenRoots:  append([]string(nil), opts.ChartForbiddenRoots...),
		PassCredentials: opts.PassCredentials,
		Credentials:     p.chartCredentials,
	})
	if err != nil {
		acquireError := cacheevent.NewAcquisitionError(cacheevent.AcquisitionEventInput{
			Source:            cacheevent.SourceChart,
			Target:            source.RepoURL,
			RequestedRevision: source.TargetRevision,
			Offline:           p.offline,
			Refresh:           p.refreshCharts,
			Err:               err,
			SensitiveValues:   chartSensitiveValues(p.chartCredentials),
		})
		p.recordCacheEvent(acquireError.Event)
		return nil, nil, fmt.Errorf("acquire chart %s: %s", source.Chart, acquireError.RedactedError)
	}
	p.recordCacheEvent(cacheevent.NewAcquisitionEvent(cacheevent.AcquisitionEventInput{
		Source:            cacheevent.SourceChart,
		Target:            source.RepoURL,
		Revision:          acquired.Version,
		RequestedRevision: source.TargetRevision,
		FromCache:         acquired.FromCache,
		Network:           !acquired.FromCache,
		Offline:           p.offline,
		Refresh:           p.refreshCharts,
	}))
	p.recordAcquisition(render.ChartAcquisitionRecord(request, acquired, p.chartCacheMaxAge))
	// The repo-server renders with the version that a range resolved to
	// as ARGOCD_APP_REVISION (newHelmClientResolveRevision and newEnv,
	// reposerver/repository/repository.go:3029-3066 and 1864-1878 at
	// v3.6.1). An exact version stays as written.
	if resolved := strings.TrimSpace(acquired.Version); resolved != "" && resolved != strings.TrimSpace(source.TargetRevision) {
		opts.ArgoEnv = withArgoAppRevision(opts.ArgoEnv, resolved)
	}

	if err := p.checkOutOfBoundsSymlinks(ctx, acquired.ChartDir, render.OutOfBoundsSymlinkScopeChart); err != nil {
		return nil, nil, err
	}
	if err := p.checkHelmRefRootsOutOfBoundsSymlinks(ctx, opts); err != nil {
		return nil, nil, err
	}
	if p.renderObserver != nil {
		p.renderObserver(source)
	}
	chartSource := render.ResolvedSource{
		RepoRoot:       acquired.ChartDir,
		Path:           ".",
		Chart:          source.Chart,
		RepoURL:        source.RepoURL,
		TargetRevision: source.TargetRevision,
		ExplicitType:   source.ExplicitType,
	}
	var renderer render.Renderer = render.HelmRenderer{}
	if gates != nil {
		if renderer, err = selectLocalRenderer(chartSource, *gates); err != nil {
			return nil, nil, err
		}
	}
	return renderer.Render(ctx, chartSource, opts)
}
