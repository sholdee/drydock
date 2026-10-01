package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/diff"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
)

// frontierParents are the static app-of-apps parents of
// writeParallelFrontierFleet, in discovery (frontier) order.
var frontierParents = []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6"}

// frontierParallelisms are compared against the sequential frontier: fewer
// workers than parents, and more workers than any frontier has Applications.
var frontierParallelisms = []int{2, 4, 16}

// frontierChildApplicationYAML is an Application document a parent chart
// renders, over the repository directory sourcePath.
func frontierChildApplicationYAML(name, sourcePath string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + name + `
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/fleet
    targetRevision: main
    path: ` + sourcePath + `
  destination:
    name: in-cluster
    namespace: ` + name + `
`
}

func frontierProjectYAML(name, description string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: ` + name + `
  namespace: argocd
spec:
  description: ` + description + `
  sourceRepos:
    - '*'
  destinations:
    - namespace: '*'
      server: '*'
`
}

// writeFrontierChart writes charts/<name>, a local chart whose only template
// holds documents. Chart templates are invisible to the static scan, so
// whatever they declare is discovered only by rendering the chart.
func writeFrontierChart(t *testing.T, root, name string, documents ...string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "charts", name, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "charts", name, "templates", "children.yaml"), strings.Join(documents, "---\n"))
}

// writeFrontierParent writes apps/<name>.yaml, a static Application over
// charts/<name>, and that chart. Each parent names its own repoURL so the
// cache events of the frontier identify which parent recorded them.
func writeFrontierParent(t *testing.T, root, name string, documents ...string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/`+name+`
    targetRevision: main
    path: charts/`+name+`
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeFrontierChart(t, root, name, documents...)
}

func writeFrontierWorkload(t *testing.T, root, name, value string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "workloads", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: `+value+`
`)
}

// writeParallelFrontierFleet writes seven app-of-apps parents whose merge
// order is observable:
//
//   - p0, p1 and p5 each render a different Application dup; the first in
//     frontier order (p0) must win, with one warning per ignored copy.
//   - p4 renders its own copy of p1's c1; p1's must keep the origin.
//   - p2 renders mid, whose own chart renders the grandchild g, so the
//     frontier runs a second, larger round.
//   - p3's chart fails to render, which the frontier skips silently and the
//     final build reports as a failed Application.
//   - p5 and p6 render different AppProjects fleet; p5 must win.
//
// version is the value of the workloads behind dup and g, the two
// Applications a diff changes.
func writeParallelFrontierFleet(t *testing.T, root, version string) {
	t.Helper()
	writeFrontierParent(t, root, "p0",
		frontierChildApplicationYAML("c0", "workloads/c0"),
		frontierChildApplicationYAML("dup", "workloads/dup-a"))
	writeFrontierParent(t, root, "p1",
		frontierChildApplicationYAML("c1", "workloads/c1"),
		frontierChildApplicationYAML("dup", "workloads/dup-b"))
	writeFrontierParent(t, root, "p2",
		frontierChildApplicationYAML("mid", "charts/mid"))
	writeFrontierChart(t, root, "mid",
		frontierChildApplicationYAML("g", "workloads/g"))
	writeFrontierParent(t, root, "p3",
		`{{ fail "p3 cannot render" }}`+"\n"+frontierChildApplicationYAML("c3", "workloads/c3"))
	writeFrontierParent(t, root, "p4",
		frontierChildApplicationYAML("c4", "workloads/c4"),
		frontierChildApplicationYAML("c1", "workloads/c1"))
	writeFrontierParent(t, root, "p5",
		frontierChildApplicationYAML("c5", "workloads/c5"),
		frontierChildApplicationYAML("dup", "workloads/dup-c"),
		frontierProjectYAML("fleet", "from-p5"))
	writeFrontierParent(t, root, "p6",
		frontierChildApplicationYAML("c6", "workloads/c6"),
		frontierProjectYAML("fleet", "from-p6"))
	for _, name := range []string{"c0", "c1", "c4", "c5", "c6", "dup-b", "dup-c"} {
		writeFrontierWorkload(t, root, name, "same")
	}
	writeFrontierWorkload(t, root, "dup-a", version)
	writeFrontierWorkload(t, root, "g", version)
}

