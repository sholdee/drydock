package source

import "strings"

// CanonicalGitURLKey reduces a git URL to "host/path" for self-repo matching
// ONLY: lowercase host, strip scheme/userinfo/port, convert scp-style
// git@host:x/y, trim ".git" and trailing slashes. It must never feed
// GitCacheKey, NormalizeURL consumers, or render-cache locators — it is a
// matching key, not a cache key (changing NormalizeURL would invalidate
// persistent caches; NormalizeURL stays untouched).
func CanonicalGitURLKey(raw string) string {
	rest := strings.TrimSpace(raw)
	if rest == "" {
		return ""
	}
	if schemeIndex := strings.Index(rest, "://"); schemeIndex >= 0 {
		rest = rest[schemeIndex+3:]
	} else if isSCPStyleGitURL(rest) {
		userHost, repoPath, _ := strings.Cut(rest, ":")
		rest = userHost + "/" + repoPath
	}
	host, repoPath, hasPath := strings.Cut(rest, "/")
	if _, afterUser, ok := strings.Cut(host, "@"); ok {
		host = afterUser
	}
	// Strip a port only when the colon follows any bracketed IPv6 literal —
	// LastIndex(":") alone would truncate ssh://git@[::1]/x/y inside the
	// brackets.
	if colon := strings.LastIndex(host, ":"); colon >= 0 && colon > strings.LastIndex(host, "]") {
		host = host[:colon]
	}
	host = strings.ToLower(host)
	if !hasPath {
		return host
	}
	repoPath = strings.TrimRight(repoPath, "/")
	repoPath = strings.TrimSuffix(repoPath, ".git")
	repoPath = strings.TrimRight(repoPath, "/")
	if repoPath == "" {
		return host
	}
	return host + "/" + repoPath
}

// IsCommitSHA reports whether rev is a full 40- or 64-char lowercase-hex id.
func IsCommitSHA(rev string) bool {
	if len(rev) != 40 && len(rev) != 64 {
		return false
	}
	for _, r := range rev {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// IsDefaultRevision reports whether rev requests the default branch.
func IsDefaultRevision(rev string) bool { return isDefaultGitRevision(rev) }
