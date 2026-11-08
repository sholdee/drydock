package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
)

// writeLFSDirectoryApp writes a directory Application whose only manifest
// lfs-cache/cm.yaml is committed as a Git LFS pointer to content, and
// returns the pointer text and the content.
func writeLFSDirectoryApp(t *testing.T, root string) (string, string) {
	t.Helper()
	writePersistentDirectoryCacheApp(t, root, "lfs-cache")
	content := `apiVersion: v1
kind: ConfigMap
metadata:
  name: lfs-cache
data:
  value: from-lfs
`
	sum := sha256.Sum256([]byte(content))
	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", hex.EncodeToString(sum[:]), len(content))
	writeTestFile(t, filepath.Join(root, ".gitattributes"), "manifests/lfs-cache/cm.yaml filter=lfs diff=lfs merge=lfs -text\n")
	writeTestFile(t, filepath.Join(root, "manifests", "lfs-cache", "cm.yaml"), pointer)
	return pointer, content
}

// TestPersistentCacheKeysSmudgedLFSFilesOnDiskContent pins that a working
// tree file whose content differs from its committed blob because git-lfs
// smudged it never reuses a cache entry keyed on the committed pointer
// blob. `git status` reports such a tree clean, because the LFS clean
// filter turns the content back into the pointer. drydock's own change set
// compares the bytes on disk with the blob, so the file is dirty and the
// key holds its content. The test runs both orders: pointer first, then
// smudged content, and the reverse.
func TestPersistentCacheKeysSmudgedLFSFilesOnDiskContent(t *testing.T) {
	for _, smudgedFirst := range []bool{false, true} {
		root := t.TempDir()
		pointer, content := writeLFSDirectoryApp(t, root)
		gitCommitAll(t, root, "lfs pointer")
		request := persistentBuildRequest(root, t.TempDir())
		states := []string{pointer, content}
		if smudgedFirst {
			states = []string{content, pointer}
		}
		for i, onDisk := range states {
			writeTestFile(t, filepath.Join(root, "manifests", "lfs-cache", "cm.yaml"), onDisk)
			result, err := (Orchestrator{}).Build(context.Background(), request)
			if err != nil {
				t.Fatalf("smudged first %t, run %d: Build() error = %v", smudgedFirst, i, err)
			}
			got := renderedConfigMapNames(result)
			wantRendered := onDisk == content
			if got["lfs-cache"] != wantRendered {
				t.Fatalf("smudged first %t, run %d: rendered ConfigMaps = %v, want lfs-cache rendered %t", smudgedFirst, i, got, wantRendered)
			}
		}
	}
}

func renderedConfigMapNames(result BuildResult) map[string]bool {
	out := map[string]bool{}
	for _, manifest := range result.Manifests {
		if manifest.Object.GetKind() == "ConfigMap" {
			out[manifest.Object.GetName()] = true
		}
	}
	return out
}
