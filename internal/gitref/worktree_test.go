package gitref

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWorktreeHeadIdentityCleanCheckout(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	hash := commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")

	revision, err := WorktreeHeadIdentity(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeHeadIdentity() error = %v", err)
	}
	if revision != hash.String() {
		t.Fatalf("WorktreeHeadIdentity() = %q, want HEAD %q", revision, hash.String())
	}
}

func TestWorktreeStatusCleanCheckout(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	hash := commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")

	status, err := WorktreeStatus(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeStatus() error = %v", err)
	}
	if status.State != WorktreeStateClean || status.Revision != hash.String() {
		t.Fatalf("WorktreeStatus() = %#v, want clean HEAD %q", status, hash.String())
	}
}

func TestWorktreeStatusDirtyTrackedFile(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	hash := commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")
	if err := os.WriteFile(filepath.Join(root, "manifests", "cm.yaml"), []byte("kind: Secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	status, err := WorktreeStatus(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeStatus() error = %v", err)
	}
	if status.State != WorktreeStateDirty || status.Revision != hash.String() {
		t.Fatalf("WorktreeStatus() = %#v, want dirty HEAD %q", status, hash.String())
	}
}

func TestWorktreeStatusDirtyTrackedExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not provide a stable executable mode bit")
	}
	root, repo, wt := newSnapshotRepo(t)
	hash := commitSnapshotFile(t, repo, wt, "scripts/render.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(root, "scripts", "render.sh"), 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	status, err := WorktreeStatus(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeStatus() error = %v", err)
	}
	if status.State != WorktreeStateDirty || status.Revision != hash.String() {
		t.Fatalf("WorktreeStatus() = %#v, want dirty HEAD %q", status, hash.String())
	}
}

func TestWorktreeStatusDirtyUntrackedFile(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	hash := commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")
	if err := os.WriteFile(filepath.Join(root, "scratch.yaml"), []byte("kind: ConfigMap\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	status, err := WorktreeStatus(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeStatus() error = %v", err)
	}
	if status.State != WorktreeStateDirty || status.Revision != hash.String() {
		t.Fatalf("WorktreeStatus() = %#v, want dirty HEAD %q", status, hash.String())
	}
}

func TestWorktreeStatusNonRepositoryIsUnknown(t *testing.T) {
	status, err := WorktreeStatus(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("WorktreeStatus() error = %v", err)
	}
	if status.State != WorktreeStateUnknown || status.Revision != "" {
		t.Fatalf("WorktreeStatus() = %#v, want unknown non-repository", status)
	}
}

func TestWorktreeHeadIdentityModifiedTrackedFileIsDirty(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")
	if err := os.WriteFile(filepath.Join(root, "manifests", "cm.yaml"), []byte("kind: Secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	revision, err := WorktreeHeadIdentity(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeHeadIdentity() error = %v", err)
	}
	if revision != "" {
		t.Fatalf("WorktreeHeadIdentity() = %q, want empty for modified file", revision)
	}
}

func TestWorktreeHeadIdentityUntrackedFileIsDirty(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")
	if err := os.WriteFile(filepath.Join(root, "scratch.yaml"), []byte("kind: ConfigMap\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	revision, err := WorktreeHeadIdentity(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeHeadIdentity() error = %v", err)
	}
	if revision != "" {
		t.Fatalf("WorktreeHeadIdentity() = %q, want empty for untracked file", revision)
	}
}

func TestWorktreeHeadIdentityIgnoredFileIsDirty(t *testing.T) {
	// Ignored files are dirt deliberately: render engines read the filesystem,
	// and a gitignored values file changes render output while being invisible
	// to a tracked-only check.
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, ".gitignore", "ignored/\n")
	if err := os.MkdirAll(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "values.yaml"), []byte("a: 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	revision, err := WorktreeHeadIdentity(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeHeadIdentity() error = %v", err)
	}
	if revision != "" {
		t.Fatalf("WorktreeHeadIdentity() = %q, want empty for ignored file", revision)
	}
}

func TestWorktreeHeadIdentityMissingTrackedFileIsDirty(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")
	commitSnapshotFile(t, repo, wt, "manifests/other.yaml", "kind: ConfigMap\n")
	if err := os.Remove(filepath.Join(root, "manifests", "cm.yaml")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	revision, err := WorktreeHeadIdentity(context.Background(), root)
	if err != nil {
		t.Fatalf("WorktreeHeadIdentity() error = %v", err)
	}
	if revision != "" {
		t.Fatalf("WorktreeHeadIdentity() = %q, want empty for missing tracked file", revision)
	}
}

func TestWorktreeHeadIdentityNonRepositoryErrors(t *testing.T) {
	if _, err := WorktreeHeadIdentity(context.Background(), t.TempDir()); err == nil {
		t.Fatalf("WorktreeHeadIdentity() error = nil, want error for non-repository path")
	}
}

func TestWorktreeHeadIdentityCanceledContext(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	revision, err := WorktreeHeadIdentity(ctx, root)
	if err == nil {
		t.Fatalf("WorktreeHeadIdentity() error = nil, want cancellation")
	}
	if revision != "" {
		t.Fatalf("WorktreeHeadIdentity() = %q, want empty on cancellation", revision)
	}
}

func TestAnyWorktreeFileDiffersContextCanceledDuringTrackedComparison(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "manifests/cm.yaml", "kind: ConfigMap\n")
	headTree, err := treeForRef(repo, "HEAD")
	if err != nil {
		t.Fatalf("treeForRef() error = %v", err)
	}
	files, err := headTreeFiles(headTree)
	if err != nil {
		t.Fatalf("headTreeFiles() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = anyWorktreeFileDiffers(ctx, root, files)
	if err == nil {
		t.Fatalf("anyWorktreeFileDiffers() error = nil, want cancellation")
	}
}

func TestWorktreeStatusDetectsSameSizeContentEdit(t *testing.T) {
	dir, _, wt := newSnapshotRepo(t)
	writeChangedPathFileForTest(t, filepath.Join(dir, "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	// Same byte length, different content: only a content comparison can
	// catch this. This pins the blob-hash comparison's exactness.
	writeChangedPathFileForTest(t, filepath.Join(dir, "cm.yaml"), "a: 2\n")

	status, err := WorktreeStatus(context.Background(), dir)
	if err != nil || status.State != WorktreeStateDirty {
		t.Fatalf("status = %+v, %v, want dirty", status, err)
	}
}

func TestWorktreeStatusCleanAfterMtimeTouch(t *testing.T) {
	dir, _, wt := newSnapshotRepo(t)
	writeChangedPathFileForTest(t, filepath.Join(dir, "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "cm.yaml"), now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	status, err := WorktreeStatus(context.Background(), dir)
	if err != nil || status.State != WorktreeStateClean {
		t.Fatalf("status = %+v, %v, want clean (content identical)", status, err)
	}
}

func TestWorktreeStatusContextCancellation(t *testing.T) {
	dir, _, wt := newSnapshotRepo(t)
	writeChangedPathFileForTest(t, filepath.Join(dir, "cm.yaml"), "a: 1\n")
	commitAllSnapshotFiles(t, wt, "fixture")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := WorktreeStatus(ctx, dir)
	if err == nil {
		t.Fatalf("WorktreeStatus(canceled) error = nil, want context error")
	}
}
