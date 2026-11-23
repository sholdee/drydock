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
// Argo CD v3.6.2 (util/kustomize/kustomize.go:357-368): under
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
		"../components/plain", "../components/valid", "./local-plain", "../components/yml",
		"../components/missing", "../components/bare", "../components/dirfile")
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

// TestKustomizeRendererIgnoreMissingComponentsAddsEveryValidComponent pins
// Argo CD v3.6.2 (util/kustomize/kustomize.go:355-372): with
// ignoreMissingComponents the repo-server checks every component, adds each
// one that holds a kustomization file, and skips the others at any position.
// Argo CD v3.6.1 left the loop after the first valid component and added
// only component-a here.
func TestKustomizeRendererIgnoreMissingComponentsAddsEveryValidComponent(t *testing.T) {
	root := t.TempDir()
	writeIgnoreMissingComponentsRepo(t, root, "a", "b", "c")
	writeFile(t, filepath.Join(root, "apps", "components", "plain", "cm.yaml"), directoryConfigMap("component-plain"))

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{
		OfflineRemoteResources: true,
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			Components: []string{
				"../components/missing", "../components/a", "../components/plain",
				"../components/b", "../components/later-missing", "../components/c",
			},
			IgnoreMissingComponents: true,
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if names, want := directoryManifestNames(manifests), []string{"component-a", "component-b", "component-c", "demo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rendered names = %#v, want every valid component %#v", names, want)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}

	// A repeat goes through `kustomize edit add component` as it does
	// without ignoreMissingComponents. The command drops an entry with the
	// same text, and adds an entry with another spelling of the same folder
	// again, which fails the build.
	for _, repeat := range []string{"../components/a", "../components//a", "../components/a/"} {
		components := []string{"../components/a", repeat, "../components/b"}
		names, err := renderIgnoreMissingComponents(t, root, true, components...)
		wantNames, wantErr := renderIgnoreMissingComponents(t, root, false, components...)
		if (err == nil) != (wantErr == nil) || !reflect.DeepEqual(names, wantNames) {
			t.Fatalf("Render() with repeat %q = %#v, %v, want the result without ignoreMissingComponents %#v, %v", repeat, names, err, wantNames, wantErr)
		}
		if repeat == "../components/a" && (err != nil || !reflect.DeepEqual(names, []string{"component-a", "component-b", "demo"})) {
			t.Fatalf("Render() with an exact repeat = %#v, %v, want each component once", names, err)
		}
	}
}

// TestKustomizeRendererIgnoreMissingComponentsReadsEveryValidComponent pins
// that a valid component after the first valid one is read. Argo CD v3.6.2
// passes every found component to `kustomize edit add component`
// (util/kustomize/kustomize.go:375-377), so a broken one fails its render at
// any position. Argo CD v3.6.1 never read it. The symlink case fails on
// drydock's own rule: drydock refuses a component that it reaches through a
// symlink, at any position.
func TestKustomizeRendererIgnoreMissingComponentsReadsEveryValidComponent(t *testing.T) {
	cases := []struct {
		name    string
		write   func(t *testing.T, parent, broken string)
		wantErr string
	}{
		{
			name: "invalid kustomization YAML",
			write: func(t *testing.T, _, broken string) {
				writeFile(t, filepath.Join(broken, "kustomization.yaml"), "}{ not: [valid\n")
			},
			wantErr: "kustomization.yaml",
		},
		{
			name: "resource escaping the repository",
			write: func(t *testing.T, parent, broken string) {
				writeFile(t, filepath.Join(parent, "outside", "cm.yaml"), directoryConfigMap("outside"))
				writeFile(t, filepath.Join(broken, "kustomization.yaml"),
					"apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - ../../../../outside/cm.yaml\n")
			},
			wantErr: "outside",
		},
		{
			name: "in-repository symlink to another component",
			write: func(t *testing.T, _, broken string) {
				symlink(t, "a", broken)
			},
			wantErr: "symlink",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "repo")
			writeIgnoreMissingComponentsRepo(t, root, "a")
			tc.write(t, parent, filepath.Join(root, "apps", "components", "broken"))

			_, err := renderIgnoreMissingComponents(t, root, true, "../components/a", "../components/broken")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Render() error = %v, want the later component to fail the render with %q", err, tc.wantErr)
			}
		})
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

	// The repo-server checks a ref after a valid component too, so drydock
	// refuses a found absolute ref at any position.
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

