package render

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGeneratedKustomizeWorkspacePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"", ".", "../escape", "nested/../../escape"} {
		if got, err := generatedKustomizeWorkspacePath(root, rel); err == nil {
			t.Fatalf("generatedKustomizeWorkspacePath(%q) = %q, nil; want error", rel, got)
		}
	}
}

func TestGeneratedKustomizeWorkspacePathRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges are not guaranteed on Windows")
	}
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if got, err := generatedKustomizeWorkspacePath(root, "link/generated.yaml"); err == nil {
		t.Fatalf("generatedKustomizeWorkspacePath(symlink) = %q, nil; want error", got)
	}
}
