package config

import (
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
)

func appendParsedResourceFilters(dst *[]ResourceFilterRule, raw string, provenance diagnostic.Provenance, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var rules []ResourceFilterRule
	if err := yaml.Unmarshal([]byte(raw), &rules); err != nil {
		return append(diags, diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource filter settings",
			Provenance: provenance,
		})
	}
	for i := range rules {
		rules[i].Provenance = provenance
	}
	*dst = append(*dst, rules...)
	return diags
}
