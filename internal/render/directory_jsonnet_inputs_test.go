package render

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// TestDirectoryJsonnetImportPaths pins the files outside a directory source
// that its Jsonnet files import: a relative import and
// what it imports, an importstr, an import through a symlink with the
// symlink target, and a missing path that the importer tries before the
// one it finds, because a new file there would win.
func TestDirectoryJsonnetImportPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "main.jsonnet"), `local shared = import '../shared/lib.libsonnet';
local text = importstr '../shared/text.txt';
local linked = import 'linked.libsonnet';
local vendored = import 'vendored.libsonnet';
[shared, text, linked, vendored]
`)
	// nested.libsonnet is missing next to lib.libsonnet, so the importer
	// finds it in the library path vendor.
	writeFile(t, filepath.Join(root, "shared", "lib.libsonnet"), "{ nested: import 'nested.libsonnet' }\n")
	writeFile(t, filepath.Join(root, "vendor", "nested.libsonnet"), "{}\n")
	writeFile(t, filepath.Join(root, "vendor", "vendored.libsonnet"), "{}\n")
	writeFile(t, filepath.Join(root, "shared", "text.txt"), "text")
	writeFile(t, filepath.Join(root, "other", "target.libsonnet"), "{}\n")
	symlink(t, "../other/target.libsonnet", filepath.Join(root, "app", "linked.libsonnet"))
	writeFile(t, filepath.Join(root, "app", "broken.jsonnet"), "local x = import '../ignored.libsonnet'; {")

	got, err := DirectoryJsonnetImportPaths(t.Context(), root, "app", argoappv1.ApplicationSourceJsonnet{Libs: []string{"vendor"}}, nil)
	if err != nil {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v", err)
	}
	// Files found in the library vendor count too: changed-only selection
	// owns a library through the files it serves, not as a whole directory.
	want := []string{"other/target.libsonnet", "shared/lib.libsonnet", "shared/nested.libsonnet", "shared/text.txt", "vendor/nested.libsonnet", "vendor/vendored.libsonnet"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("DirectoryJsonnetImportPaths() = %v, want %v", got, want)
	}
}

// TestDirectoryJsonnetImportPathsReadsOnlyBoundedRepositoryFiles pins that
// the import scan reads a file only after the renderer's importer checks.
// An entry file or an import that is a symlink to a device that never ends,
// or to a file outside the repository, is not read: the scan neither hangs
// nor follows the imports of such a file.
func TestDirectoryJsonnetImportPathsReadsOnlyBoundedRepositoryFiles(t *testing.T) {
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skipf("/dev/zero unavailable: %v", err)
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.jsonnet")
	writeFile(t, outside, "import '../shared/leak.libsonnet'\n")
	symlink(t, "/dev/zero", filepath.Join(root, "app", "zero.jsonnet"))
	symlink(t, outside, filepath.Join(root, "app", "outside.jsonnet"))
	symlink(t, "/dev/zero", filepath.Join(root, "app", "z.libsonnet"))
	writeFile(t, filepath.Join(root, "app", "main.jsonnet"), "import 'z.libsonnet'\n")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	got, err := DirectoryJsonnetImportPaths(ctx, root, "app", argoappv1.ApplicationSourceJsonnet{}, nil)
	if err != nil {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("DirectoryJsonnetImportPaths() = %v, want nothing read from outside or unbounded files", got)
	}
}

// writeJsonnetImportFanout writes the review repro: two in-repository
// symlinks to the source directory itself, imported by the only Jsonnet
// file, so every level of imports doubles the paths one file has. Argo CD
// renders it, because Jsonnet does not evaluate the unused imports.
func writeJsonnetImportFanout(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "app", "x.jsonnet"), `local a = import 's1/x.jsonnet';
local b = import 's2/x.jsonnet';
{ apiVersion: 'v1', kind: 'ConfigMap', metadata: { name: 'fanout' } }
`)
	symlink(t, ".", filepath.Join(root, "app", "s1"))
	symlink(t, ".", filepath.Join(root, "app", "s2"))
}

