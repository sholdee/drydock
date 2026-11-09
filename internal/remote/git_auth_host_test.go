package remote

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

const remoteGitHostTestToken = "remote-git-host-secret-token"

func acquireRemoteGitOverHTTP(t *testing.T, repoURL string, credentials GitCredentials, submodules bool) error {
	t.Helper()
	_, err := DefaultAcquirer{}.Acquire(context.Background(), Request{Kind: RequestGitRepo, RepoURL: repoURL, Revision: "main", Submodules: submodules}, Options{
		CacheDir:       t.TempDir(),
		GitCredentials: credentials,
	})
	return err
}

func requireRemoteNoAuth(t *testing.T, server *submoduletest.HTTPServer) {
	t.Helper()
	if got := server.AuthorizationHeaders(); len(got) != 0 {
		t.Fatalf("server %s received Authorization headers %q, want none", server.URL, got)
	}
}

func requireRemoteAuthOnEveryRequest(t *testing.T, server *submoduletest.HTTPServer, want string) {
	t.Helper()
	requests := server.Requests()
	if len(requests) == 0 {
		t.Fatalf("server %s received no request", server.URL)
	}
	for _, request := range requests {
		if request.Authorization != want {
			t.Fatalf("request %s %s to %s has Authorization %q, want %q", request.Method, request.Path, server.URL, request.Authorization, want)
		}
	}
}

// TestDefaultAcquirerSendsGitCredentialsOnlyToListedHosts checks that a
// remote Kustomize Git base gets the --git-* HTTP credentials only from a
// server whose scheme, host, and port match a --git-auth-host entry. A
// base on another port of the same machine is another host and gets none.
// Kustomize in Argo CD gets the credentials of the source repository
// through askpass for any host, so this is a recorded difference.
func TestDefaultAcquirerSendsGitCredentialsOnlyToListedHosts(t *testing.T) {
	dir := t.TempDir()
	submoduletest.NewRepo(t, dir, "repo", map[string]string{"base/kustomization.yaml": "resources: []\n"})
	listed := submoduletest.ServeSmartHTTP(t, dir)
	other := submoduletest.ServeSmartHTTP(t, dir)
	credentials := GitCredentials{BearerToken: remoteGitHostTestToken, Hosts: []string{listed.URL}}
	if err := acquireRemoteGitOverHTTP(t, listed.URL+"/repo.git", credentials, false); err != nil {
		t.Fatalf("Acquire() from the listed host error = %v", err)
	}
	if err := acquireRemoteGitOverHTTP(t, other.URL+"/repo.git", credentials, false); err != nil {
		t.Fatalf("Acquire() from the other host error = %v", err)
	}
	requireRemoteAuthOnEveryRequest(t, listed, "Bearer "+remoteGitHostTestToken)
	requireRemoteNoAuth(t, other)
}

// TestDefaultAcquirerScopesRemoteGitSubmoduleCredentials checks that a
// submodule of a remote Git base on another host gets no credentials,
// while the superproject on the listed host does.
func TestDefaultAcquirerScopesRemoteGitSubmoduleCredentials(t *testing.T) {
	listed := submoduletest.ServeSmartHTTP(t, "")
	other := submoduletest.ServeSmartHTTP(t, "")
	parentFileURL := submoduletest.SubmoduleURLFixture(t, other.URL+"/child.git")
	dir := filepath.Dir(strings.TrimPrefix(parentFileURL, "file://"))
	listed.SetRoot(dir)
	other.SetRoot(dir)
	credentials := GitCredentials{BearerToken: remoteGitHostTestToken, Hosts: []string{listed.URL, other.URL}}
	if err := acquireRemoteGitOverHTTP(t, listed.URL+"/parent.git", credentials, true); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	requireRemoteAuthOnEveryRequest(t, listed, "Bearer "+remoteGitHostTestToken)
	if len(other.Requests()) == 0 {
		t.Fatal("the submodule host received no request")
	}
	requireRemoteNoAuth(t, other)
}

// TestDefaultAcquirerRefusesGitCredentialsWithoutHosts checks that a
// remote Git base fails before any request when an HTTP Git credential has
// no --git-auth-host entry.
func TestDefaultAcquirerRefusesGitCredentialsWithoutHosts(t *testing.T) {
	dir := t.TempDir()
	submoduletest.NewRepo(t, dir, "repo", map[string]string{"base/kustomization.yaml": "resources: []\n"})
	server := submoduletest.ServeSmartHTTP(t, dir)
	err := acquireRemoteGitOverHTTP(t, server.URL+"/repo.git", GitCredentials{BearerToken: remoteGitHostTestToken}, false)
	if !errors.Is(err, gitauth.ErrMissingHosts) {
		t.Fatalf("Acquire() error = %v, want the missing --git-auth-host error", err)
	}
	if strings.Contains(err.Error(), remoteGitHostTestToken) {
		t.Fatalf("Acquire() error %q leaks the token", err)
	}
	if got := server.Requests(); len(got) != 0 {
		t.Fatalf("requests = %+v, want none", got)
	}
}
