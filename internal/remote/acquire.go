package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/cache"
	"github.com/sholdee/drydock/internal/pathsafety"
)

const defaultMaxResourceBytes int64 = 10 * 1024 * 1024

type RequestKind string

const (
	RequestHTTPFile RequestKind = "http-file"
	RequestGitRepo  RequestKind = "git-repo"
)

type Request struct {
	URL      string
	Kind     RequestKind
	RepoURL  string
	Revision string
	// AnyFileType accepts an HTTP file URL whose path does not end in a
	// YAML or JSON extension, such as a Helm value file or file parameter
	// URL, which helm fetches whatever its name. The cache key does not
	// change.
	AnyFileType bool
	// Submodules checks out the submodules of a Git repository, as
	// kustomize does unless the ref says ?submodules=false
	// (sigs.k8s.io/kustomize/api internal/git/cloner.go). The cache key
	// does not change: the entry's worktree holds the content of the last
	// request.
	Submodules bool
}

// Credentials are the --remote-* credentials for remote HTTP(S) files:
// remote Kustomize resources, Helm value files, and Helm file parameters.
type Credentials struct {
	Username    string
	Password    string
	BearerToken string
	// Hosts lists the hosts that receive the credentials, one
	// --remote-auth-host entry each: host[:port] or https://host[:port]
	// for HTTPS, and http://host[:port] to allow plain HTTP. A request
	// receives the credentials only when its scheme, host, and port match
	// an entry. Validate fails when a credential is set and Hosts is empty.
	Hosts []string
}

type GitCredentials struct {
	Username          string
	Password          string
	BearerToken       string
	SSHPrivateKeyPath string
	SSHPrivateKey     string
	SSHPassphrase     string
	SSHKnownHostsPath string
	// Hosts lists the hosts that receive Username, Password, and
	// BearerToken, one --git-auth-host entry each (gitauth.Credentials).
	// The SSH fields ignore it.
	Hosts []string
}

type Options struct {
	CacheDir       string
	Offline        bool
	Refresh        bool
	ForbiddenRoots []string
	Credentials    Credentials
	GitCredentials GitCredentials
	// LockEntry, when set, locks the cache entry of a Git request for the
	// time drydock reads or writes it, and returns the unlock function.
	// Submodule fetches lock their own entries with it.
	LockEntry func(request Request, opts Options) (unlock func())
	// AllowFileSubmoduleTransport lets a submodule URL be a local path or a
	// file URL, which git refuses for submodules. Only tests set it.
	AllowFileSubmoduleTransport bool
	// AllowFileTransport lets the URL of a Git request be a file URL or a
	// local path. drydock refuses them, so that a repository cannot make
	// drydock read another Git repository on the machine. Only tests set it.
	AllowFileTransport bool
}

type Result struct {
	Path      string
	URL       string
	Revision  string
	FromCache bool
	// Release releases any read lock or temporary resource associated with Path.
	// Callers that receive a non-nil Release must call it exactly once after they
	// finish copying from Path. Release implementations must be idempotent so
	// defensive double-calls are harmless.
	Release func()
}

type Acquirer interface {
	Acquire(ctx context.Context, request Request, opts Options) (Result, error)
}

func DefaultCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "drydock", "remotes"), nil
}

func NewCacheKey(request Request) (string, error) {
	switch requestKind(request.Kind) {
	case RequestHTTPFile:
		normalized, err := normalizeHTTPFileURL(request.URL, request.AnyFileType)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256([]byte(normalized))
		return hex.EncodeToString(sum[:]), nil
	case RequestGitRepo:
		repoURL := strings.TrimSpace(request.RepoURL)
		if repoURL == "" {
			repoURL = strings.TrimSpace(request.URL)
		}
		normalized, err := NormalizeGitRepoCacheURL(repoURL)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256([]byte(normalized + "\n" + strings.TrimSpace(request.Revision)))
		return hex.EncodeToString(sum[:]), nil
	default:
		return "", fmt.Errorf("unsupported remote resource request kind %q", request.Kind)
	}
}

func NormalizeGitRepoCacheURL(raw string) (string, error) {
	normalized, err := NormalizeGitRepoURL(raw)
	if err != nil {
		return "", err
	}
	return cache.RedactedTarget(normalized), nil
}

func requestKind(kind RequestKind) RequestKind {
	if kind == "" {
		return RequestHTTPFile
	}
	return kind
}

func NormalizeGitRepoURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("remote Git repository URL is required")
	}
	if isSCPStyleGitURL(raw) || isRedactedSCPStyleGitURL(raw) {
		base := raw
		if before, _, ok := strings.Cut(base, "#"); ok {
			base = before
		}
		if before, _, ok := strings.Cut(base, "?"); ok {
			base = before
		}
		if userHost, repoPath, ok := strings.Cut(base, ":"); ok {
			if _, host, ok := strings.Cut(userHost, "@"); ok && host != "" {
				base = host + ":" + repoPath
			}
		}
		base = strings.TrimRight(base, "/")
		base = strings.TrimSuffix(base, ".git")
		base = strings.TrimRight(base, "/")
		return base, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse remote Git repository URL %q: invalid URL syntax", RedactURL(raw))
	}
	switch parsed.Scheme {
	case "file", "http", "https", "ssh":
	default:
		return "", fmt.Errorf("remote Git repository URL %q must use file, http, https, or ssh", RedactURL(raw))
	}
	if parsed.Scheme != "file" && parsed.Host == "" {
		return "", fmt.Errorf("remote Git repository URL %q must include a host", RedactURL(raw))
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.Path = strings.TrimSuffix(parsed.Path, ".git")
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func isRedactedSCPStyleGitURL(raw string) bool {
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "/") {
		return false
	}
	colon := strings.Index(raw, ":")
	if colon <= 0 {
		return false
	}
	if slash := strings.IndexAny(raw, `/\`); slash >= 0 && slash < colon {
		return false
	}
	host := raw[:colon]
	repoPath := raw[colon+1:]
	if host == "" || repoPath == "" || strings.Contains(host, "@") {
		return false
	}
	return strings.Contains(host, ".") || strings.ContainsAny(repoPath, `/\`)
}

// NormalizeURL normalizes the URL of a remote YAML or JSON file.
func NormalizeURL(raw string) (string, error) {
	return normalizeHTTPFileURL(raw, false)
}

// normalizeHTTPFileURL normalizes the URL of a remote file. Unless
// anyFileType is set, its path must end in a YAML or JSON extension.
func normalizeHTTPFileURL(raw string, anyFileType bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("remote resource URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse remote resource URL %q: invalid URL syntax", RedactURL(raw))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("remote resource URL %q must use http or https", RedactURL(raw))
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("remote resource URL %q must include a host", RedactURL(raw))
	}
	if parsed.User != nil {
		return "", fmt.Errorf("remote resource URL %q must not include userinfo", RedactURL(raw))
	}
	if strings.Contains(parsed.Path, "//") || parsed.Query().Has("ref") {
		return "", fmt.Errorf("remote resource URL %q looks like a Kustomize Git ref; Git refs are unsupported", RedactURL(raw))
	}
	if parsed.RawQuery != "" {
		return "", fmt.Errorf("remote resource URL %q must not include query parameters", RedactURL(raw))
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("remote resource URL %q must not include a fragment", RedactURL(raw))
	}
	ext := strings.ToLower(path.Ext(parsed.Path))
	if !anyFileType && ext != ".yaml" && ext != ".yml" && ext != ".json" {
		return "", fmt.Errorf("remote resource URL %q must point to a YAML or JSON file", RedactURL(raw))
	}
	return parsed.String(), nil
}

func CachePath(cacheDir string, key string) string {
	return cache.RemoteHTTPFilePath(cacheDir, key)
}

func rejectForbiddenCachePath(resourcePath string, forbiddenRoots []string) error {
	inside, matchedRoot, err := IsPathInsideAny(resourcePath, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("remote resource cache path %q must not be inside repository root %q", resourcePath, matchedRoot)
	}
	return nil
}

func ResolveCacheDir(cacheDir string, forbiddenRoots []string) (string, error) {
	if cacheDir == "" {
		defaultDir, err := DefaultCacheDir()
		if err != nil {
			return "", err
		}
		cacheDir = defaultDir
	}
	absCacheDir, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", err
	}
	absCacheDir = filepath.Clean(absCacheDir)
	inside, matchedRoot, err := IsPathInsideAny(absCacheDir, forbiddenRoots)
	if err != nil {
		return "", err
	}
	if inside {
		return "", fmt.Errorf("remote resource cache dir %q must not be inside repository root %q", absCacheDir, matchedRoot)
	}
	return absCacheDir, nil
}

func IsPathInsideAny(targetPath string, roots []string) (bool, string, error) {
	return pathsafety.IsInsideAny(targetPath, roots)
}

func RedactURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid-url]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if parsed.Scheme == "" && parsed.Host == "" {
		return "[invalid-url]"
	}
	return parsed.String()
}
