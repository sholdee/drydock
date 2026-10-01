package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/render"
)

// writeTwoSourceFrontierParent writes apps/<name>.yaml, an Application whose
// first source is the local chart charts/<name> and whose second is the plain
// directory extras/<name>. The second source resolves, and records its cache
// event, only after the first has rendered.
func writeTwoSourceFrontierParent(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  sources:
    - repoURL: https://github.com/example/`+name+`
      targetRevision: main
      path: charts/`+name+`
    - repoURL: https://github.com/example/`+name+`-extra
      targetRevision: main
      path: extras/`+name+`
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeFrontierChart(t, root, name, frontierChildApplicationYAML(name+"-child", "workloads/"+name))
	writeTestFile(t, filepath.Join(root, "extras", name, "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: "+name+"-extra\n")
	writeFrontierWorkload(t, root, name, "same")
}

// TestRenderedFleetDiscoveryParallelRecordsCacheEventsInFrontierOrder
// requires the frontier's cache events in frontier order, as the final
// render already reports them (TestBuildParallelismPreservesCacheEventOrder).
func TestRenderedFleetDiscoveryParallelRecordsCacheEventsInFrontierOrder(t *testing.T) {
	parents := []string{"s0", "s1", "s2", "s3", "s4", "s5", "s6"}
	root := t.TempDir()
	for _, name := range parents {
		writeTwoSourceFrontierParent(t, root, name)
	}
	request := BuildRequest{Path: root, RecordCacheEvents: true}

	sequentialRequest := request
	sequentialRequest.Parallelism = 1
	sequential, err := Orchestrator{}.Build(context.Background(), sequentialRequest)
	if err != nil {
		t.Fatalf("sequential Build() error = %v", err)
	}

	delays := reversedFrontierDelays(5*time.Millisecond, parents...)
	parallelRequest := request
	parallelRequest.Parallelism = 16
	parallel, err := delays.orchestrator().Build(context.Background(), parallelRequest)
	if err != nil {
		t.Fatalf("parallel Build() error = %v", err)
	}
	targets := func(result BuildResult) []string {
		out := make([]string, 0, len(result.CacheEvents))
		for _, event := range result.CacheEvents {
			out = append(out, strings.TrimPrefix(event.Target, "https://github.com/example/"))
		}
		return out
	}
	if got, want := targets(parallel), targets(sequential); !slices.Equal(got, want) {
		t.Fatalf("parallel cache event targets = %v\nwant sequential order %v", got, want)
	}
}

// TestRenderedFleetDiscoveryParallelFailureReportsFirstFrontierError has two
// parents whose output fails discovery, e1 and e4; e1 finishes rendering
// last. The sequential frontier reports e1, the first failure in frontier
// order, and the parallel frontier must too.
func TestRenderedFleetDiscoveryParallelFailureReportsFirstFrontierError(t *testing.T) {
	root := t.TempDir()
	writeFailingFrontierFleet(t, root, "e1", "e4")
	request := BuildRequest{Path: root}

	sequentialRequest := request
	sequentialRequest.Parallelism = 1
	_, sequentialErr := Orchestrator{}.Build(context.Background(), sequentialRequest)
	if sequentialErr == nil || !strings.Contains(sequentialErr.Error(), "argocd/e1") {
		t.Fatalf("sequential Build() error = %v, want e1's discovery failure", sequentialErr)
	}

	for _, parallelism := range frontierParallelisms {
		t.Run(fmt.Sprintf("parallelism-%d", parallelism), func(t *testing.T) {
			delays := &frontierRenderDelays{delays: map[string]time.Duration{"e1": 100 * time.Millisecond}}
			parallelRequest := request
			parallelRequest.Parallelism = parallelism
			_, err := delays.orchestrator().Build(context.Background(), parallelRequest)
			if errorText(err) != sequentialErr.Error() {
				t.Fatalf("parallel Build() error = %v\nwant sequential error %v", err, sequentialErr)
			}
		})
	}
}

// TestRenderedFleetDiscoveryParallelFailureKeepsEarlierDiagnostics has e0
// and e1 render different Applications dup, and e2's output fail discovery;
// e1 finishes rendering after e2. The sequential frontier merges e0 and e1
// before failing on e2, so its error carries the duplicate warning, and the
// parallel frontier must too.
func TestRenderedFleetDiscoveryParallelFailureKeepsEarlierDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeFrontierParent(t, root, "e0", frontierChildApplicationYAML("dup", "workloads/dup-a"))
	writeFrontierParent(t, root, "e1", frontierChildApplicationYAML("dup", "workloads/dup-b"))
	writeFrontierParent(t, root, "e2", frontierUndecodableApplicationYAML("e2-child"))
	writeFrontierWorkload(t, root, "dup-a", "same")
	writeFrontierWorkload(t, root, "dup-b", "same")
	request := BuildRequest{Path: root}

	sequentialRequest := request
	sequentialRequest.Parallelism = 1
	sequentialResult, sequentialErr := Orchestrator{}.Build(context.Background(), sequentialRequest)
	if sequentialErr == nil || !strings.Contains(sequentialErr.Error(), "duplicate Application argocd/dup") {
		t.Fatalf("sequential Build() error = %v, want e2's failure carrying the duplicate warning", sequentialErr)
	}

	for _, parallelism := range frontierParallelisms {
		t.Run(fmt.Sprintf("parallelism-%d", parallelism), func(t *testing.T) {
			delays := &frontierRenderDelays{delays: map[string]time.Duration{"e1": 100 * time.Millisecond}}
			parallelRequest := request
			parallelRequest.Parallelism = parallelism
			result, err := delays.orchestrator().Build(context.Background(), parallelRequest)
			if errorText(err) != sequentialErr.Error() {
				t.Errorf("parallel Build() error = %v\nwant sequential error %v", err, sequentialErr)
			}
			if got, want := len(result.Diagnostics), len(sequentialResult.Diagnostics); got != want {
				t.Errorf("parallel diagnostics = %d %v, want sequential %d %v", got, result.Diagnostics, want, sequentialResult.Diagnostics)
			}
		})
	}
}

// TestRenderedFleetDiscoveryParallelReportsCancellationOfDispatchedFrontier
// cancels the caller's context only once every parent is in flight, so the
// parallel frontier has nothing left to schedule. It must still report the
// cancellation rather than a frontier missing every parent's children.
func TestRenderedFleetDiscoveryParallelReportsCancellationOfDispatchedFrontier(t *testing.T) {
	root := t.TempDir()
	writeFailingFrontierFleet(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	inFlight := 0
	o := Orchestrator{}
	o.renderObserver = func(source render.ResolvedSource) {
		if !strings.HasPrefix(filepath.ToSlash(source.Path), "charts/") {
			return
		}
		mu.Lock()
		inFlight++
		if inFlight == len(failingFrontierParents) {
			cancel()
		}
		mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	result, err := o.Build(ctx, BuildRequest{Path: root, Parallelism: 16})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context.Canceled", err)
	}
	if len(result.Applications) != 0 {
		t.Fatalf("Build() kept %d Applications from a cancelled frontier, want none", len(result.Applications))
	}
}
