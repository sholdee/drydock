package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func symlinkedRootApplication(name, repoURL, path string) string {
	return "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: " + name + "\n  namespace: argocd\nspec:\n  source:\n    repoURL: " + repoURL + "\n    targetRevision: main\n    path: " + path + "\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: default\n"
}

// TestOrchestratorBuildsFromASymlinkedRoot pins that a --path root that is
// itself a symlink is read through: discovery finds its Applications and
// they render, with paths relative to the root as for a plain root.
func TestOrchestratorBuildsFromASymlinkedRoot(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	writeTestFile(t, filepath.Join(real, "apps", "demo.yaml"), symlinkedRootApplication("demo", "https://git.example.test/repo.git", "app"))
	writeTestFile(t, filepath.Join(real, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	link := filepath.Join(parent, "link")
	if err := os.Symlink("real", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{Path: link, Name: "argocd/demo"})
	if err != nil {
		t.Fatalf("BuildApp() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if len(result.Manifests) != 1 || result.Manifests[0].Object.GetName() != "demo" {
		t.Fatalf("manifests = %#v, want the demo ConfigMap", result.Manifests)
	}
	if path := result.Manifests[0].Path; strings.HasPrefix(path, "/") || strings.Contains(path, "real") {
		t.Fatalf("manifest path = %q, want it relative to the root", path)
	}
}

// TestOrchestratorRendersARepoMapRootThatIsASymlink pins that a --repo-map
// root that is itself a symlink is read through, also for a directory
// source at path "." whose walk starts at the root.
func TestOrchestratorRendersARepoMapRootThatIsASymlink(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	writeTestFile(t, filepath.Join(root, "apps", "mapped.yaml"), symlinkedRootApplication("mapped", "https://git.example.test/mapped.git", "."))
	writeTestFile(t, filepath.Join(parent, "mapped-real", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: mapped\n")
	link := filepath.Join(parent, "mapped-link")
	if err := os.Symlink("mapped-real", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Path:     root,
		RepoMaps: []sourcepkg.RepoMap{{URL: "https://git.example.test/mapped.git", Path: link}},
		Name:     "argocd/mapped",
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if len(result.Manifests) != 1 || result.Manifests[0].Object.GetName() != "mapped" {
		t.Fatalf("manifests = %#v, want the mapped ConfigMap", result.Manifests)
	}
}

// TestOrchestratorDiffsSymlinkedRoots pins that diff roots that are
// themselves symlinks are read through on both sides.
func TestOrchestratorDiffsSymlinkedRoots(t *testing.T) {
	parent := t.TempDir()
	for side, value := range map[string]string{"left": "one", "right": "two"} {
		real := filepath.Join(parent, side+"-real")
		writeTestFile(t, filepath.Join(real, "apps", "demo.yaml"), symlinkedRootApplication("demo", "https://git.example.test/repo.git", "app"))
		writeTestFile(t, filepath.Join(real, "app", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  value: "+value+"\n")
		if err := os.Symlink(side+"-real", filepath.Join(parent, side)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		LeftPath:  filepath.Join(parent, "left"),
		RightPath: filepath.Join(parent, "right"),
		Unified:   3,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if got := diffedApplications(result); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("diffed Applications = %v, want demo", got)
	}
}

// TestOrchestratorDiscoversChildrenInASymlinkedRepoMapRoot pins that
// discovery reads a --repo-map root that is itself a symlink: a parent
// Application whose source is that repository at path "." declares a child
// Application, and the child is found.
func TestOrchestratorDiscoversChildrenInASymlinkedRepoMapRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	writeTestFile(t, filepath.Join(root, "apps", "parent.yaml"), symlinkedRootApplication("parent", "https://git.example.test/children.git", "."))
	writeTestFile(t, filepath.Join(parent, "children-real", "child.yaml"), symlinkedRootApplication("child", "https://git.example.test/repo.git", "app"))
	link := filepath.Join(parent, "children-link")
	if err := os.Symlink("children-real", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path:     root,
		RepoMaps: []sourcepkg.RepoMap{{URL: "https://git.example.test/children.git", Path: link}},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	names := map[string]bool{}
	for _, application := range result.Applications {
		names[application.Name] = true
	}
	if !names["parent"] || !names["child"] {
		t.Fatalf("Applications = %v, want parent and child", names)
	}
}
