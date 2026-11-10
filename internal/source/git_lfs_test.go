package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/credhost"
	"github.com/sholdee/drydock/internal/gitlfs"
	"github.com/sholdee/drydock/internal/gitlfs/lfstest"
)

const lfsSourceContent = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fetched-lfs\n"

// TestDefaultGitAcquirerDownloadsLFSObjects pins that a fetched Git source
// whose repository sets enableLfs gets the content of its Git LFS pointer
// files from the Git LFS server of the repository URL, as `git lfs fetch`
// and `git lfs checkout` give it to the Argo CD repo-server; that the
// --git-* token goes to the batch endpoint on the listed host and never to
// the object host; that --offline then reads the drydock Git LFS cache
// alone; and that a checkout that loses the object gets the pointer back.
func TestDefaultGitAcquirerDownloadsLFSObjects(t *testing.T) {
	server := lfstest.New(t, false, false)
	_, pointer := server.Add(lfsSourceContent)
	repo := lfstest.ServeRepo(t, server, map[string]string{"lfs/cm.yaml": pointer, "plain/cm.yaml": "kind: ConfigMap\n"})
	entry, _ := credhost.Entry(repo.URL)
	cacheDir := t.TempDir()
	opts := GitOptions{AllowNetwork: true, CacheDir: cacheDir, LFS: true, Credentials: GitCredentials{BearerToken: "lfs-secret", Hosts: []string{entry}}}

	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(result.Path, "lfs", "cm.yaml")); string(data) != lfsSourceContent || len(result.LFS.Pointers()) != 0 || len(result.LFSPointers) != 1 {
		t.Fatalf("Acquire() = %+v, lfs/cm.yaml = %q; want the downloaded content", result, data)
	}
	requireTokenOnlyOnBatch(t, server, "Bearer lfs-secret")

	requireOfflineLFS(t, server, repo, cacheDir, opts, pointer)
}

// requireOfflineLFS runs the offline steps of
// TestDefaultGitAcquirerDownloadsLFSObjects.
func requireOfflineLFS(t *testing.T, server *lfstest.Server, repo lfstest.Repo, cacheDir string, opts GitOptions, pointer string) {
	t.Helper()
	opts.AllowNetwork = false
	before := len(server.Requests())
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
	if err != nil || len(result.LFS.Pointers()) != 0 || len(server.Requests()) != before {
		t.Fatalf("Acquire(offline) = %+v, %v, requests %d -> %d; want the cached object", result, err, before, len(server.Requests()))
	}

	if err := os.RemoveAll(gitlfs.StoreDir(cacheDir)); err != nil {
		t.Fatal(err)
	}
	// The checkout still holds the verified content, which stays without a
	// store. A file that does not match gets the pointer back.
	result, err = DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
	if err != nil || len(result.LFS.Pointers()) != 0 {
		t.Fatalf("Acquire(offline, matching checkout) = %+v, %v; want the content kept", result, err)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "lfs", "cm.yaml"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
	if err != nil {
		t.Fatalf("Acquire(offline, no cache) error = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(result.Path, "lfs", "cm.yaml")); string(data) != pointer {
		t.Fatalf("lfs/cm.yaml = %q, want the pointer back when the object is gone", data)
	}
	if len(result.LFS.Missing) != 1 || !strings.Contains(strings.Join(result.LFS.Reasons, ";"), "--offline") {
		t.Fatalf("Acquire(offline, no cache) = %+v, want the missing pointer and the offline reason", result)
	}
}

// TestDefaultGitAcquirerSendsNoLFSTokenToUnlistedHost pins that the batch
// request gets no --git-* credential when no --git-auth-host entry names
// its host.
func TestDefaultGitAcquirerSendsNoLFSTokenToUnlistedHost(t *testing.T) {
	server := lfstest.New(t, false, false)
	_, pointer := server.Add(lfsSourceContent)
	repo := lfstest.ServeRepo(t, server, map[string]string{"lfs/cm.yaml": pointer})
	opts := GitOptions{AllowNetwork: true, CacheDir: t.TempDir(), LFS: true, Credentials: GitCredentials{BearerToken: "lfs-secret", Hosts: []string{"git.example.test"}}}
	result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
	if err != nil || len(result.LFS.Pointers()) != 0 {
		t.Fatalf("Acquire() = %+v, %v", result, err)
	}
	for _, request := range server.Requests() {
		if request.Header.Get("Authorization") != "" {
			t.Fatalf("%s request got Authorization, want none for an unlisted host", request.Host)
		}
	}
}

// requireTokenOnlyOnBatch fails unless the one batch request carries
// authorization and no object request carries any.
func requireTokenOnlyOnBatch(t *testing.T, server *lfstest.Server, authorization string) {
	t.Helper()
	batch := server.RequestsTo("batch")
	if len(batch) != 1 || batch[0].Header.Get("Authorization") != authorization {
		t.Fatalf("batch requests = %+v, want one with the token", batch)
	}
	for _, request := range server.RequestsTo("objects") {
		if request.Header.Get("Authorization") != "" {
			t.Fatalf("the object host got Authorization %q", request.Header.Get("Authorization"))
		}
	}
}

// TestDefaultGitAcquirerRestoresPointersWhenLFSTurnsOff pins that a cache
// checkout of HEAD that a run with enableLfs filled holds the pointer
// files again for a run without it, online and offline, as the Argo CD
// repo-server holds them without enableLfs, also when a run with enableLfs
// fills it again later. A HEAD checkout is not checked out again, so before
// the fix the files kept the content.
func TestDefaultGitAcquirerRestoresPointersWhenLFSTurnsOff(t *testing.T) {
	server := lfstest.New(t, false, false)
	_, pointer := server.Add(lfsSourceContent)
	repo := lfstest.ServeRepo(t, server, map[string]string{"lfs/cm.yaml": pointer})
	cacheDir := t.TempDir()
	for i, step := range []struct {
		lfs, network bool
		want         string
	}{
		{true, true, lfsSourceContent},
		{false, true, pointer},
		{true, false, lfsSourceContent},
		{false, false, pointer},
		{false, false, pointer},
		{true, false, lfsSourceContent},
		{false, true, pointer},
	} {
		opts := GitOptions{AllowNetwork: step.network, CacheDir: cacheDir, LFS: step.lfs}
		result, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
		if err != nil {
			t.Fatalf("step %d: Acquire() error = %v", i, err)
		}
		if data, _ := os.ReadFile(filepath.Join(result.Path, "lfs", "cm.yaml")); string(data) != step.want {
			t.Fatalf("step %d (LFS %t, network %t): lfs/cm.yaml = %q, want %q", i, step.lfs, step.network, data, step.want)
		}
		// The marker stays once a run with enableLfs wrote it, so a run
		// without it never misses content that another process wrote.
		if _, err := os.Stat(filepath.Join(result.Path, ".git", lfsFilledMarker)); err != nil {
			t.Fatalf("step %d: marker missing: %v", i, err)
		}
	}
}
