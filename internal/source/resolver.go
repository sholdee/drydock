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
	if redacted, ok := redactParsedURL(output); ok {
		return redacted
	}

	output = stripURLQueryFragment(output)
	if redacted, ok := redactEmbeddedSchemeUserinfo(output); ok {
		return redacted
	}
	if redacted, ok := redactSCPUserinfo(output); ok {
		return redacted
	}
	if redacted, ok := redactOpaqueSchemeUserinfo(output); ok {
		return redacted
	}
	if redacted, ok := redactGenericUserinfo(output); ok {
		return redacted
	}
	return output
}

func redactParsedURL(output string) (string, bool) {
	parsed, err := url.Parse(output)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), true
}

func stripURLQueryFragment(output string) string {
	if before, _, ok := strings.Cut(output, "#"); ok {
		output = before
	}
	if before, _, ok := strings.Cut(output, "?"); ok {
		output = before
	}
	return output
}

func redactEmbeddedSchemeUserinfo(output string) (string, bool) {
	if schemeIndex := strings.Index(output, "://"); schemeIndex >= 0 {
		prefix := output[:schemeIndex+3]
		rest := output[schemeIndex+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			return prefix + rest[at+1:], true
		}
	}
	return "", false
}

func redactSCPUserinfo(output string) (string, bool) {
	if colon := strings.Index(output, ":"); colon > 0 {
		if slash := strings.IndexAny(output, `/\`); slash == -1 || slash > colon {
			userHost := output[:colon]
			repoPath := output[colon+1:]
			if _, host, ok := strings.Cut(userHost, "@"); ok && host != "" && repoPath != "" {
				return host + ":" + repoPath, true
			}
		}
	}
	return "", false
}

func redactOpaqueSchemeUserinfo(output string) (string, bool) {
	colon := strings.Index(output, ":")
	at := strings.LastIndex(output, "@")
	if colon <= 0 || at <= colon {
		return "", false
	}
	scheme := output[:colon]
	if strings.ContainsAny(scheme, `/\`) {
		return "", false
	}
	return output[:colon+1] + output[at+1:], true
}

func redactGenericUserinfo(output string) (string, bool) {
	if at := strings.LastIndex(output, "@"); at >= 0 {
		return output[at+1:], true
	}
	return "", false
}
