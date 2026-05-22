package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"go.yaml.in/yaml/v4"
)

func LoadFromHelmValues(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	var doc struct {
		Configs struct {
			CM map[string]string `yaml:"cm"`
		} `yaml:"configs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse helm values %s: %w", path, err)
	}

	applyCMMap(&settings, doc.Configs.CM, path, "configs.cm")
	return settings, nil, nil
}

func LoadFromConfigMap(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse configmap %s: %w", path, err)
	}
	if doc.Kind != "ConfigMap" || doc.Metadata.Name != "argocd-cm" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file is not argocd-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	applyCMMap(&settings, doc.Data, path, "data")
	return settings, nil, nil
}

func LoadRepositorySecret(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
		StringData map[string]string `yaml:"stringData"`
		Data       map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse repository secret %s: %w", path, err)
	}
	if doc.Kind != "Secret" || doc.Metadata.Labels["argocd.argoproj.io/secret-type"] != "repository" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file is not an Argo CD repository Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	url := secretStringField(doc.StringData, doc.Data, "url")
	if url == "" {
		return settings, nil, nil
	}

	var diags []diagnostic.Diagnostic
	enableOCI, diag := secretBoolField(doc.StringData, doc.Data, "enableOCI", path)
	if diag != nil {
		diags = append(diags, *diag)
	}
	settings.HelmRepositories[url] = RepositorySettings{
		Name:       secretStringField(doc.StringData, doc.Data, "name"),
		Type:       secretStringField(doc.StringData, doc.Data, "type"),
		URL:        url,
		EnableOCI:  enableOCI,
		Project:    secretStringField(doc.StringData, doc.Data, "project"),
		Provenance: diagnostic.Provenance{Path: path, Pointer: secretFieldPointer(doc.StringData, "url")},
	}
	return settings, diags, nil
}

func applyCMMap(settings *ArgoSettings, values map[string]string, path, basePointer string) {
	if values == nil {
		return
	}
	if raw := values["kustomize.buildOptions"]; raw != "" {
		settings.KustomizeBuildOptions = splitShellFields(raw, diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + ".kustomize.buildOptions",
		})
	}
	if raw := values["application.resourceTrackingMethod"]; raw != "" {
		settings.TrackingMethod = Value[string]{
			Value: raw,
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + ".application.resourceTrackingMethod",
			},
		}
	}
	if raw := values["application.instanceLabelKey"]; raw != "" {
		settings.InstanceLabelKey = Value[string]{
			Value: raw,
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + ".application.instanceLabelKey",
			},
		}
	}
}

func splitShellFields(raw string, provenance diagnostic.Provenance) []Value[string] {
	fields := strings.Fields(raw)
	out := make([]Value[string], 0, len(fields))
	for _, field := range fields {
		out = append(out, Value[string]{Value: field, Provenance: provenance})
	}
	return out
}

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
