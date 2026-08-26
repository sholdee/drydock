package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildParallelismPreservesApplicationOrder(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "first")
	writeTestChart(t, chartRoot, "middle")
	writeTestChart(t, chartRoot, "last")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"first", "middle", "last"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			Parallelism: 3,
			Applications: []argoappv1.Application{
				chartOnlyApplication("first", "first", "1.0.0"),
				chartOnlyApplication("middle", "middle", "1.0.0"),
				chartOnlyApplication("last", "last", "1.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "first", "middle", "last")
	acquirer.release("last")
	acquirer.release("middle")
	acquirer.release("first")

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	assertManifestNames(t, out.result.Manifests, []string{"first", "middle", "last"})
	assertApplicationStatusOrder(t, out.result.Statuses, []string{
		"argocd/first:PASS",
		"argocd/middle:PASS",
		"argocd/last:PASS",
	})
}

func TestBuildStatusCallbackReportsCompletionOrder(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "slow")
	writeTestChart(t, chartRoot, "fast")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"slow", "fast"})

	eventsCh := make(chan ApplicationStatusEvent, 2)
	resultCh := make(chan BuildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			StatusOnly:  true,
			Parallelism: 2,
			Applications: []argoappv1.Application{
				chartOnlyApplication("slow", "slow", "1.0.0"),
				chartOnlyApplication("fast", "fast", "1.0.0"),
			},
			StatusCallback: func(event ApplicationStatusEvent) error {
				eventsCh <- event
				return nil
			},
		})
		resultCh <- result
		errCh <- err
	}()

	acquirer.waitStarted(t, "slow", "fast")
	acquirer.release("fast")
	var first ApplicationStatusEvent
	select {
	case first = <-eventsCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first status callback before slow chart release")
	}
	if got, want := eventStatusKey(first), "argocd/fast:PASS:1/2"; got != want {
		t.Fatalf("first event = %q, want %q", got, want)
	}
	select {
	case event := <-eventsCh:
		t.Fatalf("second event = %#v before slow chart was released", event)
	case <-time.After(50 * time.Millisecond):
	}
	acquirer.release("slow")

	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertApplicationStatusOrder(t, result.Statuses, []string{"argocd/slow:PASS", "argocd/fast:PASS"})
	var second ApplicationStatusEvent
	select {
	case second = <-eventsCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second status callback after slow chart release")
	}
	if got, want := eventStatusKey(second), "argocd/slow:PASS:2/2"; got != want {
		t.Fatalf("second event = %q, want %q", got, want)
	}
}

func TestBuildStatusCallbackErrorStopsBuild(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:        root,
		StatusOnly:  true,
		Parallelism: 1,
		StatusCallback: func(ApplicationStatusEvent) error {
			return errors.New("write status")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "write status") {
		t.Fatalf("Build() error = %v, want callback error", err)
	}
	assertApplicationStatusOrder(t, result.Statuses, []string{"argocd/demo:PASS"})
}

func TestBuildStatusCallbackErrorStopsParallelCallbacks(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "slow")
	writeTestChart(t, chartRoot, "fast")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"slow", "fast"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	callbacks := 0
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			StatusOnly:  true,
			Parallelism: 2,
			Applications: []argoappv1.Application{
				chartOnlyApplication("slow", "slow", "1.0.0"),
				chartOnlyApplication("fast", "fast", "1.0.0"),
			},
			StatusCallback: func(ApplicationStatusEvent) error {
				callbacks++
				return errors.New("write status")
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "slow", "fast")
	acquirer.release("fast")

	var out struct {
		result BuildResult
		err    error
	}
	select {
	case out = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Build() after callback error")
	}
	if out.err == nil || !strings.Contains(out.err.Error(), "write status") {
		t.Fatalf("Build() error = %v, want callback error", out.err)
	}
	if callbacks != 1 {
		t.Fatalf("callback count = %d, want one callback before cancellation", callbacks)
	}
}

