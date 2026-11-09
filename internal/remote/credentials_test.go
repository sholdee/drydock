package remote

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const credentialTestToken = "scoped-secret-token"

// authRecorder is an http.RoundTripper that answers each URL from a map
// and records the Authorization header of every request it receives. A
// value that starts with "->" is a 302 redirect to the rest of the value.
type authRecorder struct {
	mu        sync.Mutex
	responses map[string]string
	seen      []string
}

func (recorder *authRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder.mu.Lock()
	recorder.seen = append(recorder.seen, request.URL.String()+" auth="+request.Header.Get("Authorization"))
	body, ok := recorder.responses[request.URL.String()]
	recorder.mu.Unlock()
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: scoped\n")),
		Request:    request,
	}
	switch {
	case !ok:
		response.StatusCode = http.StatusNotFound
		response.Body = http.NoBody
	case strings.HasPrefix(body, "->"):
		response.StatusCode = http.StatusFound
		response.Header.Set("Location", strings.TrimPrefix(body, "->"))
		response.Body = http.NoBody
	}
	return response, nil
}

func (recorder *authRecorder) requests() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.seen...)
}

func acquireWithRecorder(t *testing.T, recorder *authRecorder, target string, credentials Credentials) error {
	t.Helper()
	_, err := (DefaultAcquirer{Client: &http.Client{Transport: recorder}}).Acquire(context.Background(), Request{URL: target}, Options{
		CacheDir:    t.TempDir(),
		Credentials: credentials,
	})
	return err
}

// TestDefaultAcquirerSendsCredentialsOnlyToListedHosts checks that the
// --remote-* credentials reach a request only when its scheme, host, and
// port match a --remote-auth-host entry. An entry without a scheme is
// https only, and an entry without a port has the default port of its
// scheme. The host match ignores case.
func TestDefaultAcquirerSendsCredentialsOnlyToListedHosts(t *testing.T) {
	const httpsTarget = "https://files.example.test/resource.yaml"
	const httpTarget = "http://files.example.test/resource.yaml"
	for _, tc := range []struct {
		name     string
		target   string
		hosts    []string
		wantAuth bool
	}{
		{name: "listed host", target: httpsTarget, hosts: []string{"files.example.test"}, wantAuth: true},
		{name: "case of the host", target: httpsTarget, hosts: []string{"FILES.Example.TEST"}, wantAuth: true},
		{name: "default https port", target: httpsTarget, hosts: []string{"files.example.test:443"}, wantAuth: true},
		{name: "explicit https scheme", target: httpsTarget, hosts: []string{"https://files.example.test"}, wantAuth: true},
		{name: "other host", target: httpsTarget, hosts: []string{"other.example.test"}},
		{name: "parent domain", target: httpsTarget, hosts: []string{"example.test"}},
		{name: "other port", target: httpsTarget, hosts: []string{"files.example.test:8443"}},
		{name: "http entry for an https request", target: httpsTarget, hosts: []string{"http://files.example.test"}},
		{name: "plain http without an http entry", target: httpTarget, hosts: []string{"files.example.test"}},
		{name: "plain http with an http entry", target: httpTarget, hosts: []string{"http://files.example.test"}, wantAuth: true},
		{name: "plain http with the http default port", target: httpTarget, hosts: []string{"http://files.example.test:80"}, wantAuth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &authRecorder{responses: map[string]string{tc.target: ""}}
			err := acquireWithRecorder(t, recorder, tc.target, Credentials{BearerToken: credentialTestToken, Hosts: tc.hosts})
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			want := tc.target + " auth="
			if tc.wantAuth {
				want += "Bearer " + credentialTestToken
			}
			if got := recorder.requests(); len(got) != 1 || got[0] != want {
				t.Fatalf("requests = %q, want [%q]", got, want)
			}
		})
	}
}

// TestDefaultAcquirerRefusesCredentialsWithoutHosts checks that drydock
// fails before any request when a credential is set and no
// --remote-auth-host is given, and that the error names the flag and no
// credential value.
func TestDefaultAcquirerRefusesCredentialsWithoutHosts(t *testing.T) {
	for name, credentials := range map[string]Credentials{
		"bearer token": {BearerToken: credentialTestToken},
		"username":     {Username: "scoped-user"},
		"password":     {Password: "scoped-password"},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := &authRecorder{responses: map[string]string{"https://files.example.test/resource.yaml": ""}}
			err := acquireWithRecorder(t, recorder, "https://files.example.test/resource.yaml", credentials)
			if err == nil || !strings.Contains(err.Error(), "--remote-auth-host") {
				t.Fatalf("Acquire() error = %v, want an error that names --remote-auth-host", err)
			}
			for _, secret := range []string{credentialTestToken, "scoped-user", "scoped-password"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Acquire() error %q leaks a credential", err)
				}
			}
			if got := recorder.requests(); len(got) != 0 {
				t.Fatalf("requests = %q, want none", got)
			}
		})
	}
}

