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

//nolint:gocyclo // Coordinates validation, cache lookup, fetch, extraction, and publish in acquisition order.
func (acquirer DefaultAcquirer) Acquire(ctx context.Context, request Request, opts Options) (Result, error) {
	switch request.Kind {
	case RepositoryHTTP, RepositoryOCI:
	default:
		return Result{}, fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
	if err := validateChartNamePathLeaf(request.Name); err != nil {
		return Result{}, err
	}
	if request.Kind == RepositoryOCI {
		if _, err := parseOCIChartRepository(request.Repository); err != nil {
			return Result{}, err
		}
	}
	if opts.CacheDir == "" {
		cacheDir, err := DefaultCacheDir()
		if err != nil {
			return Result{}, err
		}
		opts.CacheDir = cacheDir
	}
	key, err := NewCacheKey(request)
	if err != nil {
		return Result{}, err
	}
	keyParent := cache.ChartKindRoot(opts.CacheDir, string(request.Kind))
	keyDir := cache.ChartEntryPath(opts.CacheDir, string(request.Kind), key)
	chartDir := filepath.Join(keyDir, request.Name)
	if !opts.Refresh && chartDirReady(chartDir) {
		writeChartMetadata(keyDir, key, request)
		return resultFor(request, chartDir, true), nil
	}
	if opts.Offline {
		return Result{}, fmt.Errorf("offline cache miss for chart %s %s", request.Name, request.Version)
	}

	archive, err := acquirer.fetchChart(ctx, request, opts)
	if err != nil {
		return Result{}, err
	}
	if !chartArchiveContainsNamedChart(bytes.NewReader(archive), request.Name) {
		return Result{}, fmt.Errorf("chart archive for %s %s does not contain %s/Chart.yaml", request.Name, request.Version, request.Name)
	}

	if err := os.MkdirAll(keyParent, 0o755); err != nil {
		return Result{}, fmt.Errorf("create chart cache parent %s: %w", keyParent, err)
	}
	tmpKeyDir, err := os.MkdirTemp(keyParent, "."+key+".tmp-")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary chart cache %s: %w", keyParent, err)
	}
	defer os.RemoveAll(tmpKeyDir)

	tmpChartDir := filepath.Join(tmpKeyDir, request.Name)
	if err := extractChartArchive(bytes.NewReader(archive), tmpChartDir, request.Name); err != nil {
		return Result{}, err
	}
	if !chartDirReady(tmpChartDir) {
		return Result{}, fmt.Errorf("chart archive for %s %s did not extract Chart.yaml", request.Name, request.Version)
	}
	if err := publishChartCache(keyDir, tmpKeyDir); err != nil {
		return Result{}, err
	}
	writeChartMetadata(keyDir, key, request)
	return resultFor(request, chartDir, false), nil
}
func writeChartMetadata(keyDir, key string, request Request) {
	target := request.Repository
	if normalized, err := NormalizeCacheRepository(request.Repository, request.Kind); err == nil {
		target = normalized
	}
	_ = cache.WriteMetadata(keyDir, cache.Metadata{
		Source:  cache.SourceChart,
		Kind:    string(request.Kind),
		Key:     key,
		Target:  cache.RedactedTarget(target),
		Name:    strings.TrimSpace(request.Name),
		Version: strings.TrimSpace(request.Version),
	})
}
func (acquirer DefaultAcquirer) fetchChart(ctx context.Context, request Request, opts Options) ([]byte, error) {
	switch request.Kind {
	case RepositoryHTTP:
		return acquirer.fetchHTTPChart(ctx, request, opts.Credentials)
	case RepositoryOCI:
		return acquirer.fetchOCIChart(ctx, request, opts)
	default:
		return nil, fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
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
