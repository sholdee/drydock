package cli

import "testing"

func TestMaxDiscoveryDepthFlagDistinguishesDefaultFromExplicitZero(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "get", "apps")
	if len(recorder.listRequests) != 1 {
		t.Fatalf("len(listRequests) = %d, want 1", len(recorder.listRequests))
	}
	if request := recorder.listRequests[0]; request.MaxDiscoveryDepth != 4 || request.MaxDiscoveryDepthSet {
		t.Fatalf("default max discovery depth = %d set=%t, want 4 set=false", request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
	}

	recorder = &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "get", "apps", "--max-discovery-depth", "0")
	if len(recorder.listRequests) != 1 {
		t.Fatalf("len(listRequests) = %d, want 1", len(recorder.listRequests))
	}
	if request := recorder.listRequests[0]; request.MaxDiscoveryDepth != 0 || !request.MaxDiscoveryDepthSet {
		t.Fatalf("explicit max discovery depth = %d set=%t, want 0 set=true", request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
	}
}
