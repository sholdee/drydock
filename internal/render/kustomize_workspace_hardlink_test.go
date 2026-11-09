package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/remote"
)

// The prepared Kustomize workspace holds repository files as hard links.
// These tests pin that no workspace write reaches a repository file through
// such a link. On a case-insensitive file system (macOS by default), the
// kustomization file of a base can end up linked rather than copied, when
// the graph walk names it with other letter case than the file on disk
// (base/Kustomization.yaml, or a reference BASE to the folder base). The
// rewrite of that file then went into the repository file.

const hardlinkTestGenerator = `apiVersion: builtin
kind: ConfigMapGenerator
metadata:
  name: generated
literals:
  - key=value
`

const hardlinkTestBaseKustomization = "# keep this comment\nresources:\n  - cm.yaml\n"

// TestPreparedKustomizeWorkspaceWritesKeepHardLinkedSources builds a
// workspace whose files are all hard links to the repository, as the
// case-insensitive mismatch does for a kustomization file, and runs each kind
// of workspace write. The repository must not change.
func TestPreparedKustomizeWorkspaceWritesKeepHardLinkedSources(t *testing.T) {
	writes := []struct {
		name    string
		wantErr string
		write   func(t *testing.T, repoRoot, workspaceRoot string) error
	}{
		{
			name: "kustomization rewrite",
			write: func(t *testing.T, repoRoot, workspaceRoot string) error {
				workspace := &kustomizeWorkspace{
					originalRepoRoot: repoRoot,
					tempRepoRoot:     workspaceRoot,
					sourceRoot:       filepath.Join(workspaceRoot, "app"),
					visited:          map[string]struct{}{},
				}
				return workspace.prepareKustomizationDir(context.Background(), filepath.Join(workspaceRoot, "app"), workspaceRoot, "")
			},
		},
		{
			name: "workspace file write",
			write: func(t *testing.T, _, workspaceRoot string) error {
				return writeWorkspaceFile(workspaceRoot, filepath.Join(workspaceRoot, "app", "base", "cm.yaml"), []byte("changed\n"))
			},
		},
		{
			name:    "generated file at a linked path",
			wantErr: "already exists in the source tree",
			write: func(t *testing.T, _, workspaceRoot string) error {
				return writeGeneratedKustomizeWorkspaceFile(filepath.Join(workspaceRoot, "app"), ".drydock/helm/collide.yaml", []byte("changed\n"))
			},
		},
		{
			name: "file copy over a linked path",
			write: func(t *testing.T, _, workspaceRoot string) error {
				replacement := filepath.Join(t.TempDir(), "replacement.yaml")
				writeFile(t, replacement, "changed\n")
				return copyRegularFile(replacement, filepath.Join(workspaceRoot, "app", "base", "cm.yaml"))
			},
		},
		{
			name: "remote copy over a linked tree",
			write: func(t *testing.T, _, workspaceRoot string) error {
				acquired := filepath.Join(t.TempDir(), "acquired")
				writeFile(t, filepath.Join(acquired, "collide.yaml"), "changed\n")
				return copyAcquiredRemoteKustomizeResource(acquired, filepath.Join(workspaceRoot, "app", ".drydock", "helm"))
			},
		},
	}
	for _, write := range writes {
		t.Run(write.name, func(t *testing.T) {
			repoRoot := filepath.Join(t.TempDir(), "repo")
			writeHardlinkTestRepo(t, repoRoot, "base", "kustomization.yaml")
			writeFile(t, filepath.Join(repoRoot, "app", ".drydock", "helm", "collide.yaml"), "original\n")
			workspaceRoot := filepath.Join(t.TempDir(), "workspace")
			if err := copyWorkspaceTreeWithForceCopy(repoRoot, workspaceRoot, workspaceCopyFiles{}); err != nil {
				t.Fatalf("copyWorkspaceTreeWithForceCopy() error = %v", err)
			}
			requireHardLink(t, filepath.Join(repoRoot, "app", "base", "kustomization.yaml"), filepath.Join(workspaceRoot, "app", "base", "kustomization.yaml"))
			before := snapshotHardlinkTestTree(t, repoRoot)

			err := write.write(t, repoRoot, workspaceRoot)
			switch {
			case write.wantErr == "" && err != nil:
				t.Fatalf("write error = %v", err)
			case write.wantErr != "" && (err == nil || !strings.Contains(err.Error(), write.wantErr)):
				t.Fatalf("write error = %v, want error containing %q", err, write.wantErr)
			}
			assertTreeUnchanged(t, repoRoot, before)
		})
	}
}

