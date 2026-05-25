package config

import (
	"encoding/base64"
	"strconv"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func secretStringField(stringData, data map[string]string, key string) string {
	if value, ok := stringData[key]; ok {
		return value
	}
	encoded, ok := data[key]
	if !ok {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return string(decoded)
}
func secretBoolField(stringData, data map[string]string, key, path string) (bool, *diagnostic.Diagnostic) {
	if value, ok := stringData[key]; ok {
		return parseSecretBool(value, diagnostic.Provenance{Path: path, Pointer: "stringData." + key})
	}
	encoded, ok := data[key]
	if !ok {
		return false, nil
	}
	if encoded == "" {
		return false, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false, invalidSecretBoolDiagnostic(diagnostic.Provenance{Path: path, Pointer: "data." + key})
	}
	return parseSecretBool(string(decoded), diagnostic.Provenance{Path: path, Pointer: "data." + key})
}
func parseSecretBool(raw string, provenance diagnostic.Provenance) (bool, *diagnostic.Diagnostic) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, invalidSecretBoolDiagnostic(provenance)
	}
	return value, nil
}
func invalidSecretBoolDiagnostic(provenance diagnostic.Provenance) *diagnostic.Diagnostic {
	return &diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityError,
		Category:   "settings",
		Message:    "invalid repository Secret enableOCI value",
		Provenance: provenance,
	}
}
func secretFieldPointer(stringData map[string]string, key string) string {
	if _, ok := stringData[key]; ok {
		return "stringData." + key
	}
	return "data." + key
}
