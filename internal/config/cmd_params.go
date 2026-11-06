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
	"github.com/sholdee/drydock/internal/manifestyaml"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const commandParametersConfigMapName = "argocd-cmd-params-cm"

// maxCombinedDirectoryManifestsSizeKey sets the repo-server flag
// --max-combined-directory-manifests-size (Argo CD v3.6.1
// cmd/argocd-repo-server/commands/argocd_repo_server.go:264).
const maxCombinedDirectoryManifestsSizeKey = "reposerver.max.combined.directory.manifests.size"

// allowOutOfBoundsSymlinksKey sets the repo-server flag --allow-oob-symlinks
// through the environment variable
// ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS (Argo CD v3.6.1
// cmd/argocd-repo-server/commands/argocd_repo_server.go:266 and
// manifests/base/repo-server/argocd-repo-server-deployment.yaml:214-219).
const allowOutOfBoundsSymlinksKey = "reposerver.allow.oob.symlinks"

// enableProgressiveSyncsKey sets the ApplicationSet controller flag
// --enable-progressive-syncs through the environment variable
// ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_PROGRESSIVE_SYNCS (Argo CD v3.6.1
// cmd/argocd-applicationset-controller/commands/applicationset_controller.go:327
// and manifests/install.yaml:35041-35044).
const enableProgressiveSyncsKey = "applicationsetcontroller.enable.progressive.syncs"

// ignoreNormalizerJQTimeoutKey sets the application controller flag
// --ignore-normalizer-jq-execution-timeout-seconds through the environment
// variable ARGOCD_IGNORE_NORMALIZER_JQ_TIMEOUT (Argo CD v3.6.1
// cmd/argocd-application-controller/commands/argocd_application_controller.go:300
// and manifests/install.yaml:36973-36976).
const ignoreNormalizerJQTimeoutKey = "controller.ignore.normalizer.jq.timeout"

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

	decoder := manifestyaml.NewDecoder(bytes.NewReader(data))
	candidates := make([]ArgoSettings, 0)
	diags := make([]diagnostic.Diagnostic, 0)
	found := false
	for {
		var doc commandParametersConfigMapDocument
		if err := decodeNextYAMLDocument(decoder, &doc); err != nil {
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
		if key == maxCombinedDirectoryManifestsSizeKey {
			// The value is kept as written: the repo-server parses it with
			// resource.ParseQuantity, which rejects spaces around it.
			settings.MaxCombinedDirectoryManifestsSize = Value[string]{
				Value:      values[key],
				Provenance: setting.Provenance,
			}
		}
		if key == allowOutOfBoundsSymlinksKey {
			// Kept as written: the repo-server reads it with
			// env.ParseBoolFromEnv, which compares the raw value.
			settings.AllowOutOfBoundsSymlinks = Value[string]{
				Value:      values[key],
				Provenance: setting.Provenance,
			}
		}
		if key == enableProgressiveSyncsKey {
			// Kept as written, for the same reason: the ApplicationSet
			// controller reads it with env.ParseBoolFromEnv.
			settings.EnableProgressiveSyncs = Value[string]{
				Value:      values[key],
				Provenance: setting.Provenance,
			}
		}
		if key == ignoreNormalizerJQTimeoutKey {
			// Kept as written: the controller reads it with
			// env.ParseDurationFromEnv, which parses the raw value.
			settings.IgnoreNormalizerJQTimeout = Value[string]{
				Value:      values[key],
				Provenance: setting.Provenance,
			}
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
	case maxCombinedDirectoryManifestsSizeKey, allowOutOfBoundsSymlinksKey, enableProgressiveSyncsKey:
		return commandParameterClassification{
			classification: CommandParameterRendered,
			valueMode:      commandParameterSanitizedValue,
		}
	case ignoreNormalizerJQTimeoutKey:
		return commandParameterClassification{
			classification: CommandParameterDiffed,
			valueMode:      commandParameterSanitizedValue,
		}
	case "controller.diff.server.side",
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
		"reposerver.tls.",
		"applicationsetcontroller.wq.",
		"commitserver.",
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