// TestKustomizeSelectionPathsOwnAbsoluteComponentOutsideSource pins that
// changed-only selection owns the directory of an absolute component ref that
// holds a kustomization file. Argo CD v3.6.2 joins the ref under the source
// directory (util/kustomize/kustomize.go:357), so "/../components/abs" from
// apps/demo names apps/components/abs, outside spec.source.path. Its
// kustomization file makes the render fail at any position in the list, so
// editing it changes the result, and the Application must be selected.
func TestKustomizeSelectionPathsOwnAbsoluteComponentOutsideSource(t *testing.T) {
	for name, components := range map[string][]string{
		"first valid":       {"../components/missing", "/../components/abs", "../components/valid"},
		"after first valid": {"../components/valid", "/../components/abs"},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "repo")
			writeIgnoreMissingComponentsRepo(t, root, "valid", "abs")
			kustomize := &argoappv1.ApplicationSourceKustomize{Components: components, IgnoreMissingComponents: true}
			selection, err := KustomizeSelectionPaths(context.Background(), root, filepath.Join("apps", "demo"), kustomize)
			if err != nil {
				t.Fatalf("KustomizeSelectionPaths() error = %v", err)
			}
			const want = "apps/components/abs"
			if i := sort.SearchStrings(selection, want); i >= len(selection) || selection[i] != want {
				t.Fatalf("KustomizeSelectionPaths() = %v, missing %q", selection, want)
			}
		})
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

// TestKustomizeInputDigestPathsCoverComponentsAfterFirstValid pins the render
// cache inputs of the components listed after the first valid one. Argo CD
// v3.6.2 checks every component, so a later valid component is read like the
// first: its directory and files are required digest paths. A later directory
// without a kustomization file is an optional path, so a kustomization file
// appearing there rotates the key. A later ref that names a file is recorded
// as that path. A later valid component reached through an in-repository
// symlink fails the digest, as it fails the render.
func TestKustomizeInputDigestPathsCoverComponentsAfterFirstValid(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	writeIgnoreMissingComponentsRepo(t, root, "a", "b")
	writeFile(t, filepath.Join(root, "apps", "components", "plain", "cm.yaml"), directoryConfigMap("component-plain"))
	writeFile(t, filepath.Join(root, "apps", "components", "file-ref"), "not a directory\n")
	writeIgnoreMissingComponent(t, filepath.Join(parent, "outside"), "outside")
	source := ResolvedSource{RepoRoot: root, Path: filepath.Join("apps", "demo")}
	opts := RenderOptions{Kustomize: &argoappv1.ApplicationSourceKustomize{
		Components:              []string{"../components/a", "../components/plain", "../components/b", "../components/pending", "../components/file-ref", "../../../outside"},
		IgnoreMissingComponents: true,
	}}

	paths, err := KustomizeInputDigestPaths(context.Background(), source, opts)
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() error = %v", err)
	}
	byPath := map[string]gitref.PathDigestPath{}
	for _, item := range paths {
		byPath[item.Path] = item
	}
	for _, want := range []gitref.PathDigestPath{
		{Path: "apps/components/a", Optional: false},
		{Path: "apps/components/a/kustomization.yaml", Optional: false},
		{Path: "apps/components/b", Optional: false},
		{Path: "apps/components/b/kustomization.yaml", Optional: false},
		{Path: "apps/components/b/kustomization.yml", Optional: true},
		{Path: "apps/components/b/Kustomization", Optional: true},
		{Path: "apps/components/plain", Optional: true},
		{Path: "apps/components/pending", Optional: true},
		{Path: "apps/components/file-ref", Optional: true},
	} {
		if got, ok := byPath[want.Path]; !ok || got != want {
			t.Errorf("digest path %q = %#v (present %t), want %#v; all = %#v", want.Path, got, ok, want, paths)
		}
	}
	for path := range byPath {
		if strings.HasPrefix(path, "..") || strings.Contains(path, "outside") {
			t.Errorf("digest path %q names an out-of-repository path", path)
		}
	}

	symlink(t, "a", filepath.Join(root, "apps", "components", "alias"))
	opts.Kustomize.Components = []string{"../components/a", "../components/alias"}
	if _, err := KustomizeInputDigestPaths(context.Background(), source, opts); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("KustomizeInputDigestPaths() with a symlinked later component error = %v, want a symlink error", err)
	}
}
