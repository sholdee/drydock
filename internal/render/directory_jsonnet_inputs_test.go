package render

import (
	"path/filepath"
	"strings"
	"testing"
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

	got, err := DirectoryJsonnetImportPaths(root, "app", []string{"vendor"})
	if err != nil {
		t.Fatalf("DirectoryJsonnetImportPaths() error = %v", err)
	}
	want := []string{"other/target.libsonnet", "shared/lib.libsonnet", "shared/nested.libsonnet", "shared/text.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("DirectoryJsonnetImportPaths() = %v, want %v", got, want)
	}
}
