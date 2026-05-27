package app

import (
	"context"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOrchestratorBuildAppRendersOnlyNamedApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "first", "one")
	writeBuildApplication(t, root, "second", "two")

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name: "second",
		BuildRequest: BuildRequest{
			Path: root,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "second" {
		t.Fatalf("Applications = %#v, want only second", result.Applications)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if result.Manifests[0].Object.GetName() != "two" {
		t.Fatalf("rendered ConfigMap name = %q, want two", result.Manifests[0].Object.GetName())
	}
}

func TestOrchestratorBuildAppPreservesSelectedApplicationInputs(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "first", "one")
	writeBuildApplication(t, root, "second", "two")

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name: "second",
		BuildRequest: BuildRequest{
			Path: root,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}

	if len(result.ApplicationInputs) != 1 {
		t.Fatalf("len(ApplicationInputs) = %d, want 1: %#v", len(result.ApplicationInputs), result.ApplicationInputs)
	}
	input := result.ApplicationInputs[0]
	if input.Application.Name != "second" {
		t.Fatalf("ApplicationInputs[0].Application.Name = %q, want second", input.Application.Name)
	}
	wantPath := filepath.ToSlash(filepath.Join("apps", "second.yaml"))
	if len(input.Paths) != 1 || input.Paths[0] != wantPath {
		t.Fatalf("ApplicationInputs[0].Paths = %#v, want [%q]", input.Paths, wantPath)
	}
	if strings.Contains(strings.Join(input.Paths, ","), "first") {
		t.Fatalf("ApplicationInputs[0].Paths = %#v, want no first app paths", input.Paths)
	}
}

func TestOrchestratorBuildAppStrictValidatesOnlySelectedApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithProject(t, root, "selected", "selected", "platform", "https://github.com/example/allowed", "workloads")
	writeBuildApplicationWithProject(t, root, "unrelated", "unrelated", "platform", "https://github.com/example/denied", "workloads")
	writeTestFile(t, filepath.Join(root, "settings", "allowed-repo.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: allowed-repo
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: https://github.com/example/allowed
  project: platform
`)
	writeTestFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/allowed
  destinations:
    - server: https://kubernetes.default.svc
      namespace: workloads
`)

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "selected",
		BuildRequest: BuildRequest{Path: root, Strict: true},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if hasDiagnosticMessage(result.Diagnostics, "unrelated") || hasDiagnosticMessage(result.Diagnostics, "denied") {
		t.Fatalf("Diagnostics = %#v, want no unrelated project violation", result.Diagnostics)
	}
}

