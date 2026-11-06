package pathsafety

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveSymlinkedRoot pins which roots drydock resolves at entry: a
// root that is itself a symlink. Other roots keep their spelling, also one
// with a symlink above it, so their paths in output do not change.
func TestResolveSymlinkedRoot(t *testing.T) {
	parent := t.TempDir()
	realDir := filepath.Join(parent, "real")
	if err := os.MkdirAll(filepath.Join(realDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(parent, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ root, want string }{
		{root: filepath.Join(parent, "link"), want: filepath.Join(realParent, "real")},
		{root: realDir, want: realDir},
		{root: filepath.Join(parent, "link", "sub"), want: filepath.Join(parent, "link", "sub")},
		{root: "", want: ""},
		{root: filepath.Join(parent, "missing"), want: filepath.Join(parent, "missing")},
	} {
		if got := ResolveSymlinkedRoot(tc.root); got != tc.want {
			t.Errorf("ResolveSymlinkedRoot(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}
	t.Chdir(parent)
	if got := ResolveSymlinkedRoot("link"); got != "real" {
		t.Errorf("ResolveSymlinkedRoot(link) = %q, want the relative target real", got)
	}
}