// frontierRenderDelays is a renderObserver that holds each chart render named
// in delays for its duration before letting it run, so concurrent frontier
// renders finish out of frontier order. released records the order in which
// held renders were let go.
type frontierRenderDelays struct {
	delays   map[string]time.Duration
	mu       sync.Mutex
	released []string
}

// reversedFrontierDelays holds earlier names longer, so with enough workers
// the frontier completes in reverse order.
func reversedFrontierDelays(step time.Duration, names ...string) *frontierRenderDelays {
	delays := make(map[string]time.Duration, len(names))
	for index, name := range names {
		delays[name] = time.Duration(len(names)-index) * step
	}
	return &frontierRenderDelays{delays: delays}
}

func (d *frontierRenderDelays) observe(source render.ResolvedSource) {
	name := path.Base(filepath.ToSlash(source.Path))
	delay, ok := d.delays[name]
	if !ok {
		return
	}
	time.Sleep(delay)
	d.mu.Lock()
	d.released = append(d.released, name)
	d.mu.Unlock()
}

func (d *frontierRenderDelays) releasedOrder() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.released...)
}

func (d *frontierRenderDelays) orchestrator() Orchestrator {
	o := Orchestrator{}
	o.renderObserver = d.observe
	return o
}

// assertFrontierCompletedOutOfOrder fails unless the held renders of names
// were released in some order other than names' own, proving the parallel
// frontier saw completions out of frontier order.
func assertFrontierCompletedOutOfOrder(t *testing.T, delays *frontierRenderDelays, names []string) {
	t.Helper()
	var released []string
	for _, name := range delays.releasedOrder() {
		if slices.Contains(names, name) && !slices.Contains(released, name) {
			released = append(released, name)
		}
	}
	if slices.Equal(released, names) {
		t.Fatalf("held renders released in frontier order %v; the delays did not reorder completion", released)
	}
}

// frontierBuildSnapshot is every BuildResult output the discovery frontier
// feeds, plus the discovery result itself. Cache events are compared on
// their own, by the cache event order tests.
type frontierBuildSnapshot struct {
	Discovered           discovery.Result
	Applications         []argoappv1.Application
	ApplicationInputs    []ApplicationSelectionInput
	Projects             []argoappv1.AppProject
	Manifests            []render.Manifest
	ApplicationManifests []ApplicationManifest
	Diagnostics          []diagnostic.Diagnostic
	Statuses             []ApplicationStatus
	Error                string
}

func snapshotFrontierBuild(t *testing.T, result BuildResult, err error) frontierBuildSnapshot {
	t.Helper()
	if result.discovered == nil {
		t.Fatalf("BuildResult carries no discovery result (error %v)", err)
	}
	return frontierBuildSnapshot{
		Discovered:           *result.discovered,
		Applications:         result.Applications,
		ApplicationInputs:    result.ApplicationInputs,
		Projects:             result.Projects,
		Manifests:            result.Manifests,
		ApplicationManifests: result.ApplicationManifests,
		Diagnostics:          result.Diagnostics,
		Statuses:             result.Statuses,
		Error:                errorText(err),
	}
}

// frontierDiffSnapshot is every DiffApps output but its cache events.
type frontierDiffSnapshot struct {
	Results     []diff.Result
	Diagnostics []diagnostic.Diagnostic
	Error       string
}