// TestDefaultAcquirerDropsCredentialsOnRedirects checks the redirect
// policy. The credentials stay only while every request of the chain has
// the scheme, host, and port of the first request. net/http alone keeps
// the Authorization header on a redirect to another port of the same
// host, to a subdomain, and from https to http (shouldCopyHeaderOnRedirect
// in net/http/client.go). Every host of each chain is listed, so the
// redirect policy, not the host list, removes the header.
func TestDefaultAcquirerDropsCredentialsOnRedirects(t *testing.T) {
	const start = "https://files.example.test/resource.yaml"
	auth := " auth=Bearer " + credentialTestToken
	hosts := []string{"files.example.test", "files.example.test:8443", "sub.files.example.test", "other.example.test", "http://files.example.test"}
	for _, tc := range []struct {
		name      string
		responses map[string]string
		want      []string
	}{
		{
			name:      "same origin",
			responses: map[string]string{start: "->/moved.yaml", "https://files.example.test/moved.yaml": ""},
			want:      []string{start + auth, "https://files.example.test/moved.yaml" + auth},
		},
		{
			name:      "other port",
			responses: map[string]string{start: "->https://files.example.test:8443/resource.yaml", "https://files.example.test:8443/resource.yaml": ""},
			want:      []string{start + auth, "https://files.example.test:8443/resource.yaml auth="},
		},
		{
			name:      "subdomain",
			responses: map[string]string{start: "->https://sub.files.example.test/resource.yaml", "https://sub.files.example.test/resource.yaml": ""},
			want:      []string{start + auth, "https://sub.files.example.test/resource.yaml auth="},
		},
		{
			name:      "downgrade to http",
			responses: map[string]string{start: "->http://files.example.test/resource.yaml", "http://files.example.test/resource.yaml": ""},
			want:      []string{start + auth, "http://files.example.test/resource.yaml auth="},
		},
		{
			name:      "other host",
			responses: map[string]string{start: "->https://other.example.test/resource.yaml", "https://other.example.test/resource.yaml": ""},
			want:      []string{start + auth, "https://other.example.test/resource.yaml auth="},
		},
		{
			name: "back to the first host",
			responses: map[string]string{
				start:                                   "->https://other.example.test/bounce",
				"https://other.example.test/bounce":     "->https://files.example.test/final.yaml",
				"https://files.example.test/final.yaml": "",
			},
			want: []string{start + auth, "https://other.example.test/bounce auth=", "https://files.example.test/final.yaml auth="},
		},
		{
			// net/http keeps the header on each hop of this chain, because
			// the host name never changes.
			name: "back to the first host from another port",
			responses: map[string]string{
				start:                                    "->https://files.example.test:8443/bounce",
				"https://files.example.test:8443/bounce": "->https://files.example.test/final.yaml",
				"https://files.example.test/final.yaml":  "",
			},
			want: []string{start + auth, "https://files.example.test:8443/bounce auth=", "https://files.example.test/final.yaml auth="},
		},
		{
			name: "back to https after a downgrade",
			responses: map[string]string{
				start:                                   "->http://files.example.test/bounce",
				"http://files.example.test/bounce":      "->https://files.example.test/final.yaml",
				"https://files.example.test/final.yaml": "",
			},
			want: []string{start + auth, "http://files.example.test/bounce auth=", "https://files.example.test/final.yaml auth="},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &authRecorder{responses: tc.responses}
			if err := acquireWithRecorder(t, recorder, start, Credentials{BearerToken: credentialTestToken, Hosts: hosts}); err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			got := recorder.requests()
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("requests =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestDefaultAcquirerKeepsTheRedirectLimit checks that the credential
// redirect policy keeps the limit of 10 redirects of net/http.
func TestDefaultAcquirerKeepsTheRedirectLimit(t *testing.T) {
	const loop = "https://files.example.test/loop.yaml"
	recorder := &authRecorder{responses: map[string]string{loop: "->" + loop}}
	err := acquireWithRecorder(t, recorder, loop, Credentials{BearerToken: credentialTestToken, Hosts: []string{"files.example.test"}})
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("Acquire() error = %v, want the redirect limit", err)
	}
	if strings.Contains(err.Error(), credentialTestToken) {
		t.Fatalf("Acquire() error %q leaks the token", err)
	}
}

// TestCredentialsValidateHosts checks the --remote-auth-host entry rules.
// An error quotes the entry without userinfo.
func TestCredentialsValidateHosts(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  string
	}{
		{entry: "", want: "must not be empty"},
		{entry: "files.example.test/path", want: "without a path"},
		{entry: "https://files.example.test/", want: "without a path"},
		{entry: "files.example.test?x=1", want: "without a path"},
		{entry: "user:hunter2@files.example.test", want: "must not include userinfo"},
		{entry: "ftp://files.example.test", want: "must use http or https"},
		{entry: "files.example.test:99999", want: "invalid port"},
		{entry: "files.example.test:0", want: "invalid port"},
		{entry: "https://:443", want: "must include a host"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			err := (Credentials{BearerToken: credentialTestToken, Hosts: []string{tc.entry}}).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), credentialTestToken) {
				t.Fatalf("Validate() error %q leaks a secret", err)
			}
		})
	}
	for _, entry := range []string{"files.example.test", "files.example.test:8443", "http://127.0.0.1:8080", "[::1]:8443", "https://FILES.example.test"} {
		if err := (Credentials{BearerToken: credentialTestToken, Hosts: []string{entry}}).Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", entry, err)
		}
	}
	if err := (Credentials{}).Validate(); err != nil {
		t.Fatalf("Validate() of no credentials error = %v", err)
	}
}
