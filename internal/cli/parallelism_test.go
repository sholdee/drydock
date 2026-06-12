package cli

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/app"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildAppsParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "build", "apps", "--parallelism", "7")

	if got := recorder.buildRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildRequest.Parallelism = %d, want 7", got)
	}
}

func TestBuildAppsDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "build", "apps")

	want := defaultRenderAppsParallelism()
	if got := recorder.buildRequests[0].Parallelism; got != want {
		t.Fatalf("BuildRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestBuildAppParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "build", "app", "demo", "--parallelism", "7")

	if got := recorder.buildAppRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildAppRequest.Parallelism = %d, want 7", got)
	}
}

func TestBuildAppDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "build", "app", "demo")

	want := defaultRenderAppsParallelism()
	if got := recorder.buildAppRequests[0].Parallelism; got != want {
		t.Fatalf("BuildAppRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestTestAppsParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "test", "apps", "--parallelism", "7")

	if got := recorder.buildRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildRequest.Parallelism = %d, want 7", got)
	}
}

func TestTestAppsDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "test", "apps")

	want := max(runtime.GOMAXPROCS(0), 1)
	want = min(want, maxDefaultRenderAppsParallelism)
	if got := defaultRenderAppsParallelism(); got != want {
		t.Fatalf("defaultRenderAppsParallelism() = %d, want %d", got, want)
	}
	if got := recorder.buildRequests[0].Parallelism; got != want {
		t.Fatalf("BuildRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestTestAppsRequestsStatusOnlyBuild(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "test", "apps")

	if got := recorder.buildRequests[0].StatusOnly; !got {
		t.Fatalf("BuildRequest.StatusOnly = %t, want true", got)
	}
}

func TestTestAppsSelectorRequestsStatusOnlyBuild(t *testing.T) {
	recorder := &recordingCLIOrchestrator{listResult: app.BuildResult{Applications: []argoappv1.Application{
		{ObjectMeta: metav1.ObjectMeta{Name: "selected", Labels: map[string]string{"app": "demo"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "unselected", Labels: map[string]string{"app": "other"}}},
	}}}
	executeParallelismCommand(t, recorder, "test", "apps", "--selector", "app=demo")

	if got := len(recorder.buildSelectionRequests); got != 1 {
		t.Fatalf("build selection requests = %d, want 1", got)
	}
	if got := recorder.buildSelectionRequests[0].StatusOnly; !got {
		t.Fatalf("BuildSelection BuildRequest.StatusOnly = %t, want true", got)
	}
	if got := recorder.buildSelectionResultApplications; len(got) != 1 || got[0].Name != "selected" {
		t.Fatalf("BuildSelection selected applications = %#v, want selected", got)
	}
}

func TestTestAppParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "test", "app", "demo", "--parallelism", "7")

	if got := recorder.buildAppRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildAppRequest.Parallelism = %d, want 7", got)
	}
}

func TestTestAppDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "test", "app", "demo")

	want := defaultRenderAppsParallelism()
	if got := recorder.buildAppRequests[0].Parallelism; got != want {
		t.Fatalf("BuildAppRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestTestAppRequestsStatusOnlyBuild(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "test", "app", "demo")

	if got := recorder.buildAppRequests[0].StatusOnly; !got {
		t.Fatalf("BuildAppRequest.StatusOnly = %t, want true", got)
	}
}

func TestNonTestCommandsDoNotRequestStatusOnlyBuild(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		statusOnly func(*recordingCLIOrchestrator) bool
	}{
		{
			name: "build apps",
			args: []string{"build", "apps"},
			statusOnly: func(recorder *recordingCLIOrchestrator) bool {
				return recorder.buildRequests[0].StatusOnly
			},
		},
		{
			name: "build app",
			args: []string{"build", "app", "demo"},
			statusOnly: func(recorder *recordingCLIOrchestrator) bool {
				return recorder.buildAppRequests[0].StatusOnly
			},
		},
		{
			name: "get apps",
			args: []string{"get", "apps"},
			statusOnly: func(recorder *recordingCLIOrchestrator) bool {
				return recorder.listRequests[0].StatusOnly
			},
		},
		{
			name: "get images",
			args: []string{"get", "images"},
			statusOnly: func(recorder *recordingCLIOrchestrator) bool {
				return recorder.buildSelectionRequests[0].StatusOnly
			},
		},
		{
			name: "diag",
			args: []string{"diag"},
			statusOnly: func(recorder *recordingCLIOrchestrator) bool {
				return recorder.listRequests[0].StatusOnly
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &recordingCLIOrchestrator{}
			executeParallelismCommand(t, recorder, tt.args...)
			if got := tt.statusOnly(recorder); got {
				t.Fatalf("StatusOnly = %t, want false", got)
			}
		})
	}
}

func TestDiffAppsParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "apps", "--path-orig", "left", "--path", "right", "--parallelism", "7")

	if got := recorder.diffAppsRequests[0].Parallelism; got != 7 {
		t.Fatalf("DiffRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiffAppsDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "apps", "--path-orig", "left", "--path", "right")

	want := defaultRenderAppsParallelism()
	if got := recorder.diffAppsRequests[0].Parallelism; got != want {
		t.Fatalf("DiffRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestDiffAppParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "app", "demo", "--path-orig", "left", "--path", "right", "--parallelism", "7")

	if got := recorder.diffAppRequests[0].Parallelism; got != 7 {
		t.Fatalf("DiffAppRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiffAppDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "app", "demo", "--path-orig", "left", "--path", "right")

	want := defaultRenderAppsParallelism()
	if got := recorder.diffAppRequests[0].Parallelism; got != want {
		t.Fatalf("DiffAppRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestDiffImagesParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "images", "--path-orig", "left", "--path", "right", "--parallelism", "7")

	if got := recorder.diffImagesRequests[0].Parallelism; got != 7 {
		t.Fatalf("DiffRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiffImagesDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "images", "--path-orig", "left", "--path", "right")

	want := defaultRenderAppsParallelism()
	if got := recorder.diffImagesRequests[0].Parallelism; got != want {
		t.Fatalf("DiffRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestDiffAppsUnifiedZeroFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "apps", "--path-orig", "left", "--path", "right", "--unified", "0")

	if got := recorder.diffAppsRequests[0].Unified; got != 0 {
		t.Fatalf("DiffRequest.Unified = %d, want explicit 0", got)
	}
}

func TestGetImagesParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "get", "images", "--parallelism", "7")

	if got := recorder.buildSelectionRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildSelection BuildRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiagParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diag", "--parallelism", "7")

	if got := recorder.listRequests[0].Parallelism; got != 7 {
		t.Fatalf("ListApplications BuildRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiagDefaultParallelismIsAuto(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diag")

	want := defaultRenderAppsParallelism()
	if got := recorder.listRequests[0].Parallelism; got != want {
		t.Fatalf("ListApplications BuildRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestParallelismRejectsNegativeValue(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", t.TempDir(), "--parallelism", "-1"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want negative parallelism validation")
	}
	if !strings.Contains(err.Error(), "parallelism must be greater than or equal to 0") {
		t.Fatalf("Execute() error = %v, want parallelism validation", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func executeParallelismCommand(t *testing.T, recorder *recordingCLIOrchestrator, args ...string) {
	t.Helper()
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: recorder})
	cmd.SetArgs(args)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%v) error = %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
	}
}

type recordingCLIOrchestrator struct {
	buildRequests                    []app.BuildRequest
	buildAppRequests                 []app.BuildAppRequest
	buildSelectionRequests           []app.BuildRequest
	buildSelectionResultApplications []argoappv1.Application
	listRequests                     []app.BuildRequest
	diffAppsRequests                 []app.DiffRequest
	diffAppRequests                  []app.DiffAppRequest
	diffImagesRequests               []app.DiffRequest
	diagRequests                     []app.DiagRequest
	buildResult                      app.BuildResult
	buildError                       error
	buildHook                        func(app.BuildRequest) error
	buildAppResult                   app.BuildResult
	buildAppError                    error
	diffAppsResult                   app.DiffResult
	diffAppsError                    error
	diffAppResult                    app.DiffResult
	diffAppError                     error
	diffImagesResult                 app.ImageDiffResult
	diffImagesError                  error
	listResult                       app.BuildResult
	listError                        error
	diagResult                       app.DiagResult
	diagError                        error
}

func (orchestrator *recordingCLIOrchestrator) Build(_ context.Context, request app.BuildRequest) (app.BuildResult, error) {
	orchestrator.buildRequests = append(orchestrator.buildRequests, request)
	if orchestrator.buildHook != nil {
		if err := orchestrator.buildHook(request); err != nil {
			return orchestrator.buildResult, err
		}
	}
	return orchestrator.buildResult, orchestrator.buildError
}

func (orchestrator *recordingCLIOrchestrator) BuildApp(_ context.Context, request app.BuildAppRequest) (app.BuildResult, error) {
	orchestrator.buildAppRequests = append(orchestrator.buildAppRequests, request)
	return orchestrator.buildAppResult, orchestrator.buildAppError
}

func (orchestrator *recordingCLIOrchestrator) BuildSelection(_ context.Context, request app.BuildRequest, selectApps app.ApplicationSelector) (app.BuildResult, error) {
	orchestrator.buildSelectionRequests = append(orchestrator.buildSelectionRequests, request)
	result := orchestrator.buildResult
	if selectApps != nil {
		result.Applications = selectApps(orchestrator.listResult.Applications)
		orchestrator.buildSelectionResultApplications = result.Applications
	}
	return result, orchestrator.buildError
}

func (orchestrator *recordingCLIOrchestrator) ListApplications(_ context.Context, request app.BuildRequest) (app.BuildResult, error) {
	orchestrator.listRequests = append(orchestrator.listRequests, request)
	return orchestrator.listResult, orchestrator.listError
}

func (orchestrator *recordingCLIOrchestrator) DiffApps(_ context.Context, request app.DiffRequest) (app.DiffResult, error) {
	orchestrator.diffAppsRequests = append(orchestrator.diffAppsRequests, request)
	return orchestrator.diffAppsResult, orchestrator.diffAppsError
}

func (orchestrator *recordingCLIOrchestrator) DiffApp(_ context.Context, request app.DiffAppRequest) (app.DiffResult, error) {
	orchestrator.diffAppRequests = append(orchestrator.diffAppRequests, request)
	return orchestrator.diffAppResult, orchestrator.diffAppError
}

func (orchestrator *recordingCLIOrchestrator) DiffImages(_ context.Context, request app.DiffRequest) (app.ImageDiffResult, error) {
	orchestrator.diffImagesRequests = append(orchestrator.diffImagesRequests, request)
	return orchestrator.diffImagesResult, orchestrator.diffImagesError
}

func (orchestrator *recordingCLIOrchestrator) Diag(_ context.Context, request app.DiagRequest) (app.DiagResult, error) {
	orchestrator.diagRequests = append(orchestrator.diagRequests, request)
	return orchestrator.diagResult, orchestrator.diagError
}
