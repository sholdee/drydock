package chart

import (
	"net/url"
	"strings"
)

func redactedFetchURL(raw string, stripQueryFragment bool) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	if stripQueryFragment {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
	}
	return parsed.String()
}
func redactedFetchError(err error, rawURL string, stripQueryFragment bool) string {
	message := strings.ReplaceAll(err.Error(), rawURL, redactedFetchURL(rawURL, stripQueryFragment))
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsed.User == nil {
		return message
	}
	prefix := parsed.Scheme + "://"
	hostMarker := "@" + parsed.Host
	for {
		hostIndex := strings.Index(message, hostMarker)
		if hostIndex < 0 {
			return message
		}
		start := strings.LastIndex(message[:hostIndex], prefix)
		if start < 0 {
			return message
		}
		message = message[:start] + prefix + parsed.Host + message[hostIndex+len(hostMarker):]
	}
}
func redactedChartCredentialError(message string, credentials ChartCredentials) string {
	for _, secret := range []string{credentials.Password, credentials.BearerToken} {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return message
}
