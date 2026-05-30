package chart

import (
	"context"
	"fmt"
	"helm.sh/helm/v4/pkg/registry"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var dockerConfigEnvMu sync.Mutex

func (acquirer DefaultAcquirer) fetchOCIChart(ctx context.Context, request Request, opts Options) ([]byte, error) {
	if err := validateRegistryConfig(opts.Credentials.RegistryConfig); err != nil {
		return nil, err
	}
	puller := acquirer.OCIPuller
	if puller == nil {
		puller = HelmOCIPuller{Client: acquirer.Client}
	}
	archive, err := puller.Pull(ctx, request, opts)
	if err != nil {
		repository := redactedFetchURL(request.Repository, true)
		if isAuthError(err) {
			return nil, fmt.Errorf("authenticate OCI chart %s/%s:%s: %s", repository, request.Name, request.Version, redactedFetchError(err, request.Repository, false))
		}
		return nil, fmt.Errorf("pull OCI chart %s/%s:%s: %s", repository, request.Name, request.Version, redactedFetchError(err, request.Repository, false))
	}
	return archive, nil
}
func validateRegistryConfig(registryConfig string) error {
	registryConfig = strings.TrimSpace(registryConfig)
	if registryConfig == "" {
		return nil
	}
	info, err := os.Stat(registryConfig)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("registry config %q does not exist", registryConfig)
		}
		return fmt.Errorf("stat registry config %q: %w", registryConfig, err)
	}
	if info.IsDir() {
		return fmt.Errorf("registry config %q must be a file", registryConfig)
	}
	return nil
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
func (puller HelmOCIPuller) Pull(ctx context.Context, request Request, opts Options) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository, err := parseOCIChartRepository(request.Repository)
	if err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "drydock-oci-chart-")
	if err != nil {
		return nil, fmt.Errorf("create temporary OCI chart directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	registryConfig := strings.TrimSpace(opts.Credentials.RegistryConfig)
	if registryConfig == "" {
		registryConfig = filepath.Join(tempDir, "registry-config.json")
		if err := os.WriteFile(registryConfig, []byte("{}\n"), 0o600); err != nil {
			return nil, fmt.Errorf("write temporary Helm registry config: %w", err)
		}
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

	chartRef := ociChartRef(repository, request.Name, request.Version)
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
func ociChartRef(repository, name, version string) string {
	separator := ":"
	if strings.Contains(version, ":") {
		separator = "@"
	}
	return repository + "/" + name + separator + version
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
		return "", fmt.Errorf("OCI chart repository must not include credentials; use --registry-config")
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
	for segment := range strings.SplitSeq(cleanPath, "/") {
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
