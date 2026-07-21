package cacheevent

import "sync"

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
