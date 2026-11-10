package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/gitlfs"
	"github.com/sholdee/drydock/internal/gitlfs/lfstest"
	"github.com/sholdee/drydock/internal/gittree"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

const fetchedLFSConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: fetched-lfs
`

// lfsRepoSecret is a repository Secret that sets enableLfs for url.
func lfsRepoSecret(url string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: lfs-repo
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: %s
  enableLfs: "true"
`, url)
}

// fetchedLFSRoot serves a repository whose lfs/cm.yaml is a Git LFS
// pointer, with its object on a Git LFS server, and builds a local tree
// with an Application that renders lfs/ from it and a repository Secret
// that sets enableLfs.
func fetchedLFSRoot(t *testing.T) (string, *lfstest.Server) {
	t.Helper()
	server := lfstest.New(t, false, false)
	_, pointer := server.Add(fetchedLFSConfigMap)
	repo := lfstest.ServeRepo(t, server, map[string]string{"lfs/cm.yaml": pointer})
	root := t.TempDir()
	submoduletest.WriteFile(t, root, "argocd/repo.yaml", lfsRepoSecret(repo.URL))
	submoduletest.WriteFile(t, root, "apps/fetched.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: fetched
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+repo.URL+`
    targetRevision: main
    path: lfs
  destination:
    name: in-cluster
    namespace: default
`)
	gitCommitAll(t, root, "fetched lfs app")
	return root, server
}

func buildHasConfigMap(result BuildResult, name string) bool {
	return renderedConfigMapNames(result)[name]
}

// TestBuildFetchedSourceDownloadsLFSObjects pins that a fetched Git source
// whose repository Secret sets enableLfs renders the content of its Git
// LFS files from the Git LFS server of the repository, as the Argo CD
// repo-server does after `git lfs fetch` and `git lfs checkout`. Before
// drydock had a Git LFS client, it rendered the pointer and warned.
func TestBuildFetchedSourceDownloadsLFSObjects(t *testing.T) {
	root, server := fetchedLFSRoot(t)
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, GitCacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !buildHasConfigMap(result, "fetched-lfs") || hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("Manifests = %v, Diagnostics = %#v; want the Git LFS content without a warning", renderedConfigMapNames(result), result.Diagnostics)
	}
	if len(server.RequestsTo("objects")) != 1 {
		t.Fatalf("object requests = %d, want 1", len(server.RequestsTo("objects")))
	}
}

// TestBuildFetchedSourceWarnsWhenOfflineLacksTheLFSObject pins that --offline
// never contacts the Git LFS server: a fetched source whose object is not
// in the drydock Git LFS cache renders the pointer with the
// render.lfs-pointer warning.
func TestBuildFetchedSourceWarnsWhenOfflineLacksTheLFSObject(t *testing.T) {
	root, server := fetchedLFSRoot(t)
	gitCache := t.TempDir()
	request := BuildRequest{Path: root, GitCacheDir: gitCache}
	if _, err := (Orchestrator{}).Build(context.Background(), request); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if err := os.RemoveAll(gitlfs.StoreDir(gitCache)); err != nil {
		t.Fatal(err)
	}
	before := len(server.Requests())
	request.Offline = true
	result, err := Orchestrator{}.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build(offline) error = %v", err)
	}
	if buildHasConfigMap(result, "fetched-lfs") || !hasDiagnosticCode(result.Diagnostics, lfsPointerCode) || len(server.Requests()) != before {
		t.Fatalf("Manifests = %v, Diagnostics = %#v, requests %d -> %d; want the pointer, the warning, and no request", renderedConfigMapNames(result), result.Diagnostics, before, len(server.Requests()))
	}
}

