package chart

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/cache"
)

type DefaultAcquirer struct {
	Client    *http.Client
	OCIPuller OCIPuller
}
type OCIPuller interface {
	Pull(ctx context.Context, request Request, opts Options) ([]byte, error)
}
type HelmOCIPuller struct {
	Client *http.Client
}

func (acquirer DefaultAcquirer) Acquire(ctx context.Context, request Request, opts Options) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	cacheDir, err := ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return Result{}, err
	}
	opts.CacheDir = cacheDir
	var index *repositoryIndex
	if needsVersionResolution(request) {
		resolved, fetched, err := acquirer.resolveVersion(ctx, request, opts)
		if err != nil {
			return Result{}, err
		}
		request.Version = resolved
		index = fetched
	}
	key, err := NewCacheKey(request)
	if err != nil {
		return Result{}, err
	}
	keyParent := cache.ChartKindRoot(opts.CacheDir, string(request.Kind))
	keyDir := cache.ChartEntryPath(opts.CacheDir, string(request.Kind), key)
	chartDir := filepath.Join(keyDir, chartLeaf(request.Name))
	if err := rejectForbiddenCachePath(keyDir, opts.ForbiddenRoots); err != nil {
		return Result{}, err
	}
	if !opts.Refresh && chartDirReady(chartDir) {
		fetchedAt, known := entryFetchedAt(keyDir, request.Kind, key)
		expired := Expired(fetchedAt, known, opts.MaxAge, time.Now())
		if !expired || opts.Offline {
			// Reuse refreshes the entry's last-use time. An entry of unknown
			// age keeps its fetch time unknown, whatever the max age: dating
			// the old content as freshly fetched would let a later max-age
			// run serve it instead of refetching it.
			writeChartMetadata(keyDir, key, request, time.Time{}, !known)
			result := resultFor(request, chartDir, true)
			result.CacheKey = key
			result.FetchedAt = fetchedAt
			result.Stale = expired
			return result, nil
		}
	}
	if opts.Offline {
		return Result{}, fmt.Errorf("offline cache miss for chart %s %s", request.Name, request.Version)
	}

	archive, err := acquirer.fetchChart(ctx, request, opts, index)
	if err != nil {
		return Result{}, err
	}
	if !chartArchiveContainsNamedChart(bytes.NewReader(archive), chartLeaf(request.Name)) {
		return Result{}, fmt.Errorf("chart archive for %s %s does not contain %s/Chart.yaml", request.Name, request.Version, chartLeaf(request.Name))
	}

	return publishFetchedChart(request, opts, keyParent, keyDir, key, archive)
}

// validateRequest rejects unsupported kinds, unsafe chart names, and malformed
// OCI repositories before any cache path is touched.
func validateRequest(request Request) error {
	switch request.Kind {
	case RepositoryHTTP, RepositoryOCI:
	default:
		return fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
	if err := validateChartName(request.Kind, request.Name); err != nil {
		return err
	}
	if request.Kind == RepositoryOCI {
		if _, err := parseOCIChartRepository(request.Repository); err != nil {
			return err
		}
	}
	return nil
}

// publishFetchedChart extracts a freshly fetched archive into a temporary
// sibling directory and publishes it atomically into the cache entry.
func publishFetchedChart(request Request, opts Options, keyParent, keyDir, key string, archive []byte) (Result, error) {
	if err := rejectForbiddenCachePath(keyDir, opts.ForbiddenRoots); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(keyParent, 0o755); err != nil {
		return Result{}, fmt.Errorf("create chart cache parent %s: %w", keyParent, err)
	}
	tmpKeyDir, err := os.MkdirTemp(keyParent, "."+key+".tmp-")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary chart cache %s: %w", keyParent, err)
	}
	defer func() { _ = os.RemoveAll(tmpKeyDir) }()

	tmpChartDir := filepath.Join(tmpKeyDir, chartLeaf(request.Name))
	if err := extractChartArchive(bytes.NewReader(archive), tmpChartDir, chartLeaf(request.Name)); err != nil {
		return Result{}, err
	}
	if !chartDirReady(tmpChartDir) {
		return Result{}, fmt.Errorf("chart archive for %s %s did not extract Chart.yaml", request.Name, request.Version)
	}
	if err := publishChartCache(keyDir, tmpKeyDir); err != nil {
		return Result{}, err
	}
	// The published entry starts without metadata, so its creation time is
	// this fetch: the age a later MaxAge check judges it by.
	fetchedAt := time.Now().UTC()
	writeChartMetadata(keyDir, key, request, fetchedAt, false)
	result := resultFor(request, filepath.Join(keyDir, chartLeaf(request.Name)), false)
	result.CacheKey = key
	if storedAt, ok := entryFetchedAt(keyDir, request.Kind, key); ok {
		result.FetchedAt = storedAt
	}
	return result, nil
}

