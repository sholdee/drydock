package filedigest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathDigestFileContentChangesRotateDigest(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "apps/demo/values.yaml", "replicas: 1\n")

	first := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/values.yaml"}})
	writeDigestFile(t, root, "apps/demo/values.yaml", "replicas: 2\n")
	changed := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/values.yaml"}})

	if changed.Digest == first.Digest {
		t.Fatalf("PathDigest() digest did not change after file content changed")
	}
}

func TestPathDigestDirectoryContentAndMembershipChangesRotateDigest(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "apps/demo/a.yaml", "a: 1\n")
	writeDigestFile(t, root, "apps/other/a.yaml", "a: 1\n")

	first := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo"}})

	writeDigestFile(t, root, "apps/other/a.yaml", "a: 2\n")
	unrelated := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo"}})
	if unrelated.Digest != first.Digest {
		t.Fatalf("PathDigest() digest changed after unrelated directory edit: got %q, want %q", unrelated.Digest, first.Digest)
	}

	writeDigestFile(t, root, "apps/demo/a.yaml", "a: 2\n")
	contentChanged := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo"}})
	if contentChanged.Digest == first.Digest {
		t.Fatalf("PathDigest() digest did not change after child file content changed")
	}

	writeDigestFile(t, root, "apps/demo/b.yaml", "b: 1\n")
	membershipChanged := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo"}})
	if membershipChanged.Digest == contentChanged.Digest {
		t.Fatalf("PathDigest() digest did not change after directory membership changed")
	}
}

func TestPathDigestIncludesUntrackedAndIgnoredLikeFiles(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "apps/demo/cm.yaml", "kind: ConfigMap\n")
	writeDigestFile(t, root, "apps/demo/.gitignore", "*.tmp\n")

	first := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo"}})
	writeDigestFile(t, root, "apps/demo/local.tmp", "ignored by convention, still render-visible\n")
	ignoredLike := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo"}})

	if ignoredLike.Digest == first.Digest {
		t.Fatalf("PathDigest() digest did not change after ignored-like file was added")
	}
}

func TestPathDigestRequiredMissingErrorsAndOptionalMissingRotatesOnAdd(t *testing.T) {
	root := t.TempDir()

	_, err := PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "apps/demo/missing.yaml"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() error = nil, want required missing error")
	}
	if !strings.Contains(err.Error(), "required path is missing") {
		t.Fatalf("PathDigest() error = %q, want required missing message", err)
	}

	missing := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/values.yaml", Optional: true}})
	writeDigestFile(t, root, "apps/demo/values.yaml", "replicas: 1\n")
	added := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/values.yaml", Optional: true}})
	if added.Digest == missing.Digest {
		t.Fatalf("PathDigest() digest did not change when optional missing path became present")
	}

	if err := os.Remove(filepath.Join(root, "apps", "demo", "values.yaml")); err != nil {
		t.Fatalf("Remove(values.yaml) error = %v", err)
	}
	deleted := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/values.yaml", Optional: true}})
	if deleted.Digest != missing.Digest {
		t.Fatalf("PathDigest() optional missing digest after delete = %q, want original %q", deleted.Digest, missing.Digest)
	}
}

