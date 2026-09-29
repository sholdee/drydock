package app

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

const (
	tenantRepoURL = "https://github.com/example/tenant"
	otherRepoURL  = "https://github.com/example/repo"
)

// appProjectYAML is an AppProject in argocd that permits sourceRepo and
// every destination.
func appProjectYAML(name, sourceRepo string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: ` + name + `
  namespace: argocd
spec:
  sourceRepos:
    - "` + sourceRepo + `"
  destinations:
    - server: "*"
      namespace: "*"
`
}

// writeProjectMemberApps writes argocd, a self-managed Application in the
// default project whose spec.source.path projects holds projects (file name
// to AppProject YAML) and a ConfigMap, tenant-app in AppProject tenant
// sourced from tenantRepoURL, and other in otherProject sourced from
// otherRepoURL. No render reads an AppProject: only project validation does.
func writeProjectMemberApps(t *testing.T, root string, projects map[string]string, otherProject string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "argocd.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argocd
  namespace: argocd
spec:
  source:
    repoURL: `+otherRepoURL+`
    targetRevision: main
    path: projects
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "projects", "owners.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: project-owners
data:
  owner: platform
`)
	for name, project := range projects {
		writeTestFile(t, filepath.Join(root, "projects", name+".yaml"), project)
	}
	writeBuildApplicationWithProject(t, root, "tenant-app", "tenant-app", "tenant", tenantRepoURL, "tenant")
	writeBuildApplicationWithProject(t, root, "other", "other", otherProject, otherRepoURL, "other")
}

// writeTenantProjectApps is writeProjectMemberApps with AppProject tenant
// permitting only tenantSourceRepo.
func writeTenantProjectApps(t *testing.T, root, tenantSourceRepo string) {
	t.Helper()
	writeProjectMemberApps(t, root, map[string]string{"tenant": appProjectYAML("tenant", tenantSourceRepo)}, argoappv1.DefaultAppProjectName)
}

// writeMaybeTenantProjectApps is writeProjectMemberApps whose AppProject
// tenant exists only when present is "present": with it absent on one side
// and present on the other, changedProjectNames' all switch fires (the first
// AppProject declared, or the last removed).
func writeMaybeTenantProjectApps(t *testing.T, root, present string) {
	t.Helper()
	projects := map[string]string{}
	if present == "present" {
		projects["tenant"] = appProjectYAML("tenant", tenantRepoURL)
	}
	writeProjectMemberApps(t, root, projects, argoappv1.DefaultAppProjectName)
}

func sourceRepositoryDenied(app, repoURL, project string) projectDiagnosticKey {
	return projectDiagnosticKey{
		Code:       diagnostic.CodeProjectSourceRepositoryDenied,
		Message:    fmt.Sprintf("Application argocd/%s source repository %q is not permitted by AppProject %q", app, repoURL, project),
		Provenance: diagnostic.Provenance{Path: "argocd/" + app},
	}
}

func projectMissing(app, project string) projectDiagnosticKey {
	return projectDiagnosticKey{
		Code:       diagnostic.CodeProjectMissing,
		Message:    fmt.Sprintf("Application argocd/%s references missing AppProject %q", app, project),
		Provenance: diagnostic.Provenance{Path: "argocd/" + app},
	}
}

