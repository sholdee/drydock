package config

import (
	"github.com/sholdee/drydock/internal/diagnostic"
	"strconv"
)

func hasErrorDiagnostic(diags []diagnostic.Diagnostic) bool {
	for _, diag := range diags {
		if diag.Severity == diagnostic.SeverityError {
			return true
		}
	}
	return false
}
func parseSettingsBool(raw string, provenance diagnostic.Provenance, message string) (bool, *diagnostic.Diagnostic) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &diagnostic.Diagnostic{
			Severity:   diagnostic.SeverityError,
			Category:   "settings",
			Message:    message,
			Provenance: provenance,
		}
	}
	return value, nil
}
