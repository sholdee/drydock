package render

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
