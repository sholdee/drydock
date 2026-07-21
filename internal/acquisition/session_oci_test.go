package acquisition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sholdee/drydock/internal/ociartifact"
)

type fakeOCIAcquirer struct {
	mu           sync.Mutex
	resolveCalls int
	extractCalls int
	digest       string
	extractRoot  string
	// symlinks are created inside the extraction dir (name -> target) to
	// exercise the copy-into-session symlink guard.
	symlinks map[string]string
}

func (a *fakeOCIAcquirer) Resolve(_ context.Context, _, _ string, _ ociartifact.Options) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolveCalls++
	return a.digest, nil
}

func (a *fakeOCIAcquirer) Extract(_ context.Context, _, _ string, opts ociartifact.Options) (string, func(), error) {
	a.mu.Lock()
	a.extractCalls++
	a.mu.Unlock()
	dir, err := os.MkdirTemp(a.extractRoot, "extract-*")
	if err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "artifact.yaml"), []byte("kind: Fixture\n"), 0o600); err != nil {
		return "", nil, err
	}
	for name, target := range a.symlinks {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			return "", nil, err
		}
	}
	if opts.OnAcquired != nil {
		opts.OnAcquired(false)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func newOCITestSession(t *testing.T) (Session, string) {
	t.Helper()
	snapshotRoot := t.TempDir()
	return Session{
		Locks:              NewTargetLocks(),
		SnapshotRoot:       snapshotRoot,
		SnapshotCacheReads: true,
		SnapshotCache:      NewSnapshotCache(),
	}, snapshotRoot
}

func TestOCIArtifactAcquirerMemoizesResolveAndExtract(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	fake := &fakeOCIAcquirer{digest: digest, extractRoot: t.TempDir()}
	session, snapshotRoot := newOCITestSession(t)
	acquirer := session.OCIArtifactAcquirer(fake)
	opts := ociartifact.Options{CacheDir: t.TempDir()}
	repoURL := "oci://registry.example/org/app"

	got, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts)
	if err != nil || got != digest {
		t.Fatalf("Resolve() = %q, %v", got, err)
	}
	if _, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts); err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if fake.resolveCalls != 1 {
		t.Fatalf("delegate resolve calls = %d, want 1 (memoized)", fake.resolveCalls)
	}

	acquired := 0
	opts.OnAcquired = func(bool) { acquired++ }
	dir1, release1, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	dir2, release2, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("second Extract() error = %v", err)
	}
	if fake.extractCalls != 1 {
		t.Fatalf("delegate extract calls = %d, want 1 (memoized)", fake.extractCalls)
	}
	if acquired != 1 {
		t.Fatalf("OnAcquired calls = %d, want 1 (memo hits must not re-report acquisition)", acquired)
	}
	if dir1 != dir2 {
		t.Fatalf("memoized extract dirs differ: %q vs %q", dir1, dir2)
	}
	if !strings.HasPrefix(dir1, snapshotRoot+string(filepath.Separator)) {
		t.Fatalf("extract dir %q not inside session snapshot root %q", dir1, snapshotRoot)
	}
	if _, err := os.Stat(filepath.Join(dir1, "artifact.yaml")); err != nil {
		t.Fatalf("snapshot content missing: %v", err)
	}
	// The session, not per-call releases, owns the snapshot lifetime.
	release1()
	release2()
	if _, err := os.Stat(filepath.Join(dir1, "artifact.yaml")); err != nil {
		t.Fatalf("snapshot removed by per-call release: %v", err)
	}
}

