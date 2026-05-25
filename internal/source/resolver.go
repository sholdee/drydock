package source

import (
	"net/url"
	"strings"
)

type RepoMap struct {
	URL  string
	Path string
}

type Options struct {
	RepoMaps []RepoMap
	Offline  bool
}

type Resolver struct {
	repoMaps map[string]string
	offline  bool
}

type ResolvedRepository struct {
	URL              string
	NormalizedURL    string
	DeclaredRevision string
	LocalPath        string
	Mapped           bool
	Network          bool
}

func (r *Resolver) MappedPath(repoURL string) (string, bool) {
	localPath, ok := r.repoMaps[NormalizeURL(repoURL)]
	return localPath, ok
}

func NewResolver(opts Options) *Resolver {
	repoMaps := make(map[string]string, len(opts.RepoMaps))
	for _, repoMap := range opts.RepoMaps {
		repoMaps[NormalizeURL(repoMap.URL)] = repoMap.Path
	}
	return &Resolver{
		repoMaps: repoMaps,
		offline:  opts.Offline,
	}
}

func (r *Resolver) Resolve(repoURL, revision string) (ResolvedRepository, error) {
	normalizedURL := NormalizeURL(repoURL)
	if localPath, ok := r.repoMaps[normalizedURL]; ok {
		return ResolvedRepository{
			URL:              repoURL,
			NormalizedURL:    normalizedURL,
			DeclaredRevision: revision,
			LocalPath:        localPath,
			Mapped:           true,
		}, nil
	}

	return ResolvedRepository{
		URL:              repoURL,
		NormalizedURL:    normalizedURL,
		DeclaredRevision: revision,
		Network:          !r.offline,
	}, nil
}

func NormalizeURL(input string) string {
	output := strings.TrimSpace(input)
	output = strings.TrimRight(output, "/")
	output = strings.TrimSuffix(output, ".git")
	output = strings.TrimRight(output, "/")
	return output
}

func RedactURL(input string) string {
	output := strings.TrimSpace(input)
	if parsed, err := url.Parse(output); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		return parsed.String()
	}

	if before, _, ok := strings.Cut(output, "#"); ok {
		output = before
	}
	if before, _, ok := strings.Cut(output, "?"); ok {
		output = before
	}
	if schemeIndex := strings.Index(output, "://"); schemeIndex >= 0 {
		prefix := output[:schemeIndex+3]
		rest := output[schemeIndex+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			return prefix + rest[at+1:]
		}
	}
	return output
}
