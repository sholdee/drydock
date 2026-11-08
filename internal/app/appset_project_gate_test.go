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

func listApplicationNamesWith(t *testing.T, request BuildRequest) ([]string, []diagnostic.Diagnostic, error) {
	t.Helper()
	result, err := Orchestrator{}.ListApplications(context.Background(), request)
	names := make([]string, 0, len(result.Applications))
	for _, app := range result.Applications {
		names = append(names, app.Name)
	}
	slices.Sort(names)
	return names, result.Diagnostics, err
}

// With --project-diagnostics=off, the user says that the repository does
// not hold every AppProject, so drydock keeps a generated Application whose
// project it does not see, also under --strict.
func TestListApplicationsKeepsGeneratedApplicationWithProjectDiagnosticsOff(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), projectGateAppSet)
	writeTestFile(t, filepath.Join(root, "projects", "team-a.yaml"), projectGateTeamA)
	for _, strict := range []bool{false, true} {
		names, _, err := listApplicationNamesWith(t, BuildRequest{Path: root, Strict: strict, ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeOff})
		if err != nil {
			t.Fatalf("strict=%t: ListApplications() error = %v", strict, err)
		}
		if want := []string{"default-app", "missing-app", "team-a-app"}; !slices.Equal(names, want) {
			t.Fatalf("strict=%t: applications = %#v, want %#v", strict, names, want)
		}
	}
}

// A diff that deletes an AppProject removes the generated Applications of
// that project in a full and in a changed-only diff, and with project
// diagnostics off it removes them in neither.
func TestDiffAppsMissingProjectDropFollowsProjectDiagnosticsMode(t *testing.T) {
	writeSide := func(t *testing.T, root string, withTenant bool) {
		t.Helper()
		projects := map[string]string{"keep": appProjectYAML("keep", "*")}
		if withTenant {
			projects["tenant"] = appProjectYAML("tenant", "*")
		}
		writeProjectMemberApps(t, root, projects, "keep")
		writeTestFile(t, filepath.Join(root, "appsets", "gen.yaml"), strings.NewReplacer("name: projects", "name: gen", "project: \"{{.project}}\"", "project: tenant", "path: apps", "path: manifests/gen-app").Replace(projectGateAppSet))
		writeTestFile(t, filepath.Join(root, "manifests", "gen-app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: gen-cm\n")
	}
	for _, tc := range []struct {
		mode        diagnostic.ProjectDiagnosticsMode
		wantRemoved bool
	}{
		{mode: diagnostic.ProjectDiagnosticsModeActionable, wantRemoved: true},
		{mode: diagnostic.ProjectDiagnosticsModeOff, wantRemoved: false},
	} {
		for _, changedOnly := range []bool{false, true} {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writeSide(t, left, true)
			writeSide(t, right, false)
			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
				LeftPath: left, RightPath: right, Unified: 3, ChangedOnly: changedOnly,
				ProjectDiagnosticsMode: tc.mode,
			})
			if err != nil {
				t.Fatalf("mode=%s changedOnly=%t: DiffApps() error = %v", tc.mode, changedOnly, err)
			}
			removed := false
			for _, item := range result.Results {
				if item.Parent.Name == "team-a-app" || item.Parent.Name == "default-app" || item.Parent.Name == "missing-app" {
					removed = true
				}
			}
			if removed != tc.wantRemoved {
				t.Fatalf("mode=%s changedOnly=%t: generated Applications removed = %t, want %t (results %#v)", tc.mode, changedOnly, removed, tc.wantRemoved, result.Results)
			}
		}
	}
}

