package gitref

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestCommittedPathDigestFileUsesCommittedBlobIdentity(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	head := commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "kind: ConfigMap\n")

	first := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo/cm.yaml"},
	})
	if first.Revision != head.String() {
		t.Fatalf("CommittedPathDigest() revision = %q, want %q", first.Revision, head.String())
	}

	writeSnapshotFileForTest(t, filepath.Join(root, "apps", "demo", "cm.yaml"), "kind: Secret\n")
	dirtyWorktree := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo/cm.yaml"},
	})
	if dirtyWorktree.Digest != first.Digest {
		t.Fatalf("CommittedPathDigest() changed after uncommitted worktree edit: got %q, want %q", dirtyWorktree.Digest, first.Digest)
	}

	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "kind: Secret\n")
	changed := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo/cm.yaml"},
	})
	if changed.Digest == first.Digest {
		t.Fatalf("CommittedPathDigest() digest did not change after committed file edit")
	}
}

func TestCommittedPathDigestDirectoryUsesTreeIdentity(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "kind: ConfigMap\n")

	first := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo"},
	})

	commitSnapshotFile(t, repo, wt, "apps/other/cm.yaml", "kind: ConfigMap\n")
	unrelated := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo"},
	})
	if unrelated.Digest != first.Digest {
		t.Fatalf("CommittedPathDigest() changed for unrelated commit: got %q, want %q", unrelated.Digest, first.Digest)
	}

	commitSnapshotFile(t, repo, wt, "apps/demo/secret.yaml", "kind: Secret\n")
	changed := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo"},
	})
	if changed.Digest == first.Digest {
		t.Fatalf("CommittedPathDigest() digest did not change after committed directory tree edit")
	}
}

func TestCommittedPathDigestOptionalMissingMarkerInvalidatesOnAddAndDelete(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "kind: ConfigMap\n")

	missing := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo/values.yaml", Optional: true},
	})

	commitSnapshotFile(t, repo, wt, "apps/demo/values.yaml", "replicas: 1\n")
	added := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo/values.yaml", Optional: true},
	})
	if added.Digest == missing.Digest {
		t.Fatalf("CommittedPathDigest() digest did not change when optional missing path was added")
	}

	removeSnapshotFile(t, wt, "apps/demo/values.yaml")
	commitAllSnapshotFiles(t, wt, "delete values")
	deleted := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo/values.yaml", Optional: true},
	})
	if deleted.Digest != missing.Digest {
		t.Fatalf("CommittedPathDigest() missing marker = %q after delete, want original %q", deleted.Digest, missing.Digest)
	}
}

func TestCommittedPathDigestRequiredMissingPathErrors(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "kind: ConfigMap\n")

	_, err := CommittedPathDigest(t.Context(), PathDigestInput{
		RepoPath: root,
		Revision: "HEAD",
		Paths: []PathDigestPath{
			{Path: "apps/demo/missing.yaml"},
		},
	})
	if err == nil {
		t.Fatalf("CommittedPathDigest() error = nil, want required missing path error")
	}
	if !strings.Contains(err.Error(), "required Git path is missing") {
		t.Fatalf("CommittedPathDigest() error = %q, want required missing path message", err)
	}
}

func TestCommittedPathDigestSortsCanonicalizesAndDeduplicatesPaths(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "apps/demo/a.yaml", "a: 1\n")
	commitSnapshotFile(t, repo, wt, "apps/demo/b.yaml", "b: 1\n")

	normalized := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: "apps/demo/a.yaml"},
		{Path: "apps/demo/b.yaml"},
	})
	unsortedDuplicate := committedPathDigestForTest(t, root, "HEAD", []PathDigestPath{
		{Path: `apps\demo\.\b.yaml`},
		{Path: "apps/demo/a.yaml"},
		{Path: "apps/demo/b.yaml", Optional: true},
	})
	if unsortedDuplicate.Digest != normalized.Digest {
		t.Fatalf("CommittedPathDigest() = %q, want normalized digest %q", unsortedDuplicate.Digest, normalized.Digest)
	}
}

