package gitauth

import (
	"errors"
	"strings"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// TestAuthMethodSendsHTTPCredentialsOnlyToListedHosts checks the HTTP
// branch of AuthMethod: it returns the auth method only for a URL whose
// scheme, host, and port match a Hosts entry, returns no auth for any
// other URL, and fails without a Hosts entry.
func TestAuthMethodSendsHTTPCredentialsOnlyToListedHosts(t *testing.T) {
	const target = "https://git.example.test/org/repo.git"
	for _, tc := range []struct {
		name     string
		hosts    []string
		repoURL  string
		wantAuth bool
	}{
		{name: "listed host", hosts: []string{"git.example.test"}, repoURL: target, wantAuth: true},
		{name: "case of the host", hosts: []string{"GIT.example.test"}, repoURL: "https://Git.Example.Test/org/repo.git", wantAuth: true},
		{name: "default https port", hosts: []string{"git.example.test:443"}, repoURL: target, wantAuth: true},
		{name: "userinfo in the URL", hosts: []string{"git.example.test"}, repoURL: "https://ci@git.example.test/org/repo.git", wantAuth: true},
		{name: "other host", hosts: []string{"other.example.test"}, repoURL: target},
		{name: "subdomain", hosts: []string{"example.test"}, repoURL: target},
		{name: "host that starts with the entry", hosts: []string{"git.example.test"}, repoURL: "https://git.example.test.evil.test/org/repo.git"},
		{name: "userinfo that names the entry", hosts: []string{"git.example.test"}, repoURL: "https://git.example.test@evil.test/org/repo.git"},
		{name: "other port", hosts: []string{"git.example.test"}, repoURL: "https://git.example.test:8443/org/repo.git"},
		{name: "plain http without an http entry", hosts: []string{"git.example.test"}, repoURL: "http://git.example.test/org/repo.git"},
		{name: "plain http with an http entry", hosts: []string{"http://git.example.test"}, repoURL: "http://git.example.test/org/repo.git", wantAuth: true},
		{name: "http entry for an https URL", hosts: []string{"http://git.example.test"}, repoURL: target},
		{name: "invalid URL", hosts: []string{"git.example.test"}, repoURL: "https://git.example.test:bad/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, ok, _, err := AuthMethod(Credentials{BearerToken: "secret-token", Hosts: tc.hosts}, tc.repoURL, Environment{})
			if err != nil {
				t.Fatalf("AuthMethod() error = %v", err)
			}
			if ok != tc.wantAuth || (auth != nil) != tc.wantAuth {
				t.Fatalf("AuthMethod() = %v, %v; want auth %v", auth, ok, tc.wantAuth)
			}
			if tc.wantAuth {
				if token, isToken := auth.(*githttp.TokenAuth); !isToken || token.Token != "secret-token" {
					t.Fatalf("AuthMethod() = %#v, want the bearer token", auth)
				}
			}
		})
	}
	_, _, _, err := AuthMethod(Credentials{BearerToken: "secret-token"}, target, Environment{})
	if !errors.Is(err, ErrMissingHosts) {
		t.Fatalf("AuthMethod() without Hosts error = %v, want ErrMissingHosts", err)
	}
	if auth, ok, _, err := AuthMethod(Credentials{}, target, Environment{}); err != nil || ok || auth != nil {
		t.Fatalf("AuthMethod() without credentials = %v, %v, %v; want no auth", auth, ok, err)
	}
}

// TestCredentialsValidateGitHosts checks the --git-auth-host entry rules,
// which are those of --remote-auth-host. An error names --git-auth-host
// and quotes the entry without userinfo.
func TestCredentialsValidateGitHosts(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  string
	}{
		{entry: "", want: "must not be empty"},
		{entry: "git.example.test/org", want: "without a path"},
		{entry: "https://git.example.test/org/", want: "without a path"},
		{entry: "user:hunter2@git.example.test", want: "must not include userinfo"},
		{entry: "ssh://git.example.test", want: "must use http or https"},
		{entry: "git.example.test:0", want: "invalid port"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			err := (Credentials{BearerToken: "secret-token", Hosts: []string{tc.entry}}).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "--git-auth-host") {
				t.Fatalf("Validate() error = %v, want %q and --git-auth-host", err, tc.want)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("Validate() error %q leaks a secret", err)
			}
		})
	}
	if err := (Credentials{BearerToken: "secret-token"}).Validate(); !errors.Is(err, ErrMissingHosts) {
		t.Fatalf("Validate() without Hosts error = %v, want ErrMissingHosts", err)
	}
	if err := (Credentials{SSHPrivateKeyPath: "/keys/id_ed25519"}).Validate(); err != nil {
		t.Fatalf("Validate() of an SSH key error = %v, want none", err)
	}
}

// TestWithURLUserinfo checks which credentials a URL with userinfo uses.
func TestWithURLUserinfo(t *testing.T) {
	listed := Credentials{BearerToken: "secret-token", SSHPrivateKeyPath: "/keys/id", Hosts: []string{"git.example.test"}}
	if got := listed.WithURLUserinfo("https://git.example.test/org/repo.git", "ci", "url-pass"); got.BearerToken != "secret-token" || got.Username != "" {
		t.Fatalf("listed host credentials = %+v, want the --git-* token", got)
	}
	got := listed.WithURLUserinfo("https://other.example.test:8443/org/repo.git", "ci", "url-pass")
	if got.BearerToken != "" || got.Username != "ci" || got.Password != "url-pass" || got.SSHPrivateKeyPath != "/keys/id" {
		t.Fatalf("other host credentials = %+v, want the userinfo without the token", got)
	}
	if len(got.Hosts) != 1 || got.Hosts[0] != "https://other.example.test:8443" {
		t.Fatalf("other host Hosts = %q, want only the URL's own host", got.Hosts)
	}
	if !got.AuthorizesHTTP("https://other.example.test:8443/org/other.git") || got.AuthorizesHTTP("https://git.example.test/org/repo.git") {
		t.Fatalf("userinfo credentials %+v authorize the wrong hosts", got)
	}
	if got := listed.WithURLUserinfo("https://other.example.test/org/repo.git", "", ""); got.BearerToken != "secret-token" || len(got.Hosts) != 1 {
		t.Fatalf("credentials without userinfo = %+v, want them unchanged", got)
	}
}
