package cacheevent

import (
	"sync"
	"time"
)

// AcquisitionKind classifies a mid-render acquisition for the persistent
// render cache pin-stability gate. Unlike Event.Source, it distinguishes
// remote git bases from remote HTTP files.
type AcquisitionKind string

const (
	AcquisitionGit        AcquisitionKind = "git"
	AcquisitionChart      AcquisitionKind = "chart"
	AcquisitionRemoteGit  AcquisitionKind = "remote-git"
	AcquisitionRemoteHTTP AcquisitionKind = "remote-http"
	AcquisitionOCI        AcquisitionKind = "oci"
)

// AcquisitionRecord retains the pre-collapse requested and resolved revisions
// of one successful acquisition. The user-facing Event collapses these into a
// single Revision field, so the pin-stability gate cannot consume Events.
type AcquisitionRecord struct {
	Kind              AcquisitionKind
	RequestedRevision string
	ResolvedRevision  string
	// Chart is the chart cache identity of an AcquisitionChart record; the
	// persistent render cache checks it against the chart cache under
	// --chart-cache-max-age. Zero for other kinds.
	Chart ChartAcquisition
}

// ChartAcquisition identifies the chart cache entry one chart acquisition
// was served from, and when that entry was fetched. Target is the redacted
// repository.
type ChartAcquisition struct {
	Target    string
	Name      string
	Version   string
	CacheKind string
	CacheKey  string
	FetchedAt time.Time
	// MaxAge is the --chart-cache-max-age the acquisition ran under, and
	// Stale reports a cached chart older than it (or of unknown age) reused
	// because --offline forbids refetching it.
	MaxAge time.Duration
	Stale  bool
	// VersionListStale reports a version range that an offline run
	// resolved from a recorded version list older than MaxAge (or of
	// unknown age), fetched at VersionListFetchedAt.
	VersionListStale     bool
	VersionListFetchedAt time.Time
}

// AcquisitionCollector gathers records for one application render. It records
// unconditionally, independent of the --cache-events Recorder gating.
type AcquisitionCollector struct {
	mu      sync.Mutex
	records []AcquisitionRecord
}

func NewAcquisitionCollector() *AcquisitionCollector {
	return &AcquisitionCollector{}
}

func (c *AcquisitionCollector) Record(record AcquisitionRecord) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, record)
}

func (c *AcquisitionCollector) Records() []AcquisitionRecord {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.records == nil {
		return nil
	}
	out := make([]AcquisitionRecord, len(c.records))
	copy(out, c.records)
	return out
}

// Len reports how many records the collector holds.
func (c *AcquisitionCollector) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}
