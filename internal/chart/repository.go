package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"helm.sh/helm/v4/pkg/registry"
	"oras.land/oras-go/v2/registry/remote/auth"

	"go.yaml.in/yaml/v4"
)

type DefaultAcquirer struct {
	Client    *http.Client
	OCIPuller OCIPuller
}

type OCIPuller interface {
	Pull(ctx context.Context, request Request) ([]byte, error)
}

type HelmOCIPuller struct {
	Client *http.Client
}

var dockerConfigEnvMu sync.Mutex

type repositoryIndex struct {
	Entries map[string][]repositoryChartVersion `yaml:"entries"`
}

type repositoryChartVersion struct {
	Version string   `yaml:"version"`
	URLs    []string `yaml:"urls"`
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
	keyParent := filepath.Join(opts.CacheDir, string(request.Kind))
	keyDir := filepath.Join(keyParent, key)
	chartDir := filepath.Join(keyDir, request.Name)
	if !opts.Refresh && chartDirReady(chartDir) {
		return resultFor(request, chartDir, true), nil
	}
	if opts.Offline {
		return Result{}, fmt.Errorf("offline cache miss for chart %s %s", request.Name, request.Version)
	}

	archive, err := acquirer.fetchChart(ctx, request)
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
	return resultFor(request, chartDir, false), nil
}

func (acquirer DefaultAcquirer) fetchChart(ctx context.Context, request Request) ([]byte, error) {
	switch request.Kind {
	case RepositoryHTTP:
		return acquirer.fetchHTTPChart(ctx, request)
	case RepositoryOCI:
		return acquirer.fetchOCIChart(ctx, request)
	default:
		return nil, fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
}

//nolint:gocyclo // Keeps index and chart archive request handling together for consistent URL redaction.
func (acquirer DefaultAcquirer) fetchHTTPChart(ctx context.Context, request Request) ([]byte, error) {
	client := acquirer.Client
	if client == nil {
		client = http.DefaultClient
	}
	repository, err := NormalizeRepository(request.Repository, request.Kind)
	if err != nil {
		return nil, err
	}
	indexURL, err := repositoryIndexURL(repository)
	if err != nil {
		return nil, err
	}
	redactedIndexURL := redactedFetchURL(indexURL, false)
	indexRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create chart repository index request %s: %s", redactedIndexURL, redactedFetchError(err, indexURL, false))
	}
	indexResponse, err := client.Do(indexRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch chart repository index %s: %s", redactedIndexURL, redactedFetchError(err, indexURL, false))
	}
	defer indexResponse.Body.Close()
	if indexResponse.StatusCode == http.StatusUnauthorized || indexResponse.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authenticated chart repositories are not supported yet")
	}
	if indexResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch chart repository index %s: HTTP %s", redactedIndexURL, indexResponse.Status)
	}
	var index repositoryIndex
	if err := yaml.NewDecoder(indexResponse.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("decode chart repository index %s: %w", redactedIndexURL, err)
	}

	chartURL, err := findChartURL(repository, request, index)
	if err != nil {
		return nil, err
	}
	redactedChartURL := redactedFetchURL(chartURL, true)
	archiveRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, chartURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create chart archive request %s: %s", redactedChartURL, redactedFetchError(err, chartURL, true))
	}
	archiveResponse, err := client.Do(archiveRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch chart archive %s: %s", redactedChartURL, redactedFetchError(err, chartURL, true))
	}
	defer archiveResponse.Body.Close()
	if archiveResponse.StatusCode == http.StatusUnauthorized || archiveResponse.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authenticated chart repositories are not supported yet")
	}
	if archiveResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch chart archive %s: HTTP %s", redactedChartURL, archiveResponse.Status)
	}
	data, err := io.ReadAll(archiveResponse.Body)
	if err != nil {
		return nil, fmt.Errorf("read chart archive %s: %s", redactedChartURL, redactedFetchError(err, chartURL, true))
	}
	return data, nil
}

