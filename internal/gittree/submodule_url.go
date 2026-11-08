package gittree

import (
	"fmt"
	"net/url"
	"strings"
)

// ResolveSubmoduleURL resolves the URL of a submodule as `git submodule
// init` and `git submodule sync` do (relative_url in git's
// builtin/submodule--helper.c): a URL that starts with "./" or "../" is
// relative to remoteURL, the URL of the superproject's remote. In the Argo
// CD repo-server, that remote is origin, with the repoURL of the
// Application (util/git/client.go Init). Any other URL is returned as it
// is.
func ResolveSubmoduleURL(remoteURL, submoduleURL string) (string, error) {
	if !isRelativeSubmoduleURL(submoduleURL) {
		return submoduleURL, nil
	}
	remote := strings.TrimSuffix(remoteURL, "/")
	if remote == "" {
		return "", fmt.Errorf("relative submodule URL %q needs the URL of the superproject", submoduleURL)
	}
	relativeRemote := urlIsLocalNotSSH(remote) && !strings.HasPrefix(remote, "/")
	if relativeRemote && !strings.HasPrefix(remote, "./") && !strings.HasPrefix(remote, "../") {
		remote = "./" + remote
	}
	rest := submoduleURL
	colonSeparated := false
	for {
		switch {
		case strings.HasPrefix(rest, "../"):
			rest = rest[3:]
			chopped, colon, err := chopLastDir(remote, relativeRemote)
			if err != nil {
				return "", err
			}
			remote = chopped
			colonSeparated = colonSeparated || colon
			continue
		case strings.HasPrefix(rest, "./"):
			rest = rest[2:]
			continue
		}
		break
	}
	separator := "/"
	if colonSeparated {
		separator = ":"
	}
	out := remote + separator + rest
	if strings.HasSuffix(rest, "/") {
		out = strings.TrimSuffix(out, "/")
	}
	return strings.TrimPrefix(out, "./"), nil
}

// isRelativeSubmoduleURL reports whether git resolves url against the
// remote of the superproject: a local-looking URL (no ":" before the first
// "/") that starts with "./" or "../".
func isRelativeSubmoduleURL(value string) bool {
	return urlIsLocalNotSSH(value) && (strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../"))
}

// urlIsLocalNotSSH mirrors url_is_local_not_ssh in git: a URL with no ":",
// or with a "/" before its first ":".
func urlIsLocalNotSSH(value string) bool {
	colon := strings.Index(value, ":")
	slash := strings.Index(value, "/")
	return colon < 0 || (slash >= 0 && slash < colon)
}

// chopLastDir mirrors chop_last_dir in git: it removes the last "/"
// component of remote, or else the part after its last ":" (an scp-like
// URL), which colon then reports.
func chopLastDir(remote string, relativeRemote bool) (string, bool, error) {
	if before, _, ok := strings.CutLast(remote, "/"); ok {
		return before, false, nil
	}
	if before, _, ok := strings.CutLast(remote, ":"); ok {
		return before, true, nil
	}
	if relativeRemote || remote == "." {
		return "", false, fmt.Errorf("cannot strip one component off url %q", remote)
	}
	return ".", false, nil
}

// SubmoduleTransport returns the transport that git uses for a resolved
// submodule URL: "http", "https", "git", "ssh", "file", or another scheme
// name. An scp-like URL (host:path) is ssh. A path is file.
func SubmoduleTransport(value string) string {
	if helper, _, ok := strings.Cut(value, "::"); ok {
		// <transport>::<address> runs git-remote-<transport>.
		return "remote-helper:" + helper
	}
	if i := strings.Index(value, "://"); i > 0 {
		return strings.ToLower(value[:i])
	}
	if !urlIsLocalNotSSH(value) {
		return "ssh"
	}
	return "file"
}

// AllowedSubmoduleTransport reports whether the Argo CD repo-server's git
// would clone a submodule over transport. git allows http, https, git, and
// ssh always. It allows file only when the user runs the command, and `git
// submodule` clones with GIT_PROTOCOL_FROM_USER=0, so a file URL or a local
// path fails there (protocol.file.allow defaults to "user" since git
// 2.38.1). ext and every other transport are not allowed.
func AllowedSubmoduleTransport(transport string) bool {
	switch transport {
	case "http", "https", "git", "ssh", "git+ssh", "ssh+git":
		return true
	}
	return false
}

// SubmoduleURLHost returns the host of a resolved submodule URL, or "".
func SubmoduleURLHost(value string) string {
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return ""
		}
		return strings.ToLower(parsed.Hostname())
	}
	if urlIsLocalNotSSH(value) {
		return ""
	}
	host, _, _ := strings.Cut(value, ":")
	if _, after, ok := strings.Cut(host, "@"); ok {
		host = after
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}