func eventStatusKey(event ApplicationStatusEvent) string {
	return fmt.Sprintf("%s/%s:%s:%d/%d", event.Status.Namespace, event.Status.Name, event.Status.Status, event.Completed, event.Total)
}

func TestBuildParallelismPreservesPartialFailureStatuses(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "first")
	writeTestChart(t, chartRoot, "middle")
	writeTestChart(t, chartRoot, "last")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"first", "middle", "last"})
	acquirer.fail["middle"] = errors.New("planned chart failure")

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			Parallelism: 3,
			Applications: []argoappv1.Application{
				chartOnlyApplication("first", "first", "1.0.0"),
				chartOnlyApplication("middle", "middle", "1.0.0"),
				chartOnlyApplication("last", "last", "1.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "first", "middle", "last")
	acquirer.release("last")
	acquirer.release("middle")
	acquirer.release("first")

	out := <-resultCh
	if out.err == nil {
		t.Fatal("Build() error = nil, want partial failure")
	}
	if !strings.Contains(out.err.Error(), "1 Application failed: argocd/middle:") ||
		!strings.Contains(out.err.Error(), "acquire chart middle: planned chart failure") {
		t.Fatalf("Build() error = %q, want stable middle failure", out.err.Error())
	}
	assertApplicationStatusOrder(t, out.result.Statuses, []string{
		"argocd/first:PASS",
		"argocd/middle:FAIL",
		"argocd/last:PASS",
	})
	assertManifestNames(t, out.result.Manifests, []string{"first", "last"})
	if len(out.result.Diagnostics) == 0 || !strings.Contains(out.result.Diagnostics[0].Message, "middle") {
		t.Fatalf("Diagnostics = %#v, want middle render diagnostic in order", out.result.Diagnostics)
	}
}
func TestBuildParallelismPreservesCacheEventOrder(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "first")
	writeTestChart(t, chartRoot, "middle")
	writeTestChart(t, chartRoot, "last")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"first", "middle", "last"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:              root,
			Parallelism:       3,
			RecordCacheEvents: true,
			Applications: []argoappv1.Application{
				chartOnlyApplication("first", "first", "1.0.0"),
				chartOnlyApplication("middle", "middle", "2.0.0"),
				chartOnlyApplication("last", "last", "3.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "first", "middle", "last")
	acquirer.release("last")
	acquirer.release("middle")
	acquirer.release("first")

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	revisions := make([]string, 0, len(out.result.CacheEvents))
	for _, event := range out.result.CacheEvents {
		revisions = append(revisions, event.Revision)
	}
	if !slices.Equal(revisions, []string{"1.0.0", "2.0.0", "3.0.0"}) {
		t.Fatalf("cache event revisions = %#v, want selected Application order", revisions)
	}
}
func TestBuildParallelismSerializesSameCacheTargetAcquisition(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "shared")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"shared"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			Parallelism: 2,
			Applications: []argoappv1.Application{
				chartOnlyApplication("one", "shared", "1.0.0"),
				chartOnlyApplication("two", "shared", "1.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "shared")
	select {
	case name := <-acquirer.started:
		t.Fatalf("second acquisition for %s started before first shared target was released", name)
	case <-time.After(50 * time.Millisecond):
	}
	acquirer.release("shared")
	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	if got := acquirer.maxActive(); got != 1 {
		t.Fatalf("max concurrent acquisitions = %d, want 1", got)
	}
}
func TestBuildParallelismSerializesSameCacheTargetAcrossConcurrentBuilds(t *testing.T) {
	chartRoot := t.TempDir()
	cacheDir := t.TempDir()
	writeTestChart(t, chartRoot, "shared")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"shared"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 2)
	roots := map[string]string{
		"one": t.TempDir(),
		"two": t.TempDir(),
	}
	for _, name := range []string{"one", "two"} {
		root := roots[name]
		go func() {
			result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
				Path:          root,
				ChartCacheDir: cacheDir,
				Parallelism:   2,
				Applications: []argoappv1.Application{
					chartOnlyApplication(name, "shared", "1.0.0"),
				},
			})
			resultCh <- struct {
				result BuildResult
				err    error
			}{result: result, err: err}
		}()
	}

	acquirer.waitStarted(t, "shared")
	select {
	case name := <-acquirer.started:
		t.Fatalf("second build acquisition for %s started before first shared target was released", name)
	case <-time.After(50 * time.Millisecond):
	}
	acquirer.release("shared")
	for range 2 {
		out := <-resultCh
		if out.err != nil {
			t.Fatalf("Build() error = %v", out.err)
		}
	}
	if got := acquirer.maxActive(); got != 1 {
		t.Fatalf("max concurrent acquisitions = %d, want 1 across concurrent builds", got)
	}
}
func TestBuildParallelismProtectsSameCacheTargetDuringRenderRead(t *testing.T) {
	root := t.TempDir()
	cacheRoot := t.TempDir()
	writeTestFile(t, filepath.Join(cacheRoot, "manifests", "snapshot", ".keep"), "")
	writeTestFile(t, filepath.Join(cacheRoot, "marker.txt"), "before")

	renderStarted := make(chan string, 1)
	releaseRender := make(chan struct{})
	renderer := internalPluginRendererFunc(func(_ context.Context, request render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		renderStarted <- request.Source.RepoRoot
		<-releaseRender
		value, err := os.ReadFile(filepath.Join(request.Source.RepoRoot, "marker.txt"))
		if err != nil {
			return nil, nil, err
		}
		return []render.Manifest{{Object: cm("snapshot", string(value))}}, nil, nil
	})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{
			GitAcquirer:    staticGitAcquirer{path: cacheRoot},
			PluginRenderer: renderer,
		}).Build(context.Background(), BuildRequest{
			Path:        root,
			Parallelism: 2,
			Applications: []argoappv1.Application{
				pluginApplication("snapshot"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	snapshotRoot := <-renderStarted
	if snapshotRoot == cacheRoot {
		t.Fatalf("plugin rendered from mutable cache root %q, want snapshot", snapshotRoot)
	}
	writeTestFile(t, filepath.Join(cacheRoot, "marker.txt"), "after")
	close(releaseRender)

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	value, _, _ := unstructured.NestedString(out.result.Manifests[0].Object.Object, "data", "value")
	if value != "before" {
		t.Fatalf("rendered value = %q, want snapshot value before cache mutation", value)
	}
}
func TestBuildParallelismRejectsNegativeValue(t *testing.T) {
	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path:        t.TempDir(),
		Parallelism: -1,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want parallelism validation error")
	}
	if len(result.Statuses) != 0 {
		t.Fatalf("Statuses = %#v, want validation before rendering", result.Statuses)
	}
}
func TestBuildParallelismHonorsCallerCancellation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "manifests", "cancelled", ".keep"), "")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	renderer := internalPluginRendererFunc(func(ctx context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		close(started)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{PluginRenderer: renderer}).Build(ctx, BuildRequest{
			Path:        root,
			Parallelism: 2,
			Applications: []argoappv1.Application{
				pluginApplication("cancelled"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	<-started
	cancel()
	out := <-resultCh
	if !errors.Is(out.err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context canceled", out.err)
	}
	assertApplicationStatusOrder(t, out.result.Statuses, []string{"argocd/cancelled:FAIL"})
}
func TestDiffRequestPropagatesParallelism(t *testing.T) {
	request := DiffRequest{Parallelism: 4}

	left := request.buildRequest("left", []string{"left", "right"})
	right := request.buildRequest("right", []string{"left", "right"})

	if left.Parallelism != 4 || right.Parallelism != 4 {
		t.Fatalf("Parallelism left/right = %d/%d, want 4/4", left.Parallelism, right.Parallelism)
	}
}
