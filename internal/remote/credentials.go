package remote

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sholdee/drydock/internal/credhost"
)

// maxCredentialRedirects is the redirect limit of the default policy of
// net/http, which the credential redirect policy keeps.
const maxCredentialRedirects = 10

// remoteAuthHostFlag is the flag that names the hosts of the remote
// credentials.
const remoteAuthHostFlag = "--remote-auth-host"

// errMissingCredentialHosts names the flag that a CLI user must add, and
// the field that a pkg/drydock caller must set. It never holds a
// credential value.
var errMissingCredentialHosts = errors.New("--remote-username, --remote-password, and --remote-bearer-token need --remote-auth-host (RemoteResourceCredentials.Hosts in pkg/drydock): give it once for each host[:port] that receives the remote credentials")

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
	hosts, err := credhost.Parse(remoteAuthHostFlag, credentials.Hosts)
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
	hosts, err := credhost.Parse(remoteAuthHostFlag, credentials.Hosts)
	if err != nil {
		return false
	}
	return credhost.Contains(hosts, target)
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
	origin, ok := credhost.Of(via[0].URL)
	if !ok {
		return false
	}
	for _, hop := range via[1:] {
		if !credhost.Same(hop.URL, origin) {
			return false
		}
	}
	return credhost.Same(request.URL, origin)
}
