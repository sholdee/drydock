package config

import (
	"fmt"
	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
	"os"
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

	diags := applyCMMap(&settings, doc.Configs.CM, path, "configs.cm")
	return settings, diags, nil
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

	diags := applyCMMap(&settings, doc.Data, path, "data")
	return settings, diags, nil
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
