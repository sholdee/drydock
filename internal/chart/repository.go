package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"
)

type DefaultAcquirer struct {
	Client *http.Client
}

type repositoryIndex struct {
	Entries map[string][]repositoryChartVersion `yaml:"entries"`
}

type repositoryChartVersion struct {
	Version string   `yaml:"version"`
	URLs    []string `yaml:"urls"`
}

func (acquirer DefaultAcquirer) Acquire(ctx context.Context, request Request, opts Options) (Result, error) {
	if request.Kind != RepositoryHTTP {
		return Result{}, fmt.Errorf("unsupported chart repository kind %q", request.Kind)
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
	chartDir := filepath.Join(opts.CacheDir, string(request.Kind), key, request.Name)
	if !opts.Refresh && chartDirReady(chartDir) {
		return resultFor(request, chartDir, true), nil
	}
	if opts.Offline {
		return Result{}, fmt.Errorf("offline cache miss for chart %s %s", request.Name, request.Version)
	}

	archive, err := acquirer.fetchHTTPChart(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if !chartArchiveContainsNamedChart(bytes.NewReader(archive), request.Name) {
		return Result{}, fmt.Errorf("chart archive for %s %s does not contain %s/Chart.yaml", request.Name, request.Version, request.Name)
	}

	cacheParent := filepath.Dir(chartDir)
	if err := os.RemoveAll(cacheParent); err != nil {
		return Result{}, fmt.Errorf("remove chart cache %s: %w", cacheParent, err)
	}
	if err := os.MkdirAll(cacheParent, 0o755); err != nil {
		return Result{}, fmt.Errorf("create chart cache %s: %w", cacheParent, err)
	}
	if err := extractChartArchive(bytes.NewReader(archive), chartDir); err != nil {
		return Result{}, err
	}
	if !chartDirReady(chartDir) {
		return Result{}, fmt.Errorf("chart archive for %s %s did not extract Chart.yaml", request.Name, request.Version)
	}
	return resultFor(request, chartDir, false), nil
}

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
	indexRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, err
	}
	indexResponse, err := client.Do(indexRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch chart repository index %s: %w", indexURL, err)
	}
	defer indexResponse.Body.Close()
	if indexResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch chart repository index %s: %s", indexURL, indexResponse.Status)
	}
	var index repositoryIndex
	if err := yaml.NewDecoder(indexResponse.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("decode chart repository index %s: %w", indexURL, err)
	}

	chartURL, err := findChartURL(repository, request, index)
	if err != nil {
		return nil, err
	}
	archiveRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, chartURL, nil)
	if err != nil {
		return nil, err
	}
	archiveResponse, err := client.Do(archiveRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch chart archive %s: %w", chartURL, err)
	}
	defer archiveResponse.Body.Close()
	if archiveResponse.StatusCode == http.StatusUnauthorized || archiveResponse.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authenticated chart repositories are not supported yet")
	}
	if archiveResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch chart archive %s: %s", chartURL, archiveResponse.Status)
	}
	data, err := io.ReadAll(archiveResponse.Body)
	if err != nil {
		return nil, fmt.Errorf("read chart archive %s: %w", chartURL, err)
	}
	return data, nil
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

func extractChartArchive(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open chart archive gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read chart archive: %w", err)
		}
		rel, err := safeChartArchivePath(header.Name)
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
		case tar.TypeReg, tar.TypeRegA:
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

func safeChartArchivePath(name string) (string, error) {
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
	_, rel, ok := strings.Cut(cleaned, "/")
	if !ok {
		return "", nil
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("unsafe chart archive path %q", name)
	}
	return rel, nil
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
		if cleaned == want && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) {
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
