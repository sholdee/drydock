package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	sourcepkg "github.com/sholdee/drydock/internal/source"
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

// The Argo CD v3.6.1 ApplicationSet controller does not create a generated
// Application whose AppProject does not exist in the cluster
// (applicationset/controllers/applicationset_controller.go:626-633).
// drydock does not know the AppProjects of the cluster offline, so it keeps
// the Application, as for a plain Application, and its project.missing
// warning names the ApplicationSet and says what Argo CD does.
func TestDiagStaticKeepsGeneratedApplicationWithMissingProject(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), projectGateAppSet)
	writeTestFile(t, filepath.Join(root, "projects", "team-a.yaml"), projectGateTeamA)
	writeTestFile(t, filepath.Join(root, "apps", "plain.yaml"), projectGatePlainApp)

	names, _ := listApplicationNames(t, root)
	if want := []string{"default-app", "missing-app", "plain-missing", "team-a-app"}; !slices.Equal(names, want) {
		t.Fatalf("applications = %#v, want %#v", names, want)
	}
	result, err := Orchestrator{}.DiagStatic(context.Background(), DiagRequest{Path: root}, nil)
	if err != nil {
		t.Fatalf("DiagStatic() error = %v", err)
	}
	messages := map[string]string{}
	for _, diag := range result.Diagnostics {
		if diagnostic.StableCode(diag) != diagnostic.CodeProjectMissing {
			continue
		}
		for _, name := range []string{"missing-app", "plain-missing"} {
			if strings.Contains(diag.Message, "Application argocd/"+name+" ") {
				messages[name] = diag.Message
			}
		}
	}
	const generated = `; ApplicationSet argocd/projects generates it, and the Argo CD ApplicationSet controller does not create or update it unless that AppProject exists in the cluster`
	if got := messages["missing-app"]; !strings.Contains(got, `references missing AppProject "team-missing"`+generated) {
		t.Fatalf("missing-app project.missing message = %q, want the ApplicationSet controller note", got)
	}
	if got := messages["plain-missing"]; got == "" || strings.Contains(got, "ApplicationSet") {
		t.Fatalf("plain-missing project.missing message = %q, want the plain message", got)
	}
}

// TestArgoCDParityFixtureMissingProject pins the drydock side of
// run_missing_project_smoke, a recorded divergence: Argo CD does not create
// parity-missing-project-dropped, but drydock keeps it and warns
// project.missing, because it does not know the AppProjects of the cluster.
func TestArgoCDParityFixtureMissingProject(t *testing.T) {
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "argocd-parity"))
	if err != nil {
		t.Fatal(err)
	}
	request := DiagRequest{
		Path:     filepath.Join(fixtureRoot, "missing-project"),
		RepoMaps: []sourcepkg.RepoMap{{URL: "git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git", Path: filepath.Join(fixtureRoot, "repo")}},
	}
	result, err := Orchestrator{}.DiagStatic(context.Background(), request, nil)
	if err != nil {
		t.Fatalf("DiagStatic() error = %v", err)
	}
	names := make([]string, 0, len(result.Applications))
	for _, application := range result.Applications {
		names = append(names, application.Name)
	}
	slices.Sort(names)
	if want := []string{"parity-missing-project-dropped", "parity-missing-project-kept"}; !slices.Equal(names, want) {
		t.Fatalf("applications = %#v, want %#v", names, want)
	}
	for _, diag := range result.Diagnostics {
		if diagnostic.StableCode(diag) == diagnostic.CodeProjectMissing && strings.Contains(diag.Message, `Application argocd/parity-missing-project-dropped references missing AppProject "parity-missing-project"; ApplicationSet argocd/parity-validation-missing-project generates it`) {
			return
		}
	}
	t.Fatalf("diagnostics = %#v, want project.missing for parity-missing-project-dropped", result.Diagnostics)
}

// A build validates the AppProject twice: before rendering and in the
// rendered resource policy check. Both give the same project.missing text
// for a generated Application, so exactly one warning remains.
func TestBuildReportsOneMissingProjectWarningForGeneratedApplication(t *testing.T) {
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "argocd-parity"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:     filepath.Join(fixtureRoot, "missing-project"),
		RepoMaps: []sourcepkg.RepoMap{{URL: "git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git", Path: filepath.Join(fixtureRoot, "repo")}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	var warnings []string
	for _, diag := range result.Diagnostics {
		if diagnostic.StableCode(diag) == diagnostic.CodeProjectMissing && strings.Contains(diag.Message, "parity-missing-project-dropped") {
			warnings = append(warnings, diag.Message)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ApplicationSet argocd/parity-validation-missing-project generates it") {
		t.Fatalf("project.missing warnings for parity-missing-project-dropped = %#v, want exactly one with the ApplicationSet note", warnings)
	}
}
