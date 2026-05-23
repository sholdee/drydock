package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type DefaultAcquirer struct {
	Client   *http.Client
	MaxBytes int64
}

func (acquirer DefaultAcquirer) Acquire(ctx context.Context, request Request, opts Options) (Result, error) {
	normalized, err := NormalizeURL(request.URL)
	if err != nil {
		return Result{}, err
	}
	cacheDir, err := ResolveCacheDir(opts.CacheDir, opts.ForbiddenRoots)
	if err != nil {
		return Result{}, err
	}
	key, err := NewCacheKey(Request{URL: normalized})
	if err != nil {
		return Result{}, err
	}
	resourcePath := CachePath(cacheDir, key)
	if !opts.Refresh && regularFileReady(resourcePath) {
		return Result{Path: resourcePath, URL: normalized, FromCache: true}, nil
	}
	if opts.Offline {
		return Result{}, fmt.Errorf("offline cache miss for remote resource %s", RedactURL(normalized))
	}

	data, err := acquirer.fetch(ctx, normalized)
	if err != nil {
		return Result{}, err
	}
	if err := publishCacheFile(resourcePath, data); err != nil {
		return Result{}, err
	}
	return Result{Path: resourcePath, URL: normalized}, nil
}

func (acquirer DefaultAcquirer) fetch(ctx context.Context, normalizedURL string) ([]byte, error) {
	client := acquirer.Client
	if client == nil {
		client = http.DefaultClient
	}
	limit := acquirer.MaxBytes
	if limit == 0 {
		limit = defaultMaxResourceBytes
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalizedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create remote resource request %s: %w", RedactURL(normalizedURL), err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch remote resource %s: %s", RedactURL(normalizedURL), redactFetchError(err, normalizedURL))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authenticated remote Kustomize resources are not supported")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch remote resource %s: HTTP %s", RedactURL(normalizedURL), resp.Status)
	}
	reader := io.LimitReader(resp.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read remote resource %s: %s", RedactURL(normalizedURL), redactFetchError(err, normalizedURL))
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("remote resource %s exceeds %d bytes", RedactURL(normalizedURL), limit)
	}
	return data, nil
}

func regularFileReady(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func publishCacheFile(path string, data []byte) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create remote resource cache %s: %w", parent, err)
	}
	tmp, err := os.CreateTemp(parent, ".resource-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary remote resource cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish remote resource cache %s: %w", path, err)
	}
	return nil
}

func redactFetchError(err error, rawURL string) string {
	return strings.ReplaceAll(err.Error(), rawURL, RedactURL(rawURL))
}
