package config

import (
	"fmt"
	"sort"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// The argocd-cm keys that turn manifest generation for a source type on or
// off (sourceTypeToEnableGenerationKey, util/settings/settings.go:621-625 at
// Argo CD v3.6.1). jsonnet.enable belongs to the Directory source type: it
// decides whether a directory source evaluates its .jsonnet files.
const (
	HelmEnableKey      = "helm.enable"
	KustomizeEnableKey = "kustomize.enable"
	JsonnetEnableKey   = "jsonnet.enable"
)

// sourceTypeEnableKeys lists the keys in the order that drydock reports them.
var sourceTypeEnableKeys = []string{HelmEnableKey, KustomizeEnableKey, JsonnetEnableKey}

// SourceTypeEnabled reports whether Argo CD generates manifests with the
// tool that key names. GetEnabledSourceTypes (util/settings/settings.go:
// 1030-1047 at v3.6.1) enables every type and then reads each key that is
// present and not empty: only the exact string "true" keeps the type on.
func (s ArgoSettings) SourceTypeEnabled(key string) bool {
	return ParseSourceTypeEnabled(s.SourceTypeEnable[key].Value)
}

// ParseSourceTypeEnabled reads one value of a source type key as
// GetEnabledSourceTypes reads it.
func ParseSourceTypeEnabled(value string) bool {
	return value == "" || value == "true"
}

// DisabledSourceTypeKeys returns the source type keys that turn their tool
// off, sorted. It is empty for the defaults.
func (s ArgoSettings) DisabledSourceTypeKeys() []string {
	var out []string
	for _, key := range sourceTypeEnableKeys {
		if !s.SourceTypeEnabled(key) {
			out = append(out, key)
		}
	}
	return out
}

func setSourceTypeEnable(settings *ArgoSettings, key, raw, path, basePointer string) {
	if settings.SourceTypeEnable == nil {
		settings.SourceTypeEnable = map[string]Value[string]{}
	}
	settings.SourceTypeEnable[key] = Value[string]{
		Value: raw,
		Provenance: diagnostic.Provenance{
			Path:    path,
			Pointer: basePointer + "." + key,
		},
	}
}

func mergeSourceTypeEnable(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	keys := make([]string, 0, len(candidate.SourceTypeEnable))
	for key := range candidate.SourceTypeEnable {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var diags []diagnostic.Diagnostic
	for _, key := range keys {
		value := candidate.SourceTypeEnable[key]
		existing, ok := merged.SourceTypeEnable[key]
		// Spellings that Argo CD reads the same, such as "" and "true", do
		// not conflict.
		if ok && ParseSourceTypeEnabled(existing.Value) != ParseSourceTypeEnabled(value.Value) {
			diags = append(diags, conflictDiagnostic(
				fmt.Sprintf("conflicting %s values %q and %q", key, existing.Value, value.Value),
				value.Provenance,
			))
			continue
		}
		if merged.SourceTypeEnable == nil {
			merged.SourceTypeEnable = map[string]Value[string]{}
		}
		merged.SourceTypeEnable[key] = value
	}
	return diags
}
