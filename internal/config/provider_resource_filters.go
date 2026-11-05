package config

import (
	"fmt"

	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/labels"
	sigsyaml "sigs.k8s.io/yaml"
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

// appendParsedResourceSelectors parses argocd-cm resource.selectors the way
// Argo CD's SettingsManager.GetResourcesFilter does (util/settings/settings.go
// at v3.6.1): a list of apiGroups/kinds/clusters/selector rules, decoded
// with the same sigs.k8s.io/yaml JSON-tag rules, whose every selector must
// parse with labels.Parse. Argo CD fails settings loading on an unparsable
// list or selector, so each is an error diagnostic and none of the rules are
// kept.
func appendParsedResourceSelectors(dst *[]ResourceFilterRule, raw string, provenance diagnostic.Provenance, diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	var rules []ResourceFilterRule
	if err := sigsyaml.Unmarshal([]byte(raw), &rules); err != nil {
		return append(diags, diagnostic.Diagnostic{
			Code:       diagnostic.CodeSettingsResourceSelectorInvalid,
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    "invalid resource.selectors settings",
			Provenance: provenance,
		})
	}
	valid := true
	for i := range rules {
		rules[i].Provenance = provenance
		if _, err := labels.Parse(rules[i].Selector); err != nil {
			valid = false
			diags = append(diags, diagnostic.Diagnostic{
				Code:       diagnostic.CodeSettingsResourceSelectorInvalid,
				Severity:   diagnostic.SeverityError,
				Category:   "settings",
				Message:    fmt.Sprintf("invalid resource.selectors selector %q: %v", rules[i].Selector, err),
				Provenance: provenance,
			})
		}
	}
	if valid {
		*dst = append(*dst, rules...)
	}
	return diags
}