func snapshotFrontierDiff(result DiffResult, err error) frontierDiffSnapshot {
	return frontierDiffSnapshot{
		Results:     result.Results,
		Diagnostics: result.Diagnostics,
		Error:       errorText(err),
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// assertSameFields compares got and want, two values of one struct type,
// field by field, naming each field that differs.
func assertSameFields(t *testing.T, label string, got, want any) {
	t.Helper()
	gotValue, wantValue := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := range gotValue.NumField() {
		gotField, wantField := gotValue.Field(i).Interface(), wantValue.Field(i).Interface()
		if !reflect.DeepEqual(gotField, wantField) {
			t.Errorf("%s: %s differs from the sequential frontier\n got: %s\nwant: %s",
				label, gotValue.Type().Field(i).Name, describeFrontierValue(gotField), describeFrontierValue(wantField))
		}
	}
}

func describeFrontierValue(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%#v", value)
	}
	return string(data)
}

func discoveredApplicationFile(t *testing.T, discovered discovery.Result, name string) discovery.ApplicationFile {
	t.Helper()
	for _, appFile := range discovered.Applications {
		if appFile.Application.Name == name {
			return appFile
		}
	}
	t.Fatalf("discovered Applications have no %s", name)
	return discovery.ApplicationFile{}
}

func frontierApplicationKey(name string) string {
	return applicationKey(argoappv1.Application{Name: name, Namespace: "argocd"})
}

// assertParallelFrontierFleetBaseline pins what the sequential frontier
// produces for writeParallelFrontierFleet, so the comparisons below compare
// the merge behaviours they are meant to.
func assertParallelFrontierFleetBaseline(t *testing.T, snapshot frontierBuildSnapshot) {
	t.Helper()
	wantOrder := []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6", "c0", "dup", "c1", "mid", "c4", "c5", "c6", "g"}
	order := make([]string, 0, len(snapshot.Applications))
	for _, application := range snapshot.Applications {
		order = append(order, application.Name)
	}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("sequential Applications = %v, want %v", order, wantOrder)
	}
	for name, wantParent := range map[string]string{"dup": "p0", "c1": "p1", "mid": "p2", "g": "mid"} {
		appFile := discoveredApplicationFile(t, snapshot.Discovered, name)
		if appFile.ParentKey != frontierApplicationKey(wantParent) || appFile.Tier != discovery.SourceTierRenderedFleet {
			t.Fatalf("sequential %s origin = %q tier %v, want rendered by %s", name, appFile.ParentKey, appFile.Tier, wantParent)
		}
	}
	if got := discoveredApplicationFile(t, snapshot.Discovered, "dup").Application.Spec.Source.Path; got != "workloads/dup-a" {
		t.Fatalf("sequential dup source path = %q, want p0's workloads/dup-a", got)
	}
	var duplicates []string
	for _, diag := range snapshot.Diagnostics {
		if strings.HasPrefix(diag.Message, "duplicate ") {
			duplicates = append(duplicates, diag.Message)
		}
	}
	rendered := func(parent string) string {
		return "rendered/argocd/" + parent + "/charts/" + parent + "/templates/children.yaml"
	}
	wantDuplicates := []string{
		"duplicate Application argocd/dup from " + rendered("p1") + " ignored; " + rendered("p0") + " takes precedence",
		"duplicate Application argocd/c1 from " + rendered("p4") + " ignored; " + rendered("p1") + " takes precedence",
		"duplicate Application argocd/dup from " + rendered("p5") + " ignored; " + rendered("p0") + " takes precedence",
		"duplicate AppProject argocd/fleet from " + rendered("p6") + " ignored; " + rendered("p5") + " takes precedence",
	}
	if !slices.Equal(duplicates, wantDuplicates) {
		t.Fatalf("sequential duplicate diagnostics = %q, want %q", duplicates, wantDuplicates)
	}
	for _, status := range snapshot.Statuses {
		if wantFail := status.Name == "p3"; (status.Status == ApplicationStatusFail) != wantFail {
			t.Fatalf("sequential status of %s = %s, want only p3 to fail", status.Name, status.Status)
		}
	}
	if !strings.HasPrefix(snapshot.Error, "1 Application failed: argocd/p3: ") {
		t.Fatalf("sequential Build() error = %q, want only p3's render failure", snapshot.Error)
	}
}

