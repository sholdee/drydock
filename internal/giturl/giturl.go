// Package giturl provides shared helpers for classifying Git repository URLs
// and redacting them inside error messages.
package giturl

import (
	"net/url"
	"strings"
)

// IsSSHURL reports whether repoURL uses SSH transport, either via an
// explicit ssh:// scheme or SCP-style syntax.
func IsSSHURL(repoURL string) bool {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return false
	}
	if strings.HasPrefix(repoURL, "ssh://") {
		return true
	}
	return IsSCPStyle(repoURL)
}

// IsSCPStyle reports whether repoURL is an SCP-style Git URL (user@host:path).
func IsSCPStyle(repoURL string) bool {
	if strings.Contains(repoURL, "://") || strings.HasPrefix(repoURL, "/") {
		return false
	}
	colon := strings.Index(repoURL, ":")
	if colon <= 0 {
		return false
	}
	if slash := strings.IndexAny(repoURL, `/\`); slash >= 0 && slash < colon {
		return false
	}
	return strings.Contains(repoURL[:colon], "@")
}

// SSHUser returns the username encoded in repoURL, defaulting to "git".
func SSHUser(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if IsSCPStyle(repoURL) {
		userHost, _, _ := strings.Cut(repoURL, ":")
		if user, _, ok := strings.Cut(userHost, "@"); ok && user != "" {
			return user
		}
		return "git"
	}
	if parsed, err := url.Parse(repoURL); err == nil && parsed.User != nil {
		if user := parsed.User.Username(); user != "" {
			return user
		}
	}
	return "git"
}

// RedactInMessage replaces every recognizable form of repoURL inside message
// with the caller-supplied redacted placeholder: the raw URL, its .git-less
// variant, fragment/query/userinfo-stripped variants, and any embedded
// userinfo, query, or fragment components.
func RedactInMessage(message, repoURL, redacted string) string {
	raw := strings.TrimSpace(repoURL)
	replacements := []string{raw, strings.TrimSuffix(raw, ".git")}
	if parsed, parseErr := url.Parse(raw); parseErr == nil && parsed.Scheme != "" {
		withoutFragment := *parsed
		withoutFragment.Fragment = ""
		replacements = append(replacements, withoutFragment.String())

		withoutQueryFragment := withoutFragment
		withoutQueryFragment.RawQuery = ""
		withoutQueryFragment.ForceQuery = false
		replacements = append(replacements, withoutQueryFragment.String())

		withoutUser := withoutQueryFragment
		withoutUser.User = nil
		replacements = append(replacements, withoutUser.String())

		if parsed.User != nil {
			username := parsed.User.Username()
			replacements = append(replacements, parsed.User.String()+"@")
			if username != "" {
				replacements = append(replacements, username+":***@")
				replacements = append(replacements, username+"@")
			}
		}
		if parsed.RawQuery != "" {
			replacements = append(replacements, parsed.RawQuery)
		}
		if parsed.Fragment != "" {
			replacements = append(replacements, parsed.Fragment)
		}
	}
	for _, replacement := range replacements {
		if replacement == "" || replacement == redacted {
			continue
		}
		message = strings.ReplaceAll(message, replacement, redacted)
	}
	return message
}
