package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const commandParametersConfigMapName = "argocd-cmd-params-cm"

type commandParameterValueMode int

const (
	commandParameterKeyOnly commandParameterValueMode = iota
	commandParameterSanitizedValue
	commandParameterRedactedValue
)

type commandParameterClassification struct {
	classification CommandParameterClassification
	valueMode      commandParameterValueMode
	warnRuntime    bool
}

func LoadCommandParametersConfigMap(path string) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	candidates := make([]ArgoSettings, 0)
	diags := make([]diagnostic.Diagnostic, 0)
	found := false
	for {
		var doc commandParametersConfigMapDocument
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return settings, nil, fmt.Errorf("parse command parameters ConfigMap %s: %w", path, err)
		}
		if !isCommandParametersConfigMap(doc) {
			continue
		}
		found = true
		candidate, nextDiags := commandParametersSettings(path, "data", doc.Data)
		candidates = append(candidates, candidate)
		diags = append(diags, nextDiags...)
	}
	if !found {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file is not argocd-cmd-params-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}

	settings, mergeDiags := MergeDiscovered(candidates)
	diags = append(diags, mergeDiags...)
	return settings, diags, nil
}

func LoadCommandParametersConfigMapDocument(path string, documentIndex int) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc commandParametersConfigMapDocument
	if err := decodeYAMLDocumentAt(path, documentIndex, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse command parameters ConfigMap %s document %d: %w", path, documentIndex, err)
	}
	if !isCommandParametersConfigMap(doc) {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "file document is not argocd-cmd-params-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}
	nextSettings, diags := commandParametersSettings(path, "data", doc.Data)
	return nextSettings, diags, nil
}

func LoadCommandParametersConfigMapObject(path string, obj *unstructured.Unstructured) (ArgoSettings, []diagnostic.Diagnostic, error) {
	settings := DefaultSettings()
	var doc commandParametersConfigMapDocument
	if err := decodeUnstructuredObject(obj, &doc); err != nil {
		return settings, nil, fmt.Errorf("parse command parameters ConfigMap %s: %w", path, err)
	}
	if !isCommandParametersConfigMap(doc) {
		return settings, []diagnostic.Diagnostic{{
			Severity:   diagnostic.SeverityWarning,
			Category:   "settings",
			Message:    "object is not argocd-cmd-params-cm ConfigMap",
			Provenance: diagnostic.Provenance{Path: path},
		}}, nil
	}
	nextSettings, diags := commandParametersSettings(path, "data", doc.Data)
	return nextSettings, diags, nil
}

type commandParametersConfigMapDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
}

func isCommandParametersConfigMap(doc commandParametersConfigMapDocument) bool {
	return doc.Kind == "ConfigMap" && doc.Metadata.Name == commandParametersConfigMapName
}

func commandParametersSettings(path, basePointer string, values map[string]string) (ArgoSettings, []diagnostic.Diagnostic) {
	settings := DefaultSettings()
	if len(values) == 0 {
		return settings, nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	runtimeOnlyKeys := make([]string, 0)
	for _, key := range keys {
		classification := classifyCommandParameterKey(key)
		setting := CommandParameterSetting{
			Key:            key,
			Classification: classification.classification,
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: basePointer + "." + key,
			},
		}
		switch classification.valueMode {
		case commandParameterKeyOnly:
		case commandParameterSanitizedValue:
			setting.Value, setting.ValueRedacted = sanitizeCommandParameterValue(values[key])
		case commandParameterRedactedValue:
			setting.ValueRedacted = true
		}
		if classification.warnRuntime {
			runtimeOnlyKeys = append(runtimeOnlyKeys, key)
		}
		settings.CommandParameters = append(settings.CommandParameters, setting)
	}

	if len(runtimeOnlyKeys) == 0 {
		return settings, nil
	}
	return settings, []diagnostic.Diagnostic{commandParametersMetadataOnlyDiagnostic(path, basePointer, runtimeOnlyKeys)}
}

