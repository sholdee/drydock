package render

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestKustomizeRendererRendersResources(t *testing.T) {
	renderer := KustomizeRenderer{}
	source := ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications"),
		Path:     "kustomize",
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
	if result[0].Object.GetKind() != "ConfigMap" || result[0].Object.GetName() != "kustomized" {
		t.Fatalf("unexpected object: %#v", result[0].Object)
	}
	if result[0].Path != filepath.Join("kustomize", "kustomization.yaml") {
		t.Fatalf("Path = %q, want kustomize/kustomization.yaml", result[0].Path)
	}
}

func TestKustomizeRendererRejectsSourcePathEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)

	renderer := KustomizeRenderer{}
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

func TestKustomizeRendererRejectsKustomizationGraphEscapes(t *testing.T) {
	for _, tt := range []struct {
		name           string
		kustomization  string
		outsideRelPath string
	}{
		{
			name: "resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../outside/cm.yaml
`,
			outsideRelPath: filepath.Join("outside", "cm.yaml"),
		},
		{
			name: "base",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - ../../outside
`,
			outsideRelPath: filepath.Join("outside", "kustomization.yaml"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), tt.kustomization)
			writeFile(t, filepath.Join(root, tt.outsideRelPath), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)

			result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     filepath.Join("apps", "demo"),
			}, RenderOptions{})
			if err == nil {
				t.Fatal("Render() error = nil, want graph escape error")
			}
			if !strings.Contains(err.Error(), "escapes source root") {
				t.Fatalf("Render() error = %v, want source root escape error", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if len(result) != 0 {
				t.Fatalf("result = %#v, want no manifests", result)
			}
		})
	}
}

func TestKustomizeRendererRejectsSymlinkedSourcePathComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)
	symlink(t, outside, filepath.Join(root, "apps"))

	renderer := KustomizeRenderer{}
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

func TestKustomizeRendererRejectsSymlinkedGraphEntries(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kustomization string
		linkName      string
		targetFile    string
		targetDir     bool
	}{
		{
			name: "resource",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - linked.yaml
`,
			linkName:   "linked.yaml",
			targetFile: "cm.yaml",
		},
		{
			name: "base",
			kustomization: `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - linked-base
`,
			linkName:  "linked-base",
			targetDir: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), tt.kustomization)
			target := outside
			if !tt.targetDir {
				target = filepath.Join(outside, tt.targetFile)
			}
			writeFile(t, filepath.Join(outside, "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)
			if tt.targetFile != "" {
				writeFile(t, filepath.Join(outside, tt.targetFile), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: outside
`)
			}
			symlink(t, target, filepath.Join(root, "app", tt.linkName))

			result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "app",
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
		})
	}
}

func TestKustomizeRendererRejectsRemoteRefs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/example/repo//base?ref=main
`)

	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "app",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want remote ref error")
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Fatalf("Render() error = %v, want remote ref error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}

func TestKustomizeRendererRejectsBuildOptions(t *testing.T) {
	result, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: filepath.Join("..", "..", "testdata", "applications"),
		Path:     "kustomize",
	}, RenderOptions{
		BuildOptions: []string{"--enable-helm"},
	})
	if err == nil {
		t.Fatal("Render() error = nil, want build options error")
	}
	if !strings.Contains(err.Error(), "build options") {
		t.Fatalf("Render() error = %v, want build options error", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(result) != 0 {
		t.Fatalf("result = %#v, want no manifests", result)
	}
}
