package gitauth

import (
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/sholdee/drydock/internal/giturl"
)

// Credentials are the configured Git credential values across every supported
// transport: SSH keys, an HTTP bearer token, or HTTP basic auth.
type Credentials struct {
	Username          string
	Password          string
	BearerToken       string
	SSHPrivateKeyPath string
	SSHPrivateKey     string
	SSHPassphrase     string
	SSHKnownHostsPath string
	// Hosts lists the hosts that receive Username, Password, and
	// BearerToken, one --git-auth-host entry each: host[:port] or
	// https://host[:port] for HTTPS, and http://host[:port] to allow
	// plain HTTP. The SSH fields ignore it.
	Hosts []string
}

// AuthMethod builds the go-git auth method for repoURL. SSH and SCP-style URLs
// resolve through SSHAuth against env. Every other URL uses the bearer token
// when one is set, then username/password, and otherwise no auth at all, but
// only when the scheme, host, and port of repoURL match a Hosts entry
// (AuthorizesHTTP). For any other URL it returns no auth. It fails when an
// HTTP credential is set and Hosts is empty or invalid. The second result
// reports whether an auth method was produced, and the Resolution describes
// SSH identity resolution (zero for the non-SSH branches).
//
// go-git sends the auth method with the first request, before the server
// asks for it, so this check is what keeps a repository from collecting the
// credentials with a URL on its own host. go-git removes the auth when a
// redirect leaves the scheme, host, and port of the first request
// (stripCredentials, plumbing/transport/http/common.go).
//
// Errors never name repoURL: callers wrap them with their own redacted URL.
func AuthMethod(creds Credentials, repoURL string, env Environment) (transport.AuthMethod, bool, Resolution, error) {
	if giturl.IsSSHURL(repoURL) {
		auth, res, err := SSHAuth(SSHCredentials{
			PrivateKeyPath: creds.SSHPrivateKeyPath,
			PrivateKey:     creds.SSHPrivateKey,
			Passphrase:     creds.SSHPassphrase,
			KnownHostsPath: creds.SSHKnownHostsPath,
		}, repoURL, env)
		return auth, auth != nil, res, err
	}
	allowed, err := httpAuthAllowed(creds, repoURL)
	if err != nil || !allowed {
		return nil, false, Resolution{}, err
	}
	if strings.TrimSpace(creds.BearerToken) != "" {
		return &githttp.TokenAuth{Token: creds.BearerToken}, true, Resolution{}, nil
	}
	if strings.TrimSpace(creds.Username) != "" || creds.Password != "" {
		return &githttp.BasicAuth{Username: creds.Username, Password: creds.Password}, true, Resolution{}, nil
	}
	return nil, false, Resolution{}, nil
}
