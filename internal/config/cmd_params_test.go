package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestLoadCmdParamsClassifiesExactBeforeWildcardAndGroupsDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
data:
  reposerver.plugin.use.manifest.generate.paths: "true"
  reposerver.plugin.env: SUPER_SECRET_PLUGIN_VALUE
  controller.diff.server.side: "true"
  applicationsetcontroller.policy: create-update
  redis.server: redis:6379
  unknown.runtime.key: SUPER_SECRET_UNKNOWN_VALUE
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one grouped warning", diags)
	}
	diag := diagnostic.WithStableCodes(diags)[0]
	if diag.Code != "settings.metadata-only" || diag.Severity != diagnostic.SeverityWarning || diag.Category != "settings" {
		t.Fatalf("diagnostic = %#v, want metadata-only settings warning", diag)
	}
	if strings.Count(diag.Message, "reposerver.plugin.use.manifest.generate.paths") != 1 {
		t.Fatalf("diagnostic message = %q, want exact key classified once", diag.Message)
	}
	assertContainsAll(t, diag.Message, []string{
		"controller.diff.server.side",
		"reposerver.plugin.env",
		"applicationsetcontroller.policy",
	})
	assertContainsNone(t, diag.Message, []string{"redis.server", "unknown.runtime.key", "SUPER_SECRET"})

	assertCommandParameter(t, settings, "reposerver.plugin.use.manifest.generate.paths", "true", false, CommandParameterRuntimeOnly)
	assertCommandParameter(t, settings, "reposerver.plugin.env", "", true, CommandParameterRuntimeOnly)
	assertCommandParameter(t, settings, "redis.server", "", false, CommandParameterRuntimeWiring)
	assertCommandParameter(t, settings, "unknown.runtime.key", "", false, CommandParameterUnknown)
	assertJSONDoesNotContain(t, settings, "SUPER_SECRET")
}

func TestLoadCmdParamsSanitizesExactAllowedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
data:
  applicationsetcontroller.policy: "sync SUPER_SECRET_TOKEN"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one metadata-only warning", diags)
	}
	setting := requireCommandParameter(t, settings, "applicationsetcontroller.policy")
	if setting.Value != "[redacted]" || !setting.ValueRedacted {
		t.Fatalf("setting = %#v, want sanitized redacted value marker", setting)
	}
	serialized, err := json.Marshal(struct {
		Settings    ArgoSettings            `json:"settings"`
		Diagnostics []diagnostic.Diagnostic `json:"diagnostics"`
	}{Settings: settings, Diagnostics: diags})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(serialized), "SUPER_SECRET_TOKEN") {
		t.Fatalf("serialized settings leaked sensitive cmd-param value: %s", serialized)
	}
}

func TestLoadCmdParamsIgnoredRuntimeWiringAndUnknownKeysStaySilent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
data:
  repo.server: argocd-repo-server:8081
  server.listen.address: 0.0.0.0
  controller.status.processors: "10"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want no warnings", diags)
	}
	if requireCommandParameter(t, settings, "repo.server").Classification != CommandParameterRuntimeWiring {
		t.Fatalf("repo.server classification = %#v", requireCommandParameter(t, settings, "repo.server"))
	}
	if requireCommandParameter(t, settings, "server.listen.address").Classification != CommandParameterRuntimeWiring {
		t.Fatalf("server.listen.address classification = %#v", requireCommandParameter(t, settings, "server.listen.address"))
	}
	if requireCommandParameter(t, settings, "controller.status.processors").Classification != CommandParameterUnknown {
		t.Fatalf("controller.status.processors classification = %#v", requireCommandParameter(t, settings, "controller.status.processors"))
	}
}

func TestLoadCmdParamsDocumentLoadsLaterDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multi.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Namespace
metadata:
  name: ignored
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
data:
  reposerver.include.hidden.directories: "true"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadCommandParametersConfigMapDocument(path, 1)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMapDocument() error = %v", err)
	}
	if len(diags) != 1 || !hasDiagnosticCode(diags, "settings.metadata-only") {
		t.Fatalf("diagnostics = %#v, want one metadata-only warning", diags)
	}
	setting := requireCommandParameter(t, settings, "reposerver.include.hidden.directories")
	if setting.Value != "true" {
		t.Fatalf("setting = %#v, want sanitized value", setting)
	}
	if len(settings.KustomizeBuildOptions) != 0 || len(settings.ResourceCustomizations) != 0 {
		t.Fatalf("cmd-params mutated render settings: %#v", settings)
	}
}

func requireCommandParameter(t *testing.T, settings ArgoSettings, key string) CommandParameterSetting {
	t.Helper()
	for _, setting := range settings.CommandParameters {
		if setting.Key == key {
			return setting
		}
	}
	t.Fatalf("CommandParameters = %#v, missing %q", settings.CommandParameters, key)
	return CommandParameterSetting{}
}

func assertCommandParameter(t *testing.T, settings ArgoSettings, key, value string, redacted bool, classification CommandParameterClassification) {
	t.Helper()
	setting := requireCommandParameter(t, settings, key)
	if setting.Value != value || setting.ValueRedacted != redacted || setting.Classification != classification {
		t.Fatalf("command parameter %q = %#v, want value=%q redacted=%t classification=%s", key, setting, value, redacted, classification)
	}
}

func assertContainsAll(t *testing.T, text string, values []string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(text, value) {
			t.Fatalf("text = %q, want %q", text, value)
		}
	}
}

func assertContainsNone(t *testing.T, text string, values []string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(text, value) {
			t.Fatalf("text = %q, did not want %q", text, value)
		}
	}
}
