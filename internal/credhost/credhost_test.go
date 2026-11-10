package credhost

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// TestParseReadsHostEntries pins the entry forms: host[:port] is https,
// http:// allows plain http, and anything with a path, userinfo, or
// another scheme fails without quoting a password.
func TestParseReadsHostEntries(t *testing.T) {
	hosts, err := Parse("--git-auth-host", []string{"Git.Example.test", "http://git.example.test:8080", "https://other.example.test:8443"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for raw, want := range map[string]bool{
		"https://git.example.test/org/repo":     true,
		"https://GIT.example.test:443/x":        true,
		"http://git.example.test/x":             false,
		"http://git.example.test:8080/x":        true,
		"https://git.example.test:8080/x":       false,
		"https://sub.git.example.test/x":        false,
		"https://other.example.test:8443/r":     true,
		"https://other.example.test/r":          false,
		"ssh://git.example.test/org/repo":       false,
		"https://example.test/git.example.test": false,
	} {
		if got := Contains(hosts, mustURL(t, raw)); got != want {
			t.Errorf("Contains(%s) = %t, want %t", raw, got, want)
		}
	}
	for _, entry := range []string{"", "ftp://git.example.test", "git.example.test/path", "user:hunter2@git.example.test", "git.example.test:99999", "https://git.example.test?x=1"} {
		_, err := Parse("--git-auth-host", []string{entry})
		if err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", entry)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("Parse(%q) error %q quotes the password", entry, err)
		}
	}
}

// TestEntryAndSameNameOneOrigin pins Entry and Same, which name and
// compare the scheme, host, and default port.
func TestEntryAndSameNameOneOrigin(t *testing.T) {
	if entry, ok := Entry("https://User:pw@Git.Example.test/org/repo"); !ok || entry != "https://git.example.test:443" {
		t.Fatalf("Entry() = %q, %t", entry, ok)
	}
	if _, ok := Entry("ssh://git.example.test/repo"); ok {
		t.Fatal("Entry(ssh) succeeded")
	}
	origin, _ := Of(mustURL(t, "https://git.example.test"))
	if !Same(mustURL(t, "https://git.example.test:443/x"), origin) || Same(mustURL(t, "http://git.example.test/x"), origin) {
		t.Fatal("Same() does not compare scheme, host, and default port")
	}
}

// TestScopeRedirectsDropsAuthorizationOffOrigin pins that a redirect to
// another port of the same host, which net/http treats as the same host,
// loses Authorization, and that a redirect on the same origin keeps it.
func TestScopeRedirectsDropsAuthorizationOffOrigin(t *testing.T) {
	var seen []string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.URL.Path+"="+request.Header.Get("Authorization"))
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.URL.Path+"="+request.Header.Get("Authorization"))
		if request.URL.Path == "/same" {
			http.Redirect(writer, request, "/final", http.StatusTemporaryRedirect)
			return
		}
		if request.URL.Path == "/other" {
			http.Redirect(writer, request, target.URL+"/moved", http.StatusTemporaryRedirect)
		}
	}))
	t.Cleanup(origin.Close)
	client := ScopeRedirects(origin.Client())
	for _, path := range []string{"/same", "/other"} {
		request, _ := http.NewRequest(http.MethodGet, origin.URL+path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	want := []string{"/same=Bearer token", "/final=Bearer token", "/other=Bearer token", "/moved="}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %q, want %q", seen, want)
	}
	if !SameOrigin(&http.Request{URL: mustURL(t, "https://a.example.test/x")}, nil) {
		t.Fatal("SameOrigin() without hops = false")
	}
}

// TestScopeRedirectsKeepsTheRedirectLimit pins the limit of 10 redirects.
func TestScopeRedirectsKeepsTheRedirectLimit(t *testing.T) {
	loop := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/again", http.StatusFound)
	}))
	t.Cleanup(loop.Close)
	response, err := ScopeRedirects(loop.Client()).Get(loop.URL)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("Get() followed an endless redirect")
	}
	if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("Get() error = %v", err)
	}
}