// Expired reports whether a chart cache entry fetched at fetchedAt (known
// false when that is unknown) is past maxAge at now. A non-positive maxAge
// never expires an entry; an entry of unknown age always expires under a
// positive one. This mirrors Argo CD 3.6's repo-server, which re-pulls a
// cached chart archive older than the repo cache expiration
// (util/helm/client.go ExtractChart at v3.6.1).
func Expired(fetchedAt time.Time, known bool, maxAge time.Duration, now time.Time) bool {
	if maxAge <= 0 {
		return false
	}
	if !known {
		return true
	}
	return now.Sub(fetchedAt) > maxAge
}

// EntryFetchedAt reports when the chart cache entry kind/key under cacheDir
// was fetched, from its cache metadata. ok is false when the entry has no
// readable metadata for that key.
func EntryFetchedAt(cacheDir string, kind RepositoryKind, key string) (time.Time, bool) {
	if strings.TrimSpace(cacheDir) == "" || strings.TrimSpace(key) == "" {
		return time.Time{}, false
	}
	return entryFetchedAt(cache.ChartEntryPath(cacheDir, string(kind), key), kind, key)
}

func entryFetchedAt(keyDir string, kind RepositoryKind, key string) (time.Time, bool) {
	metadata, err := cache.ReadMetadata(keyDir, cache.SourceChart, string(kind), key)
	if err != nil || metadata == nil || metadata.CreatedAt.IsZero() || metadata.FetchTimeUnknown {
		return time.Time{}, false
	}
	return metadata.CreatedAt, true
}

// writeChartMetadata records the entry's metadata. A zero createdAt keeps the
// existing fetch time; fetchTimeUnknown marks metadata backfilled on reuse of
// an entry whose fetch time is unknown.
func writeChartMetadata(keyDir, key string, request Request, createdAt time.Time, fetchTimeUnknown bool) {
	target := request.Repository
	if normalized, err := NormalizeCacheRepository(request.Repository, request.Kind); err == nil {
		target = normalized
	}
	_ = cache.WriteMetadata(keyDir, cache.Metadata{
		Source:           cache.SourceChart,
		Kind:             string(request.Kind),
		Key:              key,
		Target:           cache.RedactedTarget(target),
		Name:             strings.TrimSpace(request.Name),
		Version:          strings.TrimSpace(request.Version),
		CreatedAt:        createdAt,
		FetchTimeUnknown: fetchTimeUnknown,
	})
}
func (acquirer DefaultAcquirer) fetchChart(ctx context.Context, request Request, opts Options, index *repositoryIndex) ([]byte, error) {
	switch request.Kind {
	case RepositoryHTTP:
		return acquirer.fetchHTTPChart(ctx, request, opts, index)
	case RepositoryOCI:
		return acquirer.fetchOCIChart(ctx, request, opts)
	default:
		return nil, fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
}
func validateChartName(kind RepositoryKind, name string) error {
	if kind == RepositoryOCI {
		return validateOCIChartName(name)
	}
	return validateChartNamePathLeaf(name)
}
func validateOCIChartName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("chart name is required")
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("chart name %q must use \"/\" separators", name)
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return fmt.Errorf("chart name %q must be a relative path", name)
	}
	for segment := range strings.SplitSeq(name, "/") {
		switch segment {
		case "", ".", "..":
			return fmt.Errorf("chart name %q must not contain empty, \".\" or \"..\" path segments", name)
		}
	}
	if path.Clean(name) != name {
		return fmt.Errorf("chart name %q must be clean", name)
	}
	return nil
}
func validateChartNamePathLeaf(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("chart name is required")
	}
	normalized := strings.ReplaceAll(name, "\\", "/")
	if filepath.IsAbs(name) || path.IsAbs(normalized) {
		return fmt.Errorf("chart name %q must be a relative path leaf", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("chart name %q must be a single path component", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("chart name %q must be a single path component", name)
	}
	if filepath.Clean(name) != name {
		return fmt.Errorf("chart name %q must be clean", name)
	}
	return nil
}
func chartLeaf(name string) string {
	return path.Base(name)
}
func chartDirReady(chartDir string) bool {
	info, err := os.Lstat(filepath.Join(chartDir, "Chart.yaml"))
	return err == nil && info.Mode().IsRegular()
}
func resultFor(request Request, chartDir string, fromCache bool) Result {
	return Result{
		ChartDir:   chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  fromCache,
	}
}
