package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/cache"
)

type DefaultAcquirer struct {
	Client   *http.Client
	MaxBytes int64
}

func (acquirer DefaultAcquirer) Acquire(ctx context.Context, request Request, opts Options) (Result, error) {
	switch requestKind(request.Kind) {
	case RequestHTTPFile:
		return acquirer.acquireHTTPFile(ctx, request, opts)
	case RequestGitRepo:
		return acquirer.acquireGitRepo(ctx, request, opts)
	default:
		return Result{}, fmt.Errorf("unsupported remote resource request kind %q", request.Kind)
	}
}

func (acquirer DefaultAcquirer) acquireHTTPFile(ctx context.Context, request Request, opts Options) (Result, error) {
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
	if err := rejectForbiddenCachePath(resourcePath, opts.ForbiddenRoots); err != nil {
		return Result{}, err
	}
	if !opts.Refresh && regularFileReady(resourcePath) {
		return Result{Path: resourcePath, URL: normalized, FromCache: true}, nil
	}
	if opts.Offline {
		return Result{}, fmt.Errorf("offline cache miss for remote resource %s", RedactURL(normalized))
	}

	data, err := acquirer.fetch(ctx, normalized, opts.Credentials)
	if err != nil {
		return Result{}, err
	}
	if err := rejectForbiddenCachePath(resourcePath, opts.ForbiddenRoots); err != nil {
		return Result{}, err
	}
	if err := publishCacheFile(resourcePath, data); err != nil {
		return Result{}, err
	}
	writeHTTPFileMetadata(filepath.Dir(resourcePath), key, normalized)
	return Result{Path: resourcePath, URL: normalized}, nil
}

func writeHTTPFileMetadata(entryRoot, key, target string) {
	_ = cache.WriteMetadata(entryRoot, cache.Metadata{
		Source: cache.SourceRemote,
		Kind:   "http-file",
		Key:    key,
		Target: cache.RedactedTarget(target),
	})
}

func (acquirer DefaultAcquirer) fetch(ctx context.Context, normalizedURL string, credentials Credentials) ([]byte, error) {
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
	applyHTTPAuth(req, credentials)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch remote resource %s: %s", RedactURL(normalizedURL), redactFetchError(err, normalizedURL, credentials))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch remote resource %s: HTTP %s", RedactURL(normalizedURL), resp.Status)
	}
	reader := io.LimitReader(resp.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read remote resource %s: %s", RedactURL(normalizedURL), redactFetchError(err, normalizedURL, credentials))
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

func applyHTTPAuth(request *http.Request, credentials Credentials) {
	if strings.TrimSpace(credentials.BearerToken) != "" {
		request.Header.Set("Authorization", "Bearer "+credentials.BearerToken)
		return
	}
	if strings.TrimSpace(credentials.Username) != "" || credentials.Password != "" {
		request.SetBasicAuth(credentials.Username, credentials.Password)
	}
}

func redactFetchError(err error, rawURL string, credentials Credentials) string {
	message := strings.ReplaceAll(err.Error(), rawURL, RedactURL(rawURL))
	return RedactCredentialError(message, credentials, GitCredentials{})
}

func RedactCredentialError(message string, credentials Credentials, gitCredentials GitCredentials) string {
	return redactCredentialValues(message, credentials, gitCredentials)
}

func redactCredentialValues(message string, credentials Credentials, gitCredentials GitCredentials) string {
	for _, secret := range []string{
		credentials.Username,
		credentials.Password,
		credentials.BearerToken,
		gitCredentials.Username,
		gitCredentials.Password,
		gitCredentials.BearerToken,
		gitCredentials.SSHPrivateKey,
		gitCredentials.SSHPassphrase,
	} {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	if gitCredentials.SSHPrivateKey != "" {
		for _, line := range strings.Split(gitCredentials.SSHPrivateKey, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			message = strings.ReplaceAll(message, line, "[redacted]")
		}
	}
	for _, marker := range []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----END OPENSSH PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----END RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----END EC PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		"-----END PRIVATE KEY-----",
	} {
		message = strings.ReplaceAll(message, marker, "[redacted]")
	}
	return message
}
