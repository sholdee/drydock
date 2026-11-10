package gitlfs

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	formatconfig "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/sholdee/drydock/internal/giturl"
)

// LFSConfigFile is the file at the top of a repository that sets Git LFS
// options for everyone who clones it.
const LFSConfigFile = ".lfsconfig"

// MaxLFSConfigSize is the largest .lfsconfig file that drydock reads.
const MaxLFSConfigSize = 64 << 10

// Endpoint is the Git LFS API endpoint of a repository.
type Endpoint struct {
	// URL is the http or https base URL of the batch API.
	URL string
	// SSH is set when git-lfs gets the endpoint and an auth header from
	// git-lfs-authenticate over SSH. URL is then the https endpoint that
	// git-lfs derives, used when the answer gives no href.
	SSH *SSHRemote
	// FromConfig reports that .lfsconfig named the endpoint, not the
	// repository URL.
	FromConfig bool
	// RepoHost is the host of the repository URL.
	RepoHost string
}

// SSHRemote is the SSH address that git-lfs runs git-lfs-authenticate on.
type SSHRemote struct {
	// URL is the SSH URL, as gitauth.SSHAuth reads it.
	URL  string
	User string
	Host string
	Port int
	// Path is the repository path as git-lfs passes it: as written after
	// the colon of the scp form, or with its leading "/" for an ssh URL.
	Path string
}

// FindEndpoint returns the Git LFS endpoint of the repository at repoURL,
// as git-lfs 3.x finds it for the remote "origin"
// (lfsapi/endpoint_finder.go):
//
//  1. lfs.url, then remote.origin.lfsurl, from lfsconfig, the .lfsconfig
//     file of the commit. A key that is set but empty gives git-lfs no
//     endpoint, also when the other key is set. drydock reads no Git
//     config file and does not follow [include] sections.
//  2. Without them, the repository URL: an http or https URL with
//     "/info/lfs" after a ".git" suffix, or ".git/info/lfs" without one.
//
// A git:// URL takes the scheme of lfs.gitprotocol from lfsconfig (https
// by default) on the same host and port. An SSH URL gives an SSHRemote,
// whose https URL drops the port, as git-lfs does. drydock refuses an
// endpoint that it does not support, such as a file URL, a local path, or
// a URL with a user, a password, a query, or a fragment: the error then
// satisfies IsUnsupported. Every other error means that git-lfs finds no
// usable endpoint either. Errors never quote a URL.
func FindEndpoint(repoURL string, lfsconfig []byte) (Endpoint, error) {
	settings, err := readLFSConfig(lfsconfig)
	if err != nil {
		return Endpoint{}, err
	}
	repoHost := hostOfURL(repoURL)
	if settings.endpointSet {
		if settings.endpoint == "" {
			return Endpoint{}, fmt.Errorf("%s in %s is empty, so git-lfs has no Git LFS server", settings.endpointKey, LFSConfigFile)
		}
		endpoint, err := configuredEndpoint(settings.endpoint, settings.gitProtocol, settings.endpointKey+" in "+LFSConfigFile)
		endpoint.FromConfig, endpoint.RepoHost = true, repoHost
		return endpoint, err
	}
	endpoint, err := cloneURLEndpoint(repoURL, settings.gitProtocol)
	endpoint.RepoHost = repoHost
	return endpoint, err
}

// lfsSettings are the keys of .lfsconfig that drydock reads.
type lfsSettings struct {
	endpoint    string
	endpointKey string
	endpointSet bool
	gitProtocol string
}

func readLFSConfig(lfsconfig []byte) (lfsSettings, error) {
	settings := lfsSettings{gitProtocol: "https"}
	if len(lfsconfig) == 0 {
		return settings, nil
	}
	if len(lfsconfig) > MaxLFSConfigSize {
		return settings, unsupportedf("%s is larger than %d bytes", LFSConfigFile, MaxLFSConfigSize)
	}
	cfg := formatconfig.New()
	if err := formatconfig.NewDecoder(bytes.NewReader(lfsconfig)).Decode(cfg); err != nil {
		return settings, fmt.Errorf("read %s: it is not a valid Git config file, which git-lfs cannot read either", LFSConfigFile)
	}
	if value, ok := lastOption(cfg.Section("lfs").Options, "gitprotocol"); ok && value != "" {
		settings.gitProtocol = value
	}
	if value, ok := lastOption(cfg.Section("lfs").Options, "url"); ok {
		settings.endpoint, settings.endpointKey, settings.endpointSet = value, "lfs.url", true
		return settings, nil
	}
	if cfg.Section("remote").HasSubsection("origin") {
		if value, ok := lastOption(cfg.Section("remote").Subsection("origin").Options, "lfsurl"); ok {
			settings.endpoint, settings.endpointKey, settings.endpointSet = value, "remote.origin.lfsurl", true
		}
	}
	return settings, nil
}

// lastOption returns the last value of key, as in git config.
func lastOption(options formatconfig.Options, key string) (string, bool) {
	value, found := "", false
	for _, option := range options {
		if option.IsKey(key) {
			value, found = strings.TrimSpace(option.Value), true
		}
	}
	return value, found
}

// configuredEndpoint reads an endpoint URL from .lfsconfig. where names
// the key for the errors.
func configuredEndpoint(raw, gitProtocol, where string) (Endpoint, error) {
	if isSSH(raw) {
		remote, err := parseSSHRemote(raw, where)
		if err != nil {
			return Endpoint{}, err
		}
		endpoint, err := checkEndpoint(remote.httpsURL(), where)
		return Endpoint{URL: endpoint, SSH: &remote}, err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return Endpoint{}, unsupportedf("%s is not a valid URL", where)
	}
	if strings.EqualFold(parsed.Scheme, "git") {
		parsed.Scheme = gitProtocol
	}
	endpoint, err := checkEndpoint(parsed.String(), where)
	return Endpoint{URL: endpoint}, err
}

