package chart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/pathsafety"
)

type RepositoryKind string

const (
	RepositoryHTTP RepositoryKind = "http"
	RepositoryOCI  RepositoryKind = "oci"
)

type Request struct {
	Repository string
	Name       string
	Version    string
	Kind       RepositoryKind
	// VersionPolicy says how Version names the chart version. A policy
	// that resolves a version range resolves it before the cache lookup,
	// so the cache entry and Result.Version are the resolved version.
	VersionPolicy VersionPolicy
}

type Result struct {
	ChartDir   string
	Repository string
	Name       string
	Version    string
	Kind       RepositoryKind
	FromCache  bool
	// CacheKey is the chart cache entry key (NewCacheKey) the chart was
	// served from or published to. Empty when the acquirer did not use the
	// chart cache.
	CacheKey string
	// FetchedAt is when the cache entry was fetched and published; it is the
	// age MaxAge is judged by. Zero when unknown, for example an entry
	// without readable cache metadata or an injected acquirer.
	FetchedAt time.Time
	// Stale reports a cached chart older than Options.MaxAge (or of unknown
	// age) that was reused because Options.Offline forbids refetching it.
	Stale bool
	// VersionListStale reports a version range that an offline run
	// resolved from a recorded version list older than Options.MaxAge (or
	// of unknown age). VersionListFetchedAt is when that list was fetched,
	// zero when unknown.
	VersionListStale     bool
	VersionListFetchedAt time.Time
}

type Options struct {
	CacheDir string
	Offline  bool
	Refresh  bool
	// MaxAge, when positive, expires a cached chart whose fetch time is
	// older than it (or unknown): it is refetched like Refresh, or reused
	// and reported Stale when Offline is set. Zero never expires entries.
	MaxAge          time.Duration
	ForbiddenRoots  []string
	PassCredentials bool
	Credentials     ChartCredentials
	// EntryHooks, when set, lets the caller act on the resolved cache
	// entry while the acquirer holds its lock.
	EntryHooks *EntryHooks
}

// EntryHooks act on a chart cache entry while the acquirer holds the lock
// on that entry. The entry is the resolved one, so requests whose versions
// resolve to one version, such as 1.2.* and ~1.2.0, share it.
type EntryHooks struct {
	// Refreshed reports whether this run already refreshed the entry at
	// entryPath. A refresh then reuses the cached entry, so a run
	// refreshes each entry at most once.
	Refreshed func(entryPath string) bool
	// MarkRefreshed records that a refresh fetched and published the entry
	// at entryPath. A failed fetch is not recorded, so the next refresh
	// request tries again.
	MarkRefreshed func(entryPath string)
	// Hold runs on a successful result before the acquirer releases the
	// entry lock, so the caller can copy the entry before another request
	// replaces it. Its result replaces the result.
	Hold func(Result) (Result, error)
}

type ChartCredentials struct {
	Username       string
	Password       string
	BearerToken    string
	RegistryConfig string
}

type Acquirer interface {
	Acquire(ctx context.Context, request Request, opts Options) (Result, error)
}

func DefaultCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "drydock", "charts"), nil
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
		return "", fmt.Errorf("chart cache dir %q must not be inside repository root %q", absCacheDir, matchedRoot)
	}
	return absCacheDir, nil
}

func rejectForbiddenCachePath(chartPath string, forbiddenRoots []string) error {
	inside, matchedRoot, err := IsPathInsideAny(chartPath, forbiddenRoots)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("chart cache path %q must not be inside repository root %q", chartPath, matchedRoot)
	}
	return nil
}

func IsPathInsideAny(targetPath string, roots []string) (bool, string, error) {
	return pathsafety.IsInsideAny(targetPath, roots)
}

func NewCacheKey(request Request) (string, error) {
	normalized, err := NormalizeCacheRepository(request.Repository, request.Kind)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(request.Name)
	version := strings.TrimSpace(request.Version)
	if name == "" {
		return "", fmt.Errorf("chart name is required")
	}
	if version == "" {
		return "", fmt.Errorf("chart version is required")
	}
	if request.Kind != RepositoryHTTP && request.Kind != RepositoryOCI {
		return "", fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(request.Kind),
		normalized,
		name,
		version,
	}, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

func NormalizeCacheRepository(repository string, kind RepositoryKind) (string, error) {
	normalized, err := NormalizeRepository(repository, kind)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), nil
}

func NormalizeRepository(repository string, kind RepositoryKind) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return "", fmt.Errorf("chart repository is required")
	}
	switch kind {
	case RepositoryHTTP:
		parsed, err := url.Parse(repository)
		if err != nil {
			return "", fmt.Errorf("parse chart repository %q: %w", repository, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", fmt.Errorf("HTTP chart repository %q must use http or https", repository)
		}
		if parsed.Host == "" {
			return "", fmt.Errorf("HTTP chart repository %q must include a host", repository)
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		return parsed.String(), nil
	case RepositoryOCI:
		if !strings.Contains(repository, "://") {
			repository = "oci://" + repository
		}
		parsed, err := url.Parse(repository)
		if err != nil {
			return "", fmt.Errorf("parse OCI chart repository: invalid repository URL")
		}
		if parsed.Scheme != "oci" {
			return "", fmt.Errorf("OCI chart repository must use oci scheme")
		}
		if parsed.Host == "" {
			return "", fmt.Errorf("OCI chart repository must include a host")
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		return parsed.String(), nil
	default:
		return "", fmt.Errorf("unsupported chart repository kind %q", kind)
	}
}