func (acquirer DefaultAcquirer) fetchOCIChart(ctx context.Context, request Request) ([]byte, error) {
	puller := acquirer.OCIPuller
	if puller == nil {
		puller = HelmOCIPuller{Client: acquirer.Client}
	}
	archive, err := puller.Pull(ctx, request)
	if err != nil {
		if isAuthError(err) {
			return nil, fmt.Errorf("authenticated chart repositories are not supported yet")
		}
		repository := redactedFetchURL(request.Repository, true)
		return nil, fmt.Errorf("pull OCI chart %s/%s:%s: %s", repository, request.Name, request.Version, redactedFetchError(err, request.Repository, false))
	}
	return archive, nil
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"401",
		"403",
		"unauthorized",
		"forbidden",
		"authentication required",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

//nolint:gocyclo // Keeps temporary credential isolation and OCI pull validation in one scoped flow.
func (puller HelmOCIPuller) Pull(ctx context.Context, request Request) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository, err := parseOCIChartRepository(request.Repository)
	if err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "argocd-local-oci-chart-")
	if err != nil {
		return nil, fmt.Errorf("create temporary OCI chart directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	registryConfig := filepath.Join(tempDir, "registry-config.json")
	if err := os.WriteFile(registryConfig, []byte("{}\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write temporary Helm registry config: %w", err)
	}
	dockerConfigDir := filepath.Join(tempDir, "docker")
	if err := os.MkdirAll(dockerConfigDir, 0o700); err != nil {
		return nil, fmt.Errorf("create temporary Docker config directory %s: %w", dockerConfigDir, err)
	}
	if err := os.WriteFile(filepath.Join(dockerConfigDir, "config.json"), []byte("{}\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write temporary Docker config: %w", err)
	}

	registryClient, err := newHelmOCIRegistryClient(puller.Client, registryConfig, dockerConfigDir)
	if err != nil {
		return nil, fmt.Errorf("create Helm OCI registry client: %w", err)
	}

	chartRef := repository + "/" + request.Name + ":" + request.Version
	result, err := registryClient.Pull(chartRef)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result.Chart == nil || len(result.Chart.Data) == 0 {
		return nil, fmt.Errorf("pulled OCI chart %s contains no chart archive", request.Name)
	}

	archivePath := filepath.Join(tempDir, request.Name+"-"+request.Version+".tgz")
	if err := os.WriteFile(archivePath, result.Chart.Data, 0o600); err != nil {
		return nil, fmt.Errorf("write pulled OCI chart archive %s: %w", filepath.Base(archivePath), err)
	}

	matches, err := filepath.Glob(filepath.Join(tempDir, request.Name+"-*.tgz"))
	if err != nil {
		return nil, fmt.Errorf("find pulled OCI chart archive: %w", err)
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("expected one pulled OCI chart archive for %s, found %d", request.Name, len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, fmt.Errorf("read pulled OCI chart archive %s: %w", filepath.Base(matches[0]), err)
	}
	return data, nil
}

func parseOCIChartRepository(repository string) (string, error) {
	normalized, err := NormalizeRepository(repository, RepositoryOCI)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if parsed.User != nil {
		return "", fmt.Errorf("authenticated chart repositories are not supported yet")
	}
	if parsed.RawQuery != "" {
		return "", fmt.Errorf("OCI chart repository must not include query")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("OCI chart repository must not include fragment")
	}

	cleanPath := strings.Trim(parsed.Path, "/")
	if cleanPath == "" {
		return parsed.Host, nil
	}
	for _, segment := range strings.Split(cleanPath, "/") {
		switch segment {
		case "":
			return "", fmt.Errorf("OCI chart repository path must not include empty path segment")
		case ".", "..":
			return "", fmt.Errorf("OCI chart repository path contains unsafe path segment %q", segment)
		}
	}
	return parsed.Host + "/" + cleanPath, nil
}

func newHelmOCIRegistryClient(httpClient *http.Client, registryConfig, dockerConfigDir string) (*registry.Client, error) {
	clientOpts := []registry.ClientOption{
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(registryConfig),
		registry.ClientOptAuthorizer(auth.Client{
			Client: httpClient,
			Credential: func(context.Context, string) (auth.Credential, error) {
				return auth.EmptyCredential, nil
			},
		}),
	}
	if httpClient != nil {
		clientOpts = append(clientOpts, registry.ClientOptHTTPClient(httpClient))
	}

	dockerConfigEnvMu.Lock()
	defer dockerConfigEnvMu.Unlock()
	previousDockerConfig, hadDockerConfig := os.LookupEnv("DOCKER_CONFIG")
	if err := os.Setenv("DOCKER_CONFIG", dockerConfigDir); err != nil {
		return nil, fmt.Errorf("set temporary Docker config: %w", err)
	}
	defer func() {
		if hadDockerConfig {
			_ = os.Setenv("DOCKER_CONFIG", previousDockerConfig)
		} else {
			_ = os.Unsetenv("DOCKER_CONFIG")
		}
	}()
	return registry.NewClient(clientOpts...)
}

func repositoryIndexURL(repository string) (string, error) {
	parsed, err := url.Parse(repository)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/index.yaml"
	return parsed.String(), nil
}

func findChartURL(repository string, request Request, index repositoryIndex) (string, error) {
	versions := index.Entries[request.Name]
	for _, version := range versions {
		if version.Version != request.Version {
			continue
		}
		if len(version.URLs) == 0 {
			return "", fmt.Errorf("chart %s version %s has no archive URLs", request.Name, request.Version)
		}
		archiveURL, err := url.Parse(version.URLs[0])
		if err != nil {
			return "", fmt.Errorf("parse chart archive URL %q: %w", version.URLs[0], err)
		}
		if archiveURL.IsAbs() {
			if archiveURL.Scheme != "http" && archiveURL.Scheme != "https" {
				return "", fmt.Errorf("absolute chart URL %s must use http or https", redactedFetchURL(archiveURL.String(), true))
			}
			return archiveURL.String(), nil
		}
		base, err := url.Parse(repository)
		if err != nil {
			return "", err
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/"
		return base.ResolveReference(archiveURL).String(), nil
	}
	return "", fmt.Errorf("chart %s version %s not found in repository index", request.Name, request.Version)
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

func extractChartArchive(r io.Reader, dest, chartName string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open chart archive gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read chart archive: %w", err)
		}
		rel, err := safeChartArchivePath(header.Name, chartName)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := ensureContainedPath(dest, target); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create chart archive directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create chart archive directory %s: %w", filepath.Dir(target), err)
			}
			if err := writeArchiveFile(target, tr, header.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported chart archive entry %s", header.Name)
		}
	}
}

func safeChartArchivePath(name, chartName string) (string, error) {
	normalized := strings.ReplaceAll(name, "\\", "/")
	if name == "" || path.IsAbs(normalized) || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe chart archive path %q", name)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", fmt.Errorf("unsafe chart archive path %q", name)
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe chart archive path %q", name)
	}
	root, rel, ok := strings.Cut(cleaned, "/")
	if root != chartName {
		return "", fmt.Errorf("chart archive entry %q is outside chart root %q", name, chartName)
	}
	if !ok {
		return "", nil
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("unsafe chart archive path %q", name)
	}
	return rel, nil
}

