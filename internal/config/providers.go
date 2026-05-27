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

func LoadFromHelmValues(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	var doc helmValuesSettingsDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse helm values %s: %w", path, err)
	}

	diags := applyCMMap(&settings, doc.Configs.CM, path, "configs.cm")
	cmpDiags, err := applyCMPPlugins(&settings, doc.Configs.CMP.Plugins, path, "configs.cmp.plugins")
	if err != nil {
		return settings, diags, err
	}
	diags = append(diags, cmpDiags...)
	return settings, diags, nil
}

func LoadFromHelmValuesDocument(path string, documentIndex int) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc helmValuesSettingsDocument
	if err := decodeYAMLDocumentAt(path, documentIndex, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse helm values %s document %d: %w", path, documentIndex, err)
	}
	diags := applyCMMap(&settings, doc.Configs.CM, path, "configs.cm")
	cmpDiags, err := applyCMPPlugins(&settings, doc.Configs.CMP.Plugins, path, "configs.cmp.plugins")
	if err != nil {
		return settings, diags, err
	}
	diags = append(diags, cmpDiags...)
	return settings, diags, nil
}

func LoadFromHelmValuesObject(path string, obj *unstructured.Unstructured) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc helmValuesSettingsDocument
	if err := decodeUnstructuredObject(obj, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse helm values %s: %w", path, err)
	}
	diags := applyCMMap(&settings, doc.Configs.CM, path, "configs.cm")
	cmpDiags, err := applyCMPPlugins(&settings, doc.Configs.CMP.Plugins, path, "configs.cmp.plugins")
	if err != nil {
		return settings, diags, err
	}
	diags = append(diags, cmpDiags...)
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

func LoadFromConfigMapObject(path string, obj *unstructured.Unstructured) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := decodeUnstructuredObject(obj, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse configmap %s: %w", path, err)
	}
	if doc.Kind != "ConfigMap" || doc.Metadata.Name != "argocd-cm" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "object is not argocd-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}
	diags := applyCMMap(&settings, doc.Data, path, "data")
	return settings, diags, nil
}

func LoadConfigManagementPluginConfigMapDocument(path string, documentIndex int) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := decodeYAMLDocumentAt(path, documentIndex, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse config management plugin ConfigMap %s document %d: %w", path, documentIndex, err)
	}
	if doc.Kind != "ConfigMap" || doc.Metadata.Name != "argocd-cmp-cm" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file document is not argocd-cmp-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	var diags []diagnostic.Diagnostic
	for key, value := range doc.Data {
		if !looksLikeConfigManagementPluginYAML(value) {
			continue
		}
		plugins, err := configManagementPluginsFromYAML([]byte(value), path, "data."+key)
		if err != nil {
			return settings, nil, fmt.Errorf("parse config management plugin %s document %d data %q: %w", path, documentIndex, key, err)
		}
		for _, plugin := range plugins {
			if diag := addConfigManagementPlugin(&settings, plugin); diag != nil {
				diags = append(diags, *diag)
			}
		}
	}
	return settings, diags, nil
}

func LoadConfigManagementPluginConfigMapObject(path string, obj *unstructured.Unstructured) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := decodeUnstructuredObject(obj, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse config management plugin ConfigMap %s: %w", path, err)
	}
	if doc.Kind != "ConfigMap" || doc.Metadata.Name != "argocd-cmp-cm" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "object is not argocd-cmp-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	var diags []diagnostic.Diagnostic
	for key, value := range doc.Data {
		if !looksLikeConfigManagementPluginYAML(value) {
			continue
		}
		plugins, err := configManagementPluginsFromYAML([]byte(value), path, "data."+key)
		if err != nil {
			return settings, nil, fmt.Errorf("parse config management plugin %s data %q: %w", path, key, err)
		}
		for _, plugin := range plugins {
			if diag := addConfigManagementPlugin(&settings, plugin); diag != nil {
				diags = append(diags, *diag)
			}
		}
	}
	return settings, diags, nil
}

func looksLikeConfigManagementPluginYAML(value string) bool {
	return strings.Contains(value, "ConfigManagementPlugin")
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

func LoadRepositorySecretObject(path string, obj *unstructured.Unstructured) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc repositorySecretDocument
	if err := decodeUnstructuredObject(obj, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse repository secret %s: %w", path, err)
	}
	if doc.Kind != "Secret" || doc.Metadata.Labels["argocd.argoproj.io/secret-type"] != "repository" {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "object is not an Argo CD repository Secret",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}
	settings, diags := repositorySecretSettings(path, doc)
	return settings, diags, nil
}

