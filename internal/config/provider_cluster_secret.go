package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func LoadClusterSecret(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	candidates := make([]ArgoSettings, 0)
	foundClusterSecret := false
	for {
		var doc clusterSecretDocument
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return settings, nil, fmt.Errorf("parse cluster secret %s: %w", path, err)
		}
		if !isClusterSecretDocument(doc) {
			continue
		}
		foundClusterSecret = true
		candidates = append(candidates, clusterSecretSettings(path, doc))
	}
	if !foundClusterSecret {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file is not an Argo CD cluster Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	merged, diags := MergeDiscovered(candidates)
	return merged, diags, nil
}

func LoadClusterSecretDocument(path string, documentIndex int) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc clusterSecretDocument
	if err := decodeYAMLDocumentAt(path, documentIndex, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse cluster secret %s document %d: %w", path, documentIndex, err)
	}
	if !isClusterSecretDocument(doc) {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file document is not an Argo CD cluster Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}
	return clusterSecretSettings(path, doc), nil, nil
}

func LoadClusterSecretObject(path string, obj *unstructured.Unstructured) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc clusterSecretDocument
	if err := decodeUnstructuredObject(obj, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse cluster secret %s: %w", path, err)
	}
	if !isClusterSecretDocument(doc) {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "object is not an Argo CD cluster Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}
	return clusterSecretSettings(path, doc), nil, nil
}

type clusterSecretDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	StringData map[string]string `yaml:"stringData"`
	Data       map[string]string `yaml:"data"`
}

func isClusterSecretDocument(doc clusterSecretDocument) bool {
	return doc.Kind == "Secret" && doc.Metadata.Labels["argocd.argoproj.io/secret-type"] == "cluster"
}

func clusterSecretSettings(path string, doc clusterSecretDocument) ArgoSettings {
	settings := DefaultSettings()
	server := normalizeClusterServer(secretStringField(doc.StringData, doc.Data, "server"))
	if server == "" {
		return settings
	}

	settings.Clusters[server] = ClusterSettings{
		Name:             strings.TrimSpace(secretStringField(doc.StringData, doc.Data, "name")),
		Server:           server,
		Namespaces:       parseClusterNamespaces(secretStringField(doc.StringData, doc.Data, "namespaces")),
		ClusterResources: parseClusterResources(secretStringField(doc.StringData, doc.Data, "clusterResources")),
		Project:          strings.TrimSpace(secretStringField(doc.StringData, doc.Data, "project")),
		Provenance:       diagnostic.Provenance{Path: path, Pointer: secretFieldPointer(doc.StringData, "server")},
	}
	return settings
}

func normalizeClusterServer(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func parseClusterNamespaces(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	namespaces := make([]string, 0, len(parts))
	for _, part := range parts {
		namespace := strings.TrimSpace(part)
		if namespace == "" {
			continue
		}
		namespaces = append(namespaces, namespace)
	}
	return namespaces
}

func parseClusterResources(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), "true")
}
