package remote

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// maxCredentialRedirects is the redirect limit of the default policy of
// net/http, which the credential redirect policy keeps.
const maxCredentialRedirects = 10

// errMissingCredentialHosts names the flag that a user must add. It never
// holds a credential value.
var errMissingCredentialHosts = errors.New("--remote-username, --remote-password, and --remote-bearer-token need --remote-auth-host: give it once for each host[:port] that receives the remote credentials")

// credentialHost is one host that receives the remote credentials: a
// scheme, a lower-case host name, and a port. A host entry without a scheme
// is https, and a missing port is the default port of the scheme.
type credentialHost struct {
	scheme string
	host   string
	port   string
}

// HasSecret reports whether any credential value is set, with the same
// test that applyHTTPAuth uses to send one.
func (credentials Credentials) HasSecret() bool {
	return strings.TrimSpace(credentials.BearerToken) != "" ||
		strings.TrimSpace(credentials.Username) != "" ||
		credentials.Password != ""
}

// Validate fails when a credential is set and Hosts is empty, or when a
// Hosts entry is not a host[:port], http://host[:port], or
// https://host[:port]. drydock never guesses the hosts that receive the
// credentials.
func (credentials Credentials) Validate() error {
	hosts, err := parseCredentialHosts(credentials.Hosts)
	if err != nil {
		return err
	}
	if credentials.HasSecret() && len(hosts) == 0 {
		return errMissingCredentialHosts
	}
	return nil
}

// authorizes reports whether a request to target receives the
// credentials: its scheme, host, and port match a Hosts entry exactly. The
// host name match ignores case.
func (credentials Credentials) authorizes(target *url.URL) bool {
	if !credentials.HasSecret() {
		return false
	}
	hosts, err := parseCredentialHosts(credentials.Hosts)
	if err != nil {
		return false
	}
	want, ok := credentialHostOf(target)
	return ok && slices.Contains(hosts, want)
}

func parseCredentialHosts(entries []string) ([]credentialHost, error) {
	out := make([]credentialHost, 0, len(entries))
	for _, entry := range entries {
		host, err := parseCredentialHost(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, host)
	}
	return out, nil
}

// parseCredentialHost reads one --remote-auth-host entry. The errors quote
// the entry without userinfo, so a password typed into an entry by mistake
// never reaches the output.
func parseCredentialHost(entry string) (credentialHost, error) {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return credentialHost{}, errors.New("--remote-auth-host must not be empty")
	}
	withScheme := raw
	if !strings.Contains(raw, "://") {
		withScheme = "https://" + raw
	}
	parsed, err := url.Parse(withScheme)
	if err != nil {
		return credentialHost{}, fmt.Errorf("--remote-auth-host %q is not a valid host[:port]", RedactURL(withScheme))
	}
	if err := checkCredentialHostURL(parsed); err != nil {
		return credentialHost{}, fmt.Errorf("--remote-auth-host %q %w", RedactURL(withScheme), err)
	}
	host, ok := credentialHostOf(parsed)
	if !ok {
		return credentialHost{}, fmt.Errorf("--remote-auth-host %q must include a host", RedactURL(withScheme))
	}
	return host, nil
}

// checkCredentialHostURL fails unless parsed is a bare http or https
// origin with a valid port. Its errors complete the sentence that starts
// with the entry.
func checkCredentialHostURL(parsed *url.URL) error {
	if parsed.User != nil {
		return errors.New("must not include userinfo")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("must use http or https")
	}
	if parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return errors.New("must be a host[:port] without a path, query, or fragment")
	}
	if port := parsed.Port(); port != "" {
		if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
			return errors.New("has an invalid port")
		}
	}
	return nil
}

// credentialHostOf returns the scheme, host, and port of target, with the
// default port of the scheme when target has none.
func credentialHostOf(target *url.URL) (credentialHost, bool) {
	if target == nil {
		return credentialHost{}, false
	}
	scheme := strings.ToLower(target.Scheme)
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return credentialHost{}, false
	}
	port := target.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return credentialHost{}, false
		}
	}
	return credentialHost{scheme: scheme, host: host, port: port}, true
}

// withCredentialRedirectPolicy returns a copy of client that removes the
// Authorization header from a redirected request unless every request of
// the chain has the scheme, host, and port of the first one. net/http
// keeps the header on a redirect to the same host name, also to another
// port, to a subdomain, or from https to http (shouldCopyHeaderOnRedirect,
// net/http/client.go), so this policy is stricter. The policy of client
// still runs after it, and without one the limit of 10 redirects of
// net/http applies.
func withCredentialRedirectPolicy(client *http.Client) *http.Client {
	scoped := *client
	next := client.CheckRedirect
	scoped.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !sameCredentialOrigin(request, via) {
			request.Header.Del("Authorization")
		}
		if next != nil {
			return next(request, via)
		}
		if len(via) >= maxCredentialRedirects {
			return fmt.Errorf("stopped after %d redirects", maxCredentialRedirects)
		}
		return nil
	}
	return &scoped
}

// sameCredentialOrigin reports whether request and every request in via
// have the scheme, host, and port of the first request.
func sameCredentialOrigin(request *http.Request, via []*http.Request) bool {
	if len(via) == 0 {
		return true
	}
	origin, ok := credentialHostOf(via[0].URL)
	if !ok {
		return false
	}
	for _, hop := range via[1:] {
		if !hasCredentialHost(hop.URL, origin) {
			return false
		}
	}
	return hasCredentialHost(request.URL, origin)
}

func hasCredentialHost(target *url.URL, want credentialHost) bool {
	got, ok := credentialHostOf(target)
	return ok && got == want
}