// TestDirectoryJsonnetImportPathsStopsAtItsBudget pins that the scan stops
// at its budget on an import graph that grows without end, and reports
// that its result is not complete.
func TestDirectoryJsonnetImportPathsStopsAtItsBudget(t *testing.T) {
	root := t.TempDir()
	writeJsonnetImportFanout(t, root)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := DirectoryJsonnetImportPaths(ctx, root, "app", argoappv1.ApplicationSourceJsonnet{}, nil); !errors.Is(err, ErrJsonnetImportScanBudget) {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v, want the budget error", err)
	}
	if _, err := DirectoryInputDigestPaths(ctx, ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{}); !errors.Is(err, ErrJsonnetImportScanBudget) {
		t.Fatalf("DirectoryInputDigestPaths() error = %v, want the budget error", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the scan ran past its deadline")
	}
}

// TestDirectoryJsonnetImportPathsRunsOutOnALargeFile pins that a Jsonnet
// file over the scan budget is not dropped silently. The renderer
// evaluates it, so its imports are inputs that the scan cannot list.
func TestDirectoryJsonnetImportPathsRunsOutOnALargeFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "lib", "inner.libsonnet"), "{}\n")
	writeFile(t, filepath.Join(root, "app", "main.jsonnet"), "import '../lib/inner.libsonnet'\n"+strings.Repeat(" ", maxJsonnetImportScanBytes))
	if _, err := DirectoryJsonnetImportPaths(t.Context(), root, "app", argoappv1.ApplicationSourceJsonnet{}, nil); !errors.Is(err, ErrJsonnetImportScanBudget) {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v, want the budget error", err)
	}
}

// TestDirectoryJsonnetImportPathsWithoutJsonnetFiles pins that a source
// without Jsonnet files does not resolve its libraries, so a library that
// would fail the render of a Jsonnet file is no scan error there.
func TestDirectoryJsonnetImportPathsWithoutJsonnetFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "a: 1\n")
	got, err := DirectoryJsonnetImportPaths(t.Context(), root, "app", argoappv1.ApplicationSourceJsonnet{Libs: []string{"../outside"}}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("DirectoryJsonnetImportPaths() = %v, %v, want nothing and no error", got, err)
	}
}

// TestDirectoryJsonnetImportPathsFollowsCode pins that the scan follows the
// imports in extVar and TLA code, with the same records as for a file: each
// path the importer tries before the one it finds, the path it finds, what
// that file imports, and the symlinks on the way. A code value has no file,
// so the importer tries the libraries, last first, and then the source
// path. Code that does not parse adds nothing.
func TestDirectoryJsonnetImportPathsFollowsCode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "main.jsonnet"), "std.extVar('cfg')\n")
	writeFile(t, filepath.Join(root, "vendor", "cfg.libsonnet"), "{ nested: import 'nested.libsonnet' }\n")
	writeFile(t, filepath.Join(root, "vendor", "nested.libsonnet"), "{}\n")
	writeFile(t, filepath.Join(root, "shared", "text.txt"), "text")
	writeFile(t, filepath.Join(root, "other", "target.libsonnet"), "{}\n")
	symlink(t, "../other/target.libsonnet", filepath.Join(root, "links", "linked.libsonnet"))
	// lib2 does not exist, so the importer tries lib2/cfg.libsonnet
	// before it finds vendor/cfg.libsonnet.
	sourceJsonnet := argoappv1.ApplicationSourceJsonnet{
		Libs: []string{"vendor", "lib2"},
		ExtVars: []argoappv1.JsonnetVar{
			{Name: "cfg", Value: "import 'cfg.libsonnet'", Code: true},
			{Name: "text", Value: "importstr '../shared/text.txt'", Code: true},
			{Name: "broken", Value: "import '../ignored.libsonnet' {", Code: true},
		},
		TLAs: []argoappv1.JsonnetVar{{Name: "linked", Value: "import '../links/linked.libsonnet'", Code: true}},
	}

	got, err := DirectoryJsonnetImportPaths(t.Context(), root, "app", sourceJsonnet, nil)
	if err != nil {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v", err)
	}
	want := []string{"lib2/cfg.libsonnet", "links/linked.libsonnet", "other/target.libsonnet", "shared/text.txt", "vendor/cfg.libsonnet", "vendor/nested.libsonnet"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("DirectoryJsonnetImportPaths() = %v, want %v", got, want)
	}

	// The renderer never evaluates the code of a source without a Jsonnet
	// file, so its imports are no inputs.
	if err := os.Remove(filepath.Join(root, "app", "main.jsonnet")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "a: 1\n")
	got, err = DirectoryJsonnetImportPaths(t.Context(), root, "app", sourceJsonnet, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("DirectoryJsonnetImportPaths() without a Jsonnet file = %v, %v, want nothing and no error", got, err)
	}
}

