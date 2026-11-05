package render

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/gitref"
)

const ignoreMissingComponentKustomization = `apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - cm.yaml
`

// writeIgnoreMissingComponentsRepo writes apps/demo (a kustomization with one
// ConfigMap) and returns the repository root. components names valid
// component directories under apps/components; each renders a ConfigMap
// named component-<name>.
func writeIgnoreMissingComponentsRepo(t *testing.T, root string, components ...string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "resources:\n  - cm.yaml\n")
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), directoryConfigMap("demo"))
	for _, name := range components {
		writeIgnoreMissingComponent(t, filepath.Join(root, "apps", "components", name), name)
	}
}

func writeIgnoreMissingComponent(t *testing.T, dir, name string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "kustomization.yaml"), ignoreMissingComponentKustomization)
	writeFile(t, filepath.Join(dir, "cm.yaml"), directoryConfigMap("component-"+name))
}

func renderIgnoreMissingComponents(t *testing.T, root string, ignoreMissing bool, components ...string) ([]string, error) {
	t.Helper()
	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		OfflineRemoteResources: true,
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			Components:              components,
			IgnoreMissingComponents: ignoreMissing,
		},
	})
	if err != nil {
		return nil, err
	}
	return directoryManifestNames(manifests), nil
}

// TestKustomizeRendererIgnoreMissingComponentsRequiresKustomizationFile pins
// Argo CD v3.6.1 (util/kustomize/kustomize.go, a16170b2e): under
// ignoreMissingComponents a component directory is kept only when it holds a
// regular kustomization.yaml, kustomization.yml or Kustomization.
func TestKustomizeRendererIgnoreMissingComponentsRequiresKustomizationFile(t *testing.T) {
	root := t.TempDir()
	writeIgnoreMissingComponentsRepo(t, root, "valid")
	writeFile(t, filepath.Join(root, "apps", "components", "plain", "cm.yaml"), directoryConfigMap("component-plain"))
	writeFile(t, filepath.Join(root, "apps", "components", "yml", "kustomization.yml"), ignoreMissingComponentKustomization)
	writeFile(t, filepath.Join(root, "apps", "components", "yml", "cm.yaml"), directoryConfigMap("component-yml"))
	writeFile(t, filepath.Join(root, "apps", "components", "bare", "Kustomization"), ignoreMissingComponentKustomization)
	writeFile(t, filepath.Join(root, "apps", "components", "bare", "cm.yaml"), directoryConfigMap("component-bare"))
	// Copied into the render workspace with the source directory, so the
	// render-time check sees it too.
	writeFile(t, filepath.Join(root, "apps", "demo", "local-plain", "cm.yaml"), directoryConfigMap("component-local-plain"))
	// A directory named like a kustomization file is not a regular file.
	if err := os.MkdirAll(filepath.Join(root, "apps", "components", "dirfile", "kustomization.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}

	names, err := renderIgnoreMissingComponents(t, root, true,
		"../components/plain", "./local-plain", "../components/valid", "../components/missing", "../components/dirfile", "../components/yml", "../components/bare")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if want := []string{"component-bare", "component-valid", "component-yml", "demo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v", names, want)
	}

	if _, err := renderIgnoreMissingComponents(t, root, false, "../components/plain"); err == nil {
		t.Fatal("Render() without ignoreMissingComponents error = nil, want missing kustomization error")
	}
}

// TestKustomizeRendererIgnoreMissingComponentsKeepsEveryValidComponent pins a
// recorded divergence from strict Argo CD v3.6.1: util/kustomize/kustomize.go
// (a16170b2e) leaves its component loop with `break component` after the
// first valid entry, so the repo-server adds only component-a here. drydock
// keeps every valid component, the documented ignoreMissingComponents intent.
func TestKustomizeRendererIgnoreMissingComponentsKeepsEveryValidComponent(t *testing.T) {
	root := t.TempDir()
	writeIgnoreMissingComponentsRepo(t, root, "a", "b")

	names, err := renderIgnoreMissingComponents(t, root, true, "../components/missing", "../components/a", "../components/b")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if want := []string{"component-a", "component-b", "demo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want every valid component %#v", names, want)
	}
}

// TestKustomizeRendererIgnoreMissingComponentsSkipsRemoteEscapingAndAbsolute
// pins the repo-server's lookup: each ref is joined with the source path and
// looked up inside an os.Root on the repository root, so a remote ref and a
// ref escaping the repository are never found, and an absolute ref is joined
// under the source path.
func TestKustomizeRendererIgnoreMissingComponentsSkipsRemoteEscapingAndAbsolute(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	writeIgnoreMissingComponentsRepo(t, root, "valid")
	// A valid component outside the repository must stay unread.
	writeIgnoreMissingComponent(t, filepath.Join(parent, "outside"), "outside")

	refs := []string{
		"https://github.com/example/components//remote?ref=v1.0.0",
		"github.com/example/components/remote?ref=v1.0.0",
		"../../../outside",
		"/components/absolute",
		"../components/valid",
	}
	names, err := renderIgnoreMissingComponents(t, root, true, refs...)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if want := []string{"component-valid", "demo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v", names, want)
	}

	// Without ignoreMissingComponents, an escaping ref still fails.
	if _, err := renderIgnoreMissingComponents(t, root, false, "../../../outside"); err == nil {
		t.Fatal("Render() without ignoreMissingComponents error = nil, want escape error")
	}

	// An absolute ref that names a component under the source path is found,
	// as upstream joins it there; drydock then refuses the absolute path.
	writeIgnoreMissingComponent(t, filepath.Join(root, "apps", "demo", "components", "absolute"), "absolute")
	_, err = renderIgnoreMissingComponents(t, root, true, "/components/absolute")
	if err == nil || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("Render() error = %v, want absolute component rejection", err)
	}
}

