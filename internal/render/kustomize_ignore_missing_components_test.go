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
	"github.com/sholdee/drydock/internal/diagnostic"
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
// Argo CD v3.6.1 (util/kustomize/kustomize.go:357-368): under
// ignoreMissingComponents a component directory is kept only when it holds a
// regular kustomization.yaml, kustomization.yml or Kustomization. Each case
// lists the invalid components first, because the repo-server stops at the
// first valid one.
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

	invalid := []string{"../components/plain", "./local-plain", "../components/missing", "../components/dirfile"}
	for _, name := range []string{"valid", "yml", "bare"} {
		names, err := renderIgnoreMissingComponents(t, root, true, append(append([]string(nil), invalid...), "../components/"+name)...)
		if err != nil {
			t.Fatalf("Render() with component %q error = %v", name, err)
		}
		if want := []string{"component-" + name, "demo"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("rendered names with component %q = %#v, want %#v", name, names, want)
		}
	}

	if _, err := renderIgnoreMissingComponents(t, root, false, "../components/plain"); err == nil {
		t.Fatal("Render() without ignoreMissingComponents error = nil, want missing kustomization error")
	}
}

// TestKustomizeRendererIgnoreMissingComponentsStopsAtFirstValidComponent pins
// Argo CD v3.6.1 (util/kustomize/kustomize.go:346-372): with
// ignoreMissingComponents the repo-server leaves its component loop with
// `break component` after the first valid component, so it adds only
// component-a here and drops component-b without a log line. drydock does the
// same and warns about component-b. A later component without a
// kustomization file adds nothing, so the warning does not name it. Without
// ignoreMissingComponents every component is added.
func TestKustomizeRendererIgnoreMissingComponentsStopsAtFirstValidComponent(t *testing.T) {
	root := t.TempDir()
	writeIgnoreMissingComponentsRepo(t, root, "a", "b", "c")
	writeFile(t, filepath.Join(root, "apps", "components", "plain", "cm.yaml"), directoryConfigMap("component-plain"))

	names, dropped := renderIgnoreMissingComponentsDropped(t, root,
		"../components/missing", "../components/a", "../components/plain", "../components/b", "../components/c")
	if want := []string{"component-a", "demo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want only the first valid component %#v", names, want)
	}
	if len(dropped) != 1 || dropped[0].Severity != diagnostic.SeverityWarning {
		t.Fatalf("%s diagnostics = %#v, want one warning", KustomizeIgnoreMissingComponentsDroppedCode, dropped)
	}
	message := dropped[0].Message
	for _, want := range []string{`"../components/a"`, `"../components/b"`, `"../components/c"`} {
		if !strings.Contains(message, want) {
			t.Errorf("warning message %q does not name %s", message, want)
		}
	}
	for _, unwanted := range []string{"missing", "plain"} {
		if strings.Contains(message, unwanted) {
			t.Errorf("warning message %q names the invalid component %q", message, unwanted)
		}
	}

	// One valid component: nothing is dropped, so nothing warns.
	if _, dropped := renderIgnoreMissingComponentsDropped(t, root, "../components/missing", "../components/a", "../components/plain"); len(dropped) != 0 {
		t.Fatalf("diagnostics with one valid component = %#v, want no %s warning", dropped, KustomizeIgnoreMissingComponentsDroppedCode)
	}

	names, err := renderIgnoreMissingComponents(t, root, false, "../components/a", "../components/b")
	if err != nil {
		t.Fatalf("Render() without ignoreMissingComponents error = %v", err)
	}
	if want := []string{"component-a", "component-b", "demo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names without ignoreMissingComponents = %#v, want every component %#v", names, want)
	}
}

// TestKustomizeRendererIgnoreMissingComponentsSkipsCopiesOfKeptComponent
// checks that the dropped warning does not name a later entry that resolves
// to the kept component. Argo CD v3.6.1 resolves each entry with
// filepath.Rel(repoRoot, filepath.Join(path, c))
// (util/kustomize/kustomize.go:357), so a repeat, a doubled slash, a "./"
// prefix, a trailing slash and a detour through ".." all name component-a.
// No resources are missing, so --strict must not fail. A distinct valid
// component after them still warns.
func TestKustomizeRendererIgnoreMissingComponentsSkipsCopiesOfKeptComponent(t *testing.T) {
	root := t.TempDir()
	writeIgnoreMissingComponentsRepo(t, root, "a", "b")

	for _, copyOfKept := range []string{
		"../components/a",
		"../components//a",
		"./../components/a",
		"../components/a/",
		"../components/b/../a",
	} {
		names, dropped := renderIgnoreMissingComponentsDropped(t, root, "../components/missing", "../components/a", copyOfKept)
		if want := []string{"component-a", "demo"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("rendered names with %q = %#v, want %#v", copyOfKept, names, want)
		}
		if len(dropped) != 0 {
			t.Fatalf("diagnostics with %q = %#v, want no %s warning for a copy of the kept component", copyOfKept, dropped, KustomizeIgnoreMissingComponentsDroppedCode)
		}
	}

	_, dropped := renderIgnoreMissingComponentsDropped(t, root, "../components/a", "../components//a", "../components/b")
	if len(dropped) != 1 {
		t.Fatalf("%s diagnostics = %#v, want one warning", KustomizeIgnoreMissingComponentsDroppedCode, dropped)
	}
	if message := dropped[0].Message; !strings.Contains(message, `"../components/b"`) || strings.Contains(message, `"../components//a"`) {
		t.Fatalf("warning message %q, want it to name only ../components/b as dropped", message)
	}
}

// renderIgnoreMissingComponentsDropped renders apps/demo with components
// under ignoreMissingComponents and returns the rendered names and the
// kustomize.ignore-missing-components-dropped diagnostics.
func renderIgnoreMissingComponentsDropped(t *testing.T, root string, components ...string) ([]string, []diagnostic.Diagnostic) {
	t.Helper()
	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		OfflineRemoteResources: true,
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			Components:              components,
			IgnoreMissingComponents: true,
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	var dropped []diagnostic.Diagnostic
	for _, diag := range diags {
		if diag.Code == KustomizeIgnoreMissingComponentsDroppedCode {
			dropped = append(dropped, diag)
		}
	}
	return directoryManifestNames(manifests), dropped
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

	// Recorded divergence: the repo-server drops a found absolute ref that
	// comes after the first valid component. drydock still rejects it.
	_, err = renderIgnoreMissingComponents(t, root, true, "../components/valid", "/components/absolute")
	if err == nil || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("Render() with a later absolute ref error = %v, want absolute component rejection", err)
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