// TestPreparedKustomizeWorkspaceRewriteBreaksHardLink pins that the
// rewritten kustomization file is a new file in the workspace, with the
// rewrite in it, and that the repository file keeps its comment.
func TestPreparedKustomizeWorkspaceRewriteBreaksHardLink(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "repo")
	writeHardlinkTestRepo(t, repoRoot, "base", "kustomization.yaml")
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	if err := copyWorkspaceTreeWithForceCopy(repoRoot, workspaceRoot, workspaceCopyFiles{}); err != nil {
		t.Fatalf("copyWorkspaceTreeWithForceCopy() error = %v", err)
	}
	source := filepath.Join(repoRoot, "app", "base", "kustomization.yaml")
	linked := filepath.Join(workspaceRoot, "app", "base", "kustomization.yaml")
	requireHardLink(t, source, linked)

	workspace := &kustomizeWorkspace{
		originalRepoRoot: repoRoot,
		tempRepoRoot:     workspaceRoot,
		sourceRoot:       filepath.Join(workspaceRoot, "app"),
		visited:          map[string]struct{}{},
	}
	if err := workspace.prepareKustomizationDir(context.Background(), filepath.Join(workspaceRoot, "app"), workspaceRoot, ""); err != nil {
		t.Fatalf("prepareKustomizationDir() error = %v", err)
	}
	if got := readTestFile(t, source); got != hardlinkTestBaseKustomization {
		t.Fatalf("repository kustomization = %q, want %q", got, hardlinkTestBaseKustomization)
	}
	if got := readTestFile(t, linked); strings.Contains(got, "keep this comment") || !strings.Contains(got, "cm.yaml") {
		t.Fatalf("workspace kustomization = %q, want the rewrite", got)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	linkedInfo, err := os.Stat(linked)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceInfo, linkedInfo) {
		t.Fatalf("workspace kustomization still shares the repository file")
	}
}

// TestWorkspaceCopyFilesMatchByFileIdentity pins that the files a
// workspace copies are matched by the file on disk, not by path text. On a
// case-sensitive file system two path texts name one file only through a
// hard link, so the test lists the kustomization file under an alias path.
func TestWorkspaceCopyFilesMatchByFileIdentity(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "repo")
	writeHardlinkTestRepo(t, repoRoot, "base", "kustomization.yaml")
	source := filepath.Join(repoRoot, "app", "base", "kustomization.yaml")
	alias := filepath.Join(t.TempDir(), "alias-kustomization.yaml")
	if err := os.Link(source, alias); err != nil {
		t.Skipf("hard link unavailable: %v", err)
	}
	var forceCopy workspaceCopyFiles
	if err := forceCopy.add(alias); err != nil {
		t.Fatalf("add() error = %v", err)
	}
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	if err := copyWorkspaceTreeWithForceCopy(repoRoot, workspaceRoot, forceCopy); err != nil {
		t.Fatalf("copyWorkspaceTreeWithForceCopy() error = %v", err)
	}
	if sameTestFile(t, source, filepath.Join(workspaceRoot, "app", "base", "kustomization.yaml")) {
		t.Fatalf("kustomization file listed under another path was hard-linked, want a copy")
	}
	if !sameTestFile(t, filepath.Join(repoRoot, "app", "base", "cm.yaml"), filepath.Join(workspaceRoot, "app", "base", "cm.yaml")) {
		t.Fatalf("unlisted file was copied, want a hard link")
	}
}

