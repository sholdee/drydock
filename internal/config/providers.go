package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sholdee/drydock/internal/diagnostic"
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

	diags := applyCMMap(&settings, doc.Configs.CM, path, "configs.cm")
	return settings, diags, nil
}

func LoadFromHelmValuesDocument(path string, documentIndex int) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc struct {
		Configs struct {
			CM map[string]string `yaml:"cm"`
		} `yaml:"configs"`
	}
	if err := decodeYAMLDocumentAt(path, documentIndex, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse helm values %s document %d: %w", path, documentIndex, err)
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

func LoadFromConfigMapDocument(path string, documentIndex int) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := decodeYAMLDocumentAt(path, documentIndex, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse configmap %s document %d: %w", path, documentIndex, err)
	}
	if doc.Kind != "ConfigMap" || doc.Metadata.Name != "argocd-cm" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file document is not argocd-cm ConfigMap",
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

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	candidates := make([]ArgoSettings, 0)
	diags := make([]diagnostic.Diagnostic, 0)
	foundRepositorySecret := false
	for {
		var doc repositorySecretDocument
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return settings, nil, fmt.Errorf("parse repository secret %s: %w", path, err)
		}
		if doc.Kind != "Secret" || doc.Metadata.Labels["argocd.argoproj.io/secret-type"] != "repository" {
			continue
		}
		foundRepositorySecret = true
		candidate, nextDiags := repositorySecretSettings(path, doc)
		candidates = append(candidates, candidate)
		diags = append(diags, nextDiags...)
	}
	if !foundRepositorySecret {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file is not an Argo CD repository Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	settings, mergeDiags := MergeDiscovered(candidates)
	diags = append(diags, mergeDiags...)
	return settings, diags, nil
}

func LoadRepositorySecretDocument(path string, documentIndex int) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc repositorySecretDocument
	if err := decodeYAMLDocumentAt(path, documentIndex, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse repository secret %s document %d: %w", path, documentIndex, err)
	}
	if doc.Kind != "Secret" || doc.Metadata.Labels["argocd.argoproj.io/secret-type"] != "repository" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file document is not an Argo CD repository Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}
	settings, diags := repositorySecretSettings(path, doc)
	return settings, diags, nil
}

func decodeYAMLDocumentAt(path string, documentIndex int, out any) error {
	if documentIndex < 0 {
		return fmt.Errorf("document index must be greater than or equal to 0")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	for index := 0; ; index++ {
		var raw any
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("document %d not found", documentIndex)
		}
		if err != nil {
			return err
		}
		if index != documentIndex {
			continue
		}
		if raw == nil {
			return fmt.Errorf("document %d is empty", documentIndex)
		}
		data, err := yaml.Marshal(raw)
		if err != nil {
			return err
		}
		return yaml.Unmarshal(data, out)
	}
}

type repositorySecretDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	StringData map[string]string `yaml:"stringData"`
	Data       map[string]string `yaml:"data"`
}

func repositorySecretSettings(path string, doc repositorySecretDocument) (ArgoSettings, []diagnostic.Diagnostic) {
	settings := DefaultSettings()
	url := secretStringField(doc.StringData, doc.Data, "url")
	if url == "" {
		return settings, nil
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
	return settings, diags
}
