package credhost

import (
	"fmt"
	"net/http"
)

// MaxRedirects is the redirect limit of the default policy of net/http,
// which ScopeRedirects keeps.
const MaxRedirects = 10

// ScopeRedirects returns a copy of client that removes the Authorization
// header from a redirected request unless every request of the chain has
// the scheme, host, and port of the first one. net/http keeps the header on
// a redirect to the same host name, also to another port, to a subdomain,
// or from https to http (shouldCopyHeaderOnRedirect, net/http/client.go),
// so this policy is stricter. The policy of client still runs after it,
// and without one the limit of 10 redirects of net/http applies.
func ScopeRedirects(client *http.Client) *http.Client {
	scoped := *client
	next := client.CheckRedirect
	scoped.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !SameOrigin(request, via) {
			request.Header.Del("Authorization")
		}
		if next != nil {
			return next(request, via)
		}
		if len(via) >= MaxRedirects {
			return fmt.Errorf("stopped after %d redirects", MaxRedirects)
		}
		return nil
	}
	return &scoped
}

// SameOrigin reports whether request and every request in via have the
// scheme, host, and port of the first request.
func SameOrigin(request *http.Request, via []*http.Request) bool {
	if len(via) == 0 {
		return true
	}
	origin, ok := Of(via[0].URL)
	if !ok {
		return false
	}
	for _, hop := range via[1:] {
		if !Same(hop.URL, origin) {
			return false
		}
	}
	return Same(request.URL, origin)
}
