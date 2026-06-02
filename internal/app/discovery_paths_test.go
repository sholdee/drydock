package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathMayContainDiscoveryObjectsSkipsUnreadableTrashDirectory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".Trash", "app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ignored
`)
	trash := filepath.Join(root, ".Trash")
	if err := os.Chmod(trash, 0); err != nil {
		t.Fatalf("chmod trash directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(trash, 0o700)
	})

	found, err := pathMayContainDiscoveryObjects(root)
	if err != nil {
		t.Fatalf("pathMayContainDiscoveryObjects() error = %v", err)
	}
	if found {
		t.Fatal("pathMayContainDiscoveryObjects() = true, want false for skipped trash directory")
	}
}
