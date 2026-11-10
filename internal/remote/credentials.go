package remote

import (
	"errors"
	"net/url"
	"strings"

	"github.com/sholdee/drydock/internal/credhost"
)

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