// An AppProject change reaches every Application in that project through
// project validation, whichever Application owns the AppProject file, so
// changed-only selects them all and reports the project diagnostics the full
// diff reports; --strict fails both. Declaring the first AppProject, or
// removing the last, changes whether every Application falls back to the
// implicit default project, so changed-only renders all of them.
func TestOrchestratorDiffAppsChangedOnlySelectsAppProjectMembers(t *testing.T) {
	tenant := appProjectYAML("tenant", tenantRepoURL)
	permissiveDefault := appProjectYAML(argoappv1.DefaultAppProjectName, "*")
	for _, tc := range []struct {
		name         string
		left, right  map[string]string
		otherProject string
		// want is the project diagnostics both diffs report.
		want []projectDiagnosticKey
		// wantRendered are the source paths changed-only renders.
		wantRendered []string
		// wantChangedOnlyCode is "" or the single changed-only diagnostic
		// code changed-only reports alongside want (changedOnlyProjectsCode
		// for the first-declared/last-removed render-all).
		wantChangedOnlyCode string
	}{
		{
			name:         "tightened sourceRepos",
			left:         map[string]string{"tenant": tenant},
			right:        map[string]string{"tenant": appProjectYAML("tenant", "https://github.com/example/platform")},
			otherProject: argoappv1.DefaultAppProjectName,
			want:         []projectDiagnosticKey{sourceRepositoryDenied("tenant-app", tenantRepoURL, "tenant")},
			wantRendered: []string{"manifests/tenant-app", "projects"},
		},
		{
			name:         "default project added",
			left:         map[string]string{"tenant": tenant},
			right:        map[string]string{"tenant": tenant, "default": appProjectYAML(argoappv1.DefaultAppProjectName, tenantRepoURL)},
			otherProject: argoappv1.DefaultAppProjectName,
			want: []projectDiagnosticKey{
				sourceRepositoryDenied("argocd", otherRepoURL, argoappv1.DefaultAppProjectName),
				sourceRepositoryDenied("other", otherRepoURL, argoappv1.DefaultAppProjectName),
			},
			wantRendered: []string{"manifests/other", "projects"},
		},
		{
			name:         "project removed",
			left:         map[string]string{"tenant": tenant, "default": permissiveDefault},
			right:        map[string]string{"default": permissiveDefault},
			otherProject: argoappv1.DefaultAppProjectName,
			want:         []projectDiagnosticKey{projectMissing("tenant-app", "tenant")},
			wantRendered: []string{"manifests/tenant-app", "projects"},
		},
		{
			// With no AppProject every Application validates against the
			// implicit default; with one, other's project is missing.
			name:                "first AppProject declared",
			right:               map[string]string{"tenant": tenant},
			otherProject:        "platform",
			want:                []projectDiagnosticKey{projectMissing("other", "platform")},
			wantRendered:        []string{"manifests/other", "manifests/tenant-app", "projects"},
			wantChangedOnlyCode: changedOnlyProjectsCode,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			writeProjectMemberApps(t, left, tc.left, tc.otherProject)
			writeProjectMemberApps(t, right, tc.right, tc.otherProject)
			request := DiffRequest{LeftPath: left, RightPath: right, Unified: 3}

			full, err := Orchestrator{}.DiffApps(context.Background(), request)
			if err != nil {
				t.Fatalf("full DiffApps() error = %v, diagnostics = %#v", err, full.Diagnostics)
			}
			if got := projectDiagnosticKeys(full.Diagnostics); !slices.Equal(got, tc.want) {
				t.Fatalf("full project diagnostics = %#v, want %#v", got, tc.want)
			}

			var mu sync.Mutex
			rendered := map[string]struct{}{}
			o := Orchestrator{}
			o.renderObserver = func(source render.ResolvedSource) {
				mu.Lock()
				defer mu.Unlock()
				rendered[source.Path] = struct{}{}
			}
			request.ChangedOnly = true
			changedOnly, err := o.DiffApps(context.Background(), request)
			if err != nil {
				t.Fatalf("changed-only DiffApps() error = %v, diagnostics = %#v", err, changedOnly.Diagnostics)
			}
			if got := projectDiagnosticKeys(changedOnly.Diagnostics); !slices.Equal(got, tc.want) {
				t.Fatalf("changed-only project diagnostics = %#v, want %#v", got, tc.want)
			}
			if got := slices.Sorted(maps.Keys(rendered)); !slices.Equal(got, tc.wantRendered) {
				t.Fatalf("changed-only rendered %v, want %v", got, tc.wantRendered)
			}
			var gotChangedOnlyCodes []string
			for _, diag := range changedOnly.Diagnostics {
				if diag.Category == "changed-only" {
					gotChangedOnlyCodes = append(gotChangedOnlyCodes, diag.Code)
				}
			}
			var wantChangedOnlyCodes []string
			if tc.wantChangedOnlyCode != "" {
				wantChangedOnlyCodes = []string{tc.wantChangedOnlyCode}
			}
			if !slices.Equal(gotChangedOnlyCodes, wantChangedOnlyCodes) {
				t.Fatalf("changed-only diagnostics = %v, want %v: %#v", gotChangedOnlyCodes, wantChangedOnlyCodes, changedOnly.Diagnostics)
			}
			assertDiffResultsEqual(t, full, changedOnly)

			request.Strict = true
			for _, changedOnly := range []bool{false, true} {
				request.ChangedOnly = changedOnly
				result, err := Orchestrator{}.DiffApps(context.Background(), request)
				if err == nil {
					t.Fatalf("strict DiffApps(ChangedOnly=%t) error = nil, want the project diagnostics to fail it: %#v", changedOnly, result.Diagnostics)
				}
			}
		})
	}
}

// Declaring the first AppProjects renders every Application with
// diff.changed-only-projects. That render-all is complete, so it fails neither
// --strict nor --strict-changed-only when no project rule is violated.
func TestOrchestratorDiffAppsChangedOnlyFirstAppProjectsRenderAllIsStrictExempt(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeProjectMemberApps(t, left, nil, argoappv1.DefaultAppProjectName)
	writeProjectMemberApps(t, right, map[string]string{
		"tenant":                        appProjectYAML("tenant", tenantRepoURL),
		argoappv1.DefaultAppProjectName: appProjectYAML(argoappv1.DefaultAppProjectName, "*"),
	}, argoappv1.DefaultAppProjectName)

	for _, tc := range []struct {
		name              string
		strict            bool
		strictChangedOnly bool
	}{
		{name: "strict", strict: true},
		{name: "strict changed-only", strictChangedOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
				LeftPath:          left,
				RightPath:         right,
				ChangedOnly:       true,
				Strict:            tc.strict,
				StrictChangedOnly: tc.strictChangedOnly,
				Unified:           3,
			})
			if err != nil {
				t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			var codes []string
			for _, diag := range result.Diagnostics {
				if diag.Category == "changed-only" {
					codes = append(codes, diag.Code)
				}
			}
			if !slices.Equal(codes, []string{changedOnlyProjectsCode}) {
				t.Fatalf("changed-only diagnostics = %v, want [%s]: %#v", codes, changedOnlyProjectsCode, result.Diagnostics)
			}
		})
	}
}

