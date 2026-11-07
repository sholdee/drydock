package app

import (
	"fmt"
	"sort"
	"time"

	"github.com/sholdee/drydock/internal/cacheevent"
	chartpkg "github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
)

// chartCacheStaleCode is the warning for a cached chart older than
// --chart-cache-max-age that an --offline run reuses instead of refetching.
const chartCacheStaleCode = "source.chart-cache-stale"

// staleChartDiagnostics reports each chart acquisition in records that
// reused an expired cache entry, or resolved a version range from an
// expired recorded version list, because --offline forbids refetching it.
// Records of one chart repeat (each acquisition records itself); one warning
// per chart cache entry is enough.
func staleChartDiagnostics(records []cacheevent.AcquisitionRecord) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	seen := map[string]struct{}{}
	for _, record := range records {
		if record.Kind != cacheevent.AcquisitionChart || (!record.Chart.Stale && !record.Chart.VersionListStale) {
			continue
		}
		chart := record.Chart
		key := chart.CacheKind + "\x00" + chart.CacheKey + "\x00" + chart.Target + "\x00" + chart.Name + "\x00" + chart.Version
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if chart.Stale {
			diags = append(diags, staleChartDiagnostic(fmt.Sprintf("cached chart %s %s from %s %s, older than --chart-cache-max-age %s; using it because --offline forbids refetching it",
				chart.Name, chart.Version, chart.Target, staleFetchAge(chart.FetchedAt), chart.MaxAge)))
		}
		if chart.VersionListStale {
			diags = append(diags, staleChartDiagnostic(fmt.Sprintf("recorded version list of chart %s from %s %s, older than --chart-cache-max-age %s; resolving version range %s to %s from it because --offline forbids refetching it",
				chart.Name, chart.Target, staleFetchAge(chart.VersionListFetchedAt), chart.MaxAge, record.RequestedRevision, record.ResolvedRevision)))
		}
	}
	return diags
}

func staleChartDiagnostic(message string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     chartCacheStaleCode,
		Severity: diagnostic.SeverityWarning,
		Category: "source",
		Message:  message,
	}
}

// staleFetchAge words when a stale cache entry or version list was fetched.
func staleFetchAge(fetchedAt time.Time) string {
	if fetchedAt.IsZero() {
		return "has no recorded fetch time"
	}
	return "was fetched at " + fetchedAt.UTC().Format(time.RFC3339)
}

// renderCacheChartsFromRecords collects the chart cache entries one render
// read, deduplicated and sorted so equal renders store equal payloads, and
// reports whether any was an expired chart reused offline.
func renderCacheChartsFromRecords(records []cacheevent.AcquisitionRecord) (renderCacheCharts, bool) {
	out := renderCacheCharts{recorded: true}
	stale := false
	seen := map[renderCacheChart]struct{}{}
	for _, record := range records {
		if record.Kind != cacheevent.AcquisitionChart {
			continue
		}
		if record.Chart.Stale || record.Chart.VersionListStale {
			stale = true
		}
		entry := renderCacheChart{
			Kind: record.Chart.CacheKind,
			Key:  record.Chart.CacheKey,
		}
		if !record.Chart.FetchedAt.IsZero() {
			entry.FetchedAt = record.Chart.FetchedAt.UTC()
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out.charts = append(out.charts, entry)
	}
	sort.Slice(out.charts, func(i, j int) bool {
		a, b := out.charts[i], out.charts[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.FetchedAt.Before(b.FetchedAt)
	})
	return out, stale
}

// renderCacheChartsFresh reports whether a cached render may be served under
// a chart cache max age: its payload recorded the charts it read, each is
// still the entry the chart cache under cacheDir holds (same fetch time, so
// not refetched since), and none is older than maxAge at now. Otherwise it
// returns the miss reason, and the render re-runs: online it refetches the
// expired chart, offline it reuses it with a source.chart-cache-stale
// warning.
func renderCacheChartsFresh(charts renderCacheCharts, cacheDir string, maxAge time.Duration, now time.Time) (string, bool) {
	if !charts.recorded {
		return renderCacheReasonChartUnverified, false
	}
	for _, entry := range charts.charts {
		if entry.Key == "" || entry.FetchedAt.IsZero() {
			return renderCacheReasonChartUnverified, false
		}
		fetchedAt, ok := chartpkg.EntryFetchedAt(cacheDir, chartpkg.RepositoryKind(entry.Kind), entry.Key)
		if !ok || !fetchedAt.Equal(entry.FetchedAt) {
			return renderCacheReasonChartUnverified, false
		}
		if chartpkg.Expired(fetchedAt, true, maxAge, now) {
			return renderCacheReasonChartExpired, false
		}
	}
	return "", true
}