// TestJsonnetCodeValuePasses pins the values that the renderer evaluates
// as Jsonnet code: the TLAs and then the extVars with code: true, after
// the env substitution of each pass. A value with code: false is a plain
// string, so it is left out, also when it looks like an import. The passes
// stop at the first pass that repeats.
func TestJsonnetCodeValuePasses(t *testing.T) {
	sourceJsonnet := argoappv1.ApplicationSourceJsonnet{
		ExtVars: []argoappv1.JsonnetVar{
			{Name: "plain", Value: "import 'plain.libsonnet'"},
			{Name: "ext", Value: "import '$${ARGOCD_APP_NAME}.libsonnet'", Code: true},
		},
		TLAs: []argoappv1.JsonnetVar{
			{Name: "tla", Value: "importstr 'tla.txt'", Code: true},
			{Name: "plainTLA", Value: "importstr 'plain.txt'"},
		},
	}
	env := argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "demo"}}
	got := JsonnetCodeValuePasses(sourceJsonnet, env, 5)
	want := [][]string{
		{"importstr 'tla.txt'", "import '${ARGOCD_APP_NAME}.libsonnet'"},
		{"importstr 'tla.txt'", "import 'demo.libsonnet'"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JsonnetCodeValuePasses() = %q, want %q", got, want)
	}
	if got := JsonnetCodeValuePasses(sourceJsonnet, env, 0); len(got) != 0 {
		t.Fatalf("JsonnetCodeValuePasses() with no pass = %q, want none", got)
	}
}

// TestJsonnetCodeImportLiterals pins the import paths of one code value, in
// source order, and that a value that does not parse has none.
func TestJsonnetCodeImportLiterals(t *testing.T) {
	got := JsonnetCodeImportLiterals("{ a: import 'a.libsonnet', b: importstr 'b.txt', c: importbin 'c.bin' }")
	if strings.Join(got, ",") != "a.libsonnet,b.txt,c.bin" {
		t.Fatalf("JsonnetCodeImportLiterals() = %v", got)
	}
	if got := JsonnetCodeImportLiterals("import 'a.libsonnet' {"); len(got) != 0 {
		t.Fatalf("JsonnetCodeImportLiterals() of code that does not parse = %v, want none", got)
	}
}

// TestDirectoryJsonnetImportPathsFollowsEveryPass pins that the scan
// follows the imports of code in every env substitution pass. With two
// Jsonnet files, the first file reads "$${ARGOCD_APP_NAME}" as
// "${ARGOCD_APP_NAME}" and the second as the Application name, so both
// paths are inputs. With one Jsonnet file, only the first pass is.
func TestDirectoryJsonnetImportPathsFollowsEveryPass(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "a.jsonnet"), "std.extVar('cfg')\n")
	writeFile(t, filepath.Join(root, "shared", "demo.libsonnet"), "{}\n")
	sourceJsonnet := argoappv1.ApplicationSourceJsonnet{
		ExtVars: []argoappv1.JsonnetVar{{Name: "cfg", Value: "import '../shared/$${ARGOCD_APP_NAME}.libsonnet'", Code: true}},
	}
	env := argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "demo"}}
	got, err := DirectoryJsonnetImportPaths(t.Context(), root, "app", sourceJsonnet, env)
	if err != nil {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v", err)
	}
	if want := "shared/${ARGOCD_APP_NAME}.libsonnet"; strings.Join(got, ",") != want {
		t.Fatalf("DirectoryJsonnetImportPaths() with one Jsonnet file = %v, want %v", got, want)
	}
	writeFile(t, filepath.Join(root, "app", "b.jsonnet"), "std.extVar('cfg')\n")
	got, err = DirectoryJsonnetImportPaths(t.Context(), root, "app", sourceJsonnet, env)
	if err != nil {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v", err)
	}
	if want := "shared/${ARGOCD_APP_NAME}.libsonnet,shared/demo.libsonnet"; strings.Join(got, ",") != want {
		t.Fatalf("DirectoryJsonnetImportPaths() with two Jsonnet files = %v, want %v", got, want)
	}
}
