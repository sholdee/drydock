package chart

import (
	"context"
	"fmt"
	"go.yaml.in/yaml/v4"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type repositoryIndex struct {
	Entries map[string][]repositoryChartVersion `yaml:"entries"`
}
type repositoryChartVersion struct {
	Version string   `yaml:"version"`
	URLs    []string `yaml:"urls"`
}

//nolint:gocyclo // Keeps index and chart archive request handling together for consistent URL redaction.
func (acquirer DefaultAcquirer) fetchHTTPChart(ctx context.Context, request Request, opts Options) ([]byte, error) {
	client := acquirer.Client
	if client == nil {
		client = http.DefaultClient
	}
	credentials := opts.Credentials
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
	applyChartAuth(indexRequest, credentials)
	indexResponse, err := client.Do(indexRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch chart repository index %s: %s", redactedIndexURL, redactedChartCredentialError(redactedFetchError(err, indexURL, false), credentials))
	}
	defer indexResponse.Body.Close()
	if indexResponse.StatusCode == http.StatusUnauthorized || indexResponse.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("fetch chart repository index %s: HTTP %s", redactedIndexURL, indexResponse.Status)
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
	if shouldPassChartCredentialsToArchive(repository, chartURL, opts.PassCredentials) {
		applyChartAuth(archiveRequest, credentials)
	}
	archiveResponse, err := client.Do(archiveRequest)
	if err != nil {
		return nil, fmt.Errorf("fetch chart archive %s: %s", redactedChartURL, redactedChartCredentialError(redactedFetchError(err, chartURL, true), credentials))
	}
	defer archiveResponse.Body.Close()
	if archiveResponse.StatusCode == http.StatusUnauthorized || archiveResponse.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("fetch chart archive %s: HTTP %s", redactedChartURL, archiveResponse.Status)
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

func applyChartAuth(request *http.Request, credentials ChartCredentials) {
	if strings.TrimSpace(credentials.BearerToken) != "" {
		request.Header.Set("Authorization", "Bearer "+credentials.BearerToken)
		return
	}
	if strings.TrimSpace(credentials.Username) != "" || credentials.Password != "" {
		request.SetBasicAuth(credentials.Username, credentials.Password)
	}
}

func shouldPassChartCredentialsToArchive(repository, archiveURL string, passCredentials bool) bool {
	if passCredentials {
		return true
	}
	repositoryURL, err := url.Parse(repository)
	if err != nil {
		return false
	}
	parsedArchiveURL, err := url.Parse(archiveURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(repositoryURL.Host, parsedArchiveURL.Host)
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
	for _, candidate := range chartVersionCandidates(request.Version) {
		for _, version := range versions {
			if version.Version != candidate {
				continue
			}
			if len(version.URLs) == 0 {
				return "", fmt.Errorf("chart %s version %s has no archive URLs", request.Name, candidate)
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
	}
	return "", fmt.Errorf("chart %s version %s not found in repository index", request.Name, request.Version)
}

func chartVersionCandidates(version string) []string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') && version[1] >= '0' && version[1] <= '9' {
		return []string{version, version[1:]}
	}
	return []string{version}
}
