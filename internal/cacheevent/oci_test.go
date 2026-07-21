package cacheevent

import "testing"

// TestRecordRoundTripsOCITargets pins that the git-oriented target redaction
// passes oci:// URLs through unchanged (RedactGitRepoURL parses them as
// scheme+host URLs and only strips userinfo/query/fragment).
func TestRecordRoundTripsOCITargets(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source:   SourceOCI,
		Action:   ActionFetch,
		Target:   "oci://ghcr.io/org/app",
		Revision: "sha256:abc",
	})
	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Target != "oci://ghcr.io/org/app" {
		t.Fatalf("Target = %q, want oci URL unchanged", events[0].Target)
	}
}

// TestActionForErrorOCIOfflineMiss pins the "offline cache miss" text
// contract the OCI acquirer errors rely on for ActionMiss.
func TestActionForErrorOCIOfflineMiss(t *testing.T) {
	result := NewAcquisitionError(AcquisitionEventInput{
		Source:            SourceOCI,
		Target:            "oci://ghcr.io/org/app",
		RequestedRevision: "1.2.3",
		Offline:           true,
		Err:               errOffline{},
	})
	if result.Event.Action != ActionMiss {
		t.Fatalf("Action = %q, want miss", result.Event.Action)
	}
}

type errOffline struct{}

func (errOffline) Error() string {
	return `offline cache miss for OCI artifact oci://ghcr.io/org/app revision "1.2.3"`
}
