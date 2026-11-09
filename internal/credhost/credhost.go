// Package credhost reads the host entries of the --remote-auth-host and
// --git-auth-host flags, and matches request URLs against them. An entry
// names one scheme, host, and port that receives a credential.
package credhost

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Host is one host that receives a credential: a scheme, a lower-case host
// name, and a port. An entry without a scheme is https, and a missing port
// is the default port of the scheme.
type Host struct {
	scheme string
	host   string
	port   string
}

// Parse reads entries. flag is the name of the flag, such as
// "--git-auth-host", that the errors quote.
func Parse(flag string, entries []string) ([]Host, error) {
	out := make([]Host, 0, len(entries))
	for _, entry := range entries {
		host, err := parseEntry(flag, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, host)
	}
	return out, nil
}

// parseEntry reads one entry. The errors quote the entry without userinfo,
// so a password typed into an entry by mistake never reaches the output.
func parseEntry(flag, entry string) (Host, error) {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return Host{}, fmt.Errorf("%s must not be empty", flag)
	}
	withScheme := raw
	if !strings.Contains(raw, "://") {
		withScheme = "https://" + raw
	}
	parsed, err := url.Parse(withScheme)
	if err != nil {
		return Host{}, fmt.Errorf("%s %q is not a valid host[:port]", flag, redact(withScheme))
	}
	if err := checkEntryURL(parsed); err != nil {
		return Host{}, fmt.Errorf("%s %q %w", flag, redact(withScheme), err)
	}
	host, ok := Of(parsed)
	if !ok {
		return Host{}, fmt.Errorf("%s %q must include a host", flag, redact(withScheme))
	}
	return host, nil
}

// checkEntryURL fails unless parsed is a bare http or https origin with a
// valid port. Its errors complete the sentence that starts with the entry.
func checkEntryURL(parsed *url.URL) error {
	if parsed.User != nil {
		return errors.New("must not include userinfo")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("must use http or https")
	}
	if parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return errors.New("must be a host[:port] without a path, query, or fragment")
	}
	if port := parsed.Port(); port != "" {
		if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
			return errors.New("has an invalid port")
		}
	}
	return nil
}

// Of returns the scheme, host, and port of target, with the default port
// of the scheme when target has none. It fails for a scheme other than
// http or https when the port is missing, and for a URL without a host.
func Of(target *url.URL) (Host, bool) {
	if target == nil {
		return Host{}, false
	}
	scheme := strings.ToLower(target.Scheme)
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return Host{}, false
	}
	port := target.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return Host{}, false
		}
	}
	return Host{scheme: scheme, host: host, port: port}, true
}

// Contains reports whether the scheme, host, and port of target match an
// entry of hosts exactly. The host name match ignores case.
func Contains(hosts []Host, target *url.URL) bool {
	want, ok := Of(target)
	return ok && slices.Contains(hosts, want)
}

// Same reports whether target has the scheme, host, and port of want.
func Same(target *url.URL, want Host) bool {
	got, ok := Of(target)
	return ok && got == want
}

// Entry returns the entry that names the scheme, host, and port of rawURL,
// such as "https://git.example.test:443". It fails unless rawURL is an
// http or https URL with a host.
func Entry(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}
	host, ok := Of(parsed)
	if !ok || (host.scheme != "http" && host.scheme != "https") {
		return "", false
	}
	return host.scheme + "://" + net.JoinHostPort(host.host, host.port), true
}

// redact removes the userinfo, the query, and the fragment of raw.
func redact(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid-url]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if parsed.Scheme == "" && parsed.Host == "" {
		return "[invalid-url]"
	}
	return parsed.String()
}