// TestKustomizeRenderKeepsRepositoryOnCaseInsensitiveFileSystem is the
// reported reproduction. It runs only when the temp directory is on a
// case-insensitive file system.
func TestKustomizeRenderKeepsRepositoryOnCaseInsensitiveFileSystem(t *testing.T) {
	requireCaseInsensitiveTempDir(t)
	for _, shape := range []struct {
		name, ref, dirName, fileName string
	}{
		{name: "capital K file name", ref: "base", dirName: "base", fileName: "Kustomization.yaml"},
		{name: "reference in other case", ref: "BASE", dirName: "base", fileName: "kustomization.yaml"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			repoRoot := filepath.Join(t.TempDir(), "repo")
			writeHardlinkTestRepo(t, repoRoot, shape.dirName, shape.fileName)
			writeFile(t, filepath.Join(repoRoot, "app", "kustomization.yaml"), "resources:\n  - "+shape.ref+"\ngenerators:\n  - gen.yaml\n")
			before := snapshotHardlinkTestTree(t, repoRoot)

			manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: "app"}, RenderOptions{})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if !containsManifest(manifests, "ConfigMap", "demo") {
				t.Fatalf("Render() manifests = %v, want ConfigMap demo", manifests)
			}
			assertTreeUnchanged(t, repoRoot, before)
		})
	}
}

// TestPreparedKustomizeRenderKeepsSourceAndCacheTrees renders through every
// prepared-workspace trigger at once (generators, helmCharts,
// spec.source.kustomize options, and a remote base from a local Git cache)
// and requires the repository and the cache to stay byte- and
// mtime-identical. On a case-insensitive file system the local and the
// remote references also use other letter case than the folders.
func TestPreparedKustomizeRenderKeepsSourceAndCacheTrees(t *testing.T) {
	baseRef, subRef := "base", "sub"
	if caseInsensitiveTempDir(t) {
		baseRef, subRef = "BASE", "SUB"
	}
	top := t.TempDir()
	repoRoot := filepath.Join(top, "repo")
	writeHardlinkTestRepo(t, repoRoot, "base", "kustomization.yaml")
	writeFile(t, filepath.Join(repoRoot, "app", "kustomization.yaml"), `# source kustomization comment
resources:
  - `+baseRef+`
  - https://github.com/example/repo.git//remote-base?ref=HEAD
generators:
  - gen.yaml
helmCharts:
  - name: demo
    releaseName: demo
`)
	writeNamedTestChart(t, filepath.Join(repoRoot, "app", "charts", "demo"), "demo", "1.2.3", `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-chart
data:
  key: value
`)
	writeFile(t, filepath.Join(repoRoot, "components", "extra", "kustomization.yaml"), "# component comment\napiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources:\n  - cm.yaml\n")
	writeFile(t, filepath.Join(repoRoot, "components", "extra", "cm.yaml"), decodeTestConfigMap("component", "value"))
	writeFile(t, filepath.Join(repoRoot, "app", "patch.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  patched: \"true\"\n")

	origin := filepath.Join(top, "origin")
	writeFile(t, filepath.Join(origin, "remote-base", "kustomization.yaml"), "# remote comment\nresources:\n  - "+subRef+"\n")
	writeFile(t, filepath.Join(origin, "remote-base", "sub", "kustomization.yaml"), "# remote sub comment\nresources:\n  - cm.yaml\n")
	writeFile(t, filepath.Join(origin, "remote-base", "sub", "cm.yaml"), decodeTestConfigMap("remote", "value"))
	commitGitFixture(t, origin)
	cacheDir := filepath.Join(top, "cache")
	acquirer := &localGitCacheAcquirer{repoURL: "file://" + filepath.ToSlash(origin)}
	// Clone the base into the Git cache first, so the render reads a
	// cache hit and the snapshot covers the cache worktree it reads.
	seeded, err := acquirer.Acquire(context.Background(), remote.Request{Kind: remote.RequestGitRepo, Revision: "HEAD"}, remote.Options{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("seed Git cache: %v", err)
	}
	cacheRoot := seeded.Path

	repoBefore := snapshotHardlinkTestTree(t, repoRoot)
	cacheBefore := snapshotHardlinkTestTree(t, cacheRoot)

	manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: "app"}, RenderOptions{
		Kustomize: &argoappv1.ApplicationSourceKustomize{
			NamePrefix:   "p-",
			CommonLabels: map[string]string{"team": "a"},
			Components:   []string{"../components/extra"},
			Patches: argoappv1.KustomizePatches{{
				Path:   "patch.yaml",
				Target: &argoappv1.KustomizeSelector{Kind: "ConfigMap", Name: "demo"},
			}},
		},
		RemoteResourceAcquirer: acquirer,
		RemoteResourceCacheDir: cacheDir,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(acquirer.paths) != 2 || acquirer.paths[1] != cacheRoot {
		t.Fatalf("remote base read from %v, want the seeded Git cache %s", acquirer.paths, cacheRoot)
	}
	for _, name := range []string{"p-demo", "p-remote", "p-component", "p-demo-chart"} {
		if !containsManifest(manifests, "ConfigMap", name) {
			t.Fatalf("Render() manifests = %v, want ConfigMap %s", manifests, name)
		}
	}
	assertTreeUnchanged(t, repoRoot, repoBefore)
	assertTreeUnchanged(t, cacheRoot, cacheBefore)
}

// localGitCacheAcquirer acquires every remote Git ref from the local Git
// repository repoURL (a file:// URL) through the Git cache of
// remote.DefaultAcquirer, which refuses the file transport unless
// AllowFileTransport is set. It records the path of each result.
type localGitCacheAcquirer struct {
	repoURL string
	paths   []string
}

func (a *localGitCacheAcquirer) Acquire(ctx context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	request.URL = a.repoURL
	request.RepoURL = a.repoURL
	opts.AllowFileTransport = true
	result, err := remote.DefaultAcquirer{}.Acquire(ctx, request, opts)
	if err == nil {
		a.paths = append(a.paths, result.Path)
	}
	return result, err
}

// commitGitFixture makes root a Git repository with one commit of every
// file under it.
func commitGitFixture(t *testing.T, root string) {
	t.Helper()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("Worktree.Add() error = %v", err)
	}
	if _, err := worktree.Commit("fixture", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0)},
	}); err != nil {
		t.Fatalf("Worktree.Commit() error = %v", err)
	}
}