// drydock drops a generated Application only when fleet discovery rendered
// everything that can hold an AppProject. Otherwise the project can exist
// in output that drydock did not see, and Argo CD creates the Application.
func TestListApplicationsKeepsGeneratedApplicationWhenProjectsIncomplete(t *testing.T) {
	appset := strings.ReplaceAll(projectGateAppSet, "project: team-missing", "project: team-x")
	projectsApp := func(source string) string {
		return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: projects
  namespace: argocd
spec:
  project: default
  source:
` + source + `
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
`
	}
	for _, tc := range []struct {
		name     string
		request  func(root string) BuildRequest
		write    func(t *testing.T, root string)
		wantDrop bool
	}{
		{
			// Control: fleet discovery sees every AppProject.
			name:     "complete",
			request:  func(root string) BuildRequest { return BuildRequest{Path: root} },
			write:    func(t *testing.T, root string) {},
			wantDrop: true,
		},
		{
			// A source path that is not in the local tree, for example in
			// a repository without a --repo-map entry.
			name:    "source path outside the local tree",
			request: func(root string) BuildRequest { return BuildRequest{Path: root} },
			write: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "apps", "projects.yaml"), projectsApp("    repoURL: https://github.com/example/other\n    targetRevision: main\n    path: not-here"))
			},
		},
		{
			// A Kustomize render renames the scanned AppProject x to
			// team-x. The scan already read the directory, so fleet
			// discovery does not render it.
			name:    "kustomize namePrefix over a scanned AppProject",
			request: func(root string) BuildRequest { return BuildRequest{Path: root} },
			write: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "apps", "projects.yaml"), projectsApp("    repoURL: https://github.com/example/repo\n    targetRevision: main\n    path: kustomized"))
				writeTestFile(t, filepath.Join(root, "kustomized", "kustomization.yaml"), "namePrefix: team-\nresources:\n- x.yaml\n")
				writeTestFile(t, filepath.Join(root, "kustomized", "x.yaml"), appProjectYAML("x", "*"))
			},
		},
		{
			// Fleet discovery never renders a chart-only Helm source, such
			// as the argocd-apps chart.
			name:    "chart-only Helm source",
			request: func(root string) BuildRequest { return BuildRequest{Path: root} },
			write: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "apps", "projects.yaml"), projectsApp("    repoURL: https://charts.example.invalid\n    chart: argocd-apps\n    targetRevision: 1.0.0"))
			},
		},
		{
			name:    "static discovery",
			request: func(root string) BuildRequest { return BuildRequest{Path: root, DiscoveryMode: DiscoveryModeStatic} },
			write:   func(t *testing.T, root string) {},
		},
		{
			name: "discovery depth zero",
			request: func(root string) BuildRequest {
				return BuildRequest{Path: root, MaxDiscoveryDepth: 0, MaxDiscoveryDepthSet: true}
			},
			write: func(t *testing.T, root string) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "appset.yaml"), appset)
			writeTestFile(t, filepath.Join(root, "projects", "team-a.yaml"), projectGateTeamA)
			writeTestFile(t, filepath.Join(root, "apps", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: apps\n")
			tc.write(t, root)
			names, diags, err := listApplicationNamesWith(t, tc.request(root))
			if err != nil {
				t.Fatalf("ListApplications() error = %v", err)
			}
			if slices.Contains(names, "missing-app") == tc.wantDrop {
				t.Fatalf("applications = %#v, want missing-app dropped %t", names, tc.wantDrop)
			}
			warned := false
			for _, diag := range diags {
				if diagnostic.StableCode(diag) == "appset.application-validation-failed" {
					warned = true
				}
			}
			if warned != tc.wantDrop {
				t.Fatalf("diagnostics = %#v, want a validation warning %t", diags, tc.wantDrop)
			}
		})
	}
}

// The ApplicationSet controller looks the AppProject up in its own
// namespace (applicationset_controller.go:627). With a known controller
// namespace, an AppProject in another namespace does not count.
func TestListApplicationsCountsOnlyControllerNamespaceProjects(t *testing.T) {
	cmdParams := `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data: {}
`
	for _, tc := range []struct {
		namespace string
		want      []string
	}{
		{namespace: "argocd", want: []string{"default-app", "team-a-app"}},
		{namespace: "elsewhere", want: []string{"default-app"}},
	} {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "appset.yaml"), projectGateAppSet)
		writeTestFile(t, filepath.Join(root, "projects", "keep.yaml"), appProjectYAML("keep", "*"))
		writeTestFile(t, filepath.Join(root, "projects", "team-a.yaml"), strings.ReplaceAll(projectGateTeamA, "namespace: argocd", "namespace: "+tc.namespace))
		writeTestFile(t, filepath.Join(root, "settings", "argocd-cmd-params-cm.yaml"), cmdParams)
		// The generated Applications render apps/, so discovery sees all
		// of their output.
		writeTestFile(t, filepath.Join(root, "apps", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: apps\n")
		names, _, err := listApplicationNamesWith(t, BuildRequest{Path: root})
		if err != nil {
			t.Fatalf("namespace %s: ListApplications() error = %v", tc.namespace, err)
		}
		if !slices.Equal(names, tc.want) {
			t.Fatalf("namespace %s: applications = %#v, want %#v", tc.namespace, names, tc.want)
		}
	}
}

// TestArgoCDParityFixtureMissingProject pins the drydock side of
// run_missing_project_smoke: on the fixture directory alone, with the
// fixture repository mapped as the smoke maps it, discovery is complete, so
// drydock drops parity-missing-project-dropped.
func TestArgoCDParityFixtureMissingProject(t *testing.T) {
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "argocd-parity"))
	if err != nil {
		t.Fatal(err)
	}
	names, diags, err := listApplicationNamesWith(t, BuildRequest{
		Path:     filepath.Join(fixtureRoot, "missing-project"),
		RepoMaps: []sourcepkg.RepoMap{{URL: "git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git", Path: filepath.Join(fixtureRoot, "repo")}},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if want := []string{"parity-missing-project-kept"}; !slices.Equal(names, want) {
		t.Fatalf("applications = %#v, want %#v", names, want)
	}
	for _, diag := range diags {
		if diagnostic.StableCode(diag) == "appset.application-validation-failed" && strings.Contains(diag.Message, "application references project parity-missing-project which does not exist") && diag.Provenance.Path == "applicationset.yaml" {
			return
		}
	}
	t.Fatalf("diagnostics = %#v, want the missing project warning for applicationset.yaml", diags)
}
