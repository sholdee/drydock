package source

import (
	"fmt"
	"strings"
)

type RepoMap struct {
	URL  string
	Path string
}

type Options struct {
	RepoMaps     []RepoMap
	AllowNetwork bool
}

type Resolver struct {
	repoMaps     map[string]string
	allowNetwork bool
}

type ResolvedRepository struct {
	URL              string
	NormalizedURL    string
	DeclaredRevision string
	LocalPath        string
	Mapped           bool
	Network          bool
}

func NewResolver(opts Options) *Resolver {
	repoMaps := make(map[string]string, len(opts.RepoMaps))
	for _, repoMap := range opts.RepoMaps {
		repoMaps[NormalizeURL(repoMap.URL)] = repoMap.Path
	}
	return &Resolver{
		repoMaps:     repoMaps,
		allowNetwork: opts.AllowNetwork,
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

	if r.allowNetwork {
		return ResolvedRepository{
			URL:              repoURL,
			NormalizedURL:    normalizedURL,
			DeclaredRevision: revision,
			Network:          true,
		}, nil
	}

	return ResolvedRepository{}, fmt.Errorf("repository %s is not mapped; pass --repo-map or --allow-network", repoURL)
}

func NormalizeURL(input string) string {
	output := strings.TrimSpace(input)
	output = strings.TrimRight(output, "/")
	output = strings.TrimSuffix(output, ".git")
	output = strings.TrimRight(output, "/")
	return output
}
