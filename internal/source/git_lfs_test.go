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
	if data, _ := os.ReadFile(filepath.Join(result.Path, "lfs", "cm.yaml")); string(data) != lfsSourceContent || len(result.LFSMissing) != 0 || len(result.LFSPointers) != 1 {
		t.Fatalf("Acquire() = %+v, lfs/cm.yaml = %q; want the downloaded content", result, data)
	}
	requireTokenOnlyOnBatch(t, server, "Bearer lfs-secret")

	opts.AllowNetwork = false
	before := len(server.Requests())
	result, err = DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
	if err != nil || len(result.LFSMissing) != 0 || len(server.Requests()) != before {
		t.Fatalf("Acquire(offline) = %+v, %v, requests %d -> %d; want the cached object", result, err, before, len(server.Requests()))
	}

	if err := os.RemoveAll(gitlfs.StoreDir(cacheDir)); err != nil {
		t.Fatal(err)
	}
	result, err = DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repo.URL, Revision: "HEAD"}, opts)
	if err != nil {
		t.Fatalf("Acquire(offline, no cache) error = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(result.Path, "lfs", "cm.yaml")); string(data) != pointer {
		t.Fatalf("lfs/cm.yaml = %q, want the pointer back when the object is gone", data)
	}
	if len(result.LFSMissing) != 1 || !strings.Contains(strings.Join(result.LFSReasons, ";"), "--offline") {
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
	if err != nil || len(result.LFSMissing) != 0 {
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
