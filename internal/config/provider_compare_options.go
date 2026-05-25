package config

import (
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
	"strings"
)

func appendParsedResourceCompareOptions(settings *ArgoSettings, raw string, provenance diagnostic.Provenance, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var options ResourceCompareOptions
	if err := yaml.Unmarshal([]byte(raw), &options); err != nil {
		return append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource.compareoptions settings",
			Provenance: provenance,
		})
	}
	if strings.TrimSpace(options.IgnoreResourceStatusField) == "" {
		options.IgnoreResourceStatusField = "all"
	}
	options.Provenance = provenance
	settings.CompareOptions = options
	if !knownIgnoreResourceStatusField(options.IgnoreResourceStatusField) {
		diags = append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "unrecognized resource.compareoptions ignoreResourceStatusField value; treating as all",
			Provenance: provenance,
		})
	}
	return diags
}
func knownIgnoreResourceStatusField(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all", "crd", "none", "off", "false":
		return true
	default:
		return false
	}
}
