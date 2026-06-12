package app

import (
	"context"
	"fmt"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

func chartSensitiveValues(credentials chart.ChartCredentials) []string {
	return cacheevent.CompactSensitiveValues(credentials.Username, credentials.Password, credentials.BearerToken)
}

func (p localProvider) renderChartOnlySource(ctx context.Context, source render.ResolvedSource, opts render.RenderOptions) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	kind := render.ChartRepositoryKind(source.RepoURL, opts.OCIChartRepositories)

	acquirer := p.chartAcquirer
	if acquirer == nil {
		acquirer = chart.DefaultAcquirer{}
	}
	acquirer = p.acquisition.ChartAcquirer(acquirer)

	acquired, err := acquirer.Acquire(ctx, chart.Request{
		Repository: source.RepoURL,
		Name:       source.Chart,
		Version:    source.TargetRevision,
		Kind:       kind,
	}, chart.Options{
		CacheDir:        p.chartCacheDir,
		Offline:         p.offline,
		Refresh:         p.refreshCharts,
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
	p.recordAcquisition(cacheevent.AcquisitionRecord{
		Kind:              cacheevent.AcquisitionChart,
		RequestedRevision: source.TargetRevision,
		ResolvedRevision:  acquired.Version,
	})

	if p.renderObserver != nil {
		p.renderObserver(source)
	}
	return (render.HelmRenderer{}).Render(ctx, render.ResolvedSource{
		RepoRoot:       acquired.ChartDir,
		Path:           ".",
		Chart:          source.Chart,
		RepoURL:        source.RepoURL,
		TargetRevision: source.TargetRevision,
	}, opts)
}
