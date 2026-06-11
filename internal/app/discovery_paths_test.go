package app

import (
	"os"
	"path/filepath"
	"sync"
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

func TestPathMayContainDiscoveryObjectsCachedMemoizes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.yaml"), []byte("kind: Application\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	memo := &sync.Map{}
	first, err := pathMayContainDiscoveryObjectsCached(memo, root)
	if err != nil || !first {
		t.Fatalf("first = %t, %v; want true, nil", first, err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	second, err := pathMayContainDiscoveryObjectsCached(memo, root)
	if err != nil || !second {
		t.Fatalf("second = %t, %v; want memoized true, nil", second, err)
	}
}