func commandParametersMetadataOnlyDiagnostic(path, pointer string, keys []string) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Code:     "settings.metadata-only",
		Severity: diagnostic.SeverityWarning,
		Category: "settings",
		Message: "argocd-cmd-params-cm runtime-only keys parsed as metadata only: " +
			strings.Join(keys, ", ") +
			"; drydock does not emulate live repo-server, controller, or ApplicationSet controller runtime behavior for these settings",
		Provenance: diagnostic.Provenance{
			Path:    path,
			Pointer: pointer,
		},
	}
	return diag
}

func classifyCommandParameterKey(key string) commandParameterClassification {
	switch key {
	case "reposerver.plugin.use.manifest.generate.paths",
		"reposerver.include.hidden.directories",
		"applicationsetcontroller.enable.new.git.file.globbing",
		"applicationsetcontroller.policy",
		"applicationsetcontroller.enable.policy.override":
		return commandParameterClassification{
			classification: CommandParameterRuntimeOnly,
			valueMode:      commandParameterSanitizedValue,
			warnRuntime:    true,
		}
	case "controller.diff.server.side",
		"reposerver.allow.oob.symlinks",
		"reposerver.max.combined.directory.manifests.size",
		"reposerver.enable.git.submodule",
		"applicationsetcontroller.enable.scm.providers",
		"applicationsetcontroller.allowed.scm.providers",
		"applicationsetcontroller.enable.tokenref.strict.mode":
		return commandParameterClassification{
			classification: CommandParameterRuntimeOnly,
			valueMode:      commandParameterRedactedValue,
			warnRuntime:    true,
		}
	case "repo.server", "commit.server":
		return commandParameterClassification{
			classification: CommandParameterRuntimeWiring,
			valueMode:      commandParameterKeyOnly,
		}
	}

	switch {
	case hasCommandParameterPrefix(key,
		"hydrator.",
		"reposerver.plugin.",
		"reposerver.streamed.manifest.",
		"reposerver.oci.",
		"reposerver.git.",
		"applicationsetcontroller.global.preserved.",
	):
		return commandParameterClassification{
			classification: CommandParameterRuntimeOnly,
			valueMode:      commandParameterRedactedValue,
			warnRuntime:    true,
		}
	case hasCommandParameterPrefix(key,
		"redis.",
		"otlp.",
		"log.",
		"server.",
		"controller.log.",
		"controller.metrics.",
		"reposerver.log.",
		"reposerver.metrics.",
		"dexserver.",
		"notificationscontroller.",
	) || isOtherRuntimeWiringCommandParameter(key):
		return commandParameterClassification{
			classification: CommandParameterRuntimeWiring,
			valueMode:      commandParameterKeyOnly,
		}
	default:
		return commandParameterClassification{
			classification: CommandParameterUnknown,
			valueMode:      commandParameterKeyOnly,
		}
	}
}

func hasCommandParameterPrefix(key string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) && strings.TrimPrefix(key, prefix) != "" {
			return true
		}
	}
	return false
}

func isOtherRuntimeWiringCommandParameter(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "cache") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "listen") ||
		strings.HasSuffix(lower, ".address") ||
		strings.HasSuffix(lower, ".port")
}

func sanitizeCommandParameterValue(value string) (string, bool) {
	value = strings.Join(strings.Fields(value), " ")
	if commandParameterValueLooksSensitive(value) {
		return "[redacted]", true
	}
	const maxRunes = 256
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "...", false
	}
	return value, false
}

func commandParameterValueLooksSensitive(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") && strings.Contains(lower, "@") {
		return true
	}
	for _, marker := range []string{
		"secret",
		"token",
		"password",
		"passwd",
		"bearer",
		"credential",
		"client_secret",
		"api_key",
		"apikey",
		"private key",
		"-----begin",
		"ssh-rsa",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
