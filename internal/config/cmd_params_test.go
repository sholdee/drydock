package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	argoenv "github.com/argoproj/argo-cd/v3/util/env"
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

func TestLoadCmdParamsClassifiesArgoCD36Keys(t *testing.T) {
	// Keys Argo CD v3.6.1 added to argocd-cmd-params-cm (docs and
	// manifests/base configMapKeyRef diff against v3.5.4) are runtime wiring:
	// none of them changes what the repo-server renders.
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
data:
  applicationsetcontroller.wq.backoff.factor: "1.5"
  applicationsetcontroller.wq.basedelay.ns: "1000000"
  applicationsetcontroller.wq.bucket.qps: "50"
  applicationsetcontroller.wq.bucket.size: "500"
  applicationsetcontroller.wq.cooldown.ns: "10000000000"
  applicationsetcontroller.wq.maxdelay.ns: "1000000000000"
  commitserver.signing.key.path: /app/config/SUPER_SECRET_KEY_PATH
  commitserver.signing.key.passphrase.file: /app/config/SUPER_SECRET_PASSPHRASE
  controller.metrics.application.conditions: OrphanedResourceWarning
  controller.metrics.application.labels: team
  controller.metrics.cluster.labels: env
  otlp.sample.ratio: "0.1"
  redis.key.prefix: argocd
  redis.sentinel.hosts: sentinel:26379
  redis.sentinel.master: mymaster
  reposerver.tls.curvepreferences: X25519
  server.disable.swagger.ui: "true"
  server.tls.curvepreferences: X25519
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none for runtime wiring keys", diags)
	}
	if len(settings.CommandParameters) != 18 {
		t.Fatalf("CommandParameters = %#v, want 18 keys", settings.CommandParameters)
	}
	for _, setting := range settings.CommandParameters {
		assertCommandParameter(t, settings, setting.Key, "", false, CommandParameterRuntimeWiring)
	}
	assertJSONDoesNotContain(t, settings, "SUPER_SECRET")
}

func TestLoadCmdParamsRecordsProgressiveSyncsWithoutWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cmd-params-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmd-params-cm
data:
  applicationsetcontroller.enable.progressive.syncs: "true"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadCommandParametersConfigMap(path)
	if err != nil {
		t.Fatalf("LoadCommandParametersConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want no metadata-only warning (--strict would promote it)", diags)
	}
	assertCommandParameter(t, settings, "applicationsetcontroller.enable.progressive.syncs", "true", false, CommandParameterRendered)
	if got := settings.EnableProgressiveSyncs.Value; got != "true" {
		t.Fatalf("EnableProgressiveSyncs = %q, want true", got)
	}
	if settings.EnableProgressiveSyncs.Provenance.Pointer != "data.applicationsetcontroller.enable.progressive.syncs" {
		t.Fatalf("provenance = %#v", settings.EnableProgressiveSyncs.Provenance)
	}
}

// TestMergeEnableProgressiveSyncs pins that two settings sources conflict
// only when the ApplicationSet controller would read them differently.
func TestMergeEnableProgressiveSyncs(t *testing.T) {
	on, _ := commandParametersSettings("on.yaml", "data", map[string]string{enableProgressiveSyncsKey: "true"})
	merged, diags := MergeDiscovered([]ArgoSettings{on, on})
	if merged.EnableProgressiveSyncs.Value != "true" || len(diags) != 0 {
		t.Fatalf("MergeDiscovered() = %q, %#v", merged.EnableProgressiveSyncs.Value, diags)
	}
	off, _ := commandParametersSettings("off.yaml", "data", map[string]string{enableProgressiveSyncsKey: "false"})
	if _, conflict := MergeDiscovered([]ArgoSettings{on, off}); len(conflict) != 1 {
		t.Fatalf("MergeDiscovered() conflict diagnostics = %#v, want one", conflict)
	}
	for _, pair := range [][2]string{{"TRUE", "true"}, {"false", "1"}, {"", " true"}} {
		left, _ := commandParametersSettings("left.yaml", "data", map[string]string{enableProgressiveSyncsKey: pair[0]})
		right, _ := commandParametersSettings("right.yaml", "data", map[string]string{enableProgressiveSyncsKey: pair[1]})
		if _, diags := MergeDiscovered([]ArgoSettings{left, right}); len(diags) != 0 {
			t.Fatalf("MergeDiscovered(%q, %q) diagnostics = %#v, want none", pair[0], pair[1], diags)
		}
	}
}