func publishChartCache(keyDir, tmpKeyDir string) error {
	parent := filepath.Dir(keyDir)
	base := filepath.Base(keyDir)
	var backupDir string
	if _, err := os.Lstat(keyDir); err == nil {
		var err error
		backupDir, err = os.MkdirTemp(parent, "."+base+".old-")
		if err != nil {
			return fmt.Errorf("create chart cache backup %s: %w", parent, err)
		}
		if err := os.Remove(backupDir); err != nil {
			return fmt.Errorf("prepare chart cache backup %s: %w", backupDir, err)
		}
		if err := os.Rename(keyDir, backupDir); err != nil {
			return fmt.Errorf("backup chart cache %s: %w", keyDir, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat chart cache %s: %w", keyDir, err)
	}

	if err := os.Rename(tmpKeyDir, keyDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, keyDir)
		}
		return fmt.Errorf("publish chart cache %s: %w", keyDir, err)
	}
	if backupDir != "" {
		if err := os.RemoveAll(backupDir); err != nil {
			return fmt.Errorf("remove old chart cache %s: %w", backupDir, err)
		}
	}
	return nil
}

func redactedFetchURL(raw string, stripQueryFragment bool) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	if stripQueryFragment {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
	}
	return parsed.String()
}

func redactedFetchError(err error, rawURL string, stripQueryFragment bool) string {
	message := strings.ReplaceAll(err.Error(), rawURL, redactedFetchURL(rawURL, stripQueryFragment))
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsed.User == nil {
		return message
	}
	prefix := parsed.Scheme + "://"
	hostMarker := "@" + parsed.Host
	for {
		hostIndex := strings.Index(message, hostMarker)
		if hostIndex < 0 {
			return message
		}
		start := strings.LastIndex(message[:hostIndex], prefix)
		if start < 0 {
			return message
		}
		message = message[:start] + prefix + parsed.Host + message[hostIndex+len(hostMarker):]
	}
}

func ensureContainedPath(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("validate chart archive path %s: %w", target, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("chart archive path %s escapes destination %s", target, root)
	}
	return nil
}

func writeArchiveFile(target string, src io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create chart archive file %s: %w", target, err)
	}
	_, copyErr := io.Copy(file, src)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write chart archive file %s: %w", target, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close chart archive file %s: %w", target, closeErr)
	}
	return nil
}

func chartArchiveContainsNamedChart(r io.Reader, name string) bool {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return false
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	want := path.Join(name, "Chart.yaml")
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return false
		}
		cleaned := path.Clean(strings.ReplaceAll(header.Name, "\\", "/"))
		if cleaned == want && header.Typeflag == tar.TypeReg {
			return true
		}
	}
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