// TestRenderedFleetDiscoveryParallelMatchesSequential runs one fleet
// through the sequential frontier and through parallel frontiers whose
// renders complete in reverse order, and requires byte-identical discovery
// and build output: the parallel frontier must merge in frontier order, not
// completion order.
func TestRenderedFleetDiscoveryParallelMatchesSequential(t *testing.T) {
	root := t.TempDir()
	writeParallelFrontierFleet(t, root, "v1")
	request := BuildRequest{Path: root}

	sequentialRequest := request
	sequentialRequest.Parallelism = 1
	sequentialResult, err := Orchestrator{}.Build(context.Background(), sequentialRequest)
	sequential := snapshotFrontierBuild(t, sequentialResult, err)
	assertParallelFrontierFleetBaseline(t, sequential)

	for _, parallelism := range frontierParallelisms {
		t.Run(fmt.Sprintf("parallelism-%d", parallelism), func(t *testing.T) {
			delays := reversedFrontierDelays(5*time.Millisecond, frontierParents...)
			parallelRequest := request
			parallelRequest.Parallelism = parallelism
			result, err := delays.orchestrator().Build(context.Background(), parallelRequest)
			assertSameFields(t, "Build", snapshotFrontierBuild(t, result, err), sequential)
			if parallelism >= len(frontierParents) {
				assertFrontierCompletedOutOfOrder(t, delays, frontierParents)
			}
		})
	}
}

// TestRenderedFleetDiffParallelMatchesSequential is the DiffApps twin: each
// side's frontier gets half the workers, and the diff must not depend on how
// many there are.
func TestRenderedFleetDiffParallelMatchesSequential(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeParallelFrontierFleet(t, left, "v1")
	writeParallelFrontierFleet(t, right, "v2")
	request := DiffRequest{LeftPath: left, RightPath: right, Unified: 3}

	sequentialRequest := request
	sequentialRequest.Parallelism = 1
	sequentialResult, err := Orchestrator{}.DiffApps(context.Background(), sequentialRequest)
	sequential := snapshotFrontierDiff(sequentialResult, err)
	if !strings.HasPrefix(sequential.Error, "1 Application failed: argocd/p3: ") {
		t.Fatalf("sequential DiffApps() error = %q, want only p3's render failure", sequential.Error)
	}
	changed := make([]string, 0, len(sequential.Results))
	for _, result := range sequential.Results {
		changed = append(changed, result.Parent.Namespace+"/"+result.Parent.Name)
	}
	if want := []string{"argocd/dup", "argocd/g"}; !slices.Equal(changed, want) {
		t.Fatalf("sequential diff changed %v, want %v", changed, want)
	}

	// Each side gets half the workers: 4 is two per side, 32 is more per side
	// than any frontier has Applications.
	for _, parallelism := range []int{4, 8, 32} {
		t.Run(fmt.Sprintf("parallelism-%d", parallelism), func(t *testing.T) {
			delays := reversedFrontierDelays(5*time.Millisecond, frontierParents...)
			parallelRequest := request
			parallelRequest.Parallelism = parallelism
			parallel, err := delays.orchestrator().DiffApps(context.Background(), parallelRequest)
			assertSameFields(t, "DiffApps", snapshotFrontierDiff(parallel, err), sequential)
		})
	}
}

// failingFrontierParents are the static parents of writeFailingFrontierFleet,
// in frontier order.
var failingFrontierParents = []string{"e0", "e1", "e2", "e3", "e4", "e5", "e6"}