// TestParseCommandParameterBoolsMatchArgoCD compares drydock's parsers with
// the Argo CD v3.6.1 env.ParseBoolFromEnv that the repo-server and the
// ApplicationSet controller call for these keys, on the same values.
func TestParseCommandParameterBoolsMatchArgoCD(t *testing.T) {
	const envVar = "DRYDOCK_TEST_PARSE_BOOL_FROM_ENV"
	for _, value := range []string{"", "true", "TRUE", "True", "false", "FALSE", "1", "0", "yes", " true", "true ", "t"} {
		t.Setenv(envVar, value)
		want := argoenv.ParseBoolFromEnv(envVar, false)
		if got := ParseEnableProgressiveSyncs(value); got != want {
			t.Errorf("ParseEnableProgressiveSyncs(%q) = %t, want %t", value, got, want)
		}
		if got := ParseAllowOutOfBoundsSymlinks(value); got != want {
			t.Errorf("ParseAllowOutOfBoundsSymlinks(%q) = %t, want %t", value, got, want)
		}
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

// TestCommandParametersReadMaxCombinedDirectoryManifestsSize pins that drydock
// applies reposerver.max.combined.directory.manifests.size instead of
// recording it as runtime-only metadata.
func TestCommandParametersReadMaxCombinedDirectoryManifestsSize(t *testing.T) {
	settings, diags := commandParametersSettings("argocd-cmd-params-cm.yaml", "data", map[string]string{
		"reposerver.max.combined.directory.manifests.size": "20M",
	})
	if got := settings.MaxCombinedDirectoryManifestsSize.Value; got != "20M" {
		t.Fatalf("MaxCombinedDirectoryManifestsSize = %q, want 20M", got)
	}
	// The value is kept as written. The repo-server parses it with
	// resource.ParseQuantity, which rejects spaces around it, so drydock must
	// see the spaces to fail the same way.
	spaced, _ := commandParametersSettings("argocd-cmd-params-cm.yaml", "data", map[string]string{
		"reposerver.max.combined.directory.manifests.size": " 20M ",
	})
	if got := spaced.MaxCombinedDirectoryManifestsSize.Value; got != " 20M " {
		t.Fatalf("MaxCombinedDirectoryManifestsSize = %q, want \" 20M \"", got)
	}
	if settings.MaxCombinedDirectoryManifestsSize.Provenance.Pointer != "data.reposerver.max.combined.directory.manifests.size" {
		t.Fatalf("provenance = %#v", settings.MaxCombinedDirectoryManifestsSize.Provenance)
	}
	if len(diags) != 0 {
		t.Fatalf("diags = %#v, want no runtime-only warning", diags)
	}
	if len(settings.CommandParameters) != 1 || settings.CommandParameters[0].Classification != CommandParameterRendered || settings.CommandParameters[0].Value != "20M" {
		t.Fatalf("CommandParameters = %#v, want one rendered setting with its value", settings.CommandParameters)
	}
}

// TestCommandParametersReadAllowOutOfBoundsSymlinks pins that drydock
// applies reposerver.allow.oob.symlinks instead of recording it as
// runtime-only metadata. The value is kept as written, because the
// repo-server compares the raw value with env.ParseBoolFromEnv.
func TestCommandParametersReadAllowOutOfBoundsSymlinks(t *testing.T) {
	settings, diags := commandParametersSettings("argocd-cmd-params-cm.yaml", "data", map[string]string{
		"reposerver.allow.oob.symlinks": "TRUE",
	})
	if got := settings.AllowOutOfBoundsSymlinks.Value; got != "TRUE" {
		t.Fatalf("AllowOutOfBoundsSymlinks = %q, want TRUE", got)
	}
	if settings.AllowOutOfBoundsSymlinks.Provenance.Pointer != "data.reposerver.allow.oob.symlinks" {
		t.Fatalf("provenance = %#v", settings.AllowOutOfBoundsSymlinks.Provenance)
	}
	if len(diags) != 0 {
		t.Fatalf("diags = %#v, want no runtime-only warning", diags)
	}
	if len(settings.CommandParameters) != 1 || settings.CommandParameters[0].Classification != CommandParameterRendered || settings.CommandParameters[0].Value != "TRUE" {
		t.Fatalf("CommandParameters = %#v, want one rendered setting with its value", settings.CommandParameters)
	}
	merged, mergeDiags := MergeDiscovered([]ArgoSettings{settings, settings})
	if merged.AllowOutOfBoundsSymlinks.Value != "TRUE" || len(mergeDiags) != 0 {
		t.Fatalf("MergeDiscovered() = %q, %#v", merged.AllowOutOfBoundsSymlinks.Value, mergeDiags)
	}
	other, _ := commandParametersSettings("other.yaml", "data", map[string]string{"reposerver.allow.oob.symlinks": "false"})
	if _, conflict := MergeDiscovered([]ArgoSettings{settings, other}); len(conflict) != 1 {
		t.Fatalf("MergeDiscovered() conflict diagnostics = %#v, want one", conflict)
	}
	// Spellings that the repo-server reads the same do not conflict.
	for _, pair := range [][2]string{{"TRUE", "true"}, {"false", "no"}, {"", "False"}} {
		left, _ := commandParametersSettings("left.yaml", "data", map[string]string{"reposerver.allow.oob.symlinks": pair[0]})
		right, _ := commandParametersSettings("right.yaml", "data", map[string]string{"reposerver.allow.oob.symlinks": pair[1]})
		if _, diags := MergeDiscovered([]ArgoSettings{left, right}); len(diags) != 0 {
			t.Fatalf("MergeDiscovered(%q, %q) diagnostics = %#v, want none", pair[0], pair[1], diags)
		}
	}
}