// TestPersistentCacheKeysFetchedLFSOnMissingObjects pins that the render
// cache key of a fetched Git source holds the Git LFS pointers that its
// checkout could not fill: a run that loses the objects never reuses the
// render of a run that had them.
func TestPersistentCacheKeysFetchedLFSOnMissingObjects(t *testing.T) {
	root, _ := fetchedLFSRoot(t)
	gitCache := t.TempDir()
	request := persistentBuildRequest(root, t.TempDir())
	request.GitCacheDir = gitCache
	var renders atomic.Int64
	result, err := countingOrchestrator(&renders).Build(context.Background(), request)
	if err != nil || !buildHasConfigMap(result, "fetched-lfs") || renders.Load() == 0 {
		t.Fatalf("cold Build() = %v, %v, renders %d; want the content rendered", renderedConfigMapNames(result), err, renders.Load())
	}
	renders.Store(0)
	result, err = countingOrchestrator(&renders).Build(context.Background(), request)
	if err != nil || !buildHasConfigMap(result, "fetched-lfs") || renders.Load() != 0 {
		t.Fatalf("warm Build() = %v, %v, renders %d; want a cache hit", renderedConfigMapNames(result), err, renders.Load())
	}

	if err := os.RemoveAll(gitlfs.StoreDir(gitCache)); err != nil {
		t.Fatal(err)
	}
	request.Offline = true
	renders.Store(0)
	result, err = countingOrchestrator(&renders).Build(context.Background(), request)
	if err != nil {
		t.Fatalf("offline Build() error = %v", err)
	}
	if buildHasConfigMap(result, "fetched-lfs") || renders.Load() == 0 || !hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("offline Build() = %v, renders %d, Diagnostics %#v; want a new render of the pointer", renderedConfigMapNames(result), renders.Load(), result.Diagnostics)
	}
}

// lfsServedSnapshotRepo is lfsRepo with the origin remote and the
// repository Secret on a served repository URL, whose Git LFS server holds
// the object. The object is in .git/lfs when stored. It returns the
// repository, the base commit, and the server.
func lfsServedSnapshotRepo(t *testing.T, stored bool) (string, string, *lfstest.Server) {
	t.Helper()
	server := lfstest.New(t, false, false)
	oid, pointer := server.Add(lfsTestConfigMap)
	repo := lfstest.ServeRepo(t, server, map[string]string{"README.md": "served\n"})
	root := t.TempDir()
	submoduletest.Run(t, root, "init", "-q")
	submoduletest.Run(t, root, "remote", "add", "origin", repo.URL)
	submoduletest.WriteFile(t, root, "argocd/repo.yaml", lfsRepoSecret(repo.URL))
	submoduletest.Run(t, root, "add", "-A")
	submoduletest.Run(t, root, "commit", "-q", "-m", "base")
	base := submoduletest.Run(t, root, "rev-parse", "HEAD")
	// Only the second commit holds the pointer, so the snapshot of HEAD is
	// the one that gets the object.
	submoduletest.WriteFile(t, root, "lfs/cm.yaml", pointer)
	submoduletest.WriteFile(t, root, "apps/lfs.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: lfs
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+repo.URL+`
    targetRevision: HEAD
    path: lfs
  destination:
    name: in-cluster
    namespace: default
`)
	submoduletest.Run(t, root, "add", "-A")
	submoduletest.Run(t, root, "commit", "-q", "-m", "add app")
	if stored {
		object := gittree.LFSObjectPath(filepath.Join(root, ".git", "lfs"), oid)
		submoduletest.WriteFile(t, filepath.Dir(object), filepath.Base(object), lfsTestConfigMap)
	}
	return root, base, server
}

// TestDiffRefSnapshotDownloadsLFSObject pins that a --ref snapshot whose
// object is not in the repository's .git/lfs store gets it from the Git
// LFS server of the repository Secret's URL, never writing the
// repository. Before drydock had a Git LFS client, it rendered the pointer
// and warned.
func TestDiffRefSnapshotDownloadsLFSObject(t *testing.T) {
	root, base, server := lfsServedSnapshotRepo(t, false)
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base,
		GitCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v", err)
	}
	if !hasConfigMap(result, "from-lfs") || hasDiagnosticCode(result.Diagnostics, lfsPointerCode) || len(server.RequestsTo("objects")) != 1 {
		t.Fatalf("Results = %#v, Diagnostics = %#v, object requests = %d; want the downloaded content", result.Results, result.Diagnostics, len(server.RequestsTo("objects")))
	}
	if entries, _ := os.ReadDir(filepath.Join(root, ".git", "lfs")); len(entries) != 0 {
		t.Fatal("drydock wrote the repository's .git/lfs store")
	}
}

