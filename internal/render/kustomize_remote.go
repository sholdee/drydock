package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/sholdee/drydock/internal/remote"
)

type kustomizeRemoteKind string

const (
	kustomizeRemoteNone     kustomizeRemoteKind = ""
	kustomizeRemoteHTTPFile kustomizeRemoteKind = "http-file"
	kustomizeRemoteGit      kustomizeRemoteKind = "git"
)

type kustomizeRemoteRef struct {
	Original string
	Kind     kustomizeRemoteKind
	URL      string
	RepoURL  string
	Revision string
	Subpath  string
	// Submodules is the ?submodules= query parameter of a Git ref, true
	// by default (kustomizeRemoteQuery).
	Submodules bool
}

func parseKustomizeRemoteRef(ref string) (kustomizeRemoteRef, bool, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return kustomizeRemoteRef{}, false, nil
	}

	if normalized, err := remote.NormalizeURL(trimmed); err == nil {
		return kustomizeRemoteRef{
			Original: trimmed,
			Kind:     kustomizeRemoteHTTPFile,
			URL:      normalized,
		}, true, nil
	}

	return parseKustomizeGitRemoteRef(trimmed)
}

func parseKustomizeGitRemoteRef(ref string) (kustomizeRemoteRef, bool, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return kustomizeRemoteRef{}, false, nil
	}

	if isFileTransportKustomizeRef(trimmed) {
		return kustomizeRemoteRef{}, true, fmt.Errorf("kustomize remote ref %q uses the local file transport; %s", redactKustomizeRemoteRef(trimmed), fileTransportRefusal)
	}
	withoutPrefix := strings.TrimPrefix(trimmed, "git::")
	if strings.Contains(withoutPrefix, "://") {
		return parseKustomizeGitURLRef(trimmed, withoutPrefix)
	}
	if scpRefHasRemoteCredentialUserinfo(withoutPrefix) {
		return kustomizeRemoteRef{}, true, fmt.Errorf("kustomize remote ref %q must not include userinfo", redactKustomizeRemoteRef(trimmed))
	}
	if isSCPStyleKustomizeGitRef(withoutPrefix) {
		return parseKustomizeGitSCPRef(trimmed, withoutPrefix)
	}
	return kustomizeRemoteRef{}, false, nil
}

func parseKustomizeGitURLRef(original, raw string) (kustomizeRemoteRef, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return kustomizeRemoteRef{}, true, fmt.Errorf("parse kustomize remote ref %q: invalid URL syntax", redactKustomizeRemoteRef(original))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ssh" {
		return kustomizeRemoteRef{}, false, nil
	}
	if parsed.Host == "" {
		return kustomizeRemoteRef{}, false, nil
	}
	if parsed.User != nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || hasURLPassword(parsed.User)) {
		return kustomizeRemoteRef{}, true, fmt.Errorf("kustomize remote ref %q must not include userinfo", redactKustomizeRemoteRef(original))
	}
	if parsed.Fragment != "" {
		return kustomizeRemoteRef{}, true, fmt.Errorf("kustomize remote ref %q must not include a fragment", redactKustomizeRemoteRef(original))
	}
	revision, submodules, err := kustomizeRemoteQuery(parsed.RawQuery, original)
	if err != nil {
		return kustomizeRemoteRef{}, true, err
	}

	repoPath, subpath, ok := strings.Cut(parsed.Path, "//")
	if !ok {
		repoPath, subpath, ok = inferKustomizeGitURLPath(parsed.Host, parsed.Path)
		if !ok {
			return kustomizeRemoteRef{}, false, nil
		}
	}
	repoPath = strings.TrimSuffix(repoPath, "/")
	subpath = strings.TrimPrefix(subpath, "/")
	if repoPath == "" {
		return kustomizeRemoteRef{}, false, nil
	}
	subpath = cleanKustomizeGitSubpath(subpath)

	repo := *parsed
	repo.Path = repoPath
	repo.RawQuery = ""
	repo.Fragment = ""
	return kustomizeRemoteRef{
		Original:   strings.TrimSpace(original),
		Kind:       kustomizeRemoteGit,
		RepoURL:    repo.String(),
		Revision:   revision,
		Subpath:    path.Clean(subpath),
		Submodules: submodules,
	}, true, nil
}

// fileTransportRefusal ends the error for a Kustomize ref that names the
// file transport.
const fileTransportRefusal = "drydock does not clone Git repositories from the local file system"

// isFileTransportKustomizeRef reports whether ref names the file
// transport, with or without the git:: prefix and in any case. kustomize
// clones a file:// ref from the local file system (internal/git/repospec.go
// extractScheme and extractHost), so a repository could make drydock read
// any Git repository on the machine. file: and file:: forms are refused
// too, so that no file form reaches the Git client.
func isFileTransportKustomizeRef(ref string) bool {
	lower := strings.ToLower(strings.TrimSpace(ref))
	lower = strings.TrimPrefix(lower, "git::")
	return strings.HasPrefix(lower, "file:")
}

