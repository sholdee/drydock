package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// writeGitGeneratorAppSet writes an ApplicationSet with a list generator
// and a Git directories generator over apps/*, whose template names
// project.
func writeGitGeneratorAppSet(t *testing.T, root, project string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "appsets", "git.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: git-projects
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - path: {basename: from-list, path: apps/alpha}
    - git:
        repoURL: https://example.com/repo.git
        revision: HEAD
        directories:
          - path: apps/*
  template:
    metadata:
      name: 'git-{{.path.basename}}'
      namespace: argocd
    spec:
      project: `+project+`
      source:
        repoURL: https://example.com/repo.git
        path: '{{.path.path}}'
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: default
`)
	writeTestFile(t, filepath.Join(root, "apps", "alpha", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: alpha\n")
}

func writeAppProject(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "projects", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: `+name+`
  namespace: argocd
spec:
  sourceRepos: ["*"]
  destinations: [{server: "*", namespace: "*"}]
`)
}

func listGitGeneratorApplications(t *testing.T, root string) ([]string, []diagnostic.Diagnostic) {
	t.Helper()
	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path:        root,
		Parallelism: 1,
		RepoMaps:    []sourcepkg.RepoMap{{URL: "https://example.com/repo.git", Path: root}},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	names, _ := discoveryOutcome(result)
	return names, diagnostic.WithStableCodes(result.Diagnostics)
}

// TestGitGeneratorMissingAppProjectKeepsApplicationsWithWarning pins how
// drydock reports the AppProject lookup of Argo CD v3.6.1
// GitGenerator.resolveSourceIntegrity
// (applicationset/generators/git.go:93-111). Argo CD fails the whole
// ApplicationSet when the AppProject of its template does not exist in the
// cluster. drydock cannot know the AppProjects of the cluster offline, so it
// keeps the Applications, also the one of the list generator. Each one
// that names the missing AppProject gets exactly one project.missing
// warning in a build, with the Git generator note. When the Applications
// name another AppProject, through a generator template, drydock warns once
// for the ApplicationSet instead. "default" always exists, without any
// declared AppProject drydock cannot tell, and with --project-diagnostics=off
// the warnings go away.
//
//nolint:gocyclo // One table drives every project case.
func TestGitGeneratorMissingAppProjectKeepsApplicationsWithWarning(t *testing.T) {
	for _, tc := range []struct {
		name          string
		project       string
		appProject    string
		projects      []string
		mode          diagnostic.ProjectDiagnosticsMode
		appWarnings   int
		appSetWarning bool
	}{
		{name: "missing", project: "missing", projects: []string{"team"}, appWarnings: 1},
		{name: "missing with project diagnostics off", project: "missing", projects: []string{"team"}, mode: diagnostic.ProjectDiagnosticsModeOff},
		{name: "Applications name another project", project: "missing", appProject: "team", projects: []string{"team"}, appSetWarning: true},
		{name: "declared", project: "team", projects: []string{"team"}},
		{name: "default", project: "default", projects: []string{"team"}},
		{name: "no declared projects", project: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeGitGeneratorAppSet(t, root, tc.project)
			if tc.appProject != "" {
				path := filepath.Join(root, "appsets", "git.yaml")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// A generator template sets the project of the Applications
				// of each generator.
				override := "        template: {metadata: {}, spec: {project: " + tc.appProject + "}}\n"
				patched := strings.Replace(string(data), "          - path: {basename: from-list, path: apps/alpha}\n", "          - path: {basename: from-list, path: apps/alpha}\n"+override, 1)
				patched = strings.Replace(patched, "          - path: apps/*\n", "          - path: apps/*\n"+override, 1)
				writeTestFile(t, path, patched)
			}
			for _, project := range tc.projects {
				writeAppProject(t, root, project)
			}
			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
				Path:                   root,
				Parallelism:            1,
				ProjectDiagnosticsMode: tc.mode,
				RepoMaps:               []sourcepkg.RepoMap{{URL: "https://example.com/repo.git", Path: root}},
			})
			if err != nil {
				t.Fatalf("Build() error = %v, diagnostics = %#v", err, result.Diagnostics)
			}
			names := make([]string, 0, len(result.Applications))
			for _, app := range result.Applications {
				names = append(names, app.Namespace+"/"+app.Name)
			}
			slices.Sort(names)
			if want := []string{"argocd/git-alpha", "argocd/git-from-list"}; !slices.Equal(names, want) {
				t.Fatalf("Applications = %v, want %v", names, want)
			}
			appWarnings := map[string]int{}
			appSetWarnings := 0
			for _, diag := range diagnostic.WithStableCodes(result.Diagnostics) {
				if diag.Code == diagnostic.CodeAppSetGeneratorFailed {
					t.Fatalf("diagnostics = %#v, want no appset.generator-failed", result.Diagnostics)
				}
				if diag.Code != diagnostic.CodeProjectMissing {
					continue
				}
				switch {
				case strings.Contains(diag.Message, "the Git generator of ApplicationSet argocd/git-projects references missing AppProject \"missing\""):
					appSetWarnings++
				case strings.Contains(diag.Message, "ApplicationSet argocd/git-projects generates it, and its Git generator looks that AppProject up first"):
					for _, name := range []string{"git-alpha", "git-from-list"} {
						if strings.Contains(diag.Message, "Application argocd/"+name+" ") {
							appWarnings[name]++
						}
					}
				}
			}
			for _, name := range []string{"git-alpha", "git-from-list"} {
				if appWarnings[name] != tc.appWarnings {
					t.Fatalf("project.missing warnings for %s = %d, want %d: %#v", name, appWarnings[name], tc.appWarnings, result.Diagnostics)
				}
			}
			if (appSetWarnings == 1) != tc.appSetWarning || appSetWarnings > 1 {
				t.Fatalf("ApplicationSet project.missing warnings = %d, want %t: %#v", appSetWarnings, tc.appSetWarning, result.Diagnostics)
			}
		})
	}
}

// writeGenerationSettingsCycle writes Git generator ApplicationSets and a
// list ApplicationSet whose Application renders the argocd-cmd-params-cm
// that turns new Git file globbing and hidden directories on. The first
// discovery run expands the Git generators with both off and finds the
// settings late, so a second run must use them.
func writeGenerationSettingsLate(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "appsets", "settings.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: settings
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: settings
  template:
    metadata:
      name: '{{.name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://example.com/repo.git
        path: charts/settings
        targetRevision: main
      destination:
        name: in-cluster
        namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "charts", "settings", "Chart.yaml"), "apiVersion: v2\nname: settings\nversion: 0.1.0\n")
	writeTestFile(t, filepath.Join(root, "charts", "settings", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
  namespace: argocd
data:
  applicationsetcontroller.enable.new.git.file.globbing: "true"
  reposerver.include.hidden.directories: "TRUE"
`)
	for _, generator := range []string{
		"files: [{path: 'configs/*/config.yaml'}]",
		"directories: [{path: 'dirs/*'}]",
	} {
		name := "files"
		if strings.HasPrefix(generator, "directories") {
			name = "dirs"
		}
		writeTestFile(t, filepath.Join(root, "appsets", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: `+name+`
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://example.com/repo.git
        revision: HEAD
        `+generator+`
  template:
    metadata:
      name: '`+name+`-{{.path.path | replace "/" "-" | replace "." "dot"}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://example.com/repo.git
        path: dirs/alpha
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: default
`)
	}
	for _, rel := range []string{"configs/a/config.yaml", "configs/a/b/config.yaml", "dirs/alpha/cm.yaml", "dirs/.hidden/cm.yaml"} {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(rel)), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")
	}
}

// TestGitGeneratorSettingsFromRenderedInstallRestartDiscovery pins that
// drydock reads applicationsetcontroller.enable.new.git.file.globbing and
// reposerver.include.hidden.directories like
// applicationsetcontroller.enable.progressive.syncs: from settings it finds
// late, a second discovery run uses them from the start.
func TestGitGeneratorSettingsFromRenderedInstallRestartDiscovery(t *testing.T) {
	root := t.TempDir()
	writeGenerationSettingsLate(t, root)
	runs := 0
	result, err := Orchestrator{discoveryRunObserver: func() { runs++ }}.ListApplications(context.Background(), BuildRequest{
		Path:        root,
		Parallelism: 1,
		RepoMaps:    []sourcepkg.RepoMap{{URL: "https://example.com/repo.git", Path: root}},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if runs != 2 {
		t.Fatalf("discovery runs = %d, want 2", runs)
	}
	names, _ := discoveryOutcome(result)
	// New globbing: "*" stops at "/", so configs/a/b is not matched. Hidden
	// directories: dirs/.hidden is listed.
	want := []string{"argocd/dirs-dirs-alpha", "argocd/dirs-dirs-dothidden", "argocd/files-configs-a", "argocd/settings"}
	if !slices.Equal(names, want) {
		t.Fatalf("Applications = %v, want %v", names, want)
	}
}

// TestGitGeneratorSkipsIgnoredUntrackedPaths pins the working-tree rule:
// the Git generator does not see a path that Git ignores and does not
// track, as no Argo CD checkout holds one. It keeps an untracked path that
// Git does not ignore, which a commit could hold.
func TestGitGeneratorSkipsIgnoredUntrackedPaths(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, ".gitignore"), "ignored/\n*.local.yaml\n")
	writeTestFile(t, filepath.Join(root, "appsets", "files.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: files
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://example.com/repo.git
        revision: HEAD
        files: [{path: 'configs'}]
  template:
    metadata:
      name: '{{.name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://example.com/repo.git
        path: configs
        targetRevision: HEAD
      destination:
        name: in-cluster
        namespace: default
`)
	writeTestFile(t, filepath.Join(root, "configs", "tracked.yaml"), "name: tracked\n")
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("fixture", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "configs", "untracked.yaml"), "name: untracked\n")
	writeTestFile(t, filepath.Join(root, "configs", "ignored", "config.yaml"), "name: ignored-dir\n")
	writeTestFile(t, filepath.Join(root, "configs", "dev.local.yaml"), "name: ignored-file\n")

	names, _ := listGitGeneratorApplications(t, root)
	if want := []string{"argocd/tracked", "argocd/untracked"}; !slices.Equal(names, want) {
		t.Fatalf("Applications = %v, want %v", names, want)
	}
}

