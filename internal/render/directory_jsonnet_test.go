package render

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDirectoryRendererRendersJsonnetObject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'jsonnet-object' },
}`)

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
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"jsonnet-object"}) {
		t.Fatalf("rendered names = %#v, want jsonnet-object", got)
	}
	if result[0].Path != filepath.Join("apps", "main.jsonnet") {
		t.Fatalf("Path = %q, want apps/main.jsonnet", result[0].Path)
	}
}

func TestDirectoryRendererRendersJsonnetArray(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `[
  {
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: { name: 'first' },
  },
  {
    apiVersion: 'v1',
    kind: 'Secret',
    metadata: { name: 'second' },
  },
]`)

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
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("rendered names = %#v, want first and second", got)
	}
}

func TestDirectoryRendererRendersJsonnetExtVarsAndTLAs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `function(namespace)
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: std.extVar('name'),
    namespace: namespace,
  },
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{
		Jsonnet: argoappv1.ApplicationSourceJsonnet{
			ExtVars: []argoappv1.JsonnetVar{{Name: "name", Value: "from-ext"}},
			TLAs:    []argoappv1.JsonnetVar{{Name: "namespace", Value: "from-tla"}},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if name := result[0].Object.GetName(); name != "from-ext" {
		t.Fatalf("name = %q, want from-ext", name)
	}
	if namespace := result[0].Object.GetNamespace(); namespace != "from-tla" {
		t.Fatalf("namespace = %q, want from-tla", namespace)
	}
}

func TestDirectoryRendererRendersJsonnetCodeVars(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `function(suffix)
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'jsonnet-' + suffix },
  data: { value: std.extVar('value') },
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{
		Jsonnet: argoappv1.ApplicationSourceJsonnet{
			ExtVars: []argoappv1.JsonnetVar{{Name: "value", Value: "\"from-code\"", Code: true}},
			TLAs:    []argoappv1.JsonnetVar{{Name: "suffix", Value: "\"coded\"", Code: true}},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if name := result[0].Object.GetName(); name != "jsonnet-coded" {
		t.Fatalf("name = %q, want jsonnet-coded", name)
	}
	value, _, err := unstructured.NestedString(result[0].Object.Object, "data", "value")
	if err != nil {
		t.Fatalf("NestedString() error = %v", err)
	}
	if value != "from-code" {
		t.Fatalf("data.value = %q, want from-code", value)
	}
}

func TestDirectoryRendererEnvSubstitutesJsonnetVars(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `function(namespace)
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: std.extVar('name'),
    namespace: namespace,
  },
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{
		ArgoEnv: argoappv1.Env{
			{Name: "ARGOCD_APP_NAME", Value: "demo"},
			{Name: "ARGOCD_APP_NAMESPACE", Value: "workloads"},
		},
		Jsonnet: argoappv1.ApplicationSourceJsonnet{
			ExtVars: []argoappv1.JsonnetVar{{Name: "name", Value: "$ARGOCD_APP_NAME"}},
			TLAs:    []argoappv1.JsonnetVar{{Name: "namespace", Value: "$ARGOCD_APP_NAMESPACE"}},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if name := result[0].Object.GetName(); name != "demo" {
		t.Fatalf("name = %q, want demo", name)
	}
	if namespace := result[0].Object.GetNamespace(); namespace != "workloads" {
		t.Fatalf("namespace = %q, want workloads", namespace)
	}
}

func TestDirectoryRendererRendersJsonnetWithRepoRootLibs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `local common = import 'common.libsonnet';
common.configMap('from-lib')`)
	writeFile(t, filepath.Join(root, "lib", "common.libsonnet"), `{
  configMap(name):: {
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: { name: name },
  },
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{
		Jsonnet: argoappv1.ApplicationSourceJsonnet{Libs: []string{"lib"}},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"from-lib"}) {
		t.Fatalf("rendered names = %#v, want from-lib", got)
	}
}

func TestDirectoryRendererRendersJsonnetWithAppPathImports(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `local common = import 'common.libsonnet';
common.configMap('from-app-import')`)
	writeFile(t, filepath.Join(root, "apps", "common.libsonnet"), `{
  configMap(name):: {
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: { name: name },
  },
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"from-app-import"}) {
		t.Fatalf("rendered names = %#v, want from-app-import", got)
	}
}