func inferKustomizeGitURLPath(host, rawPath string) (string, string, bool) {
	segments := pathSegments(rawPath)
	if len(segments) < 2 {
		return "", "", false
	}
	for i, segment := range segments {
		if strings.HasSuffix(segment, ".git") {
			if i == len(segments)-1 {
				return "/" + path.Join(segments[:i+1]...), ".", true
			}
			return "/" + path.Join(segments[:i+1]...), path.Join(segments[i+1:]...), true
		}
	}
	if !isKnownGitHost(strings.ToLower(host)) || len(segments) < 2 {
		return "", "", false
	}
	if len(segments) == 2 {
		return "/" + path.Join(segments[:2]...), ".", true
	}
	return "/" + path.Join(segments[:2]...), path.Join(segments[2:]...), true
}

func pathSegments(rawPath string) []string {
	trimmed := strings.Trim(rawPath, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	segments := parts[:0]
	for _, part := range parts {
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func parseKustomizeGitSCPRef(original, raw string) (kustomizeRemoteRef, bool, error) {
	beforeFragment, fragment, hasFragment := strings.Cut(raw, "#")
	if hasFragment && fragment != "" {
		return kustomizeRemoteRef{}, true, fmt.Errorf("kustomize remote ref %q must not include a fragment", redactKustomizeRemoteRef(original))
	}
	beforeQuery, rawQuery, _ := strings.Cut(beforeFragment, "?")
	revision, submodules, err := kustomizeRemoteQuery(rawQuery, original)
	if err != nil {
		return kustomizeRemoteRef{}, true, err
	}

	repo, subpath, ok := strings.Cut(beforeQuery, "//")
	if !ok {
		if !isRootSCPStyleKustomizeGitRepo(beforeQuery) {
			return kustomizeRemoteRef{}, false, nil
		}
		repo = beforeQuery
		subpath = "."
	}
	repo = strings.TrimSuffix(repo, "/")
	subpath = strings.TrimPrefix(subpath, "/")
	if repo == "" {
		return kustomizeRemoteRef{}, false, nil
	}
	subpath = cleanKustomizeGitSubpath(subpath)
	if userHost, _, ok := strings.Cut(repo, ":"); ok {
		if user, _, ok := strings.Cut(userHost, "@"); ok && strings.Contains(user, ":") {
			return kustomizeRemoteRef{}, true, fmt.Errorf("kustomize remote ref %q must not include userinfo", redactKustomizeRemoteRef(original))
		}
	}

	return kustomizeRemoteRef{
		Original:   strings.TrimSpace(original),
		Kind:       kustomizeRemoteGit,
		RepoURL:    repo,
		Revision:   revision,
		Subpath:    path.Clean(subpath),
		Submodules: submodules,
	}, true, nil
}

func cleanKustomizeGitSubpath(subpath string) string {
	cleaned := path.Clean(subpath)
	if cleaned == "" {
		return "."
	}
	return cleaned
}

// kustomizeRemoteQuery reads the query of a Git ref: the ref, and
// whether kustomize checks out submodules. kustomize clones submodules by
// default and reads ?submodules= with strconv.ParseBool, keeping the
// default for a value it cannot parse (sigs.k8s.io/kustomize/api
// internal/git/repospec.go parseQuery). Any other parameter is
// unsupported.
func kustomizeRemoteQuery(rawQuery, original string) (string, bool, error) {
	if rawQuery == "" {
		return "HEAD", true, nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", false, fmt.Errorf("parse kustomize remote ref query %q: invalid query syntax", redactKustomizeRemoteRef(original))
	}
	for key := range values {
		if key != "ref" && key != "submodules" {
			return "", false, fmt.Errorf("kustomize remote ref %q uses unsupported query parameter", redactKustomizeRemoteRef(original))
		}
	}
	submodules := true
	if value := values.Get("submodules"); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			submodules = parsed
		}
	}
	revisions := values["ref"]
	if len(revisions) == 0 || revisions[len(revisions)-1] == "" {
		return "HEAD", submodules, nil
	}
	return revisions[len(revisions)-1], submodules, nil
}

func redactKustomizeRemoteRef(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return ""
	}

	withoutPrefix := strings.TrimPrefix(trimmed, "git::")
	if strings.Contains(withoutPrefix, "://") {
		parsed, err := url.Parse(withoutPrefix)
		if err != nil || (parsed.User != nil && hasURLPassword(parsed.User)) {
			return "[remote-ref]"
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}

	if base, _, ok := strings.Cut(trimmed, "?"); ok {
		trimmed = base
	}
	if base, _, ok := strings.Cut(trimmed, "#"); ok {
		trimmed = base
	}
	if scpRefHasCredentialUserinfo(trimmed) {
		return "[remote-ref]"
	}
	if strings.Contains(trimmed, "user:") || strings.Contains(trimmed, "token=") {
		return "[remote-ref]"
	}
	return trimmed
}

func generatedRemoteRefName(prefix string, ref kustomizeRemoteRef) string {
	prefix = safeRemoteRefNamePart(prefix)
	if prefix == "" {
		prefix = "remote"
	}
	base := ref.Subpath
	if base == "" {
		base = ref.URL
	}
	if base == "" {
		base = ref.RepoURL
	}
	name := safeRemoteRefNamePart(path.Base(strings.TrimSuffix(base, "/")))
	if name == "" || name == "." {
		name = "resource"
	}
	sum := sha256.Sum256([]byte(remoteRefIdentity(ref)))
	return prefix + "-" + name + "-" + hex.EncodeToString(sum[:])[:12]
}

func remoteRefIdentity(ref kustomizeRemoteRef) string {
	switch ref.Kind {
	case kustomizeRemoteNone:
		return string(ref.Kind) + "\n" + redactKustomizeRemoteRef(ref.Original)
	case kustomizeRemoteHTTPFile:
		return string(ref.Kind) + "\n" + ref.URL
	case kustomizeRemoteGit:
		return strings.Join([]string{string(ref.Kind), ref.RepoURL, ref.Revision, ref.Subpath}, "\n")
	default:
		return string(ref.Kind) + "\n" + redactKustomizeRemoteRef(ref.Original)
	}
}

func hasURLPassword(user *url.Userinfo) bool {
	if user == nil {
		return false
	}
	_, ok := user.Password()
	return ok
}

func isSCPStyleKustomizeGitRef(ref string) bool {
	if strings.Contains(ref, "://") {
		return false
	}
	beforePath := ref
	if before, _, ok := strings.Cut(beforePath, "?"); ok {
		beforePath = before
	}
	if before, _, ok := strings.Cut(beforePath, "#"); ok {
		beforePath = before
	}
	repo, _, ok := strings.Cut(beforePath, "//")
	if !ok {
		return isRootSCPStyleKustomizeGitRepo(beforePath)
	}
	if repo == "" {
		return false
	}
	return isSCPStyleKustomizeGitRepo(repo)
}

func isRootSCPStyleKustomizeGitRepo(repo string) bool {
	repo = strings.TrimSuffix(strings.TrimSpace(repo), "/")
	return strings.HasSuffix(repo, ".git") && isSCPStyleKustomizeGitRepo(repo)
}

func isSCPStyleKustomizeGitRepo(repo string) bool {
	beforeColon, afterColon, ok := strings.Cut(repo, ":")
	if !ok || beforeColon == "" || afterColon == "" || strings.ContainsAny(beforeColon, `/\`) {
		return false
	}
	host := beforeColon
	if user, afterAt, ok := strings.Cut(beforeColon, "@"); ok {
		if user == "" || afterAt == "" || strings.ContainsAny(afterAt, `/\`) {
			return false
		}
		host = afterAt
	}
	host = strings.ToLower(host)
	return isKnownGitHost(host) || looksLikeRemoteHost(host)
}

func scpRefHasCredentialUserinfo(ref string) bool {
	beforePath := ref
	if before, _, ok := strings.Cut(beforePath, "?"); ok {
		beforePath = before
	}
	if before, _, ok := strings.Cut(beforePath, "#"); ok {
		beforePath = before
	}
	repo, _, ok := strings.Cut(beforePath, "//")
	if !ok {
		repo = beforePath
	}
	at := strings.LastIndex(repo, "@")
	if at <= 0 {
		return false
	}
	return strings.Contains(repo[:at], ":")
}

func scpRefHasRemoteCredentialUserinfo(ref string) bool {
	beforePath := ref
	if before, _, ok := strings.Cut(beforePath, "?"); ok {
		beforePath = before
	}
	if before, _, ok := strings.Cut(beforePath, "#"); ok {
		beforePath = before
	}
	repo, _, ok := strings.Cut(beforePath, "//")
	if !ok {
		repo = beforePath
	}
	at := strings.LastIndex(repo, "@")
	if at <= 0 || !strings.Contains(repo[:at], ":") {
		return false
	}
	hostPath := repo[at+1:]
	host, _, ok := strings.Cut(hostPath, ":")
	if !ok {
		return false
	}
	host = strings.ToLower(host)
	return isKnownGitHost(host) || looksLikeRemoteHost(host)
}

func safeRemoteRefNamePart(value string) string {
	value = strings.TrimSpace(value)
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, value)
}