// writeHardlinkTestRepo writes app/kustomization.yaml, which reads the base
// folder dirName and runs a builtin generator, so rendering prepares a
// workspace, and the base kustomization fileName with a comment that a
// rewrite drops.
func writeHardlinkTestRepo(t *testing.T, repoRoot, dirName, fileName string) {
	t.Helper()
	writeFile(t, filepath.Join(repoRoot, "app", "kustomization.yaml"), "resources:\n  - "+dirName+"\ngenerators:\n  - gen.yaml\n")
	writeFile(t, filepath.Join(repoRoot, "app", "gen.yaml"), hardlinkTestGenerator)
	writeFile(t, filepath.Join(repoRoot, "app", dirName, fileName), hardlinkTestBaseKustomization)
	writeFile(t, filepath.Join(repoRoot, "app", dirName, "cm.yaml"), decodeTestConfigMap("demo", "value"))
}

type hardlinkTreeEntry struct {
	hash    string
	mode    fs.FileMode
	modTime time.Time
}

// snapshotHardlinkTestTree records the content hash, mode and mtime of
// every entry under root. It skips .git directories: the workspace never
// copies them, and Git may update its own files there.
func snapshotHardlinkTestTree(t *testing.T, root string) map[string]hardlinkTreeEntry {
	t.Helper()
	out := map[string]hardlinkTreeEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		record := hardlinkTreeEntry{mode: info.Mode(), modTime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			record.hash = hex.EncodeToString(sum[:])
		}
		out[filepath.ToSlash(rel)] = record
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]hardlinkTreeEntry) {
	t.Helper()
	after := snapshotHardlinkTestTree(t, root)
	for rel, want := range before {
		got, ok := after[rel]
		switch {
		case !ok:
			t.Errorf("%s: %s was removed", root, rel)
		case got.hash != want.hash:
			t.Errorf("%s: %s content changed", root, rel)
		case got.mode != want.mode:
			t.Errorf("%s: %s mode changed from %v to %v", root, rel, want.mode, got.mode)
		case !got.modTime.Equal(want.modTime):
			t.Errorf("%s: %s mtime changed from %v to %v", root, rel, want.modTime, got.modTime)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("%s: %s was added", root, rel)
		}
	}
}

func requireHardLink(t *testing.T, left, right string) {
	t.Helper()
	if !sameTestFile(t, left, right) {
		t.Skipf("workspace holds %s as a copy, not a hard link; the file system may not support hard links", right)
	}
}

