package app

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/sholdee/drydock/internal/gitlfs"
)

// TestPersistentCacheNeverHidesASnapshotLFSFailure pins that a --ref
// snapshot whose Git LFS fill fails online fails its sources also when the
// render cache holds a render of the filled snapshot: the root then has no
// cache key. (Regression test of review R2-1.)
func TestPersistentCacheNeverHidesASnapshotLFSFailure(t *testing.T) {
	root, base, server := lfsServedSnapshotRepo(t, false)
	gitCache := t.TempDir()
	request := DiffRequest{
		RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base,
		GitCacheDir:        gitCache,
		RenderCacheEnabled: true, RenderCacheDir: t.TempDir(), EngineFingerprint: testEngineFingerprint(),
	}
	var renders atomic.Int64
	result, err := countingOrchestrator(&renders).DiffApps(context.Background(), request)
	if err != nil || !hasConfigMap(result, "from-lfs") {
		t.Fatalf("warm: %v", err)
	}
	if err := os.RemoveAll(gitlfs.StoreDir(gitCache)); err != nil {
		t.Fatal(err)
	}
	server.BatchStatus = http.StatusForbidden
	renders.Store(0)
	result, err = countingOrchestrator(&renders).DiffApps(context.Background(), request)
	t.Logf("after failure: err=%v renders=%d hasContent=%v errDiag=%v", err, renders.Load(), hasConfigMap(result, "from-lfs"), hasErrorDiagnostic(result.Diagnostics, "HTTP 403"))
	if hasConfigMap(result, "from-lfs") || (err == nil && !hasErrorDiagnostic(result.Diagnostics, "HTTP 403")) {
		t.Errorf("snapshot fill failed online, but the run succeeded (render cache hit masks the failure)")
	}
}

// TestPersistentCacheNeverHidesAFetchedLFSFailure pins the same for a
// fetched Git source, whose acquisition fails before any cache lookup.
func TestPersistentCacheNeverHidesAFetchedLFSFailure(t *testing.T) {
	root, server := fetchedLFSRoot(t)
	gitCache := t.TempDir()
	request := persistentBuildRequest(root, t.TempDir())
	request.GitCacheDir = gitCache
	var renders atomic.Int64
	result, err := countingOrchestrator(&renders).Build(context.Background(), request)
	if err != nil || !buildHasConfigMap(result, "fetched-lfs") {
		t.Fatalf("warm: %v", err)
	}
	if err := os.RemoveAll(gitlfs.StoreDir(gitCache)); err != nil {
		t.Fatal(err)
	}
	server.BatchStatus = http.StatusForbidden
	renders.Store(0)
	result, err = countingOrchestrator(&renders).Build(context.Background(), request)
	t.Logf("after failure: err=%v renders=%d hasContent=%v errDiag=%v", err, renders.Load(), buildHasConfigMap(result, "fetched-lfs"), hasErrorDiagnostic(result.Diagnostics, "HTTP 403"))
	if buildHasConfigMap(result, "fetched-lfs") || (err == nil && !hasErrorDiagnostic(result.Diagnostics, "HTTP 403")) {
		t.Errorf("fetched fill failed online, but the run rendered the content or succeeded")
	}
}
