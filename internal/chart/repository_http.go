package chart

import (
	"context"
	"errors"
	"fmt"
	"go.yaml.in/yaml/v3"
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

// fetchHTTPIndex fetches the index of the HTTP chart repository of request.
func (acquirer DefaultAcquirer) fetchHTTPIndex(ctx context.Context, request Request, opts Options) (repositoryIndex, error) {
	client := acquirer.Client
	if client == nil {
		client = http.DefaultClient
	}
	credentials := opts.Credentials
	repository, err := NormalizeRepository(request.Repository, request.Kind)
	if err != nil {
		return repositoryIndex{}, err
	}
	indexURL, err := repositoryIndexURL(repository)
	if err != nil {
		return repositoryIndex{}, err
	}
	redactedIndexURL := redactedFetchURL(indexURL, false)
	indexRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return repositoryIndex{}, fmt.Errorf("create chart repository index request %s: %s", redactedIndexURL, redactedFetchError(err, indexURL, false))
	}
	applyChartAuth(indexRequest, credentials)
	indexResponse, err := client.Do(indexRequest)
	if err != nil {
		return repositoryIndex{}, fmt.Errorf("fetch chart repository index %s: %s", redactedIndexURL, redactedChartCredentialError(redactedFetchError(err, indexURL, false), credentials))
	}
	defer func() { _ = indexResponse.Body.Close() }()
	if indexResponse.StatusCode != http.StatusOK {
		return repositoryIndex{}, fmt.Errorf("fetch chart repository index %s: HTTP %s", redactedIndexURL, indexResponse.Status)
	}
	var index repositoryIndex
	if err := yaml.NewDecoder(indexResponse.Body).Decode(&index); err != nil {
		return repositoryIndex{}, fmt.Errorf("decode chart repository index %s: %w", redactedIndexURL, err)
	}
	return index, nil
}

// fetchHTTPChart fetches the chart archive of request from its HTTP chart
// repository. index is the repository index when the caller already
// fetched it, or nil.
func (acquirer DefaultAcquirer) fetchHTTPChart(ctx context.Context, request Request, opts Options, index *repositoryIndex) ([]byte, error) {
	client := acquirer.Client
	if client == nil {
		client = http.DefaultClient
	}
	credentials := opts.Credentials
	repository, err := NormalizeRepository(request.Repository, request.Kind)
	if err != nil {
		return nil, err
	}
	if index == nil {
		fetched, err := acquirer.fetchHTTPIndex(ctx, request, opts)
		if err != nil {
			return nil, err
		}
		index = &fetched
	}

	chartURL, err := findChartURL(repository, request, *index)
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
	defer func() { _ = archiveResponse.Body.Close() }()
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

// findChartURL returns the archive URL of the index entry that request
// names: under VersionAsWritten the entry with the version as written or
// without a leading "v", and under the other policies the entry that helm
// pull takes (helmIndexGet).
func findChartURL(repository string, request Request, index repositoryIndex) (string, error) {
	entry, err := findChartVersion(request, index)
	if err != nil {
		return "", err
	}
	if len(entry.URLs) == 0 {
		return "", fmt.Errorf("chart %s version %s has no archive URLs", request.Name, entry.Version)
	}
	archiveURL, err := url.Parse(entry.URLs[0])
	if err != nil {
		return "", fmt.Errorf("parse chart archive URL %q: %w", entry.URLs[0], err)
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

func findChartVersion(request Request, index repositoryIndex) (repositoryChartVersion, error) {
	versions := index.Entries[request.Name]
	if request.VersionPolicy != VersionAsWritten {
		entry, err := helmIndexGet(versions, request.Name, strings.TrimSpace(request.Version))
		if errors.Is(err, errChartVersionNotFound) {
			return repositoryChartVersion{}, fmt.Errorf("chart %s version %s not found in repository index", request.Name, request.Version)
		}
		return entry, err
	}
	for _, candidate := range chartVersionCandidates(request.Version) {
		for _, version := range versions {
			if version.Version == candidate {
				return version, nil
			}
		}
	}
	return repositoryChartVersion{}, fmt.Errorf("chart %s version %s not found in repository index", request.Name, request.Version)
}

func chartVersionCandidates(version string) []string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') && version[1] >= '0' && version[1] <= '9' {
		return []string{version, version[1:]}
	}
	return []string{version}
}