// TestPersistentCacheSnapshotLFSKeyIgnoresObjectSource pins that the
// render cache key of a --ref snapshot depends on which pointers it still
// holds, not on where the objects came from: a fill from the network hits
// the entry of a fill from .git/lfs, and a run that lacks the object
// renders again.
func TestPersistentCacheSnapshotLFSKeyIgnoresObjectSource(t *testing.T) {
	root, base, server := lfsServedSnapshotRepo(t, true)
	gitCache := t.TempDir()
	request := DiffRequest{
		RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base,
		GitCacheDir: gitCache, Offline: true,
		RenderCacheEnabled: true, RenderCacheDir: t.TempDir(), EngineFingerprint: testEngineFingerprint(),
	}
	var renders atomic.Int64
	result, err := countingOrchestrator(&renders).DiffApps(context.Background(), request)
	if err != nil || !hasConfigMap(result, "from-lfs") || renders.Load() == 0 {
		t.Fatalf("local-store DiffApps() = %v, renders %d; want the content", err, renders.Load())
	}

	if err := os.RemoveAll(filepath.Join(root, ".git", "lfs")); err != nil {
		t.Fatal(err)
	}
	request.Offline = false
	renders.Store(0)
	result, err = countingOrchestrator(&renders).DiffApps(context.Background(), request)
	if err != nil || !hasConfigMap(result, "from-lfs") || len(server.RequestsTo("objects")) != 1 || renders.Load() != 0 {
		t.Fatalf("network DiffApps() = %v, renders %d, object requests %d; want the download and a cache hit", err, renders.Load(), len(server.RequestsTo("objects")))
	}

	if err := os.RemoveAll(gitlfs.StoreDir(gitCache)); err != nil {
		t.Fatal(err)
	}
	request.Offline = true
	renders.Store(0)
	result, err = countingOrchestrator(&renders).DiffApps(context.Background(), request)
	if err != nil || hasConfigMap(result, "from-lfs") || renders.Load() == 0 || !hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("offline DiffApps() = %v, renders %d, Diagnostics %#v; want a new render of the pointer", err, renders.Load(), result.Diagnostics)
	}
}

// TestDiffFetchedSourceWithEnableLFSOnOneSide pins that a diff in which
// one side sets enableLfs and the other does not renders the content on
// the first side and the pointer on the second, although both read one
// cache checkout of HEAD. Before the fix the side without enableLfs
// rendered the content that the other side filled, so the diff was empty.
func TestDiffFetchedSourceWithEnableLFSOnOneSide(t *testing.T) {
	for _, enabledOnHead := range []bool{true, false} {
		server := lfstest.New(t, false, false)
		_, pointer := server.Add(fetchedLFSConfigMap)
		repo := lfstest.ServeRepo(t, server, map[string]string{"lfs/cm.yaml": pointer})
		root := t.TempDir()
		submoduletest.Run(t, root, "init", "-q")
		submoduletest.WriteFile(t, root, "apps/fetched.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: fetched
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+repo.URL+`
    targetRevision: HEAD
    path: lfs
  destination:
    name: in-cluster
    namespace: default
`)
		secret := lfsRepoSecret(repo.URL)
		off := strings.Replace(secret, `enableLfs: "true"`, `enableLfs: "false"`, 1)
		first, second := off, secret
		if !enabledOnHead {
			first, second = secret, off
		}
		submoduletest.WriteFile(t, root, "argocd/repo.yaml", first)
		submoduletest.Run(t, root, "add", "-A")
		submoduletest.Run(t, root, "commit", "-q", "-m", "base")
		base := submoduletest.Run(t, root, "rev-parse", "HEAD")
		submoduletest.WriteFile(t, root, "argocd/repo.yaml", second)
		submoduletest.Run(t, root, "commit", "-q", "-am", "toggle enableLfs")
		result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
			RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base,
			GitCacheDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("enableLfs on HEAD %t: DiffApps() error = %v", enabledOnHead, err)
		}
		if !hasConfigMap(result, "fetched-lfs") {
			t.Fatalf("enableLfs on HEAD %t: Results = %#v; want the ConfigMap as a change between the sides", enabledOnHead, result.Results)
		}
	}
}

