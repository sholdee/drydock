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

// sameURLParts fails unless git and go-git read the same user, host, port,
// and path from value, whose go-git endpoint is endpoint.
func sameURLParts(value string, endpoint *transport.Endpoint) error {
	gitParts, err := gitURLParts(value, endpoint.Protocol)
	if err != nil {
		return err
	}
	goGitParts := urlParts{user: endpoint.User, host: endpoint.Host, port: normalPort(endpoint.Protocol, endpoint.Port), path: requestPath(endpoint)}
	gitParts.port = normalPort(endpoint.Protocol, gitParts.port)
	if gitParts != goGitParts {
		// No part is named: git can read a token as the host, as in
		// "TOKEN:x-oauth-basic@github.com:org/repo".
		return fmt.Errorf("git and drydock's Git client read the submodule URL differently")
	}
	return nil
}

// requestPath is the path that go-git sends for endpoint. go-git keeps the
// decoded path, and its http client sends it escaped again, so an http
// path such as "/My%20Project/_git/repo" reads the same as git's raw path.
func requestPath(endpoint *transport.Endpoint) string {
	if endpoint.Protocol == "http" || endpoint.Protocol == "https" {
		return (&url.URL{Path: endpoint.Path}).EscapedPath()
	}
	return endpoint.Path
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
// drydock refuses forms that the two clients may read differently
// (checkURLForm), a ":" in an ssh or git user, and a port that is not a
// number from 1 to 65535.
func gitURLParts(value, protocol string) (urlParts, error) {
	scheme, rest, isURL := strings.Cut(value, "://")
	if !isURL {
		return scpURLParts(value)
	}
	authority, path := rest, ""
	if i := strings.Index(rest, "/"); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	if err := checkURLForm(scheme, value, authority); err != nil {
		return urlParts{}, err
	}
	parts, err := splitUserHost(authority, scheme == "http" || scheme == "https")
	if err != nil {
		return parts, err
	}
	parts.path = path
	return splitPort(parts, protocol)
}

// checkURLForm refuses the parts of a URL with "://" that git, curl, and
// go-git read differently:
//
//   - a percent sign, a bracket, "#", "?", or "\" in the authority, such
//     as "https://u#@host/", where curl connects to the host "u";
//   - "#" or "?" anywhere in an http or https URL: a query can carry a
//     token, which the cache entry would store, and git and go-git split
//     it off differently;
//   - "[" or "]" anywhere in an ssh or git URL: git reads "@[host]" after
//     the authority as the host (host_end in connect.c);
//   - a user in a git URL, which git reads as part of the host.
func checkURLForm(scheme, value, authority string) error {
	if strings.ContainsAny(authority, "[]#?\\") || strings.Contains(hostOnly(authority), "%") {
		return fmt.Errorf("submodule URL authority holds a bracket, \"#\", \"?\", or \"\\\", or its host a percent sign")
	}
	switch scheme {
	case "http", "https":
		if strings.ContainsAny(value, "#?") {
			return fmt.Errorf("submodule URL holds a query or a fragment, which drydock does not fetch")
		}
		return checkHTTPPath(value[len(scheme)+len("://")+len(authority):])
	case "ssh", "git":
		if strings.ContainsAny(value, "[]") {
			return fmt.Errorf("submodule URL holds a bracket")
		}
	}
	if scheme == "git" && strings.Contains(authority, "@") {
		return fmt.Errorf("submodule URL has a user, which the git transport does not take")
	}
	return nil
}

// checkHTTPPath refuses an http path whose decoded form holds "?", "#",
// or "%": git sends the path as written, and go-git decodes it and
// escapes it again, which turns "%3F" into a query, "%23" into a
// fragment, and "%25" into another escape.
func checkHTTPPath(path string) error {
	decoded, err := url.PathUnescape(path)
	if err != nil || strings.ContainsAny(decoded, "?#%") {
		return fmt.Errorf("submodule URL path holds an escape that drydock's Git client sends differently")
	}
	return nil
}

// scpURLParts reads an scp-like URL as git does. git reads a bracketed host
// anywhere after an "@" (host_end in git's connect.c), and drydock's Git
// client does not, so a bracket fails. A path that starts with ":", or
// holds a "~" after its first character, fails too: git and go-git read
// it differently.
func scpURLParts(value string) (urlParts, error) {
	if strings.ContainsAny(value, "[]") {
		return urlParts{}, fmt.Errorf("submodule URL holds a bracket")
	}
	hostPart, path, _ := strings.Cut(value, ":")
	if strings.HasPrefix(path, ":") || strings.Contains(strings.TrimPrefix(path, "~"), "~") {
		return urlParts{}, fmt.Errorf("submodule URL path is not supported")
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
		return urlParts{}, fmt.Errorf("submodule URL host %q is not supported", hostOnly(value))
	}
	user, host, found := strings.Cut(value, "@")
	if !found {
		return urlParts{host: value}, nil
	}
	if user == "" {
		return urlParts{}, fmt.Errorf("submodule URL host %q has an empty user", hostOnly(value))
	}
	if strings.Contains(user, ":") {
		if !password {
			return urlParts{}, fmt.Errorf("submodule URL has a password, which only http and https take")
		}
		user, _, _ = strings.Cut(user, ":")
	}
	if password {
		// go-git decodes the user of an http URL.
		user = unescapeUserinfo(user)
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
		return urlParts{}, fmt.Errorf("submodule URL port is not a number from 1 to 65535 for %s", protocol)
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

// String says whether a user and a password are set, never their values.
func (info URLUserinfo) String() string {
	if info == (URLUserinfo{}) {
		return "none"
	}
	return "[redacted]"
}

// GoString is String, for %#v.
func (info URLUserinfo) GoString() string {
	return info.String()
}

// SplitURLUserinfo removes the userinfo from a URL with "://", so the URL
// that drydock clones from and stores in a cache entry never holds a
// token. An ssh URL keeps its user, which names the ssh login, and loses
// only a password. Other URLs and an scp-like URL come back as they are.
// found reports that the URL held userinfo.
//
// The authority ends at the first "/", "?", or "#", as net/url reads it.
// So the host of clean is the host that net/url, go-git, and the cache key
// see: in "https://a.test?@b.test/x" the host is a.test, not b.test.
func SplitURLUserinfo(value string) (clean string, info URLUserinfo, found bool) {
	scheme, rest, isURL := strings.Cut(value, "://")
	if !isURL {
		return value, URLUserinfo{}, false
	}
	authority, path := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
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

// CheckURLAuthority fails when the text between "://" and the first "/"
// of value holds "?", "#", or "\". Parsers end the authority at different
// places for such a URL: net/url ends it at "?" or "#", and code that cuts
// at "/" does not. A Git repository URL never needs these characters
// there, so drydock refuses the URL instead of guessing its host. A URL
// without "://", such as an scp-like URL, passes. The error does not quote
// value.
func CheckURLAuthority(value string) error {
	_, rest, isURL := strings.Cut(strings.TrimSpace(value), "://")
	if !isURL {
		return nil
	}
	authority, _, _ := strings.Cut(rest, "/")
	if strings.ContainsAny(authority, "?#\\") {
		return fmt.Errorf("the host part of the URL holds \"?\", \"#\", or \"\\\"; drydock does not fetch such a URL")
	}
	return nil
}

func unescapeUserinfo(value string) string {
	if unescaped, err := url.PathUnescape(value); err == nil {
		return unescaped
	}
	return value
}

// hostOnly is the part of an authority after its last "@", so an error
// never shows a user or password.
func hostOnly(authority string) string {
	if _, host, ok := strings.CutLast(authority, "@"); ok {
		return host
	}
	return authority
}