func sameTestFile(t *testing.T, left, right string) bool {
	t.Helper()
	leftInfo, err := os.Lstat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Lstat(right)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(leftInfo, rightInfo)
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// caseInsensitiveTempDir reports whether the temp directory is on a
// case-insensitive file system: a file created as "probe" is found as
// "PROBE".
func caseInsensitiveTempDir(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	writeFile(t, probe, "probe\n")
	upper, err := os.Lstat(filepath.Join(dir, "PROBE"))
	if err != nil {
		return false
	}
	lower, err := os.Lstat(probe)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(lower, upper)
}

func requireCaseInsensitiveTempDir(t *testing.T) {
	t.Helper()
	if !caseInsensitiveTempDir(t) {
		t.Skip("temp directory is on a case-sensitive file system")
	}
}

// TestPreparedWorkspaceCopyRefusesSymlinkedDestination pins that no copy
// step writes through a symlink that the workspace holds. On a
// case-insensitive file system the copier could reach app/lnk, a symlink
// that the tree copy kept, under the name APP/lnk. The test builds that
// state on any file system: the repository holds app/lnk as a real folder,
// and the workspace holds app/lnk as a symlink to a folder outside it.
func TestPreparedWorkspaceCopyRefusesSymlinkedDestination(t *testing.T) {
	for _, target := range []string{"file", "folder"} {
		t.Run(target, func(t *testing.T) {
			top := t.TempDir()
			repoRoot := filepath.Join(top, "repo")
			writeFile(t, filepath.Join(repoRoot, "app", "lnk", "cm.yaml"), decodeTestConfigMap("repo", "value"))
			outside := filepath.Join(top, "shared")
			writeFile(t, filepath.Join(outside, "cm.yaml"), decodeTestConfigMap("shared", "value"))
			if err := os.Chmod(filepath.Join(outside, "cm.yaml"), 0o600); err != nil {
				t.Fatal(err)
			}
			workspaceRoot := filepath.Join(top, "workspace")
			symlink(t, outside, filepath.Join(workspaceRoot, "app", "lnk"))
			before := snapshotHardlinkTestTree(t, outside)

			copier := &preparedKustomizeWorkspaceCopier{
				repoRoot: repoRoot,
				dstRoot:  workspaceRoot,
				copied:   map[string]struct{}{},
			}
			path := filepath.Join(repoRoot, "app", "lnk")
			if target == "file" {
				path = filepath.Join(path, "cm.yaml")
			}
			err := copier.copyPath(path)
			if err == nil || !strings.Contains(err.Error(), "goes through symlink") {
				t.Fatalf("copyPath() error = %v, want error containing %q", err, "goes through symlink")
			}
			assertTreeUnchanged(t, outside, before)
		})
	}
}

// TestPreparedWorkspaceCopySkipsSymlinkedSourcePath pins that the copier
// never copies a path through a symlinked folder of the repository. Such a
// path reaches the copier from spec.source.kustomize patches before any
// symlink check, and the render rejects it later.
func TestPreparedWorkspaceCopySkipsSymlinkedSourcePath(t *testing.T) {
	top := t.TempDir()
	repoRoot := filepath.Join(top, "repo")
	writeFile(t, filepath.Join(repoRoot, "shared", "cm.yaml"), decodeTestConfigMap("shared", "value"))
	symlink(t, filepath.Join(repoRoot, "shared"), filepath.Join(repoRoot, "app", "lnk"))
	workspaceRoot := filepath.Join(top, "workspace")

	copier := &preparedKustomizeWorkspaceCopier{
		repoRoot: repoRoot,
		dstRoot:  workspaceRoot,
		copied:   map[string]struct{}{},
	}
	if err := copier.copyPath(filepath.Join(repoRoot, "app", "lnk", "cm.yaml")); err != nil {
		t.Fatalf("copyPath() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, "app", "lnk")); !os.IsNotExist(err) {
		t.Fatalf("copyPath() copied a path through a symlink (Lstat error %v), want nothing copied", err)
	}
}

// TestPreparedWorkspaceCopierCoversByFolderIdentity pins that a path under
// a folder already copied is covered when it names that folder another
// way. On a case-insensitive file system BASE names base. On any file
// system, a path through a symlink above the folder names it too.
func TestPreparedWorkspaceCopierCoversByFolderIdentity(t *testing.T) {
	top := t.TempDir()
	repoRoot := filepath.Join(top, "repo")
	writeFile(t, filepath.Join(repoRoot, "base", "extra", "cm.yaml"), decodeTestConfigMap("extra", "value"))
	symlink(t, "base", filepath.Join(repoRoot, "alias"))

	copier := &preparedKustomizeWorkspaceCopier{
		repoRoot: repoRoot,
		dstRoot:  filepath.Join(top, "workspace"),
		copied:   map[string]struct{}{},
	}
	if err := copier.copyPath(filepath.Join(repoRoot, "base", "extra")); err != nil {
		t.Fatalf("copyPath() error = %v", err)
	}
	if !copier.pathCovered(filepath.Join(repoRoot, "alias", "extra", "cm.yaml")) {
		t.Fatalf("pathCovered(alias/extra/cm.yaml) = false, want true: alias/extra names the copied folder base/extra")
	}
	if copier.pathCovered(filepath.Join(repoRoot, "other")) {
		t.Fatalf("pathCovered(other) = true, want false")
	}
}

// TestKustomizeRenderCaseInsensitiveSymlinkAndDoubleCopy runs the two
// review reproductions on a case-insensitive file system: a source option
// patch through APP/lnk, a symlink to a repository folder, and a base named
// both BASE and base/extra, whose second copy met a symlink the first copy
// made.
func TestKustomizeRenderCaseInsensitiveSymlinkAndDoubleCopy(t *testing.T) {
	requireCaseInsensitiveTempDir(t)
	t.Run("source option patch through a symlink", func(t *testing.T) {
		repoRoot := filepath.Join(t.TempDir(), "repo")
		writeHardlinkTestRepo(t, repoRoot, "base", "kustomization.yaml")
		writeFile(t, filepath.Join(repoRoot, "shared", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  patched: \"true\"\n")
		if err := os.Chmod(filepath.Join(repoRoot, "shared", "cm.yaml"), 0o600); err != nil {
			t.Fatal(err)
		}
		symlink(t, filepath.Join(repoRoot, "shared"), filepath.Join(repoRoot, "app", "lnk"))
		before := snapshotHardlinkTestTree(t, repoRoot)

		_, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: "app"}, RenderOptions{
			Kustomize: &argoappv1.ApplicationSourceKustomize{
				Patches: argoappv1.KustomizePatches{{Path: "../APP/lnk/cm.yaml"}},
			},
		})
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Render() error = %v, want a symlink error", err)
		}
		assertTreeUnchanged(t, repoRoot, before)
	})
	t.Run("base copied under two names", func(t *testing.T) {
		repoRoot := filepath.Join(t.TempDir(), "repo")
		writeFile(t, filepath.Join(repoRoot, "app", "kustomization.yaml"), "resources:\n  - ../BASE\n  - ../base/extra\ngenerators:\n  - gen.yaml\n")
		writeFile(t, filepath.Join(repoRoot, "app", "gen.yaml"), hardlinkTestGenerator)
		writeFile(t, filepath.Join(repoRoot, "base", "kustomization.yaml"), hardlinkTestBaseKustomization)
		writeFile(t, filepath.Join(repoRoot, "base", "cm.yaml"), decodeTestConfigMap("demo", "value"))
		writeFile(t, filepath.Join(repoRoot, "base", "extra", "kustomization.yaml"), "resources:\n  - cm.yaml\n")
		writeFile(t, filepath.Join(repoRoot, "base", "extra", "cm.yaml"), decodeTestConfigMap("extra", "value"))
		symlink(t, "cm.yaml", filepath.Join(repoRoot, "base", "extra", "unused.yaml"))
		before := snapshotHardlinkTestTree(t, repoRoot)

		manifests, _, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: "app"}, RenderOptions{})
		if err != nil {
			t.Fatalf("Render() error = %v", err)
		}
		for _, name := range []string{"demo", "extra"} {
			if !containsManifest(manifests, "ConfigMap", name) {
				t.Fatalf("Render() manifests = %v, want ConfigMap %s", manifests, name)
			}
		}
		assertTreeUnchanged(t, repoRoot, before)
	})
}
