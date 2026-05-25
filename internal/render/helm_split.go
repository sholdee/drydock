package render

import (
	"regexp"
	"strings"
	"unicode"
)

var helmManifestSeparator = regexp.MustCompile(`(?:^|\s*\n)---\s*`)

func splitHelmRenderedManifests(rendered string) []string {
	rendered = strings.TrimLeftFunc(rendered, unicode.IsSpace)
	if rendered == "" {
		return nil
	}
	parts := helmManifestSeparator.Split(rendered, -1)
	manifests := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimLeftFunc(part, unicode.IsSpace)
		if strings.TrimSpace(part) == "" {
			continue
		}
		manifests = append(manifests, part)
	}
	return manifests
}