func TestCommittedPathDigestRejectsInvalidPaths(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "kind: ConfigMap\n")

	for _, testCase := range []struct {
		name string
		path string
	}{
		{name: "posix absolute", path: "/apps/demo/cm.yaml"},
		{name: "windows absolute slash", path: `C:/repo/apps/demo/cm.yaml`},
		{name: "windows absolute backslash", path: `C:\repo\apps\demo\cm.yaml`},
		{name: "windows drive relative", path: `C:apps\demo\cm.yaml`},
		{name: "parent escape", path: "../apps/demo/cm.yaml"},
		{name: "cleaned parent escape", path: "apps/../../demo/cm.yaml"},
		{name: "nul", path: "apps/demo/cm.yaml\x00other"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := CommittedPathDigest(t.Context(), PathDigestInput{
				RepoPath: root,
				Revision: "HEAD",
				Paths: []PathDigestPath{
					{Path: testCase.path, Optional: true},
				},
			})
			if err == nil {
				t.Fatalf("CommittedPathDigest() error = nil, want invalid path error")
			}
			if !strings.Contains(err.Error(), "git path") {
				t.Fatalf("CommittedPathDigest() error = %q, want git path error", err)
			}
		})
	}
}

func TestCommittedPathDigestCanceledContext(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSnapshotFile(t, repo, wt, "apps/demo/cm.yaml", "kind: ConfigMap\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := CommittedPathDigest(ctx, PathDigestInput{
		RepoPath: root,
		Revision: "HEAD",
		Paths: []PathDigestPath{
			{Path: "apps/demo/cm.yaml"},
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CommittedPathDigest() error = %v, want context.Canceled", err)
	}
	if result != (PathDigestResult{}) {
		t.Fatalf("CommittedPathDigest() result = %#v, want zero result", result)
	}
}

func TestCommittedPathDigestRejectsGitlinkSubmodule(t *testing.T) {
	root, repo, wt := newSnapshotRepo(t)
	commitSubmoduleEntryForTest(t, repo, wt, "vendor/dep")

	_, err := CommittedPathDigest(t.Context(), PathDigestInput{
		RepoPath: root,
		Revision: "HEAD",
		Paths: []PathDigestPath{
			{Path: "vendor/dep"},
		},
	})
	if err == nil {
		t.Fatalf("CommittedPathDigest() error = nil, want submodule rejection")
	}
	if !strings.Contains(err.Error(), "gitlink/submodule") {
		t.Fatalf("CommittedPathDigest() error = %q, want gitlink/submodule rejection", err)
	}

	_, err = CommittedPathDigest(t.Context(), PathDigestInput{
		RepoPath: root,
		Revision: "HEAD",
		Paths: []PathDigestPath{
			{Path: "vendor"},
		},
	})
	if err == nil {
		t.Fatalf("CommittedPathDigest() directory error = nil, want nested submodule rejection")
	}
	if !strings.Contains(err.Error(), "gitlink/submodule") {
		t.Fatalf("CommittedPathDigest() directory error = %q, want gitlink/submodule rejection", err)
	}
}

func committedPathDigestForTest(t *testing.T, repoPath, revision string, paths []PathDigestPath) PathDigestResult {
	t.Helper()
	result, err := CommittedPathDigest(t.Context(), PathDigestInput{
		RepoPath: repoPath,
		Revision: revision,
		Paths:    paths,
	})
	if err != nil {
		t.Fatalf("CommittedPathDigest() error = %v", err)
	}
	return result
}

func removeSnapshotFile(t *testing.T, wt *git.Worktree, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(wt.Filesystem.Root(), filepath.FromSlash(name))); err != nil {
		t.Fatalf("Remove(%s) error = %v", name, err)
	}
	if _, err := wt.Remove(name); err != nil {
		t.Fatalf("Remove(%s) from worktree error = %v", name, err)
	}
}

func commitSubmoduleEntryForTest(t *testing.T, repo *git.Repository, wt *git.Worktree, name string) plumbing.Hash {
	t.Helper()
	idx, err := repo.Storer.Index()
	if err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	entry := idx.Add(name)
	entry.Mode = filemode.Submodule
	entry.Hash = plumbing.NewHash("1111111111111111111111111111111111111111")
	if err := repo.Storer.SetIndex(idx); err != nil {
		t.Fatalf("SetIndex() error = %v", err)
	}
	hash, err := wt.Commit("add submodule "+name, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.invalid"},
	})
	if err != nil {
		t.Fatalf("Commit(submodule) error = %v", err)
	}
	return hash
}