// cloneURLEndpoint derives the endpoint from the repository URL
// (NewEndpointFromCloneURL in git-lfs).
func cloneURLEndpoint(repoURL, gitProtocol string) (Endpoint, error) {
	raw := strings.TrimSpace(repoURL)
	const where = "the repository URL"
	if isSSH(raw) {
		remote, err := parseSSHRemote(raw, where)
		if err != nil {
			return Endpoint{}, err
		}
		endpoint, err := checkEndpoint(withInfoLFS(remote.httpsURL()), where)
		return Endpoint{URL: endpoint, SSH: &remote}, err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return Endpoint{}, unsupportedf("the repository URL is not an http, https, git, or ssh URL, so drydock knows no Git LFS server for it")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	case "git":
		parsed.Scheme = gitProtocol
	default:
		return Endpoint{}, unsupportedf("the repository URL is not an http, https, git, or ssh URL, so drydock knows no Git LFS server for it")
	}
	endpoint, err := checkEndpoint(withInfoLFS(parsed.String()), where)
	return Endpoint{URL: endpoint}, err
}

// withInfoLFS adds "/info/lfs" after a ".git" suffix, or ".git/info/lfs"
// without one, after it drops a trailing "/".
func withInfoLFS(raw string) string {
	raw = strings.TrimSuffix(raw, "/")
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if path.Ext(parsed.Path) == ".git" {
		return raw + "/info/lfs"
	}
	return raw + ".git/info/lfs"
}

// checkEndpoint fails unless raw is an http or https URL with a host and
// without userinfo, query, or fragment. A user and password in a URL from
// the repository content are never used as credentials. where names the
// origin of raw for the errors.
func checkEndpoint(raw, where string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", unsupportedf("%s is not a valid URL", where)
	}
	if parsed.Scheme == "file" || strings.HasPrefix(raw, "/") {
		return "", unsupportedf("%s names a local Git LFS store, which drydock does not read", where)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", unsupportedf("%s does not name an http, https, git, or ssh Git LFS server", where)
	}
	if parsed.Host == "" || parsed.Opaque != "" || strings.ContainsAny(parsed.Host, `\?#`) {
		return "", unsupportedf("%s has no valid host", where)
	}
	if parsed.User != nil {
		return "", unsupportedf("%s holds a user or password, which drydock does not use", where)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", unsupportedf("%s holds a query or fragment, which drydock does not send", where)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

// parseSSHRemote reads an ssh:// or scp-style URL.
func parseSSHRemote(raw, where string) (SSHRemote, error) {
	if strings.HasPrefix(raw, "git+ssh://") || strings.HasPrefix(raw, "ssh+git://") {
		return SSHRemote{}, unsupportedf("%s uses git+ssh or ssh+git, which drydock's Git client does not support", where)
	}
	endpoint, err := transport.NewEndpoint(raw)
	if err != nil || endpoint.Protocol != "ssh" || endpoint.Host == "" || endpoint.Path == "" {
		return SSHRemote{}, unsupportedf("%s is not a valid SSH URL", where)
	}
	if strings.ContainsAny(endpoint.Host, "\\?#/ ") || strings.HasPrefix(endpoint.Host, "-") || strings.HasPrefix(endpoint.User, "-") {
		return SSHRemote{}, unsupportedf("%s has no valid SSH host", where)
	}
	user := endpoint.User
	if user == "" {
		user = giturl.SSHUser(raw)
	}
	port := endpoint.Port
	if port <= 0 {
		port = 22
	}
	return SSHRemote{URL: raw, User: user, Host: endpoint.Host, Port: port, Path: endpoint.Path}, nil
}

// httpsURL is the https URL that git-lfs derives for an SSH remote: the
// host without the port, and the path with a leading "/".
func (r SSHRemote) httpsURL() string {
	host := r.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return (&url.URL{Scheme: "https", Host: host, Path: "/" + strings.TrimPrefix(r.Path, "/")}).String()
}

// isSSH reports whether git-lfs reads raw as an SSH URL: ssh://,
// git+ssh://, ssh+git://, or the scp form user@host:path.
func isSSH(raw string) bool {
	for _, prefix := range []string{"git+ssh://", "ssh+git://"} {
		if strings.HasPrefix(raw, prefix) {
			return true
		}
	}
	return giturl.IsSSHURL(raw)
}

// hostOfURL returns the host name of a repository URL, or "".
func hostOfURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if isSSH(raw) {
		if endpoint, err := transport.NewEndpoint(raw); err == nil {
			return endpoint.Host
		}
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// isLoopbackHost reports whether host is localhost, a name below
// localhost, or a loopback IP.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// BatchURL is the URL of the batch API of endpoint.
func BatchURL(endpoint string) string {
	return strings.TrimSuffix(endpoint, "/") + "/objects/batch"
}

// unsupportedError marks a reason where drydock itself is the limit, not
// the Git LFS server: a pointer then stays with a warning.
type unsupportedError struct {
	message string
}

func (e *unsupportedError) Error() string { return e.message }

func unsupportedf(format string, args ...any) error {
	return &unsupportedError{message: fmt.Sprintf(format, args...)}
}

// IsUnsupported reports whether err is a reason where drydock itself is
// the limit.
func IsUnsupported(err error) bool {
	_, ok := errors.AsType[*unsupportedError](err)
	return ok
}
