package source

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

const gitHostTestToken = "git-host-secret-token"

// twoGitHosts starts two smart HTTP Git servers on two local ports, which
// are two hosts for the credential check. Both serve the bare repository
// repo.git of one directory.
func twoGitHosts(t *testing.T) (listed, other *submoduletest.HTTPServer) {
	t.Helper()
	dir := t.TempDir()
	submoduletest.NewRepo(t, dir, "repo", map[string]string{"app/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: scoped\n"})
	return submoduletest.ServeSmartHTTP(t, dir), submoduletest.ServeSmartHTTP(t, dir)
}

func acquireOverHTTP(t *testing.T, repoURL string, credentials GitCredentials) error {
	t.Helper()
	_, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: repoURL, Revision: "main"}, GitOptions{
		AllowNetwork: true,
		CacheDir:     t.TempDir(),
		Credentials:  credentials,
	})
	return err
}

// requireAuthOnEveryRequest fails unless server received requests and each
// one carried want.
func requireAuthOnEveryRequest(t *testing.T, server *submoduletest.HTTPServer, want string) {
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

func requireNoAuth(t *testing.T, server *submoduletest.HTTPServer) {
	t.Helper()
	if got := server.AuthorizationHeaders(); len(got) != 0 {
		t.Fatalf("server %s received Authorization headers %q, want none", server.URL, got)
	}
}

func hostPort(server *submoduletest.HTTPServer) string {
	return strings.TrimPrefix(server.URL, "http://")
}

// TestDefaultGitAcquirerSendsCredentialsOnlyToListedHosts checks that the
// --git-* HTTP credentials reach a Git server only when the scheme, host,
// and port of the repository URL match a --git-auth-host entry. The two
// servers run on two ports of 127.0.0.1. A request to the other server
// gets no credentials, and the clone still works for a public repository.
// An entry without a scheme is https only, so it does not match an http
// URL.
func TestDefaultGitAcquirerSendsCredentialsOnlyToListedHosts(t *testing.T) {
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("git-user:git-host-secret-password"))
	for _, tc := range []struct {
		name        string
		credentials GitCredentials
		hosts       func(listed, other *submoduletest.HTTPServer) []string
		listedAuth  string
	}{
		{
			name:        "bearer token for the listed host",
			credentials: GitCredentials{BearerToken: gitHostTestToken},
			hosts:       func(listed, _ *submoduletest.HTTPServer) []string { return []string{listed.URL} },
			listedAuth:  "Bearer " + gitHostTestToken,
		},
		{
			name:        "basic auth for the listed host",
			credentials: GitCredentials{Username: "git-user", Password: "git-host-secret-password"},
			hosts:       func(listed, _ *submoduletest.HTTPServer) []string { return []string{listed.URL} },
			listedAuth:  basic,
		},
		{
			name:        "entry without a scheme is https only",
			credentials: GitCredentials{BearerToken: gitHostTestToken},
			hosts:       func(listed, _ *submoduletest.HTTPServer) []string { return []string{hostPort(listed)} },
		},
		{
			name:        "entry for another host",
			credentials: GitCredentials{BearerToken: gitHostTestToken},
			hosts: func(_, other *submoduletest.HTTPServer) []string {
				return []string{"http://other.example.test:" + strings.Split(hostPort(other), ":")[1]}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed, other := twoGitHosts(t)
			credentials := tc.credentials
			credentials.Hosts = tc.hosts(listed, other)
			if err := acquireOverHTTP(t, listed.URL+"/repo.git", credentials); err != nil {
				t.Fatalf("Acquire() from the listed host error = %v", err)
			}
			if err := acquireOverHTTP(t, other.URL+"/repo.git", credentials); err != nil {
				t.Fatalf("Acquire() from the other host error = %v", err)
			}
			if tc.listedAuth == "" {
				requireNoAuth(t, listed)
			} else {
				requireAuthOnEveryRequest(t, listed, tc.listedAuth)
			}
			requireNoAuth(t, other)
		})
	}
}

// TestDefaultGitAcquirerDropsCredentialsOnRedirect checks that go-git
// removes the credentials when the listed host redirects the first
// request to another port. go-git follows that redirect for the
// info/refs request and then sends every request to the new host. Both
// hosts are listed, so the redirect rule of go-git, not the host list,
// removes the header (stripCredentials and ModifyEndpointIfRedirect in
// go-git plumbing/transport/http/common.go).
func TestDefaultGitAcquirerDropsCredentialsOnRedirect(t *testing.T) {
	listed, other := twoGitHosts(t)
	listed.RedirectTo(other.URL)
	credentials := GitCredentials{BearerToken: gitHostTestToken, Hosts: []string{listed.URL, other.URL}}
	if err := acquireOverHTTP(t, listed.URL+"/repo.git", credentials); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if got := listed.Requests(); len(got) != 1 || got[0].Authorization != "Bearer "+gitHostTestToken {
		t.Fatalf("listed host requests = %+v, want one request with the token", got)
	}
	if len(other.Requests()) == 0 {
		t.Fatal("the redirect target received no request")
	}
	requireNoAuth(t, other)
}