func decodeUnstructuredObject(obj *unstructured.Unstructured, out any) error {
	if obj == nil {
		return fmt.Errorf("object is nil")
	}
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
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

type helmValuesSettingsDocument struct {
	Configs struct {
		CM  map[string]string `yaml:"cm"`
		CMP struct {
			Plugins map[string]any `yaml:"plugins"`
		} `yaml:"cmp"`
	} `yaml:"configs"`
}

type cmpPluginSpec struct {
	Version  string     `yaml:"version"`
	Init     cmpCommand `yaml:"init"`
	Generate cmpCommand `yaml:"generate"`
}

type cmpCommand struct {
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`
}

func applyCMPPlugins(settings *ArgoSettings, plugins map[string]any, path, pointer string) ([]diagnostic.Diagnostic, error) {
	var diags []diagnostic.Diagnostic
	for key, value := range plugins {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		plugins, err := configManagementPluginsFromHelmValue(name, value, path, pointer+"."+name)
		if err != nil {
			return diags, err
		}
		for _, plugin := range plugins {
			if diag := addConfigManagementPlugin(settings, plugin); diag != nil {
				diags = append(diags, *diag)
			}
		}
	}
	return diags, nil
}

func configManagementPluginsFromHelmValue(name string, value any, path, pointer string) ([]ConfigManagementPlugin, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		if !looksLikeConfigManagementPluginYAML(typed) {
			return nil, nil
		}
		return configManagementPluginsFromYAML([]byte(typed), path, pointer)
	default:
		data, err := yaml.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("parse config management plugin %s: %w", pointer, err)
		}
		if looksLikeConfigManagementPluginYAML(string(data)) {
			return configManagementPluginsFromYAML(data, path, pointer)
		}
		var spec cmpPluginSpec
		if err := yaml.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("parse config management plugin %s: %w", pointer, err)
		}
		return []ConfigManagementPlugin{configManagementPluginFromSpec(name, spec, path, pointer)}, nil
	}
}

func addConfigManagementPlugin(settings *ArgoSettings, plugin ConfigManagementPlugin) *diagnostic.Diagnostic {
	name := plugin.EffectiveName()
	if name == "" {
		return nil
	}
	existing, ok := settings.ConfigManagementPlugins[name]
	if ok && existing.commandFingerprint != plugin.commandFingerprint {
		diag := conflictDiagnostic(
			fmt.Sprintf("conflicting config management plugin settings discovered for %q", name),
			plugin.Provenance,
		)
		return &diag
	}
	settings.ConfigManagementPlugins[name] = plugin
	return nil
}

func configManagementPluginsFromYAML(data []byte, path, pointer string) ([]ConfigManagementPlugin, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var plugins []ConfigManagementPlugin
	for index := 0; ; index++ {
		var doc configManagementPluginDocument
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if doc.Kind != "ConfigManagementPlugin" {
			continue
		}
		name := strings.TrimSpace(doc.Metadata.Name)
		if name == "" {
			continue
		}
		plugin := configManagementPluginFromSpec(name, doc.Spec, path, fmt.Sprintf("%s#%d", pointer, index))
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

type configManagementPluginDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec cmpPluginSpec `yaml:"spec"`
}

func configManagementPluginFromSpec(name string, spec cmpPluginSpec, path, pointer string) ConfigManagementPlugin {
	plugin := ConfigManagementPlugin{
		Name:            name,
		Version:         strings.TrimSpace(spec.Version),
		GenerateCommand: append([]string(nil), spec.Generate.Command...),
		GenerateArgs:    append([]string(nil), spec.Generate.Args...),
		HasInit:         len(spec.Init.Command) > 0 || len(spec.Init.Args) > 0,
		Provenance:      diagnostic.Provenance{Path: path, Pointer: pointer},
	}
	plugin.commandFingerprint = pluginCommandFingerprint(plugin)
	return plugin
}

func pluginCommandFingerprint(plugin ConfigManagementPlugin) string {
	return fmt.Sprintf("name=%s\x00version=%s\x00init=%t\x00command=%q\x00args=%q",
		plugin.Name,
		plugin.Version,
		plugin.HasInit,
		plugin.GenerateCommand,
		plugin.GenerateArgs,
	)
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
