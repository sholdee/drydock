package cache

import (
	"path/filepath"
	"testing"
)

func TestCacheLayoutPaths(t *testing.T) {
	root := filepath.Join("cache", "drydock")
	key := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if got, want := GitEntryPath(root, key), filepath.Join(root, key); got != want {
		t.Fatalf("GitEntryPath() = %q, want %q", got, want)
	}
	if got, want := ChartKindRoot(root, "http"), filepath.Join(root, "http"); got != want {
		t.Fatalf("ChartKindRoot() = %q, want %q", got, want)
	}
	if got, want := ChartEntryPath(root, "oci", key), filepath.Join(root, "oci", key); got != want {
		t.Fatalf("ChartEntryPath() = %q, want %q", got, want)
	}
	if got, want := RemoteEntryPath(root, key), filepath.Join(root, key); got != want {
		t.Fatalf("RemoteEntryPath() = %q, want %q", got, want)
	}
	if got, want := RemoteHTTPFilePath(root, key), filepath.Join(root, key, "resource.yaml"); got != want {
		t.Fatalf("RemoteHTTPFilePath() = %q, want %q", got, want)
	}
	if got, want := RemoteGitRepoPath(root, key), filepath.Join(root, key, "repo"); got != want {
		t.Fatalf("RemoteGitRepoPath() = %q, want %q", got, want)
	}
}