// TestDotGitSourceRendersEmpty pins an Application whose source path is
// ".git", which a Git directories generator lists with
// reposerver.include.hidden.directories on. Argo CD renders it with no
// manifests. drydock renders it empty with a warning and never reads .git.
func TestDotGitSourceRendersEmpty(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".git", "config.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: from-git\n")
	writeTestFile(t, filepath.Join(root, "apps", "git.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: dot-git
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://example.com/repo.git
    targetRevision: HEAD
    path: .git
  destination: {server: https://kubernetes.default.svc, namespace: default}
`)
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:        root,
		Parallelism: 1,
		RepoMaps:    []sourcepkg.RepoMap{{URL: "https://example.com/repo.git", Path: root}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if !hasDiagnosticCode(diagnostic.WithStableCodes(result.Diagnostics), "render.git-directory-source") {
		t.Fatalf("diagnostics = %#v, want render.git-directory-source", result.Diagnostics)
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("manifests = %d, want none", len(result.Manifests))
	}
}

// TestGitFilesApplicationInputsFollowSymlinks pins that an Application
// generated from a param file read through a symlink owns the symlink's
// target too, so a changed-only diff renders it when only the target
// changes.
func TestGitFilesApplicationInputsFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "targets", "t.yaml"), "name: a\n")
	if err := os.MkdirAll(filepath.Join(root, "params"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../targets/t.yaml", filepath.Join(root, "params", "link.yaml")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "appsets", "files.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: files
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://example.com/repo.git
        revision: HEAD
        files: [{path: 'params/*.yaml'}]
  template:
    metadata:
      name: 'f-{{.name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://example.com/repo.git
        path: targets
        targetRevision: HEAD
      destination: {server: https://kubernetes.default.svc, namespace: default}
`)
	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path:        root,
		Parallelism: 1,
		RepoMaps:    []sourcepkg.RepoMap{{URL: "https://example.com/repo.git", Path: root}},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.ApplicationInputs) != 1 {
		t.Fatalf("ApplicationInputs = %#v, want one", result.ApplicationInputs)
	}
	for _, want := range []string{"params/link.yaml", "targets/t.yaml"} {
		if !slices.Contains(result.ApplicationInputs[0].Paths, want) {
			t.Fatalf("ApplicationInputs paths = %q, want %s", result.ApplicationInputs[0].Paths, want)
		}
	}
}

// TestDotGitSourceIsExact pins that only the exact path ".git" is the .git
// directory: on Linux, where the repo-server runs, ".GIT" is another path.
func TestDotGitSourceIsExact(t *testing.T) {
	for path, want := range map[string]bool{".git": true, "./.git/": true, ".GIT": false, ".Git": false, "apps/.git": false} {
		if _, got := dotGitSourceDiagnostic(render.ResolvedSource{Path: path, RepoURL: "https://example.com/repo.git"}); got != want {
			t.Fatalf("dotGitSourceDiagnostic(%q) = %t, want %t", path, got, want)
		}
	}
}
