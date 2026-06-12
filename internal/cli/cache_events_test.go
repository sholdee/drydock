package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/cacheevent"
)

func TestRenderCacheEventsText(t *testing.T) {
	var buf bytes.Buffer
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionHit, Target: "argocd/demo"},
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionSkipped, Target: "argocd/demo", Reason: "inputs-changed", Error: "boom"},
		{Source: cacheevent.SourceChart, Action: cacheevent.ActionFetch, Target: "https://charts.example.test", Revision: "1.2.3"},
	}
	if err := renderCacheEventsText(&buf, events); err != nil {
		t.Fatalf("renderCacheEventsText() error = %v", err)
	}
	want := "cache render hit target=argocd/demo\n" +
		"cache render skipped target=argocd/demo reason=inputs-changed error=\"boom\"\n" +
		"cache chart fetch target=https://charts.example.test revision=1.2.3\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

func TestBuildAppsCacheEventsEmitToStderr(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionHit, Target: "argocd/demo"},
		{Source: cacheevent.SourceChart, Action: cacheevent.ActionFetch, Target: "https://charts.example.test", Revision: "1.2.3"},
	}
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			CacheEvents: events,
		},
	}
	result := runCLIWithDependencies(t, Dependencies{Orchestrator: recorder},
		"build", "apps", "--cache-events")

	// Event lines must appear on stderr.
	if !strings.Contains(result.Stderr, "cache render hit target=argocd/demo") {
		t.Fatalf("stderr missing render hit event:\nstderr:\n%s", result.Stderr)
	}
	if !strings.Contains(result.Stderr, "cache chart fetch target=https://charts.example.test revision=1.2.3") {
		t.Fatalf("stderr missing chart fetch event:\nstderr:\n%s", result.Stderr)
	}

	// Stdout must contain no "cache " event lines.
	if strings.Contains(result.Stdout, "cache ") {
		t.Fatalf("stdout must not contain cache event lines:\nstdout:\n%s", result.Stdout)
	}
}

func TestBuildAppsCacheEventsAbsentWithoutFlag(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionHit, Target: "argocd/demo"},
	}
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			CacheEvents: events,
		},
	}
	result := runCLIWithDependencies(t, Dependencies{Orchestrator: recorder},
		"build", "apps")

	// Without --cache-events, no event lines anywhere.
	if strings.Contains(result.Stdout, "cache ") {
		t.Fatalf("stdout must not contain cache event lines without flag:\nstdout:\n%s", result.Stdout)
	}
	if strings.Contains(result.Stderr, "cache ") {
		t.Fatalf("stderr must not contain cache event lines without flag:\nstderr:\n%s", result.Stderr)
	}
}

func TestTestAppsCacheEventsEmitToStderr(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionHit, Target: "argocd/demo"},
		{Source: cacheevent.SourceGit, Action: cacheevent.ActionFetch, Target: "https://git.example.test", Revision: "abc123"},
	}
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses:    []app.ApplicationStatus{{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass}},
			CacheEvents: events,
		},
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: recorder})
	cmd.SetArgs([]string{"test", "apps", "--cache-events"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// Event lines must appear on stderr.
	if !strings.Contains(stderr.String(), "cache render hit target=argocd/demo") {
		t.Fatalf("stderr missing render hit event:\nstderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "cache git fetch target=https://git.example.test revision=abc123") {
		t.Fatalf("stderr missing git fetch event:\nstderr:\n%s", stderr.String())
	}

	// Stdout must contain no "cache " event lines (statuses only).
	if strings.Contains(stdout.String(), "cache ") {
		t.Fatalf("stdout must not contain cache event lines:\nstdout:\n%s", stdout.String())
	}
}

func TestTestAppsCacheEventsAbsentWithoutFlag(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionHit, Target: "argocd/demo"},
	}
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			Statuses:    []app.ApplicationStatus{{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass}},
			CacheEvents: events,
		},
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: recorder})
	cmd.SetArgs([]string{"test", "apps"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// Without --cache-events, no event lines anywhere.
	if strings.Contains(stdout.String(), "cache ") {
		t.Fatalf("stdout must not contain cache event lines without flag:\nstdout:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "cache ") {
		t.Fatalf("stderr must not contain cache event lines without flag:\nstderr:\n%s", stderr.String())
	}
}

func TestBuildAppEmitsCacheEventsOnStderr(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionHit, Target: "argocd/demo"},
		{Source: cacheevent.SourceChart, Action: cacheevent.ActionFetch, Target: "https://charts.example.test", Revision: "1.2.3"},
	}
	recorder := &recordingCLIOrchestrator{
		buildAppResult: app.BuildResult{
			CacheEvents: events,
		},
	}
	result := runCLIWithDependencies(t, Dependencies{Orchestrator: recorder},
		"build", "app", "demo", "--cache-events")

	// Event lines must appear on stderr.
	if !strings.Contains(result.Stderr, "cache render hit target=argocd/demo") {
		t.Fatalf("stderr missing render hit event:\nstderr:\n%s", result.Stderr)
	}
	if !strings.Contains(result.Stderr, "cache chart fetch target=https://charts.example.test revision=1.2.3") {
		t.Fatalf("stderr missing chart fetch event:\nstderr:\n%s", result.Stderr)
	}

	// Stdout must contain no "cache " event lines.
	if strings.Contains(result.Stdout, "cache ") {
		t.Fatalf("stdout must not contain cache event lines:\nstdout:\n%s", result.Stdout)
	}
}