func TestOrchestratorListApplicationsPreservesMatrixGeneratorInputPaths(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "apps", "alpha"),
		filepath.Join(root, "clusters", "dev"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	writeTestFile(t, filepath.Join(root, "appsets", "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-paths
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - git:
              pathParamPrefix: app
              directories:
                - path: apps/*
          - git:
              pathParamPrefix: cluster
              directories:
                - path: clusters/*
  template:
    metadata:
      name: '{{.app.path.basename}}-{{.cluster.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{.app.path.path}}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.cluster.path.basename}}'
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.ApplicationInputs) != 1 {
		t.Fatalf("len(ApplicationInputs) = %d, want 1: %#v", len(result.ApplicationInputs), result.ApplicationInputs)
	}
	input := result.ApplicationInputs[0]
	for _, want := range []string{"appsets/appset.yaml", "apps/alpha", "clusters/dev"} {
		if !slices.Contains(input.Paths, want) {
			t.Fatalf("ApplicationInputs[0].Paths = %#v, missing %q", input.Paths, want)
		}
	}

	selected, unowned := SelectChangedApplicationInputs(result.ApplicationInputs, []string{"clusters/dev/config.yaml"})
	if len(unowned) != 0 {
		t.Fatalf("unowned = %#v, want none", unowned)
	}
	if len(selected) != 1 || selected[0].Name != "alpha-dev" {
		t.Fatalf("selected = %#v, want alpha-dev", selected)
	}
}

func TestOrchestratorBuildAppReportsMissingApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")

	_, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "missing",
		BuildRequest: BuildRequest{Path: root},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want missing application error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found`) {
		t.Fatalf("BuildApp() error = %v, want missing application message", err)
	}
}

func TestOrchestratorBuildAppRequiresName(t *testing.T) {
	_, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         " ",
		BuildRequest: BuildRequest{Path: t.TempDir()},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want required name error")
	}
	if !strings.Contains(err.Error(), "application name is required") {
		t.Fatalf("BuildApp() error = %v, want required name message", err)
	}
}

func TestOrchestratorBuildAppPreservesBuildOptionsForSelectedApplication(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	cacheDir := filepath.Join(root, "chart-cache")
	writeAppTestValueChart(t, chartDir)
	writeBuildApplication(t, root, "plain", "plain")
	writeChartOnlyBuildApplication(t, root, "chart-only")
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	result, err := (Orchestrator{ChartAcquirer: acquirer}).BuildApp(context.Background(), BuildAppRequest{
		Name: "chart-only",
		BuildRequest: BuildRequest{
			Path: root,
			AcquisitionOptions: AcquisitionOptions{
				ChartCacheDir: cacheDir,
				Offline:       true,
				RefreshCharts: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "chart-only" {
		t.Fatalf("Applications = %#v, want only chart-only", result.Applications)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got, want := acquirer.options[0], (chart.Options{
		CacheDir: cacheDir,
		Offline:  true,
		Refresh:  true,
	}); got != want {
		t.Fatalf("chart options = %#v, want %#v", got, want)
	}
}

func TestOrchestratorBuildAppPreservesListAndRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "direct",
		BuildRequest: BuildRequest{Path: root},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want 2: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	categories := map[string]bool{}
	for _, diag := range result.Diagnostics {
		categories[diag.Category] = true
	}
	for _, want := range []string{"appset", "repeated-resource"} {
		if !categories[want] {
			t.Fatalf("diagnostic categories = %#v, want %q", categories, want)
		}
	}
}

func TestOrchestratorBuildAppMarksSelectedApplicationSkippedWhenPreconditionFails(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "direct",
		BuildRequest: BuildRequest{Path: root, Strict: true},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want strict ApplicationSet precondition error")
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "direct", Status: ApplicationStatusSkipped},
	})
}

func TestOrchestratorListApplicationsSkipsUnsupportedApplicationSetInNonStrictMode(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Name != "direct" {
		t.Fatalf("Application name = %q, want direct", result.Applications[0].Name)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	diag := result.Diagnostics[0]
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
	}
	if diag.Category != "appset" {
		t.Fatalf("diagnostic category = %q, want appset", diag.Category)
	}
}

func TestOrchestratorLoadsAndMergesProviderFixtureConfig(t *testing.T) {
	root := t.TempDir()
	fixturePath := filepath.Join(root, "provider.yaml")
	writeTestFile(t, fixturePath, `
clusters:
  - name: prod
    server: https://prod.example.invalid
`)
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path: root,
		ApplicationSetOptions: ApplicationSetOptions{
			ApplicationSetProviderFixtures: []string{fixturePath},
			ApplicationSetProviderData: appset.ProviderData{
				Clusters: []appset.ClusterInput{{
					Name:   "prod",
					Server: "https://prod.example.invalid",
				}},
			},
		},
	})
	if err == nil {
		t.Fatal("ListApplications() error = nil, want duplicate provider fixture identity error")
	}
	if !strings.Contains(err.Error(), "duplicate provider fixture cluster identity") {
		t.Fatalf("ListApplications() error = %v, want duplicate provider fixture identity", err)
	}
	if len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0].Message, "duplicate provider fixture cluster identity") {
		t.Fatalf("Diagnostics = %#v, want provider fixture duplicate diagnostic", result.Diagnostics)
	}
}

func TestOrchestratorListApplicationsFailsUnsupportedApplicationSetInStrictMode(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatalf("ListApplications() error = nil, want unsupported ApplicationSet error")
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("diagnostic severity = %s, want error", result.Diagnostics[0].Severity)
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("ListApplications() error = %q, want unsupported ApplicationSet generator", err.Error())
	}
}