// frontierUndecodableApplicationYAML is an Application document whose spec
// does not decode, so scanning the output of the parent that renders it fails
// discovery.
func frontierUndecodableApplicationYAML(name string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ` + name + `
  namespace: argocd
spec:
  source: not-an-object
`
}

// writeFailingFrontierFleet writes failingFrontierParents, each rendering
// the child Application <parent>-child, except that each parent in
// undecodable renders an Application discovery cannot decode.
func writeFailingFrontierFleet(t *testing.T, root string, undecodable ...string) {
	t.Helper()
	for _, name := range failingFrontierParents {
		if slices.Contains(undecodable, name) {
			writeFrontierParent(t, root, name, frontierUndecodableApplicationYAML(name+"-child"))
			continue
		}
		writeFrontierParent(t, root, name, frontierChildApplicationYAML(name+"-child", "workloads/"+name))
		writeFrontierWorkload(t, root, name, "same")
	}
}

// frontierRenderedParents lists, in frontier order, the parents of
// writeFailingFrontierFleet whose render completed and was cached. A render
// cancelled mid-flight is never cached.
func frontierRenderedParents(t *testing.T, result BuildResult) []string {
	t.Helper()
	if result.renderCache == nil {
		t.Fatal("BuildResult carries no render cache")
	}
	result.renderCache.mu.RLock()
	defer result.renderCache.mu.RUnlock()
	rendered := map[string]bool{}
	for _, entry := range result.renderCache.entries {
		if entry.err != nil {
			continue
		}
		for _, renderedManifest := range entry.result.Manifests {
			if renderedManifest.Object == nil || renderedManifest.Object.GetKind() != "Application" {
				continue
			}
			rendered[strings.TrimSuffix(renderedManifest.Object.GetName(), "-child")] = true
		}
	}
	var out []string
	for _, name := range failingFrontierParents {
		if rendered[name] {
			out = append(out, name)
		}
	}
	return out
}

// TestRenderedFleetDiscoveryParallelFailureMatchesSequential pins the
// frontier's error path. e1's output fails discovery while e0 is still held
// and e2..e6 are held or not yet started: the parallel frontier must report
// exactly the sequential error and diagnostics, let e0 finish as the
// sequential frontier does, and cancel every render after e1 that had not
// finished, so it completes the sequential frontier's renders and no others.
func TestRenderedFleetDiscoveryParallelFailureMatchesSequential(t *testing.T) {
	root := t.TempDir()
	writeFailingFrontierFleet(t, root, "e1")
	request := BuildRequest{Path: root}

	sequentialRequest := request
	sequentialRequest.Parallelism = 1
	sequentialResult, err := Orchestrator{}.Build(context.Background(), sequentialRequest)
	sequential := snapshotFrontierBuild(t, sequentialResult, err)
	if !strings.Contains(sequential.Error, "discover rendered Application argocd/e1 output") {
		t.Fatalf("sequential Build() error = %q, want e1's discovery failure", sequential.Error)
	}
	sequentialRendered := frontierRenderedParents(t, sequentialResult)
	if want := []string{"e0", "e1"}; !slices.Equal(sequentialRendered, want) {
		t.Fatalf("sequential frontier rendered %v, want %v", sequentialRendered, want)
	}

	for _, parallelism := range frontierParallelisms {
		t.Run(fmt.Sprintf("parallelism-%d", parallelism), func(t *testing.T) {
			delays := &frontierRenderDelays{delays: map[string]time.Duration{"e0": 200 * time.Millisecond}}
			for _, name := range failingFrontierParents[2:] {
				delays.delays[name] = 50 * time.Millisecond
			}
			parallelRequest := request
			parallelRequest.Parallelism = parallelism
			result, err := delays.orchestrator().Build(context.Background(), parallelRequest)
			assertSameFields(t, "Build", snapshotFrontierBuild(t, result, err), sequential)
			if got := frontierRenderedParents(t, result); !slices.Equal(got, sequentialRendered) {
				t.Fatalf("parallel frontier completed renders of %v after e1 failed, want only %v", got, sequentialRendered)
			}
		})
	}
}

// TestRenderedFleetDiscoveryParallelHonorsCallerCancellation cancels the
// caller's context while both workers hold a render, so the parallel frontier
// stops scheduling and reports the cancellation instead of a partial
// discovery.
func TestRenderedFleetDiscoveryParallelHonorsCallerCancellation(t *testing.T) {
	root := t.TempDir()
	writeFailingFrontierFleet(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	o := Orchestrator{}
	o.renderObserver = func(source render.ResolvedSource) {
		switch path.Base(filepath.ToSlash(source.Path)) {
		case "e0":
			cancel()
			time.Sleep(50 * time.Millisecond)
		case "e1":
			// Hold e1 until e0 cancels, however late e0 reaches its render.
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
	result, err := o.Build(ctx, BuildRequest{Path: root, Parallelism: 2})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context.Canceled", err)
	}
	if got := frontierRenderedParents(t, result); len(got) != 0 {
		t.Fatalf("frontier completed renders of %v after cancellation, want none", got)
	}
}
