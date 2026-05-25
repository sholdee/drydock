package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
)

func TestBuildAppsParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "build", "apps", "--parallelism", "7")

	if got := recorder.buildRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildRequest.Parallelism = %d, want 7", got)
	}
}

func TestBuildAppParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "build", "app", "demo", "--parallelism", "7")

	if got := recorder.buildAppRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildAppRequest.Parallelism = %d, want 7", got)
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

	want := defaultTestAppsParallelism()
	if want < 1 {
		t.Fatalf("defaultTestAppsParallelism() = %d, want >= 1", want)
	}
	if got := recorder.buildRequests[0].Parallelism; got != want {
		t.Fatalf("BuildRequest.Parallelism = %d, want %d", got, want)
	}
}

func TestTestAppParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "test", "app", "demo", "--parallelism", "7")

	if got := recorder.buildAppRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildAppRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiffAppsParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "apps", "--path-orig", "left", "--path", "right", "--parallelism", "7")

	if got := recorder.diffAppsRequests[0].Parallelism; got != 7 {
		t.Fatalf("DiffRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiffAppParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "app", "demo", "--path-orig", "left", "--path", "right", "--parallelism", "7")

	if got := recorder.diffAppRequests[0].Parallelism; got != 7 {
		t.Fatalf("DiffAppRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiffImagesParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diff", "images", "--path-orig", "left", "--path", "right", "--parallelism", "7")

	if got := recorder.diffImagesRequests[0].Parallelism; got != 7 {
		t.Fatalf("DiffRequest.Parallelism = %d, want 7", got)
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

	if got := recorder.listRequests[0].Parallelism; got != 7 {
		t.Fatalf("ListApplications BuildRequest.Parallelism = %d, want 7", got)
	}
	if got := recorder.buildRequests[0].Parallelism; got != 7 {
		t.Fatalf("BuildRequest.Parallelism = %d, want 7", got)
	}
}

func TestDiagParallelismFlag(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeParallelismCommand(t, recorder, "diag", "--parallelism", "7")

	if got := recorder.diagRequests[0].Parallelism; got != 7 {
		t.Fatalf("DiagRequest.Parallelism = %d, want 7", got)
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
	buildRequests      []app.BuildRequest
	buildAppRequests   []app.BuildAppRequest
	listRequests       []app.BuildRequest
	diffAppsRequests   []app.DiffRequest
	diffAppRequests    []app.DiffAppRequest
	diffImagesRequests []app.DiffRequest
	diagRequests       []app.DiagRequest
}

func (orchestrator *recordingCLIOrchestrator) Build(_ context.Context, request app.BuildRequest) (app.BuildResult, error) {
	orchestrator.buildRequests = append(orchestrator.buildRequests, request)
	return app.BuildResult{}, nil
}

func (orchestrator *recordingCLIOrchestrator) BuildApp(_ context.Context, request app.BuildAppRequest) (app.BuildResult, error) {
	orchestrator.buildAppRequests = append(orchestrator.buildAppRequests, request)
	return app.BuildResult{}, nil
}

func (orchestrator *recordingCLIOrchestrator) ListApplications(_ context.Context, request app.BuildRequest) (app.BuildResult, error) {
	orchestrator.listRequests = append(orchestrator.listRequests, request)
	return app.BuildResult{}, nil
}

func (orchestrator *recordingCLIOrchestrator) DiffApps(_ context.Context, request app.DiffRequest) (app.DiffResult, error) {
	orchestrator.diffAppsRequests = append(orchestrator.diffAppsRequests, request)
	return app.DiffResult{}, nil
}

func (orchestrator *recordingCLIOrchestrator) DiffApp(_ context.Context, request app.DiffAppRequest) (app.DiffResult, error) {
	orchestrator.diffAppRequests = append(orchestrator.diffAppRequests, request)
	return app.DiffResult{}, nil
}

func (orchestrator *recordingCLIOrchestrator) DiffImages(_ context.Context, request app.DiffRequest) (app.ImageDiffResult, error) {
	orchestrator.diffImagesRequests = append(orchestrator.diffImagesRequests, request)
	return app.ImageDiffResult{}, nil
}

func (orchestrator *recordingCLIOrchestrator) Diag(_ context.Context, request app.DiagRequest) (app.DiagResult, error) {
	orchestrator.diagRequests = append(orchestrator.diagRequests, request)
	return app.DiagResult{}, nil
}
