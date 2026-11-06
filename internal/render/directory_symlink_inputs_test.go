package render

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gitref"
)

// TestDirectorySymlinkInputPaths pins the paths outside a directory source
// that its symlinks read or would read: each symlink passed on the way, a
// symlinked directory component included, and the end path, even when it
// is missing. A path inside the source, or outside the repository, is left
// out.
func TestDirectorySymlinkInputPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "plain.yaml"), "a: 1\n")
	writeFile(t, filepath.Join(root, "targets", "end.yaml"), "a: 1\n")
	writeFile(t, filepath.Join(root, "common", "shared.yaml"), "a: 1\n")
	writeFile(t, filepath.Join(root, "libs", "jsonnet", "x.libsonnet"), "{}\n")
	symlink(t, "end.yaml", filepath.Join(root, "targets", "mid.yaml"))
	symlink(t, "../targets/mid.yaml", filepath.Join(root, "apps", "chain.yaml"))
	symlink(t, "common", filepath.Join(root, "shared"))
	symlink(t, "../shared/shared.yaml", filepath.Join(root, "apps", "via-dir.yaml"))
	symlink(t, "../targets/missing.yaml", filepath.Join(root, "apps", "dangling.yaml"))
	symlink(t, "plain.yaml", filepath.Join(root, "apps", "inside.yaml"))
	symlink(t, "/etc/hosts", filepath.Join(root, "apps", "outside.yaml"))
	symlink(t, "../../targets/end.yaml", filepath.Join(root, "libs", "jsonnet", "lib.yaml"))

	got := DirectorySymlinkInputPaths(root, []string{"apps", "libs/jsonnet"})
	want := []string{"common/shared.yaml", "shared", "targets/end.yaml", "targets/mid.yaml", "targets/missing.yaml"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("DirectorySymlinkInputPaths() = %v, want %v", got, want)
	}
}

// TestDirectoryInputDigestPathsCoverSymlinkTargets pins that the persistent
// render cache digest of a directory source covers the targets of its
// symlinks outside the source path, as optional paths.
func TestDirectoryInputDigestPathsCoverSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "targets", "end.yaml"), "a: 1\n")
	symlink(t, "../targets/end.yaml", filepath.Join(root, "apps", "linked.yaml"))
	symlink(t, "../targets/missing.yaml", filepath.Join(root, "apps", "dangling.yaml"))
	paths, err := DirectoryInputDigestPaths(t.Context(), ResolvedSource{RepoRoot: root, Path: "apps"}, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []gitref.PathDigestPath{
		{Path: "apps"},
		{Path: "targets/end.yaml", Optional: true},
		{Path: "targets/missing.yaml", Optional: true},
	}
	if len(paths) != len(want) {
		t.Fatalf("DirectoryInputDigestPaths() = %#v, want %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("DirectoryInputDigestPaths() = %#v, want %#v", paths, want)
		}
	}
}

// TestDirectorySymlinkInputPathsSkipsATreeOutsideTheRepository pins that a
// library reached through a symlink in a middle path part that points
// outside the repository is not walked: the renderer never reads there.
func TestDirectorySymlinkInputPathsSkipsATreeOutsideTheRepository(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	writeFile(t, filepath.Join(root, "targets", "x.yaml"), "a: 1\n")
	writeFile(t, filepath.Join(root, "app", "cm.yaml"), "a: 1\n")
	symlink(t, "../../repo/targets/x.yaml", filepath.Join(parent, "outside", "lib", "link.yaml"))
	symlink(t, "../outside", filepath.Join(root, "out"))
	if got := DirectorySymlinkInputPaths(root, []string{"app", "out/lib"}); len(got) != 0 {
		t.Fatalf("DirectorySymlinkInputPaths() = %v, want the outside tree not walked", got)
	}
}
