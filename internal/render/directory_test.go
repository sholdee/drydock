package render

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	cachepkg "github.com/sholdee/drydock/internal/cache"
)

func TestDirectoryRendererRendersYAML(t *testing.T) {
	renderer := DirectoryRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications"),
		Path:     "plain-dir",
	}

	result, diags, err := renderer.Render(context.Background(), source, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Object.GetKind() != "ConfigMap" || result[0].Object.GetName() != "app-config" {
		t.Fatalf("unexpected object: %#v", result[0].Object)
	}
	if result[0].Path != filepath.Join("plain-dir", "cm.yaml") {
		t.Fatalf("Path = %q, want plain-dir/cm.yaml", result[0].Path)
	}
}

func TestDirectoryRendererFlattensListObjects(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "list.yaml"), `
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: first
      namespace: default
  - apiVersion: v1
    kind: Secret
    metadata:
      name: second
      namespace: default
`)

	renderer := DirectoryRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}

	gotKinds := []string{result[0].Object.GetKind(), result[1].Object.GetKind()}
	if wantKinds := []string{"ConfigMap", "Secret"}; !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("kinds = %#v, want %#v", gotKinds, wantKinds)
	}
	for _, manifest := range result {
		if manifest.Path != filepath.Join("apps", "list.yaml") {
			t.Fatalf("Path = %q, want apps/list.yaml", manifest.Path)
		}
	}
}

func TestDirectoryRendererSkipsDotDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", ".cache", "ignored.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: ignored
`)
	writeFile(t, filepath.Join(root, "apps", "visible.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: visible
`)

	renderer := DirectoryRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Object.GetName() != "visible" {
		t.Fatalf("rendered object name = %q, want visible", result[0].Object.GetName())
	}
}

func TestDirectoryRendererDefaultSkipsNestedManifests(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "root.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: root
`)
	writeFile(t, filepath.Join(root, "apps", "nested", "child.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: child
`)

	result, diags, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"root"}) {
		t.Fatalf("rendered names = %#v, want root only", got)
	}
}

func TestDirectoryRendererHonorsDirectoryRecurse(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "root.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: root
`)
	writeFile(t, filepath.Join(root, "apps", "nested", "child.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: child
`)

	result, diags, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{DirectoryRecurse: true})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"child", "root"}) {
		t.Fatalf("rendered names = %#v, want root and child", got)
	}
}

func TestDirectoryRendererHonorsDirectoryInclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "root.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: root
`)
	writeFile(t, filepath.Join(root, "apps", "nested", "child.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: child
`)
	writeFile(t, filepath.Join(root, "apps", "nested", "ignored.json"), `{
  "apiVersion": "v1",
  "kind": "ConfigMap",
  "metadata": {"name": "ignored"}
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{DirectoryRecurse: true, DirectoryInclude: "*.yaml"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"child", "root"}) {
		t.Fatalf("*.yaml rendered names = %#v, want root and child", got)
	}

	result, _, err = (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{DirectoryRecurse: true, DirectoryInclude: "**/*.yaml"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"child"}) {
		t.Fatalf("**/*.yaml rendered names = %#v, want child", got)
	}
}

func TestDirectoryRendererHonorsDirectoryExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "root.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: root
`)
	writeFile(t, filepath.Join(root, "apps", "disabled", "ignored.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: ignored
`)
	writeFile(t, filepath.Join(root, "apps", "enabled", "child.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: child
`)

	result, diags, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{DirectoryRecurse: true, DirectoryExclude: "disabled/*"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"child", "root"}) {
		t.Fatalf("rendered names = %#v, want root and child", got)
	}
}

func TestDirectoryRendererSkipsDrydockCacheMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "visible.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: visible
`)
	if err := cachepkg.WriteMetadata(root, cachepkg.Metadata{
		Source: cachepkg.SourceGit,
		Kind:   "git",
		Key:    strings.Repeat("a", 64),
		Target: "https://example.test/org/repo.git",
	}); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}

	renderer := DirectoryRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     ".",
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want only visible manifest: %#v", len(result), result)
	}
	if result[0].Path != "visible.yaml" {
		t.Fatalf("Path = %q, want visible.yaml", result[0].Path)
	}
	if result[0].Object.GetName() != "visible" {
		t.Fatalf("rendered object name = %q, want visible", result[0].Object.GetName())
	}
}

func TestDirectoryRendererSkipsKustomizeGeneratorDataFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "kustomization.yaml"), `
configMapGenerator:
  - name: generated-config
    files:
      - settings=config/settings.yaml
secretGenerator:
  - name: generated-secret
    envs:
      - secrets/credentials.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "config", "settings.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: generator-data-config
`)
	writeFile(t, filepath.Join(root, "apps", "secrets", "credentials.yaml"), `
apiVersion: v1
kind: Secret
metadata:
  name: generator-data-secret
`)
	writeFile(t, filepath.Join(root, "apps", "visible.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: visible
`)

	renderer := DirectoryRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if result[0].Object.GetName() != "visible" {
		t.Fatalf("rendered object name = %q, want visible", result[0].Object.GetName())
	}
}

func TestDirectoryRendererRejectsSourcePathEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(outside, "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)

	renderer := DirectoryRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     ".." + string(filepath.Separator) + "outside",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want path escape error")
	}
	if !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("Render() error = %v, want escape error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestDirectoryRendererRejectsSymlinkedSourcePathComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)
	symlink(t, outside, filepath.Join(root, "apps"))

	renderer := DirectoryRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestDirectoryRendererSkipsSymlinkedYAMLFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "outside.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)
	symlink(t, filepath.Join(outside, "outside.yaml"), filepath.Join(root, "apps", "linked.yaml"))

	renderer := DirectoryRenderer{}
	result, diags, err := renderer.Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests from symlinked YAML", result)
	}
}

func directoryManifestNames(manifests []Manifest) []string {
	names := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		names = append(names, manifest.Object.GetName())
	}
	sort.Strings(names)
	return names
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(newname), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(oldname, newname); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}
}