func TestKustomizeRendererIgnoreMissingComponentsSymlinkedKustomization(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	writeIgnoreMissingComponentsRepo(t, root, "valid")
	writeFile(t, filepath.Join(parent, "outside-kustomization.yaml"), ignoreMissingComponentKustomization)
	writeFile(t, filepath.Join(root, "apps", "components", "escaping", "cm.yaml"), directoryConfigMap("component-escaping"))
	symlink(t, filepath.Join(parent, "outside-kustomization.yaml"), filepath.Join(root, "apps", "components", "escaping", "kustomization.yaml"))

	// os.Root does not follow a symlink out of the repository: not found.
	names, err := renderIgnoreMissingComponents(t, root, true, "../components/escaping", "../components/valid")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if want := []string{"component-valid", "demo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want %#v", names, want)
	}

	// An in-repository symlink is followed, so the component is kept, and
	// drydock's graph walk then refuses the symlinked kustomization file.
	writeFile(t, filepath.Join(root, "shared", "component.yaml"), ignoreMissingComponentKustomization)
	writeFile(t, filepath.Join(root, "apps", "components", "linked", "cm.yaml"), directoryConfigMap("component-linked"))
	symlink(t, filepath.Join("..", "..", "..", "shared", "component.yaml"), filepath.Join(root, "apps", "components", "linked", "kustomization.yaml"))
	_, err = renderIgnoreMissingComponents(t, root, true, "../components/linked")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlinked kustomization rejection", err)
	}
}

func writeSkippedComponentDigestRepo(t *testing.T) (string, RenderOptions) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	writeIgnoreMissingComponentsRepo(t, root, "valid")
	writeFile(t, filepath.Join(root, "apps", "components", "plain", "cm.yaml"), directoryConfigMap("component-plain"))
	writeIgnoreMissingComponent(t, filepath.Join(parent, "outside"), "outside")
	return root, RenderOptions{Kustomize: &argoappv1.ApplicationSourceKustomize{
		Components: []string{
			"../components/plain",
			"../components/missing",
			"../components/valid",
			"https://github.com/example/components//remote?ref=v1.0.0",
			"../../../outside",
		},
		IgnoreMissingComponents: true,
	}}
}

// TestKustomizeSelectionPathsOwnSkippedComponentDirectories pins that
// changed-only selection still owns a skipped component's directory, so a
// kustomization file added there selects the Application.
func TestKustomizeSelectionPathsOwnSkippedComponentDirectories(t *testing.T) {
	root, opts := writeSkippedComponentDigestRepo(t)
	selection, err := KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), opts.Kustomize)
	if err != nil {
		t.Fatalf("KustomizeSelectionPaths() error = %v", err)
	}
	for _, want := range []string{"apps/components/plain", "apps/components/missing", "apps/components/valid"} {
		if i := sort.SearchStrings(selection, want); i >= len(selection) || selection[i] != want {
			t.Errorf("KustomizeSelectionPaths() = %v, missing %q", selection, want)
		}
	}
	for _, path := range selection {
		if strings.HasPrefix(path, "..") || strings.Contains(path, "remote") {
			t.Errorf("KustomizeSelectionPaths() owns skipped remote or out-of-repository path %q", path)
		}
	}
}

// TestKustomizeInputDigestPathsKeepSkippedComponentDirectories pins that a
// component skipped under ignoreMissingComponents stays a render input: its
// in-repository directory is an optional digest path, so a kustomization file
// appearing there rotates the persistent render cache key.
func TestKustomizeInputDigestPathsKeepSkippedComponentDirectories(t *testing.T) {
	root, opts := writeSkippedComponentDigestRepo(t)
	source := ResolvedSource{RepoRoot: root, Path: filepath.Join("apps", "demo")}
	paths, err := KustomizeInputDigestPaths(context.Background(), source, opts)
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() error = %v", err)
	}
	byPath := map[string]gitref.PathDigestPath{}
	for _, item := range paths {
		byPath[item.Path] = item
	}
	for _, want := range []gitref.PathDigestPath{
		{Path: "apps/components/plain", Optional: true},
		{Path: "apps/components/missing", Optional: true},
		{Path: "apps/components/valid", Optional: false},
		{Path: "apps/components/valid/kustomization.yaml", Optional: false},
	} {
		if got, ok := byPath[want.Path]; !ok || got != want {
			t.Errorf("digest path %q = %#v (present %t), want %#v; all = %#v", want.Path, got, ok, want, paths)
		}
	}
	for path := range byPath {
		if strings.HasPrefix(path, "..") || strings.Contains(path, "remote") {
			t.Errorf("digest path %q names a skipped remote or out-of-repository component", path)
		}
	}

	// Adding a kustomization file to the skipped directory makes it a
	// component, and the digest now walks its graph.
	writeFile(t, filepath.Join(root, "apps", "components", "plain", "kustomization.yaml"), ignoreMissingComponentKustomization)
	paths, err = KustomizeInputDigestPaths(context.Background(), source, opts)
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() after add error = %v", err)
	}
	found := false
	for _, item := range paths {
		if item.Path == "apps/components/plain/kustomization.yaml" && !item.Optional {
			found = true
		}
	}
	if !found {
		t.Fatalf("digest paths after add = %#v, want apps/components/plain/kustomization.yaml", paths)
	}
}