// Under --project-diagnostics off no project diagnostic is reported, so an
// AppProject change selects only the Application that owns its file.
func TestOrchestratorDiffAppsChangedOnlyProjectDiagnosticsOffSkipsMembers(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	writeTenantProjectApps(t, left, tenantRepoURL)
	writeTenantProjectApps(t, right, "https://github.com/example/platform")

	var mu sync.Mutex
	rendered := map[string]struct{}{}
	o := Orchestrator{}
	o.renderObserver = func(source render.ResolvedSource) {
		mu.Lock()
		defer mu.Unlock()
		rendered[source.Path] = struct{}{}
	}
	result, err := o.DiffApps(context.Background(), DiffRequest{
		LeftPath:               left,
		RightPath:              right,
		Unified:                3,
		ChangedOnly:            true,
		Strict:                 true,
		ProjectDiagnosticsMode: diagnostic.ProjectDiagnosticsModeOff,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if got, want := slices.Sorted(maps.Keys(rendered)), []string{"projects"}; !slices.Equal(got, want) {
		t.Fatalf("changed-only rendered %v, want %v", got, want)
	}
}

func TestChangedProjectNames(t *testing.T) {
	project := func(namespace, name, sourceRepo string) argoappv1.AppProject {
		return argoappv1.AppProject{Namespace: namespace, Name: name, Spec: argoappv1.AppProjectSpec{SourceRepos: []string{sourceRepo}}}
	}
	a := project("argocd", "a", "https://github.com/example/a")
	b := project("argocd", "b", "https://github.com/example/b")
	labeled := *a.DeepCopy()
	labeled.Labels = map[string]string{"team": "a"}
	for _, tc := range []struct {
		name        string
		left, right []argoappv1.AppProject
		want        []string
		wantAll     bool
	}{
		{name: "no AppProjects"},
		{name: "unchanged", left: []argoappv1.AppProject{a, b}, right: []argoappv1.AppProject{a, b}},
		{name: "reordered", left: []argoappv1.AppProject{a, b}, right: []argoappv1.AppProject{b, a}},
		{name: "labels only", left: []argoappv1.AppProject{a, b}, right: []argoappv1.AppProject{labeled, b}},
		{name: "spec changed", left: []argoappv1.AppProject{a, b}, right: []argoappv1.AppProject{project("argocd", "a", "*"), b}, want: []string{"a"}},
		{name: "namespace changed", left: []argoappv1.AppProject{a, b}, right: []argoappv1.AppProject{project("tenants", "a", "https://github.com/example/a"), b}, want: []string{"a"}},
		{name: "added", left: []argoappv1.AppProject{a}, right: []argoappv1.AppProject{a, b}, want: []string{"b"}},
		{name: "removed", left: []argoappv1.AppProject{a, b}, right: []argoappv1.AppProject{b}, want: []string{"a"}},
		{
			// Validation looks AppProjects up by name, the last one winning.
			name:  "shadowed duplicate changed",
			left:  []argoappv1.AppProject{project("tenants", "a", "*"), a},
			right: []argoappv1.AppProject{project("tenants", "a", "https://github.com/example/x"), a},
		},
		{
			name:  "duplicates reordered",
			left:  []argoappv1.AppProject{project("tenants", "a", "*"), a},
			right: []argoappv1.AppProject{a, project("tenants", "a", "*")},
			want:  []string{"a"},
		},
		{
			// Validation skips an AppProject without a name.
			name:  "unnamed changed",
			left:  []argoappv1.AppProject{project("argocd", "", "*"), b},
			right: []argoappv1.AppProject{project("argocd", "", "https://github.com/example/x"), b},
		},
		{name: "first declared", right: []argoappv1.AppProject{a}, wantAll: true},
		{name: "last removed", left: []argoappv1.AppProject{a}, wantAll: true},
		// Any AppProject, named or not, turns the implicit default off.
		{name: "first unnamed declared", right: []argoappv1.AppProject{project("argocd", "", "*")}, wantAll: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names, all := changedProjectNames(tc.left, tc.right)
			if all != tc.wantAll {
				t.Fatalf("changedProjectNames() all = %t, want %t", all, tc.wantAll)
			}
			if got := slices.Sorted(maps.Keys(names)); !slices.Equal(got, tc.want) {
				t.Fatalf("changedProjectNames() names = %v, want %v", got, tc.want)
			}
		})
	}
}
