package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/app"
	"github.com/home-operations/argocd-local/internal/chart"
)

func TestDiagCleanRepositoryPrintsNoManifests(t *testing.T) {
	root := t.TempDir()
	writeSimpleAppForCLI(t, root, "ok")

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiagPrintsUnsupportedApplicationSetWarning(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "warning appset: unsupported ApplicationSet generator; supported generators are git directories, git files, list, matrix, and merge (path: unsupported-appset.yaml, pointer: spec.generators)\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestDiagJSONOutputContainsDiagnosticsWithCodes(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
	}
	var report struct {
		Diagnostics []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Category string `json:"category"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one diagnostic", report.Diagnostics)
	}
	if got := report.Diagnostics[0].Code; got != "appset.unsupported-generator" {
		t.Fatalf("diagnostic code = %q, want appset.unsupported-generator", got)
	}
}

func TestDiagYAMLOutputContainsDiagnosticsWithCodes(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "yaml"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty for structured diag output", stderr.String())
	}
	if !strings.Contains(stdout.String(), "code: appset.unsupported-generator") {
		t.Fatalf("stdout = %q, want diagnostic code", stdout.String())
	}
}

func TestDiagJSONOutputCanIncludeCacheEvents(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeCLITestFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    chart: demo
    targetRevision: 1.2.3
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeCLITestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{ChartAcquirer: &recordingDiagChartAcquirer{chartDir: chartDir, fromCache: true}},
	})
	cmd.SetArgs([]string{"diag", "--path", root, "-o", "json", "--cache-events"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var report struct {
		CacheEvents []struct {
			Source string `json:"source"`
			Action string `json:"action"`
			Target string `json:"target"`
		} `json:"cacheEvents"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\nstdout:\n%s", err, stdout.String())
	}
	if len(report.CacheEvents) == 0 {
		t.Fatalf("cacheEvents = %#v, want non-empty", report.CacheEvents)
	}
	if report.CacheEvents[0].Source != "chart" || report.CacheEvents[0].Action != "hit" || report.CacheEvents[0].Target != "https://charts.example.test" {
		t.Fatalf("cacheEvents = %#v, want chart hit", report.CacheEvents)
	}
}

func TestDiagRejectsUnsupportedStructuredOutput(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "-o", "name"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "diag output supports text, json, or yaml") {
		t.Fatalf("Execute() error = %v, want diag output error", err)
	}
}

func TestDiagRejectsUnsupportedOutputBeforeAcquisition(t *testing.T) {
	root := t.TempDir()
	writeCLITestFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/external
    path: missing
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	gitAcquirer := &recordingCLIGitAcquirer{err: errors.New("git acquirer should not be called")}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{GitAcquirer: gitAcquirer},
	})
	cmd.SetArgs([]string{"diag", "--path", root, "--allow-network", "-o", "name"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "diag output supports text, json, or yaml") {
		t.Fatalf("Execute() error = %v, want diag output error", err)
	}
	if len(gitAcquirer.requests) != 0 {
		t.Fatalf("Git requests = %#v, want none before output validation", gitAcquirer.requests)
	}
}

func TestDiagStrictUnsupportedApplicationSetErrors(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetForCLI(t, root)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--path", root, "--strict"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want strict diagnostic error")
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("error = %v, want unsupported ApplicationSet generator", err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "error appset:") {
		t.Fatalf("stderr = %q, want error appset diagnostic", stderr.String())
	}
}

func TestDiagRejectsInvalidRepoMap(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"diag", "--repo-map", "https://github.com/example/repo"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want repo-map error")
	}
	if !strings.Contains(err.Error(), "must use URL=PATH") {
		t.Fatalf("error = %v, want repo-map parse error", err)
	}
}

func writeUnsupportedApplicationSetForCLI(t *testing.T, root string) {
	t.Helper()
	writeSimpleAppForCLI(t, root, "ok")
	writeCLITestFile(t, filepath.Join(root, "unsupported-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
  namespace: argocd
spec:
  generators:
    - scmProvider: {}
  template:
    metadata:
      name: generated
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: manifests/generated
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)
}

type recordingDiagChartAcquirer struct {
	chartDir  string
	fromCache bool
}

func (acquirer *recordingDiagChartAcquirer) Acquire(_ context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	return chart.Result{
		ChartDir:   acquirer.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  acquirer.fromCache,
	}, nil
}
