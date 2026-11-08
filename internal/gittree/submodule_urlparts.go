package gittree

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// urlParts are the user, host, port, and path that a client reads from a
// submodule URL. A zero port is the default port of the transport.
type urlParts struct {
	user string
	host string
	port int
	path string
}

func (p urlParts) String() string {
	return fmt.Sprintf("user %q, host %q, port %d, path %q", p.user, p.host, p.port, p.path)
}

// sameURLParts fails unless git and go-git read the same user, host, port,
// and path from value, whose go-git endpoint is endpoint.
func sameURLParts(value string, endpoint *transport.Endpoint) error {
	gitParts, err := gitURLParts(value, endpoint.Protocol)
	if err != nil {
		return err
	}
	goGitParts := urlParts{user: endpoint.User, host: endpoint.Host, port: normalPort(endpoint.Protocol, endpoint.Port), path: endpoint.Path}
	gitParts.port = normalPort(endpoint.Protocol, gitParts.port)
	if gitParts != goGitParts {
		return fmt.Errorf("git reads the submodule URL as %s, but drydock's Git client reads it as %s", gitParts, goGitParts)
	}
	return nil
}

func normalPort(protocol string, port int) int {
	if port == 0 {
		return defaultPorts[protocol]
	}
	return port
}

// gitURLParts reads value as git does. An scp-like URL ("user@host:path")
// is split at its first ":", so git never reads a port from it. A URL with
// "://" is split into its authority, up to the first "/", and its path.
// drydock refuses forms that the two clients may read differently: a
// bracketed host, more than one "@", a percent sign in the authority, a
// ":" in an ssh or git user, and a port that is not a number from 1 to
// 65535.
func gitURLParts(value, protocol string) (urlParts, error) {
	scheme, rest, isURL := strings.Cut(value, "://")
	if !isURL {
		return scpURLParts(value)
	}
	authority, path := rest, ""
	if i := strings.Index(rest, "/"); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	if strings.ContainsAny(authority, "%[]") {
		return urlParts{}, fmt.Errorf("submodule URL authority %q holds a percent sign or a bracket", authority)
	}
	parts, err := splitUserHost(authority, scheme == "http" || scheme == "https")
	if err != nil {
		return parts, err
	}
	parts.path = path
	return splitPort(parts, protocol)
}

// scpURLParts reads an scp-like URL as git does. git reads a bracketed host
// anywhere after an "@" (host_end in git's connect.c), and drydock's Git
// client does not, so a bracket fails. A path that starts with ":", or
// holds a "~" after its first character, fails too: git and go-git read
// it differently.
func scpURLParts(value string) (urlParts, error) {
	if strings.ContainsAny(value, "[]") {
		return urlParts{}, fmt.Errorf("submodule URL %q holds a bracket", value)
	}
	hostPart, path, _ := strings.Cut(value, ":")
	if strings.HasPrefix(path, ":") || strings.Contains(strings.TrimPrefix(path, "~"), "~") {
		return urlParts{}, fmt.Errorf("submodule URL path %q is not supported", path)
	}
	parts, err := splitUserHost(hostPart, false)
	parts.path = path
	return parts, err
}

// splitUserHost splits "user@host" with at most one "@". With password, a
// user may be "user:password", as in an http URL.
func splitUserHost(value string, password bool) (urlParts, error) {
	// git refuses a user or host that starts with "-" ("strange
	// hostname"), which ssh would read as an option.
	if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "-") || strings.Count(value, "@") > 1 {
		return urlParts{}, fmt.Errorf("submodule URL host %q is not supported", value)
	}
	user, host, found := strings.Cut(value, "@")
	if !found {
		return urlParts{host: value}, nil
	}
	if user == "" {
		return urlParts{}, fmt.Errorf("submodule URL %q has an empty user", value)
	}
	if strings.Contains(user, ":") {
		if !password {
			return urlParts{}, fmt.Errorf("submodule URL user %q holds a \":\"", user)
		}
		user, _, _ = strings.Cut(user, ":")
	}
	return urlParts{user: user, host: host}, nil
}

// splitPort splits the port off the host of a URL authority.
func splitPort(parts urlParts, protocol string) (urlParts, error) {
	host, portText, found := strings.Cut(parts.host, ":")
	if !found {
		return parts, nil
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return urlParts{}, fmt.Errorf("submodule URL port %q is not valid for %s", portText, protocol)
	}
	parts.host = host
	parts.port = port
	return parts, nil
}

// URLUserinfo is the user and password that a URL with "://" carries.
type URLUserinfo struct {
	Username string
	Password string
}

// SplitURLUserinfo removes the userinfo from a URL with "://", so the URL
// that drydock clones from and stores in a cache entry never holds a
// token. An ssh URL keeps its user, which names the ssh login, and loses
// only a password. Other URLs and an scp-like URL come back as they are.
// found reports that the URL held userinfo.
func SplitURLUserinfo(value string) (clean string, info URLUserinfo, found bool) {
	scheme, rest, isURL := strings.Cut(value, "://")
	if !isURL {
		return value, URLUserinfo{}, false
	}
	authority, path := rest, ""
	if i := strings.Index(rest, "/"); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return value, URLUserinfo{}, false
	}
	user, password, _ := strings.Cut(authority[:at], ":")
	info = URLUserinfo{Username: unescapeUserinfo(user), Password: unescapeUserinfo(password)}
	host := authority[at+1:]
	if scheme == "ssh" {
		return scheme + "://" + user + "@" + host + path, URLUserinfo{Password: info.Password}, password != ""
	}
	return scheme + "://" + host + path, info, true
}

func unescapeUserinfo(value string) string {
	if unescaped, err := url.PathUnescape(value); err == nil {
		return unescaped
	}
	return value
}
