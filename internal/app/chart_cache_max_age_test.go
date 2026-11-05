package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestStaleChartDiagnostics(t *testing.T) {
	fetchedAt := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)
	stale := cacheevent.AcquisitionRecord{
		Kind: cacheevent.AcquisitionChart,
		Chart: cacheevent.ChartAcquisition{
			Target: "https://charts.example.test", Name: "demo", Version: "1.2.3",
			CacheKind: "http", CacheKey: "abc", FetchedAt: fetchedAt, MaxAge: 24 * time.Hour, Stale: true,
		},
	}
	unknown := stale
	unknown.Chart.CacheKey = "def"
	unknown.Chart.Name = "other"
	unknown.Chart.FetchedAt = time.Time{}
	fresh := stale
	fresh.Chart.Stale = false
	fresh.Chart.CacheKey = "ghi"

	diags := staleChartDiagnostics([]cacheevent.AcquisitionRecord{stale, fresh, stale, unknown, {Kind: cacheevent.AcquisitionGit}})
	if len(diags) != 2 {
		t.Fatalf("diagnostics = %#v, want one per stale chart entry", diags)
	}
	want := "cached chart demo 1.2.3 from https://charts.example.test was fetched at 2026-11-01T08:30:00Z, older than --chart-cache-max-age 24h0m0s; using it because --offline forbids refetching it"
	if diags[0].Message != want || diags[0].Code != chartCacheStaleCode || diags[0].Severity != diagnostic.SeverityWarning || diags[0].Category != "source" {
		t.Fatalf("diagnostic = %#v, want message %q", diags[0], want)
	}
	if !strings.Contains(diags[1].Message, "cached chart other 1.2.3 from https://charts.example.test has no recorded fetch time") {
		t.Fatalf("unknown-age diagnostic = %q", diags[1].Message)
	}
}

func TestRenderCacheChartsFromRecordsIsDeterministic(t *testing.T) {
	at := time.Date(2026, 11, 1, 8, 30, 0, 0, time.FixedZone("x", 3600))
	records := []cacheevent.AcquisitionRecord{
		{Kind: cacheevent.AcquisitionChart, Chart: cacheevent.ChartAcquisition{CacheKind: "oci", CacheKey: "b", FetchedAt: at}},
		{Kind: cacheevent.AcquisitionGit},
		{Kind: cacheevent.AcquisitionChart, Chart: cacheevent.ChartAcquisition{CacheKind: "http", CacheKey: "a", FetchedAt: at}},
		{Kind: cacheevent.AcquisitionChart, Chart: cacheevent.ChartAcquisition{CacheKind: "oci", CacheKey: "b", FetchedAt: at}},
	}
	charts, stale := renderCacheChartsFromRecords(records)
	if stale {
		t.Fatal("stale = true, want false")
	}
	want := renderCacheCharts{recorded: true, charts: []renderCacheChart{
		{Kind: "http", Key: "a", FetchedAt: at.UTC()},
		{Kind: "oci", Key: "b", FetchedAt: at.UTC()},
	}}
	if !reflect.DeepEqual(charts, want) {
		t.Fatalf("charts = %#v, want %#v", charts, want)
	}
	reversed := []cacheevent.AcquisitionRecord{records[3], records[2], records[1], records[0]}
	if again, _ := renderCacheChartsFromRecords(reversed); !reflect.DeepEqual(again, want) {
		t.Fatalf("charts from reversed records = %#v, want %#v", again, want)
	}
	if empty, _ := renderCacheChartsFromRecords(nil); !empty.recorded || len(empty.charts) != 0 {
		t.Fatalf("charts without records = %#v, want recorded and empty", empty)
	}
}
