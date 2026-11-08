package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sholdee/drydock/internal/render"
)

// TestSymlinkBoundsMemoInvalidatesAfterSubmoduleFill pins that a change to
// the submodule content of a snapshot root makes the out-of-bounds symlink
// check walk the root again: content that the fill adds can hold such a
// symlink.
func TestSymlinkBoundsMemoInvalidatesAfterSubmoduleFill(t *testing.T) {
	root := t.TempDir()
	memo := newSymlinkBoundsMemo()
	file, err := memo.findOutOfBoundsSymlink(t.Context(), root, render.OutOfBoundsSymlinkScopeRepository, false)
	if err != nil || file != "" {
		t.Fatalf("first check = %q, %v", file, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "sub", "escape.yaml")); err != nil {
		t.Skip(err)
	}
	if file, _ := memo.findOutOfBoundsSymlink(t.Context(), root, render.OutOfBoundsSymlinkScopeRepository, false); file != "" {
		t.Fatalf("kept check = %q, want the memo to keep the first answer until invalidated", file)
	}
	memo.invalidateUnder(root)
	file, err = memo.findOutOfBoundsSymlink(t.Context(), root, render.OutOfBoundsSymlinkScopeRepository, false)
	if err != nil || file == "" {
		t.Fatalf("check after invalidation = %q, %v; want the new symlink found", file, err)
	}
}
