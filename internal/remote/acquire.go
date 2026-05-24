package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
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
}

type Credentials struct {
	Username    string
	Password    string
	BearerToken string
}

type GitCredentials struct {
	Username          string
	Password          string
	BearerToken       string
	SSHPrivateKeyPath string
	SSHPrivateKey     string
	SSHPassphrase     string
	SSHKnownHostsPath string
}

type Options struct {
	CacheDir       string
	Offline        bool
	Refresh        bool
	ForbiddenRoots []string
	Credentials    Credentials
	GitCredentials GitCredentials
}

type Result struct {
	Path      string
	URL       string
	Revision  string
	FromCache bool
}

type Acquirer interface {
	Acquire(ctx context.Context, request Request, opts Options) (Result, error)
}

func DefaultCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "argocd-local", "remotes"), nil
}

func NewCacheKey(request Request) (string, error) {
	switch requestKind(request.Kind) {
	case RequestHTTPFile:
		normalized, err := NormalizeURL(request.URL)
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
		normalized, err := NormalizeGitRepoURL(repoURL)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256([]byte(normalized + "\n" + strings.TrimSpace(request.Revision)))
		return hex.EncodeToString(sum[:]), nil
	default:
		return "", fmt.Errorf("unsupported remote resource request kind %q", request.Kind)
	}
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
	if isSCPStyleGitURL(raw) {
		base := raw
		if before, _, ok := strings.Cut(base, "#"); ok {
			base = before
		}
		if before, _, ok := strings.Cut(base, "?"); ok {
			base = before
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

func NormalizeURL(raw string) (string, error) {
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
	if ext != ".yaml" && ext != ".yml" && ext != ".json" {
		return "", fmt.Errorf("remote resource URL %q must point to a YAML or JSON file", RedactURL(raw))
	}
	return parsed.String(), nil
}

func CachePath(cacheDir string, key string) string {
	return filepath.Join(cacheDir, key, "resource.yaml")
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
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return false, "", err
	}
	absPath = filepath.Clean(absPath)
	resolvedPath, err := resolvePathForContainment(absPath)
	if err != nil {
		return false, "", err
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return false, "", err
		}
		absRoot = filepath.Clean(absRoot)
		resolvedRoot, err := resolvePathForContainment(absRoot)
		if err != nil {
			return false, "", err
		}
		rel, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true, absRoot, nil
		}
	}
	return false, "", nil
}

func resolvePathForContainment(targetPath string) (string, error) {
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absPath)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
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