func TestDirectoryRendererRendersJsonnetNestedAppImportsWithinRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "sub", "main.jsonnet"), `local common = import '../common.libsonnet';
common.configMap('from-nested-app-import')`)
	writeFile(t, filepath.Join(root, "apps", "common.libsonnet"), `{
  configMap(name):: {
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: { name: name },
  },
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{DirectoryRecurse: true})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"from-nested-app-import"}) {
		t.Fatalf("rendered names = %#v, want from-nested-app-import", got)
	}
}

func TestDirectoryRendererRendersJsonnetNestedLibImportsWithinRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `import 'nested/main.libsonnet'`)
	writeFile(t, filepath.Join(root, "lib", "nested", "main.libsonnet"), `local common = import '../common.libsonnet';
common.configMap('from-nested-lib-import')`)
	writeFile(t, filepath.Join(root, "lib", "common.libsonnet"), `{
  configMap(name):: {
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: { name: name },
  },
}`)

	result, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{
		Jsonnet: argoappv1.ApplicationSourceJsonnet{Libs: []string{"lib"}},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := directoryManifestNames(result); !reflect.DeepEqual(got, []string{"from-nested-lib-import"}) {
		t.Fatalf("rendered names = %#v, want from-nested-lib-import", got)
	}
}

func TestDirectoryRendererReturnsInvalidJsonnetErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `{ invalid: }`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want Jsonnet error")
	}
	if !strings.Contains(err.Error(), "failed to evaluate jsonnet") {
		t.Fatalf("Render() error = %v, want failed to evaluate jsonnet", err)
	}
}

func TestDirectoryRendererRejectsJsonnetLibEscapes(t *testing.T) {
	for _, lib := range []string{"../outside", "/tmp/outside"} {
		t.Run(lib, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'demo' },
}`)

			_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
				RepoRoot: root,
				Path:     "apps",
			}, RenderOptions{
				Jsonnet: argoappv1.ApplicationSourceJsonnet{Libs: []string{lib}},
			})
			if err == nil {
				t.Fatal("Render() error = nil, want Jsonnet lib safety error")
			}
			if !strings.Contains(err.Error(), "jsonnet lib") {
				t.Fatalf("Render() error = %v, want jsonnet lib message", err)
			}
		})
	}
}

func TestDirectoryRendererRejectsJsonnetImportEscapes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "outside.libsonnet"), `{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'outside' },
}`)
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `import '../outside.libsonnet'`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want Jsonnet import escape error")
	}
	if !strings.Contains(err.Error(), "jsonnet import") {
		t.Fatalf("Render() error = %v, want jsonnet import message", err)
	}
}

func TestDirectoryRendererRejectsJsonnetAbsoluteImports(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.libsonnet")
	writeFile(t, outside, `{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'outside' },
}`)
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), fmt.Sprintf("import %q", filepath.ToSlash(outside)))

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want Jsonnet absolute import error")
	}
	if !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("Render() error = %v, want relative import message", err)
	}
}

func TestDirectoryRendererRejectsJsonnetSymlinkImports(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.libsonnet")
	writeFile(t, outside, `{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'outside' },
}`)
	if err := os.MkdirAll(filepath.Join(root, "apps"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "apps", "linked.libsonnet")); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}
	writeFile(t, filepath.Join(root, "apps", "main.jsonnet"), `import 'linked.libsonnet'`)

	_, _, err := (DirectoryRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     "apps",
	}, RenderOptions{})
	if err == nil {
		t.Fatal("Render() error = nil, want Jsonnet symlink import error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Render() error = %v, want symlink message", err)
	}
}