func TestPathDigestSyntheticMarkerDoesNotStatFilesystem(t *testing.T) {
	root := t.TempDir()

	first := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/*.values.yaml", MarkerKind: "missing-glob"}})
	second := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/*.values.yaml", MarkerKind: "missing-glob"}})
	if second.Digest != first.Digest {
		t.Fatalf("PathDigest() synthetic marker digest = %q, want stable %q", second.Digest, first.Digest)
	}

	changed := pathDigestForTest(t, root, []PathDigestPath{{Path: "apps/demo/*.values.yaml", MarkerKind: "missing-optional-glob"}})
	if changed.Digest == first.Digest {
		t.Fatalf("PathDigest() digest did not change after marker kind changed")
	}
}

func TestPathDigestSortsDeduplicatesAndRequiredWins(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "apps/demo/a.yaml", "a: 1\n")
	writeDigestFile(t, root, "apps/demo/b.yaml", "b: 1\n")

	normalized := pathDigestForTest(t, root, []PathDigestPath{
		{Path: "apps/demo/a.yaml"},
		{Path: "apps/demo/b.yaml"},
	})
	unsortedDuplicate := pathDigestForTest(t, root, []PathDigestPath{
		{Path: `apps\demo\.\b.yaml`},
		{Path: "apps/demo/a.yaml"},
		{Path: "apps/demo/b.yaml", Optional: true},
	})
	if unsortedDuplicate.Digest != normalized.Digest {
		t.Fatalf("PathDigest() digest = %q, want normalized digest %q", unsortedDuplicate.Digest, normalized.Digest)
	}

	_, err := PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths: []PathDigestPath{
			{Path: "apps/demo/missing.yaml", Optional: true},
			{Path: "apps/demo/missing.yaml"},
		},
	})
	if err == nil {
		t.Fatalf("PathDigest() error = nil, want required duplicate to win")
	}
}

func TestPathDigestExecutableModeClassRotatesDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not provide a stable executable mode bit")
	}

	root := t.TempDir()
	path := filepath.Join(root, "scripts", "render.sh")
	writeDigestFile(t, root, "scripts/render.sh", "#!/bin/sh\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod(0644) error = %v", err)
	}
	regular := pathDigestForTest(t, root, []PathDigestPath{{Path: "scripts/render.sh"}})

	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("Chmod(0755) error = %v", err)
	}
	executable := pathDigestForTest(t, root, []PathDigestPath{{Path: "scripts/render.sh"}})

	if executable.Digest == regular.Digest {
		t.Fatalf("PathDigest() digest did not change after executable bit changed")
	}
}

func TestPathDigestRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "target.yaml", "kind: ConfigMap\n")
	if err := os.Symlink("target.yaml", filepath.Join(root, "linked.yaml")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "linked.yaml"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() symlink path error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("PathDigest() symlink path error = %q, want symlink rejection", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "real-dir"), 0o755); err != nil {
		t.Fatalf("MkdirAll(real-dir) error = %v", err)
	}
	writeDigestFile(t, root, "real-dir/cm.yaml", "kind: ConfigMap\n")
	if err := os.Symlink("real-dir", filepath.Join(root, "linked-dir")); err != nil {
		t.Fatalf("Symlink(linked-dir) error = %v", err)
	}
	_, err = PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "linked-dir/cm.yaml"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() symlink component error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("PathDigest() symlink component error = %q, want symlink rejection", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "apps", "demo"), 0o755); err != nil {
		t.Fatalf("MkdirAll(apps/demo) error = %v", err)
	}
	if err := os.Symlink("../../target.yaml", filepath.Join(root, "apps", "demo", "linked.yaml")); err != nil {
		t.Fatalf("Symlink(nested) error = %v", err)
	}
	_, err = PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "apps/demo"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() nested symlink error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("PathDigest() nested symlink error = %q, want symlink rejection", err)
	}
}

func TestPathDigestRejectsGitDirectory(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "apps/demo/.git/config", "[core]\n")

	_, err := PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "apps/demo"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() .git error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), ".git") {
		t.Fatalf("PathDigest() .git error = %q, want .git rejection", err)
	}
}

func TestPathDigestRejectsForbiddenRoots(t *testing.T) {
	root := t.TempDir()
	forbidden := filepath.Join(root, ".drydock-cache")
	writeDigestFile(t, root, ".drydock-cache/render.db", "cache\n")

	_, err := PathDigest(t.Context(), PathDigestInput{
		RepoRoot:       root,
		ForbiddenRoots: []string{forbidden},
		Paths:          []PathDigestPath{{Path: ".drydock-cache"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() forbidden root error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "forbidden root") {
		t.Fatalf("PathDigest() forbidden root error = %q, want forbidden root rejection", err)
	}

	cacheRoot := filepath.Join(t.TempDir(), "cache")
	repoInsideCache := filepath.Join(cacheRoot, "repo")
	writeDigestFile(t, repoInsideCache, "apps/demo/cm.yaml", "kind: ConfigMap\n")
	_, err = PathDigest(t.Context(), PathDigestInput{
		RepoRoot:       repoInsideCache,
		ForbiddenRoots: []string{cacheRoot},
		Paths:          []PathDigestPath{{Path: "apps/demo/cm.yaml"}},
	})
	if err == nil {
		t.Fatalf("PathDigest() forbidden ancestor error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "forbidden root") {
		t.Fatalf("PathDigest() forbidden ancestor error = %q, want forbidden root rejection", err)
	}
}

func TestPathDigestRejectsInvalidPaths(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "apps/demo/cm.yaml", "kind: ConfigMap\n")

	for _, testCase := range []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "posix absolute", path: "/apps/demo/cm.yaml"},
		{name: "windows absolute slash", path: `C:/repo/apps/demo/cm.yaml`},
		{name: "windows absolute backslash", path: `C:\repo\apps\demo\cm.yaml`},
		{name: "windows drive relative", path: `C:apps\demo\cm.yaml`},
		{name: "parent escape", path: "../apps/demo/cm.yaml"},
		{name: "cleaned parent escape", path: "apps/../../demo/cm.yaml"},
		{name: "nul", path: "apps/demo/cm.yaml\x00other"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := PathDigest(t.Context(), PathDigestInput{
				RepoRoot: root,
				Paths:    []PathDigestPath{{Path: testCase.path, Optional: true}},
			})
			if err == nil {
				t.Fatalf("PathDigest() error = nil, want invalid path error")
			}
			if !strings.Contains(err.Error(), "path") {
				t.Fatalf("PathDigest() error = %q, want path error", err)
			}
		})
	}
}

func TestPathDigestCanceledContext(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "apps/demo/cm.yaml", "kind: ConfigMap\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := PathDigest(ctx, PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "apps/demo/cm.yaml"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PathDigest() error = %v, want context.Canceled", err)
	}
	if result != (PathDigestResult{}) {
		t.Fatalf("PathDigest() result = %#v, want zero result", result)
	}
}

func BenchmarkPathDigestDirectoryTree(b *testing.B) {
	root := b.TempDir()
	for dir := range 20 {
		for file := range 25 {
			writeDigestFile(b, root, filepath.ToSlash(filepath.Join("apps", "demo", "dir-"+string(rune('a'+dir)), "file-"+string(rune('a'+file))+".yaml")), strings.Repeat("kind: ConfigMap\n", 8))
		}
	}

	ctx := context.Background()
	input := PathDigestInput{
		RepoRoot: root,
		Paths:    []PathDigestPath{{Path: "apps/demo"}},
	}
	b.ResetTimer()
	for range b.N {
		if _, err := PathDigest(ctx, input); err != nil {
			b.Fatalf("PathDigest() error = %v", err)
		}
	}
}

func TestPathDigestContentCacheByteIdentityAndReuse(t *testing.T) {
	repoRoot := t.TempDir()
	target := filepath.Join(repoRoot, "manifests", "demo", "cm.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	paths := []PathDigestPath{{Path: "manifests/demo"}}
	cache := NewContentDigestCache()

	cached, err := PathDigest(context.Background(), PathDigestInput{RepoRoot: repoRoot, Paths: paths, ContentCache: cache})
	if err != nil {
		t.Fatalf("PathDigest(cache) error = %v", err)
	}
	uncached, err := PathDigest(context.Background(), PathDigestInput{RepoRoot: repoRoot, Paths: paths})
	if err != nil {
		t.Fatalf("PathDigest(nil) error = %v", err)
	}
	if cached.Digest != uncached.Digest {
		t.Fatalf("cache must not change digest values: %s != %s", cached.Digest, uncached.Digest)
	}

	// The cache is run-scoped: a content edit is invisible through the same
	// cache instance (callers own staleness) but visible without it.
	if err := os.WriteFile(target, []byte("a: 2\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	stale, err := PathDigest(context.Background(), PathDigestInput{RepoRoot: repoRoot, Paths: paths, ContentCache: cache})
	if err != nil {
		t.Fatalf("PathDigest(warm cache) error = %v", err)
	}
	if stale.Digest != cached.Digest {
		t.Fatalf("warm cache digest = %s, want the cached value %s (proves the cache is consulted)", stale.Digest, cached.Digest)
	}
	fresh, err := PathDigest(context.Background(), PathDigestInput{RepoRoot: repoRoot, Paths: paths})
	if err != nil {
		t.Fatalf("PathDigest(nil, post-edit) error = %v", err)
	}
	if fresh.Digest == cached.Digest {
		t.Fatalf("nil-cache digest must see the edit")
	}
}

func pathDigestForTest(t *testing.T, root string, paths []PathDigestPath) PathDigestResult {
	t.Helper()
	result, err := PathDigest(t.Context(), PathDigestInput{
		RepoRoot: root,
		Paths:    paths,
	})
	if err != nil {
		t.Fatalf("PathDigest() error = %v", err)
	}
	return result
}

type testingWriter interface {
	Helper()
	Fatalf(format string, args ...any)
}

func writeDigestFile(t testingWriter, root, name, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", target, err)
	}
}
