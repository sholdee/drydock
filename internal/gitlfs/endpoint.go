package gitlfs

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	formatconfig "github.com/go-git/go-git/v5/plumbing/format/config"

	"github.com/sholdee/drydock/internal/giturl"
)

// LFSConfigFile is the file at the top of a repository that sets Git LFS
// options for everyone who clones it.
const LFSConfigFile = ".lfsconfig"

// MaxLFSConfigSize is the largest .lfsconfig file that drydock reads.
const MaxLFSConfigSize = 64 << 10

// ErrSSHEndpoint reports a Git LFS endpoint that git-lfs reaches over SSH
// with git-lfs-authenticate, which drydock does not run.
var ErrSSHEndpoint = errors.New("the Git LFS server is reached over SSH (git-lfs-authenticate), and drydock has no Git LFS client for SSH")

// Endpoint returns the Git LFS API endpoint of the repository at repoURL,
// as git-lfs 3.x finds it for the remote "origin" (lfsapi/endpoint_finder.go):
//
//  1. lfs.url, then remote.origin.lfsurl, from lfsconfig, the .lfsconfig
//     file of the commit. drydock reads no Git config file: the cache clone
//     of the Argo CD repo-server sets neither key.
//  2. Without them, the repository URL: an http or https URL with
//     "/info/lfs" after a ".git" suffix, or ".git/info/lfs" without one; a
//     git:// URL the same way with https, as git-lfs does.
//
// The result is an http or https URL without userinfo, query, or fragment.
// An SSH endpoint fails with ErrSSHEndpoint, and any other endpoint, such
// as a file URL or a local path, fails too. Errors never quote a URL.
func Endpoint(repoURL string, lfsconfig []byte) (string, error) {
	configured, err := configuredEndpoint(lfsconfig)
	if err != nil {
		return "", err
	}
	if configured != "" {
		return checkEndpoint(configured, "lfs.url in "+LFSConfigFile)
	}
	return cloneURLEndpoint(repoURL)
}

// configuredEndpoint reads lfs.url, then remote.origin.lfsurl, from the
// .lfsconfig file. The last value of a key wins, as in git config.
func configuredEndpoint(lfsconfig []byte) (string, error) {
	if len(lfsconfig) == 0 {
		return "", nil
	}
	if len(lfsconfig) > MaxLFSConfigSize {
		return "", fmt.Errorf("%s is larger than %d bytes", LFSConfigFile, MaxLFSConfigSize)
	}
	cfg := formatconfig.New()
	if err := formatconfig.NewDecoder(bytes.NewReader(lfsconfig)).Decode(cfg); err != nil {
		return "", fmt.Errorf("read %s: it is not a valid Git config file", LFSConfigFile)
	}
	if value := lastOption(cfg.Section("lfs").Options, "url"); value != "" {
		return value, nil
	}
	if !cfg.Section("remote").HasSubsection("origin") {
		return "", nil
	}
	return lastOption(cfg.Section("remote").Subsection("origin").Options, "lfsurl"), nil
}

func lastOption(options formatconfig.Options, key string) string {
	value := ""
	for _, option := range options {
		if option.IsKey(key) {
			value = strings.TrimSpace(option.Value)
		}
	}
	return value
}

// cloneURLEndpoint derives the endpoint from the repository URL
// (NewEndpointFromCloneURL in git-lfs).
func cloneURLEndpoint(repoURL string) (string, error) {
	raw := strings.TrimSpace(repoURL)
	if isSSH(raw) {
		return "", ErrSSHEndpoint
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("the repository URL is not an http, https, or git URL, so it names no Git LFS server")
	}
	switch parsed.Scheme {
	case "http", "https":
	case "git":
		parsed.Scheme = "https"
	default:
		return "", errors.New("the repository URL is not an http, https, or git URL, so it names no Git LFS server")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""
	if path.Ext(parsed.Path) == ".git" {
		parsed.Path += "/info/lfs"
	} else {
		parsed.Path += ".git/info/lfs"
	}
	return checkEndpoint(parsed.String(), "the repository URL")
}

// checkEndpoint fails unless raw is an http or https URL with a host and
// without userinfo, query, or fragment. A user and password in a URL from
// the repository content are never used as credentials. where names the
// origin of raw for the errors.
func checkEndpoint(raw, where string) (string, error) {
	if isSSH(raw) {
		return "", ErrSSHEndpoint
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid URL", where)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%s does not name an http or https Git LFS server", where)
	}
	if parsed.Host == "" || parsed.Opaque != "" || strings.ContainsAny(parsed.Host, `\?#`) {
		return "", fmt.Errorf("%s has no valid host", where)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%s holds a user or password, which drydock does not use", where)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%s holds a query or fragment, which drydock does not send", where)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

// BatchURL is the URL of the batch API of endpoint.
func BatchURL(endpoint string) string {
	return strings.TrimSuffix(endpoint, "/") + "/objects/batch"
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
