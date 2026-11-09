package gitauth

import (
	"errors"
	"net/url"
	"strings"

	"github.com/sholdee/drydock/internal/credhost"
)

// AuthHostFlag is the flag that names the hosts of the HTTP Git
// credentials.
const AuthHostFlag = "--git-auth-host"

// ErrMissingHosts names the flag that a CLI user must add, and the field
// that a pkg/drydock caller must set. It never holds a credential value.
var ErrMissingHosts = errors.New("--git-username, --git-password, and --git-bearer-token need --git-auth-host (GitCredentials.Hosts in pkg/drydock): give it once for each host[:port] that receives the Git credentials")

// HasHTTPSecret reports whether a username, a password, or a bearer token
// is set, with the same test that AuthMethod uses to send one.
func (creds Credentials) HasHTTPSecret() bool {
	return strings.TrimSpace(creds.BearerToken) != "" ||
		strings.TrimSpace(creds.Username) != "" ||
		creds.Password != ""
}

// Validate fails when an HTTP credential is set and Hosts is empty, or
// when a Hosts entry is not a host[:port], http://host[:port], or
// https://host[:port]. drydock never guesses the hosts that receive the
// credentials. SSH keys need no Hosts entry.
func (creds Credentials) Validate() error {
	hosts, err := credhost.Parse(AuthHostFlag, creds.Hosts)
	if err != nil {
		return err
	}
	if creds.HasHTTPSecret() && len(hosts) == 0 {
		return ErrMissingHosts
	}
	return nil
}

// AuthorizesHTTP reports whether a request to repoURL receives the HTTP
// credentials: an HTTP credential is set, and the scheme, host, and port
// of repoURL match a Hosts entry exactly. The host name match ignores
// case. An invalid entry or URL authorizes nothing.
func (creds Credentials) AuthorizesHTTP(repoURL string) bool {
	if !creds.HasHTTPSecret() {
		return false
	}
	hosts, err := credhost.Parse(AuthHostFlag, creds.Hosts)
	if err != nil {
		return false
	}
	target, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return false
	}
	return credhost.Contains(hosts, target)
}

// WithURLUserinfo returns the credentials of a request to cleanURL, whose
// URL carried the user name and password of the userinfo. The HTTP
// credentials win when Hosts authorizes cleanURL. Otherwise the userinfo
// becomes the HTTP credentials, and Hosts names only the scheme, host, and
// port of cleanURL, so the userinfo goes nowhere else. The SSH fields do
// not change. Without userinfo, or for a URL that is not http or https,
// creds come back unchanged.
func (creds Credentials) WithURLUserinfo(cleanURL, username, password string) Credentials {
	if username == "" && password == "" {
		return creds
	}
	if creds.AuthorizesHTTP(cleanURL) {
		return creds
	}
	entry, ok := credhost.Entry(cleanURL)
	if !ok {
		return creds
	}
	creds.Username = username
	creds.Password = password
	creds.BearerToken = ""
	creds.Hosts = []string{entry}
	return creds
}

// httpAuthAllowed decides whether AuthMethod sends the HTTP credentials
// to repoURL. It fails when they are set without a valid Hosts entry.
func httpAuthAllowed(creds Credentials, repoURL string) (bool, error) {
	if !creds.HasHTTPSecret() {
		return false, nil
	}
	if err := creds.Validate(); err != nil {
		return false, err
	}
	return creds.AuthorizesHTTP(repoURL), nil
}
