package gittree

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ResolveSubmoduleURL resolves the URL of a submodule as `git submodule
// init` and `git submodule sync` do (relative_url in git's
// builtin/submodule--helper.c): a URL that starts with "./" or "../" is
// relative to remoteURL, the URL of the superproject's remote. In the Argo
// CD repo-server, that remote is origin, with the repoURL of the
// Application (util/git/client.go Init). Any other URL is returned as it
// is.
func ResolveSubmoduleURL(remoteURL, submoduleURL string) (string, error) {
	if !isRelativeSubmoduleURL(submoduleURL) {
		return submoduleURL, nil
	}
	remote := strings.TrimSuffix(remoteURL, "/")
	if remote == "" {
		return "", fmt.Errorf("relative submodule URL %q needs the URL of the superproject", submoduleURL)
	}
	relativeRemote := urlIsLocalNotSSH(remote) && !strings.HasPrefix(remote, "/")
	if relativeRemote && !strings.HasPrefix(remote, "./") && !strings.HasPrefix(remote, "../") {
		remote = "./" + remote
	}
	rest := submoduleURL
	colonSeparated := false
	for {
		switch {
		case strings.HasPrefix(rest, "../"):
			rest = rest[3:]
			chopped, colon, err := chopLastDir(remote, relativeRemote)
			if err != nil {
				return "", err
			}
			remote = chopped
			colonSeparated = colonSeparated || colon
			continue
		case strings.HasPrefix(rest, "./"):
			rest = rest[2:]
			continue
		}
		break
	}
	separator := "/"
	if colonSeparated {
		separator = ":"
	}
	out := remote + separator + rest
	if strings.HasSuffix(rest, "/") {
		out = strings.TrimSuffix(out, "/")
	}
	return strings.TrimPrefix(out, "./"), nil
}

// isRelativeSubmoduleURL reports whether git resolves url against the
// remote of the superproject: a local-looking URL (no ":" before the first
// "/") that starts with "./" or "../".
func isRelativeSubmoduleURL(value string) bool {
	return urlIsLocalNotSSH(value) && (strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../"))
}

// urlIsLocalNotSSH mirrors url_is_local_not_ssh in git: a URL with no ":",
// or with a "/" before its first ":".
func urlIsLocalNotSSH(value string) bool {
	colon := strings.Index(value, ":")
	slash := strings.Index(value, "/")
	return colon < 0 || (slash >= 0 && slash < colon)
}

// chopLastDir mirrors chop_last_dir in git: it removes the last "/"
// component of remote, or else the part after its last ":" (an scp-like
// URL), which colon then reports.
func chopLastDir(remote string, relativeRemote bool) (string, bool, error) {
	if before, _, ok := strings.CutLast(remote, "/"); ok {
		return before, false, nil
	}
	if before, _, ok := strings.CutLast(remote, ":"); ok {
		return before, true, nil
	}
	if relativeRemote || remote == "." {
		return "", false, fmt.Errorf("cannot strip one component off url %q", remote)
	}
	return ".", false, nil
}

// remoteHelperPattern matches the <transport>:: prefix that makes git run
// git-remote-<transport>: letters, digits, "+", ".", and "-", starting with
// a letter or digit, followed by "::" (transport_get in git's transport.c).
// So "192.0.2.1::repo" runs the helper "192.0.2.1".
var remoteHelperPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+.-]*::`)

// SubmoduleTransport returns the transport that git uses for a resolved
// submodule URL: "http", "https", "git", "ssh", "file", another scheme
// as written, or "remote-helper:<name>" for <name>::<address>. git matches
// the built-in schemes in lower case only, so "HTTPS://" names a remote
// helper, which AllowedSubmoduleTransport refuses. An scp-like URL
// (host:path, with no "/" before the ":") is ssh. A path is file.
func SubmoduleTransport(value string) string {
	if helper := remoteHelperPattern.FindString(value); helper != "" {
		return "remote-helper:" + strings.TrimSuffix(helper, "::")
	}
	if i := strings.Index(value, "://"); i > 0 {
		return value[:i]
	}
	if !urlIsLocalNotSSH(value) {
		return "ssh"
	}
	return "file"
}

// AllowedSubmoduleTransport reports whether the Argo CD repo-server's git
// would clone a submodule over transport, and drydock can too. git allows
// http, https, git, and ssh always. It allows file only when the user runs
// the command, and `git submodule` clones with GIT_PROTOCOL_FROM_USER=0, so
// a file URL or a local path fails there (protocol.file.allow defaults to
// "user" since git 2.38.1). ext and every other transport are not allowed.
// git+ssh and ssh+git are ssh to git, but go-git does not support them, so
// drydock refuses them.
func AllowedSubmoduleTransport(transport string) bool {
	switch transport {
	case "http", "https", "git", "ssh":
		return true
	}
	return false
}

// ClassifySubmoduleURL returns the endpoint that go-git fetches a resolved
// submodule URL from, and fails when drydock must not fetch it:
//
//   - the URL holds a control character;
//   - git would use a transport that AllowedSubmoduleTransport refuses, or
//     the file transport without allowFile;
//   - go-git would use another transport than git, such as a local path
//     for "x y:/path", which git reads as ssh to the host "x y";
//   - git and go-git read another user, host, port, or path from it
//     (gitURLParts), such as "host:2222:path", where go-git finds a port
//     and git a path;
//   - the host is empty, starts with "-", or holds a space, a control
//     character, or a bracket, or the path is empty or starts with "-",
//     which git refuses.
func ClassifySubmoduleURL(resolved string, allowFile bool) (*transport.Endpoint, error) {
	if strings.ContainsFunc(resolved, unicode.IsControl) {
		return nil, fmt.Errorf("submodule URL holds a control character")
	}
	gitTransport := SubmoduleTransport(resolved)
	if !AllowedSubmoduleTransport(gitTransport) && (gitTransport != "file" || !allowFile) {
		return nil, fmt.Errorf("transport %q not allowed", gitTransport)
	}
	endpoint, err := transport.NewEndpoint(resolved)
	if err != nil {
		return nil, fmt.Errorf("parse submodule URL: %w", err)
	}
	if endpoint.Protocol != gitTransport {
		return nil, fmt.Errorf("git reads the submodule URL as transport %q, but drydock's Git client reads it as %q", gitTransport, endpoint.Protocol)
	}
	if endpoint.Protocol == "file" {
		return endpoint, nil
	}
	if err := sameURLParts(resolved, endpoint); err != nil {
		return nil, err
	}
	return endpoint, validEndpoint(endpoint)
}

func validEndpoint(endpoint *transport.Endpoint) error {
	if !validEndpointHost(endpoint.Host) {
		return fmt.Errorf("submodule URL host %q is not valid", endpoint.Host)
	}
	if strings.Trim(endpoint.Path, "/") == "" {
		return fmt.Errorf("submodule URL has no repository path")
	}
	if strings.HasPrefix(endpoint.Path, "-") {
		return fmt.Errorf("submodule URL path %q starts with \"-\"", endpoint.Path)
	}
	return nil
}

// CheckEndpointTransport fails unless go-git fetches url over protocol.
// Callers run it right before a clone or fetch, so a URL that a change
// made between classification and fetch reads differently is refused.
func CheckEndpointTransport(url, protocol string) error {
	if protocol == "" {
		return nil
	}
	endpoint, err := transport.NewEndpoint(url)
	if err != nil {
		return err
	}
	if endpoint.Protocol != protocol {
		return fmt.Errorf("drydock's Git client reads the URL as transport %q, want %q", endpoint.Protocol, protocol)
	}
	return nil
}

func validEndpointHost(host string) bool {
	if host == "" || strings.HasPrefix(host, "-") {
		return false
	}
	return !strings.ContainsFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '[' || r == ']'
	})
}

// credentialScope is where credentials may go: the protocol, host, and
// port of an endpoint.
type credentialScope struct {
	protocol string
	host     string
	port     int
}

func endpointScope(url string) (credentialScope, bool) {
	endpoint, err := transport.NewEndpoint(url)
	if err != nil || endpoint.Host == "" {
		return credentialScope{}, false
	}
	port := endpoint.Port
	if port == 0 {
		port = defaultPorts[endpoint.Protocol]
	}
	return credentialScope{protocol: endpoint.Protocol, host: strings.ToLower(endpoint.Host), port: port}, true
}

var defaultPorts = map[string]int{"http": 80, "https": 443, "ssh": 22, "git": 9418}

// SameCredentialScope reports whether HTTP credentials for superURL may go
// to submoduleURL: the same host and port, and never from https to http.
func SameCredentialScope(superURL, submoduleURL string) bool {
	super, ok := endpointScope(superURL)
	if !ok {
		return false
	}
	sub, ok := endpointScope(submoduleURL)
	if !ok || super.host != sub.host || super.port != sub.port {
		return false
	}
	return super.protocol != "https" || sub.protocol != "http"
}
