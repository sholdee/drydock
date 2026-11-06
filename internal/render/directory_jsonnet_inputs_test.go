package render

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDirectoryJsonnetImportPaths pins the files outside a directory source
// and its libraries that its Jsonnet files import: a relative import and
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

	got, err := DirectoryJsonnetImportPaths(t.Context(), root, "app", []string{"vendor"})
	if err != nil {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v", err)
	}
	want := []string{"other/target.libsonnet", "shared/lib.libsonnet", "shared/nested.libsonnet", "shared/text.txt"}
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
	big := filepath.Join(root, "app", "big.jsonnet")
	writeFile(t, big, "import '../shared/big.libsonnet'\n"+strings.Repeat(" ", maxJsonnetImportScanBytes))

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	got, err := DirectoryJsonnetImportPaths(ctx, root, "app", nil)
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
	if _, err := DirectoryJsonnetImportPaths(ctx, root, "app", nil); !errors.Is(err, ErrJsonnetImportScanBudget) {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v, want the budget error", err)
	}
	if _, err := DirectoryInputDigestPaths(ctx, ResolvedSource{RepoRoot: root, Path: "app"}, RenderOptions{}); !errors.Is(err, ErrJsonnetImportScanBudget) {
		t.Fatalf("DirectoryInputDigestPaths() error = %v, want the budget error", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the scan ran past its deadline")
	}
}
