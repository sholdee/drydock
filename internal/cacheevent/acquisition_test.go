package cacheevent

import (
	"reflect"
	"sync"
	"testing"
)

func TestAcquisitionCollectorRecordsIndependentlyOfRecorder(t *testing.T) {
	// The collector must work with the user-facing recorder disabled: the
	// pin-stability gate depends on unconditional collection.
	collector := NewAcquisitionCollector()
	collector.Record(AcquisitionRecord{Kind: AcquisitionRemoteGit, RequestedRevision: "main", ResolvedRevision: "0123456789abcdef0123456789abcdef01234567"})
	collector.Record(AcquisitionRecord{Kind: AcquisitionChart, RequestedRevision: "1.2.3", ResolvedRevision: "1.2.3"})

	got := collector.Records()
	want := []AcquisitionRecord{
		{Kind: AcquisitionRemoteGit, RequestedRevision: "main", ResolvedRevision: "0123456789abcdef0123456789abcdef01234567"},
		{Kind: AcquisitionChart, RequestedRevision: "1.2.3", ResolvedRevision: "1.2.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Records() = %#v, want %#v", got, want)
	}
}

func TestAcquisitionCollectorNilSafe(t *testing.T) {
	var collector *AcquisitionCollector
	collector.Record(AcquisitionRecord{Kind: AcquisitionGit})
	if got := collector.Records(); got != nil {
		t.Fatalf("nil collector Records() = %#v, want nil", got)
	}
}

func TestAcquisitionCollectorConcurrentRecords(t *testing.T) {
	collector := NewAcquisitionCollector()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				collector.Record(AcquisitionRecord{Kind: AcquisitionGit, RequestedRevision: "main", ResolvedRevision: "abc"})
			}
		})
	}
	wg.Wait()
	if got := len(collector.Records()); got != 400 {
		t.Fatalf("Records() length = %d, want 400", got)
	}
}