// TestBuildFetchedSourceFailsWhenTheLFSServerFails pins that a Git LFS
// server that refuses the batch request fails the source, as `git lfs
// fetch --all` fails the manifest request of the Argo CD repo-server, and
// that the render cache keeps nothing for it: once the server works, the
// content renders.
func TestBuildFetchedSourceFailsWhenTheLFSServerFails(t *testing.T) {
	root, server := fetchedLFSRoot(t)
	server.BatchStatus = http.StatusInternalServerError
	cacheDir := t.TempDir()
	request := persistentBuildRequest(root, cacheDir)
	request.GitCacheDir = t.TempDir()
	result, err := Orchestrator{}.Build(context.Background(), request)
	if err == nil && !hasErrorDiagnostic(result.Diagnostics, "HTTP 500") {
		t.Fatalf("Build() = %v, Diagnostics %#v; want the source to fail with the batch status", renderedConfigMapNames(result), result.Diagnostics)
	}
	if buildHasConfigMap(result, "fetched-lfs") || countRenderCacheEntries(t, cacheDir) != 0 {
		t.Fatalf("Build() rendered %v and stored %d render cache entries, want nothing", renderedConfigMapNames(result), countRenderCacheEntries(t, cacheDir))
	}
	server.BatchStatus = 0
	result, err = Orchestrator{}.Build(context.Background(), request)
	if err != nil || !buildHasConfigMap(result, "fetched-lfs") || countRenderCacheEntries(t, cacheDir) == 0 {
		t.Fatalf("Build() after the fix = %v, %v, %d entries; want the content stored", renderedConfigMapNames(result), err, countRenderCacheEntries(t, cacheDir))
	}
}

// TestBuildFetchedSourceWarnsForUnsupportedLFS pins that a pointer that
// drydock itself cannot fill, here because .lfsconfig names a local store,
// stays with the render.lfs-unsupported warning and does not fail the
// source.
func TestBuildFetchedSourceWarnsForUnsupportedLFS(t *testing.T) {
	server := lfstest.New(t, false, false)
	_, pointer := server.Add(fetchedLFSConfigMap)
	repo := lfstest.ServeRepo(t, server, map[string]string{"lfs/cm.yaml": pointer, ".lfsconfig": "[lfs]\n\turl = file:///srv/lfs\n"})
	root := t.TempDir()
	submoduletest.WriteFile(t, root, "argocd/repo.yaml", lfsRepoSecret(repo.URL))
	submoduletest.WriteFile(t, root, "apps/fetched.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: fetched
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+repo.URL+`
    targetRevision: main
    path: lfs
  destination:
    name: in-cluster
    namespace: default
`)
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, GitCacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if buildHasConfigMap(result, "fetched-lfs") || !hasDiagnosticCode(result.Diagnostics, lfsUnsupportedCode) || hasDiagnosticCode(result.Diagnostics, lfsPointerCode) {
		t.Fatalf("Diagnostics = %#v; want only %s", result.Diagnostics, lfsUnsupportedCode)
	}
	if len(server.Requests()) != 0 {
		t.Fatalf("requests = %d, want none for a refused endpoint", len(server.Requests()))
	}
}

// TestDiffRefSnapshotFailsWhenTheLFSServerFails pins that a --ref snapshot
// whose object the server does not have fails its sources online.
func TestDiffRefSnapshotFailsWhenTheLFSServerFails(t *testing.T) {
	root, base, server := lfsServedSnapshotRepo(t, false)
	server.BatchStatus = http.StatusForbidden
	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
		RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base,
		GitCacheDir: t.TempDir(),
	})
	if err == nil && !hasErrorDiagnostic(result.Diagnostics, "HTTP 403") {
		t.Fatalf("DiffApps() = %#v, Diagnostics %#v; want the source to fail", result.Results, result.Diagnostics)
	}
}

func hasErrorDiagnostic(diags []diagnostic.Diagnostic, text string) bool {
	for _, diag := range diags {
		if diag.Severity == diagnostic.SeverityError && strings.Contains(diag.Message, text) {
			return true
		}
	}
	return false
}