// TestDefaultGitAcquirerRefusesCredentialsWithoutHosts checks that an HTTP
// credential without a --git-auth-host entry fails before any request, and
// that the error names the flag and no credential value. An SSH key alone
// needs no entry.
func TestDefaultGitAcquirerRefusesCredentialsWithoutHosts(t *testing.T) {
	for name, credentials := range map[string]GitCredentials{
		"bearer token": {BearerToken: gitHostTestToken},
		"username":     {Username: "git-host-user"},
		"password":     {Password: "git-host-secret-password"},
	} {
		t.Run(name, func(t *testing.T) {
			listed, _ := twoGitHosts(t)
			err := acquireOverHTTP(t, listed.URL+"/repo.git", credentials)
			if !errors.Is(err, gitauth.ErrMissingHosts) || !strings.Contains(err.Error(), "--git-auth-host") {
				t.Fatalf("Acquire() error = %v, want the missing --git-auth-host error", err)
			}
			for _, secret := range []string{gitHostTestToken, "git-host-user", "git-host-secret-password"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Acquire() error %q leaks a credential", err)
				}
			}
			if got := listed.Requests(); len(got) != 0 {
				t.Fatalf("requests = %+v, want none", got)
			}
		})
	}
	listed, _ := twoGitHosts(t)
	if err := acquireOverHTTP(t, listed.URL+"/repo.git", GitCredentials{SSHPrivateKeyPath: filepath.Join(t.TempDir(), "id_ed25519")}); err != nil {
		t.Fatalf("Acquire() with an SSH key only error = %v", err)
	}
}

// TestDefaultGitAcquirerScopesURLUserinfo checks the userinfo of a
// repository URL. When the --git-* credentials are not for the host of the
// URL, the user and password of the URL go to that host instead, and the
// token never does. When the host is listed, the --git-* credentials win,
// as before.
func TestDefaultGitAcquirerScopesURLUserinfo(t *testing.T) {
	listed, other := twoGitHosts(t)
	credentials := GitCredentials{BearerToken: gitHostTestToken, Hosts: []string{listed.URL}}
	withUserinfo := strings.Replace(other.URL, "http://", "http://ci-user:url-secret@", 1) + "/repo.git"
	if err := acquireOverHTTP(t, withUserinfo, credentials); err != nil {
		t.Fatalf("Acquire() from the other host error = %v", err)
	}
	requireAuthOnEveryRequest(t, other, "Basic "+base64.StdEncoding.EncodeToString([]byte("ci-user:url-secret")))
	listedWithUserinfo := strings.Replace(listed.URL, "http://", "http://ci-user:url-secret@", 1) + "/repo.git"
	if err := acquireOverHTTP(t, listedWithUserinfo, credentials); err != nil {
		t.Fatalf("Acquire() from the listed host error = %v", err)
	}
	requireAuthOnEveryRequest(t, listed, "Bearer "+gitHostTestToken)
}

// TestDefaultGitAcquirerScopesSubmoduleCredentials checks that a submodule
// fetch gets the --git-* credentials only on a listed host that is also
// the host of the superproject. A .gitmodules URL on another host gets
// none, even when that host is listed: the same-host rule still applies.
func TestDefaultGitAcquirerScopesSubmoduleCredentials(t *testing.T) {
	for _, tc := range []struct {
		name          string
		submoduleURL  func(listed, other *submoduletest.HTTPServer) string
		listOther     bool
		wantOtherHits bool
	}{
		{name: "relative URL on the listed host", submoduleURL: func(_, _ *submoduletest.HTTPServer) string { return "../child.git" }},
		{name: "absolute URL on another host", submoduleURL: func(_, other *submoduletest.HTTPServer) string { return other.URL + "/child.git" }, wantOtherHits: true},
		{name: "absolute URL on another listed host", submoduleURL: func(_, other *submoduletest.HTTPServer) string { return other.URL + "/child.git" }, listOther: true, wantOtherHits: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed := submoduletest.ServeSmartHTTP(t, "")
			other := submoduletest.ServeSmartHTTP(t, "")
			parentFileURL := submoduletest.SubmoduleURLFixture(t, tc.submoduleURL(listed, other))
			dir := filepath.Dir(strings.TrimPrefix(parentFileURL, "file://"))
			listed.SetRoot(dir)
			other.SetRoot(dir)
			hosts := []string{listed.URL}
			if tc.listOther {
				hosts = append(hosts, other.URL)
			}
			_, err := DefaultGitAcquirer{}.Acquire(context.Background(), GitRequest{URL: listed.URL + "/parent.git", Revision: "main"}, GitOptions{
				AllowNetwork: true,
				CacheDir:     t.TempDir(),
				Submodules:   true,
				Credentials:  GitCredentials{BearerToken: gitHostTestToken, Hosts: hosts},
			})
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			requireAuthOnEveryRequest(t, listed, "Bearer "+gitHostTestToken)
			if tc.wantOtherHits && len(other.Requests()) == 0 {
				t.Fatal("the submodule host received no request")
			}
			requireNoAuth(t, other)
		})
	}
}
