package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

const projectGateAppSet = `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: projects
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: team-a-app
            project: team-a
          - name: default-app
            project: default
          - name: missing-app
            project: team-missing
  template:
    metadata:
      name: "{{.name}}"
    spec:
      project: "{{.project}}"
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: apps
      destination:
        name: in-cluster
        namespace: default
`

const projectGateTeamA = `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: team-a
  namespace: argocd
spec:
  sourceRepos: ["*"]
  destinations:
    - name: in-cluster
      namespace: "*"
`

const projectGatePlainApp = `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plain-missing
  namespace: argocd
spec:
  project: team-missing
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: apps
  destination:
    name: in-cluster
    namespace: default
`

func listApplicationNames(t *testing.T, root string) ([]string, []diagnostic.Diagnostic) {
	t.Helper()
	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	names := make([]string, 0, len(result.Applications))
	for _, app := range result.Applications {
		names = append(names, app.Name)
	}
	slices.Sort(names)
	return names, result.Diagnostics
}

// The Argo CD v3.6.1 ApplicationSet controller does not create a generated
// Application whose AppProject does not exist, and keeps the others
// (applicationset/controllers/applicationset_controller.go:627-633). A plain
// Application that names the same missing project stays: the application
// controller reports it.
func TestListApplicationsDropsGeneratedApplicationWithMissingProject(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), projectGateAppSet)
	writeTestFile(t, filepath.Join(root, "projects", "team-a.yaml"), projectGateTeamA)
	writeTestFile(t, filepath.Join(root, "apps", "plain.yaml"), projectGatePlainApp)

	names, diags := listApplicationNames(t, root)
	if want := []string{"default-app", "plain-missing", "team-a-app"}; !slices.Equal(names, want) {
		t.Fatalf("applications = %#v, want %#v", names, want)
	}
	var found int
	for _, diag := range diags {
		if diagnostic.StableCode(diag) != "appset.application-validation-failed" {
			continue
		}
		found++
		if !strings.Contains(diag.Message, "application references project team-missing which does not exist") || diag.Provenance.Path != "appset.yaml" {
			t.Fatalf("diagnostic = %#v, want the Argo CD message for appset.yaml", diag)
		}
	}
	if found != 1 {
		t.Fatalf("diagnostics = %#v, want one appset.application-validation-failed warning", diags)
	}
}

// When the repository declares no AppProject, any project may exist only on
// the cluster, so drydock keeps every generated Application.
func TestListApplicationsKeepsGeneratedApplicationsWithoutLocalProjects(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), projectGateAppSet)

	names, diags := listApplicationNames(t, root)
	if want := []string{"default-app", "missing-app", "team-a-app"}; !slices.Equal(names, want) {
		t.Fatalf("applications = %#v, want %#v", names, want)
	}
	for _, diag := range diags {
		if diagnostic.StableCode(diag) == "appset.application-validation-failed" {
			t.Fatalf("diagnostics = %#v, want no validation warning", diags)
		}
	}
}

// A generator error that Argo CD reports as an ErrorOccurred condition
// fails only its own ApplicationSet; the run continues.
func TestListApplicationsScopesGeneratorFailureToOneApplicationSet(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "healthy.yaml"), strings.ReplaceAll(projectGateAppSet, "name: projects", "name: healthy"))
	writeTestFile(t, filepath.Join(root, "failing.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: failing
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - list:
              elements:
                - name: one
          - {}
  template:
    metadata:
      name: "failing-{{.name}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: apps
      destination:
        name: in-cluster
        namespace: default
`)

	names, diags := listApplicationNames(t, root)
	if want := []string{"default-app", "missing-app", "team-a-app"}; !slices.Equal(names, want) {
		t.Fatalf("applications = %#v, want %#v", names, want)
	}
	var failed int
	for _, diag := range diags {
		if strings.Contains(diag.Message, "generated zero Applications") {
			t.Fatalf("diagnostics = %#v, want no empty-ApplicationSet warning for the failing ApplicationSet", diags)
		}
		if diagnostic.StableCode(diag) == diagnostic.CodeAppSetGeneratorFailed && strings.Contains(diag.Message, "child generator generated no parameters") {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("diagnostics = %#v, want one appset.generator-failed warning", diags)
	}
}