func TestTestAppEmitsCacheEventsOnStderr(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionHit, Target: "argocd/demo"},
		{Source: cacheevent.SourceGit, Action: cacheevent.ActionFetch, Target: "https://git.example.test", Revision: "abc123"},
	}
	recorder := &recordingCLIOrchestrator{
		buildAppResult: app.BuildResult{
			Statuses:    []app.ApplicationStatus{{Namespace: "argocd", Name: "demo", Status: app.ApplicationStatusPass}},
			CacheEvents: events,
		},
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: recorder})
	cmd.SetArgs([]string{"test", "app", "demo", "--cache-events"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// Event lines must appear on stderr.
	if !strings.Contains(stderr.String(), "cache render hit target=argocd/demo") {
		t.Fatalf("stderr missing render hit event:\nstderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "cache git fetch target=https://git.example.test revision=abc123") {
		t.Fatalf("stderr missing git fetch event:\nstderr:\n%s", stderr.String())
	}

	// Stdout must contain no "cache " event lines (statuses only).
	if strings.Contains(stdout.String(), "cache ") {
		t.Fatalf("stdout must not contain cache event lines:\nstdout:\n%s", stdout.String())
	}
}

func TestBuildAppsEmitsCacheEventsOnError(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionSkipped, Target: "argocd/demo", Reason: "inputs-changed"},
	}
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			CacheEvents: events,
		},
		buildError: errors.New("render failed"),
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: recorder})
	cmd.SetArgs([]string{"build", "apps", "--cache-events"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	// Execute must return an error.
	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want non-nil error\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	// Cache events must appear on stderr even when the command errors.
	if !strings.Contains(stderr.String(), "cache render skipped target=argocd/demo reason=inputs-changed") {
		t.Fatalf("stderr missing skipped event:\nstderr:\n%s", stderr.String())
	}

	// Stdout must contain no "cache " event lines.
	if strings.Contains(stdout.String(), "cache ") {
		t.Fatalf("stdout must not contain cache event lines:\nstdout:\n%s", stdout.String())
	}
}

func TestTestAppsEmitsCacheEventsOnError(t *testing.T) {
	events := []cacheevent.Event{
		{Source: cacheevent.SourceRender, Action: cacheevent.ActionSkipped, Target: "argocd/demo", Reason: "inputs-changed"},
	}
	recorder := &recordingCLIOrchestrator{
		buildResult: app.BuildResult{
			CacheEvents: events,
		},
		buildError: errors.New("build failed"),
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: recorder})
	cmd.SetArgs([]string{"test", "apps", "--cache-events"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	// Execute must return an error (statuses is nil, so testCommandError returns the orchestrator error).
	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want non-nil error\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	// Cache events must appear on stderr even when the command errors.
	if !strings.Contains(stderr.String(), "cache render skipped target=argocd/demo reason=inputs-changed") {
		t.Fatalf("stderr missing skipped event:\nstderr:\n%s", stderr.String())
	}

	// Stdout must contain no "cache " event lines.
	if strings.Contains(stdout.String(), "cache ") {
		t.Fatalf("stdout must not contain cache event lines:\nstdout:\n%s", stdout.String())
	}
}