// TestOCIArtifactAcquirerClosesDelegateReleaseAfterSnapshot pins that argo's
// extraction closer is closed immediately after the snapshot copy: the
// delegate's temp extraction dir must be gone while the snapshot survives.
func TestOCIArtifactAcquirerClosesDelegateReleaseAfterSnapshot(t *testing.T) {
	digest := "sha256:" + strings.Repeat("cd", 32)
	extractRoot := t.TempDir()
	fake := &fakeOCIAcquirer{digest: digest, extractRoot: extractRoot}
	session, _ := newOCITestSession(t)
	acquirer := session.OCIArtifactAcquirer(fake)

	dir, _, err := acquirer.Extract(t.Context(), "oci://registry.example/org/app", digest, ociartifact.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	entries, err := os.ReadDir(extractRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("delegate extraction dir not released after snapshot copy: %d entries remain", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "artifact.yaml")); err != nil {
		t.Fatalf("snapshot content missing after delegate release: %v", err)
	}
}

// TestOCIArtifactAcquirerRejectsOutOfBoundsSymlinks pins the defense-in-depth
// guard at the copy-into-session step: an extracted tree whose symlinks
// escape it never enters the session snapshot area.
func TestOCIArtifactAcquirerRejectsOutOfBoundsSymlinks(t *testing.T) {
	digest := "sha256:" + strings.Repeat("aa", 32)
	for name, target := range map[string]string{
		"absolute-escape": "/etc/passwd",
		"relative-escape": "../outside.yaml",
		"nested-escape":   "sub/../../outside.yaml",
	} {
		fake := &fakeOCIAcquirer{digest: digest, extractRoot: t.TempDir(), symlinks: map[string]string{name: target}}
		session, snapshotRoot := newOCITestSession(t)
		acquirer := session.OCIArtifactAcquirer(fake)

		_, _, err := acquirer.Extract(t.Context(), "oci://registry.example/org/app", digest, ociartifact.Options{CacheDir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "out-of-bounds symlink") {
			t.Fatalf("Extract() error = %v, want out-of-bounds symlink rejection (%s)", err, name)
		}
		entries, readErr := os.ReadDir(snapshotRoot)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("rejected extraction reached the session snapshot area (%s)", name)
		}
	}
}

// In-bounds symlinks are legitimate artifact content and survive the copy.
func TestOCIArtifactAcquirerAllowsInBoundsSymlink(t *testing.T) {
	digest := "sha256:" + strings.Repeat("bb", 32)
	fake := &fakeOCIAcquirer{digest: digest, extractRoot: t.TempDir(), symlinks: map[string]string{"link.yaml": "artifact.yaml"}}
	session, _ := newOCITestSession(t)
	acquirer := session.OCIArtifactAcquirer(fake)

	dir, _, err := acquirer.Extract(t.Context(), "oci://registry.example/org/app", digest, ociartifact.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	target, err := os.Readlink(filepath.Join(dir, "link.yaml"))
	if err != nil || target != "artifact.yaml" {
		t.Fatalf("in-bounds symlink not preserved in snapshot: %q, %v", target, err)
	}
}

// TestOCIArtifactAcquirerRejectsEmptySnapshotRoot pins the snapshot-root
// guard: with snapshot reads on and no root, snapshotCachePath would pass the
// extraction dir through unchanged, the delegate release would delete it, and
// the deleted path would be memoized and returned.
func TestOCIArtifactAcquirerRejectsEmptySnapshotRoot(t *testing.T) {
	digest := "sha256:" + strings.Repeat("cc", 32)
	fake := &fakeOCIAcquirer{digest: digest, extractRoot: t.TempDir()}
	session := Session{
		Locks:              NewTargetLocks(),
		SnapshotCacheReads: true,
		SnapshotCache:      NewSnapshotCache(),
	}
	acquirer := session.OCIArtifactAcquirer(fake)

	_, _, err := acquirer.Extract(t.Context(), "oci://registry.example/org/app", digest, ociartifact.Options{CacheDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "snapshot root is empty") {
		t.Fatalf("Extract() error = %v, want empty snapshot root rejection", err)
	}
	if fake.extractCalls != 0 {
		t.Fatalf("delegate extract calls = %d, want 0 (guard fires before the doomed extraction)", fake.extractCalls)
	}
}

func TestOCIArtifactAcquirerPassthroughWithoutLocks(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ef", 32)
	fake := &fakeOCIAcquirer{digest: digest, extractRoot: t.TempDir()}
	acquirer := Session{}.OCIArtifactAcquirer(fake)
	if _, ok := acquirer.(*fakeOCIAcquirer); !ok {
		t.Fatalf("lock-less session should return the delegate unchanged, got %T", acquirer)
	}
}
