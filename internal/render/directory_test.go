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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func TestDirectoryRendererRecurseIncludesHiddenDirectoriesExceptDrydockCache(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "root.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: root
`)
	writeFile(t, filepath.Join(root, "apps", ".hidden", "child.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: hidden-child
`)
	writeFile(t, filepath.Join(root, "apps", drydockCacheMetadataDirName, "ignored.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: cache-metadata
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
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"hidden-child", "root"}) {
		t.Fatalf("rendered names = %#v, want root and hidden-child", got)
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

func TestDirectoryRendererEmitsKustomizeGeneratorDataFilesLikeArgoCDFindManifests(t *testing.T) {
	// Argo CD findManifests has no kustomization awareness — every matching
	// manifest file in a directory source is processed normally. Generator data
	// files that are valid Kubernetes objects are emitted; the kustomization.yaml
	// itself has no apiVersion/kind so it is silently skipped by the existing
	// kind-less-document behavior.
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
	// kustomization.yaml: no apiVersion/kind → silently skipped (kind-less behavior).
	// config/settings.yaml and secrets/credentials.yaml are not in the top-level
	// directory and non-recursive mode does not descend into subdirectories.
	// Only visible.yaml is emitted.
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1 (non-recursive; subdirs not walked): %#v", len(result), result)
	}
	if result[0].Object.GetName() != "visible" {
		t.Fatalf("rendered object name = %q, want visible", result[0].Object.GetName())
	}
}

func TestDirectoryRendererRecursiveEmitsKustomizationDocsWithAPIVersionKind(t *testing.T) {
	// Argo CD findManifests processes every matching manifest file without
	// kustomization awareness. A kustomization.yaml that carries apiVersion/kind
	// is emitted as a manifest. A generator data file that is a valid Kubernetes
	// object is also emitted. A kustomization.yaml without apiVersion/kind is
	// silently skipped by the existing kind-less-document behavior.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", ".hidden", "kustomization.yaml"), `
configMapGenerator:
  - name: generated-config
    files:
      - config/settings.yaml
`)
	writeFile(t, filepath.Join(root, "apps", ".hidden", "config", "settings.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: generator-data
`)
	writeFile(t, filepath.Join(root, "apps", ".hidden", "visible.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: visible
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
	// kustomization.yaml has no apiVersion/kind → silently skipped.
	// config/settings.yaml is a valid Kubernetes object → emitted.
	// visible.yaml → emitted.
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"generator-data", "visible"}) {
		t.Fatalf("rendered names = %#v, want generator-data and visible", got)
	}
}

func TestDirectoryRendererEmitsKustomizationDocumentWithAPIVersionKind(t *testing.T) {
	// A kustomization.yaml that carries apiVersion/kind/metadata is treated as a
	// normal Kubernetes manifest and emitted — matching Argo CD findManifests
	// behavior. The namePrefix/resources fields are preserved unchanged.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
metadata:
  name: test-kustomization
resources:
  - configmap.yaml
namePrefix: prefix-
`)
	writeFile(t, filepath.Join(root, "apps", "configmap.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
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
	// Both the Kustomization doc (with apiVersion/kind) and the ConfigMap are emitted.
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"test-cm", "test-kustomization"}) {
		t.Fatalf("rendered names = %#v, want test-cm and test-kustomization", got)
	}
	// Verify the Kustomization object preserves its fields.
	var kustObj *unstructured.Unstructured
	for _, m := range result {
		if m.Object.GetName() == "test-kustomization" {
			kustObj = m.Object
		}
	}
	if kustObj == nil {
		t.Fatal("kustomization object not found in result")
	}
	if kustObj.GetAPIVersion() != "kustomize.config.k8s.io/v1beta1" {
		t.Fatalf("kustomization apiVersion = %q, want kustomize.config.k8s.io/v1beta1", kustObj.GetAPIVersion())
	}
	if kustObj.GetKind() != "Kustomization" {
		t.Fatalf("kustomization kind = %q, want Kustomization", kustObj.GetKind())
	}
}

func TestDirectoryRendererSkipsYAMLDocumentsWithoutAPIVersionAndKind(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "values.yaml"), `
image:
  tag: latest
`)
	writeFile(t, filepath.Join(root, "apps", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: visible
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
	if len(result) != 1 || result[0].Object.GetName() != "visible" {
		t.Fatalf("result = %#v, want visible ConfigMap only", result)
	}
}

func TestDirectoryRendererSkipsArgocdSkipFileRenderingMarkerBeforeDecode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "skip.yaml"), `
# +argocd:skip-file-rendering
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
metadata:
  name: duplicate-would-fail
`)
	writeFile(t, filepath.Join(root, "apps", "skip.jsonnet"), `
// +argocd:skip-file-rendering
this is not valid jsonnet
`)
	writeFile(t, filepath.Join(root, "apps", "visible.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: visible
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
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"visible"}) {
		t.Fatalf("rendered names = %#v, want visible only", got)
	}
}

func TestDirectoryRendererIgnoresNonKubernetesDecodeErrorsAndScalars(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "duplicate-values.yaml"), `
image:
  tag: one
  tag: two
`)
	writeFile(t, filepath.Join(root, "apps", "scalar.yaml"), `plain scalar`)
	writeFile(t, filepath.Join(root, "apps", "array.json"), `["plain", "array"]`)
	writeFile(t, filepath.Join(root, "apps", "visible.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: visible
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
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"visible"}) {
		t.Fatalf("rendered names = %#v, want visible only", got)
	}
}

func TestDirectoryRendererRejectsKubernetesLookingDecodeErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "broken.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: first
metadata:
  name: duplicate
`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want Kubernetes-looking decode error")
	}
	if !strings.Contains(err.Error(), "decode YAML document failed") {
		t.Fatalf("Render() error = %v, want decode YAML document failure", err)
	}
}

func TestDirectoryRendererRejectsYAMLDocumentWithKindButNoAPIVersion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "broken.yaml"), `
kind: ConfigMap
metadata:
  name: broken
`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want partial manifest error")
	}
	if !strings.Contains(err.Error(), "missing apiVersion") {
		t.Fatalf("Render() error = %v, want missing apiVersion message", err)
	}
}

func TestDirectoryRendererRejectsYAMLDocumentWithAPIVersionButNoKind(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "broken.yaml"), `
apiVersion: v1
metadata:
  name: broken
`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want partial manifest error")
	}
	if !strings.Contains(err.Error(), "missing kind") {
		t.Fatalf("Render() error = %v, want missing kind message", err)
	}
}

func TestDirectoryRendererRejectsListDocumentWithKindButNoAPIVersion(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{
			name: "with item",
			body: `
kind: List
items:
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: demo
`,
		},
		{
			name: "empty items",
			body: `
kind: List
items: []
`,
		},
		{
			name: "missing items",
			body: `
kind: List
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", "broken-list.yaml"), tt.body)

			_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "apps",
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want partial List manifest error")
			}
			if !strings.Contains(err.Error(), "missing apiVersion") {
				t.Fatalf("Render() error = %v, want missing apiVersion message", err)
			}
		})
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
