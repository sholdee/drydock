package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestLoadHelmValuesSettings(t *testing.T) {
	settings, diags, err := LoadFromHelmValues(filepath.Join("..", "..", "testdata", "settings", "argocd-values.yaml"))
	if err != nil {
		t.Fatalf("LoadFromHelmValues() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := settings.KustomizeBuildOptions; len(got) != 3 || got[0].Value != "--enable-helm" {
		t.Fatalf("KustomizeBuildOptions = %#v", got)
	}
	if settings.InstanceLabelKey.Value != "argocd.argoproj.io/instance" {
		t.Fatalf("InstanceLabelKey = %#v", settings.InstanceLabelKey)
	}
	if settings.InstanceLabelKey.Provenance.Path == "" {
		t.Fatalf("expected provenance")
	}
}

func TestLoadConfigMapSettings(t *testing.T) {
	settings, diags, err := LoadFromConfigMap(filepath.Join("..", "..", "testdata", "settings", "argocd-cm.yaml"))
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := settings.KustomizeBuildOptions; len(got) != 1 || got[0].Value != "--enable-helm" {
		t.Fatalf("KustomizeBuildOptions = %#v", got)
	}
	if settings.InstanceLabelKey.Value != "app.kubernetes.io/instance" {
		t.Fatalf("InstanceLabelKey = %#v", settings.InstanceLabelKey)
	}
}

func TestLoadConfigMapResourceFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.exclusions: |
    - apiGroups: ["events.k8s.io"]
    - apiGroups: [""]
      kinds: ["Event"]
  resource.inclusions: |
    - apiGroups: ["apps"]
      kinds: ["Deployment"]
      clusters: ["prod-*"]
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(settings.ResourceExclusions) != 2 {
		t.Fatalf("ResourceExclusions = %#v", settings.ResourceExclusions)
	}
	if len(settings.ResourceInclusions) != 1 {
		t.Fatalf("ResourceInclusions = %#v", settings.ResourceInclusions)
	}
	if settings.ResourceInclusions[0].Clusters[0] != "prod-*" {
		t.Fatalf("ResourceInclusions = %#v", settings.ResourceInclusions)
	}
	if settings.ResourceInclusions[0].Provenance.Path != path {
		t.Fatalf("ResourceInclusions provenance = %#v", settings.ResourceInclusions[0].Provenance)
	}
}

func TestLoadHelmValuesResourceSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cm:
    resource.exclusions: |
      - apiGroups: ["coordination.k8s.io"]
        kinds: ["Lease"]
    resource.customizations.ignoreDifferences.apps_Deployment: |
      jsonPointers:
        - /spec/replicas
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromHelmValues(path)
	if err != nil {
		t.Fatalf("LoadFromHelmValues() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(settings.ResourceExclusions) != 1 {
		t.Fatalf("ResourceExclusions = %#v", settings.ResourceExclusions)
	}
	customization := settings.ResourceCustomizations["apps/Deployment"]
	if len(customization.IgnoreDifferences.JSONPointers) != 1 || customization.IgnoreDifferences.JSONPointers[0] != "/spec/replicas" {
		t.Fatalf("Deployment customization = %#v", customization)
	}
}

func TestLoadConfigMapResourceCompareOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.compareoptions: |
    ignoreResourceStatusField: crd
    ignoreAggregatedRoles: true
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if settings.CompareOptions.IgnoreResourceStatusField != "crd" {
		t.Fatalf("CompareOptions = %#v", settings.CompareOptions)
	}
	if !settings.CompareOptions.IgnoreAggregatedRoles {
		t.Fatalf("IgnoreAggregatedRoles = false, want true")
	}
	if settings.CompareOptions.Provenance.Path != path {
		t.Fatalf("CompareOptions provenance = %#v", settings.CompareOptions.Provenance)
	}
}

func TestLoadHelmValuesResourceCompareOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cm:
    resource.compareoptions: |
      ignoreResourceStatusField: none
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromHelmValues(path)
	if err != nil {
		t.Fatalf("LoadFromHelmValues() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if settings.CompareOptions.IgnoreResourceStatusField != "none" {
		t.Fatalf("CompareOptions = %#v", settings.CompareOptions)
	}
}

func TestLoadConfigMapCompareOptionsUnknownStatusWarns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.compareoptions: |
    ignoreResourceStatusField: typo
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one warning", diags)
	}
	if diags[0].Severity != diagnostic.SeverityWarning || diags[0].Category != "settings" {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
	if settings.CompareOptions.IgnoreResourceStatusField != "typo" {
		t.Fatalf("CompareOptions = %#v", settings.CompareOptions)
	}
}

func TestLoadConfigMapCompareOptionsFalseAndOffAreKnown(t *testing.T) {
	for _, value := range []string{"false", "off"} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
			if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.compareoptions: |
    ignoreResourceStatusField: `+value+`
`), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			settings, diags, err := LoadFromConfigMap(path)
			if err != nil {
				t.Fatalf("LoadFromConfigMap() error = %v", err)
			}
			if len(diags) != 0 {
				t.Fatalf("diagnostics = %#v", diags)
			}
			if settings.CompareOptions.IgnoreResourceStatusField != value {
				t.Fatalf("CompareOptions = %#v", settings.CompareOptions)
			}
		})
	}
}

func TestLoadConfigMapResourceCustomizations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      ignoreDifferences: |
        jsonPointers:
          - /spec/replicas
        jqPathExpressions:
          - .spec.template.metadata.annotations
    "*/*":
      ignoreDifferences: |
        jsonPointers:
          - /metadata/annotations/generated
  resource.customizations.ignoreDifferences.admissionregistration.k8s.io_MutatingWebhookConfiguration: |
    jsonPointers:
      - /webhooks/0/clientConfig/caBundle
    managedFieldsManagers:
      - kube-controller-manager
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(settings.ResourceCustomizations) != 3 {
		t.Fatalf("ResourceCustomizations = %#v", settings.ResourceCustomizations)
	}
	deployment := settings.ResourceCustomizations["apps/Deployment"]
	if len(deployment.IgnoreDifferences.JSONPointers) != 1 || deployment.IgnoreDifferences.JSONPointers[0] != "/spec/replicas" {
		t.Fatalf("Deployment customization = %#v", deployment)
	}
	wildcard := settings.ResourceCustomizations["*/*"]
	if len(wildcard.IgnoreDifferences.JSONPointers) != 1 || wildcard.IgnoreDifferences.JSONPointers[0] != "/metadata/annotations/generated" {
		t.Fatalf("Wildcard customization = %#v", wildcard)
	}
	webhook := settings.ResourceCustomizations["admissionregistration.k8s.io/MutatingWebhookConfiguration"]
	if len(webhook.IgnoreDifferences.JSONPointers) != 1 || webhook.IgnoreDifferences.JSONPointers[0] != "/webhooks/0/clientConfig/caBundle" {
		t.Fatalf("Webhook customization = %#v", webhook)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestLoadConfigMapAdvancedResourceCustomizations(t *testing.T) {
	settings, diags := loadAdvancedResourceCustomizations(t)
	if len(diags) != 8 {
		t.Fatalf("len(diags) = %d, want 8 warnings: %#v", len(diags), diags)
	}
	if settings.IgnoreResourceUpdatesEnabled.Value {
		t.Fatalf("IgnoreResourceUpdatesEnabled = true, want false")
	}
}

func TestLoadConfigMapAdvancedResourceCustomizationsBulkSections(t *testing.T) {
	settings, _ := loadAdvancedResourceCustomizations(t)
	assertDeploymentAdvancedCustomization(t, settings.ResourceCustomizations["apps/Deployment"])
}

func TestLoadConfigMapAdvancedResourceCustomizationsSplitSections(t *testing.T) {
	settings, _ := loadAdvancedResourceCustomizations(t)
	assertSplitAdvancedResourceCustomizations(t, settings)
}

func TestLoadConfigMapHealthLuaReportsHashWithoutBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      health.lua: |
        return { status = "Healthy", message = "SUPER_SECRET_HEALTH_TOKEN" }
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if !hasDiagnosticCode(diags, "settings.metadata-only") {
		t.Fatalf("diagnostics = %#v, want settings.metadata-only", diags)
	}
	customization := settings.ResourceCustomizations["apps/Deployment"]
	if !customization.HasHealthLua {
		t.Fatalf("HasHealthLua = false, want true")
	}
	const wantHash = "5891509de2d4c98e33ce3c17387504bc74033b0bfc02f2a307ccf58a8e826a9b"
	if customization.HealthLuaSHA256 != wantHash {
		t.Fatalf("HealthLuaSHA256 = %q, want %q", customization.HealthLuaSHA256, wantHash)
	}
	assertJSONDoesNotContain(t, settings, "SUPER_SECRET_HEALTH_TOKEN")
}

func TestLoadConfigMapActionsReportNamesAndHashesWithoutBodies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.actions.apps_Deployment: |
    discovery.lua: |
      return { { name = "restart" } }
    definitions:
      - name: restart
        action.lua: |
          return obj
      - name: restart
        action.lua: |
          obj.metadata.annotations = { token = "SUPER_SECRET_ACTION_TOKEN" }
          return obj
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if !hasDiagnosticCode(diags, "settings.metadata-only") {
		t.Fatalf("diagnostics = %#v, want settings.metadata-only", diags)
	}
	actions := settings.ResourceCustomizations["apps/Deployment"].Actions
	if !actions.HasActions || !actions.HasDiscoveryLua {
		t.Fatalf("actions metadata = %#v, want actions and discovery Lua", actions)
	}
	const wantDiscoveryHash = "0596745fe0c0878b1a95592a3bbdcb73f103017dc971de03e911b4074303afbd"
	if actions.DiscoveryLuaSHA256 != wantDiscoveryHash {
		t.Fatalf("DiscoveryLuaSHA256 = %q, want %q", actions.DiscoveryLuaSHA256, wantDiscoveryHash)
	}
	wantActionHashes := []ResourceActionLuaHash{
		{Name: "restart", Index: 0, SHA256: "70c6e5307755641aca90a429eeaaff5903ab4cfe1ca79866be48c56ea62cc721"},
		{Name: "restart", Index: 1, SHA256: "e5b0bdd6e3d65ea212b780b9c00247603f5da7a2b90cb024311732875322b51e"},
	}
	if len(actions.ActionLuaSHA256) != len(wantActionHashes) {
		t.Fatalf("ActionLuaSHA256 = %#v, want %#v", actions.ActionLuaSHA256, wantActionHashes)
	}
	for i, want := range wantActionHashes {
		if actions.ActionLuaSHA256[i] != want {
			t.Fatalf("ActionLuaSHA256[%d] = %#v, want %#v", i, actions.ActionLuaSHA256[i], want)
		}
	}
	assertJSONDoesNotContain(t, settings, "SUPER_SECRET_ACTION_TOKEN")
}

func TestLoadConfigMapIgnoreResourceUpdatesMetadataOnlyDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.ignoreResourceUpdates.apps_Deployment: |
    jsonPointers:
      - /status
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if !hasDiagnosticCode(diags, "settings.metadata-only") {
		t.Fatalf("diagnostics = %#v, want settings.metadata-only", diags)
	}
	customization := settings.ResourceCustomizations["apps/Deployment"]
	if len(customization.IgnoreResourceUpdates.JSONPointers) != 1 || customization.IgnoreResourceUpdates.JSONPointers[0] != "/status" {
		t.Fatalf("IgnoreResourceUpdates = %#v, want /status", customization.IgnoreResourceUpdates)
	}
}

func loadAdvancedResourceCustomizations(t *testing.T) (ArgoSettings, []diagnostic.Diagnostic) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.ignoreResourceUpdatesEnabled: "false"
  resource.customizations: |
    apps/Deployment:
      ignoreResourceUpdates: |
        jsonPointers:
          - /status
      knownTypeFields:
        - field: spec.template.spec
          type: core/v1/PodSpec
      health.lua: |
        return { status = "Healthy" }
      health.lua.useOpenLibs: true
      actions: |
        discovery.lua: |
          return {}
        definitions:
          - name: restart
            action.lua: |
              return obj
        mergeBuiltinActions: true
  resource.customizations.ignoreResourceUpdates.batch_Job: |
    jqPathExpressions:
      - .status
  resource.customizations.knownTypeFields.argoproj.io_Rollout: |
    - field: spec.template.spec
      type: core/v1/PodSpec
  resource.customizations.health.cert-manager.io_Certificate: |
    return { status = "Progressing" }
  resource.customizations.useOpenLibs.cert-manager.io_Certificate: "true"
  resource.customizations.actions.ConfigMap: |
    definitions:
      - name: inspect
        action.lua: |
          return obj
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	return settings, diags
}

func assertDeploymentAdvancedCustomization(t *testing.T, deployment ResourceCustomization) {
	t.Helper()
	if len(deployment.IgnoreResourceUpdates.JSONPointers) != 1 || deployment.IgnoreResourceUpdates.JSONPointers[0] != "/status" {
		t.Fatalf("deployment IgnoreResourceUpdates = %#v", deployment.IgnoreResourceUpdates)
	}
	if len(deployment.KnownTypeFields) != 1 || deployment.KnownTypeFields[0].Field != "spec.template.spec" || deployment.KnownTypeFields[0].Type != "core/v1/PodSpec" {
		t.Fatalf("deployment KnownTypeFields = %#v", deployment.KnownTypeFields)
	}
	if !deployment.HasHealthLua || !deployment.HasUseOpenLibs || !deployment.UseOpenLibs {
		t.Fatalf("deployment health metadata = %#v", deployment)
	}
	if !deployment.Actions.HasActions || !deployment.Actions.MergeBuiltinActions || !containsString(deployment.Actions.ActionNames, "restart") {
		t.Fatalf("deployment actions metadata = %#v", deployment.Actions)
	}
}

func assertSplitAdvancedResourceCustomizations(t *testing.T, settings ArgoSettings) {
	t.Helper()
	job := settings.ResourceCustomizations["batch/Job"]
	if len(job.IgnoreResourceUpdates.JQPathExpressions) != 1 || job.IgnoreResourceUpdates.JQPathExpressions[0] != ".status" {
		t.Fatalf("job IgnoreResourceUpdates = %#v", job.IgnoreResourceUpdates)
	}
	rollout := settings.ResourceCustomizations["argoproj.io/Rollout"]
	if len(rollout.KnownTypeFields) != 1 || rollout.KnownTypeFields[0].Field != "spec.template.spec" {
		t.Fatalf("rollout KnownTypeFields = %#v", rollout.KnownTypeFields)
	}
	cert := settings.ResourceCustomizations["cert-manager.io/Certificate"]
	if !cert.HasHealthLua || !cert.HasUseOpenLibs || !cert.UseOpenLibs {
		t.Fatalf("cert health metadata = %#v", cert)
	}
	configMap := settings.ResourceCustomizations["ConfigMap"]
	if !configMap.Actions.HasActions || !containsString(configMap.Actions.ActionNames, "inspect") {
		t.Fatalf("configMap actions metadata = %#v", configMap.Actions)
	}
}

func TestLoadConfigMapUseOpenLibsFalseIsPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      health.lua.useOpenLibs: false
  resource.customizations.useOpenLibs.ConfigMap: "false"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("len(diags) = %d, want 2 useOpenLibs warnings: %#v", len(diags), diags)
	}
	deployment := settings.ResourceCustomizations["apps/Deployment"]
	if !deployment.HasUseOpenLibs || deployment.UseOpenLibs {
		t.Fatalf("deployment useOpenLibs = present %v value %v, want present false", deployment.HasUseOpenLibs, deployment.UseOpenLibs)
	}
	configMap := settings.ResourceCustomizations["ConfigMap"]
	if !configMap.HasUseOpenLibs || configMap.UseOpenLibs {
		t.Fatalf("configMap useOpenLibs = present %v value %v, want present false", configMap.HasUseOpenLibs, configMap.UseOpenLibs)
	}
}

func TestLoadConfigMapConflictingHealthLuaBodyFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      health.lua: |
        return { status = "Healthy" }
  resource.customizations.health.apps_Deployment: |
    return { status = "Progressing" }
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if !hasDiagnosticMessage(diags, "conflicting resource customization settings discovered") {
		t.Fatalf("diagnostics = %#v, want conflicting health Lua diagnostic", diags)
	}
}

func TestLoadConfigMapConflictingActionLuaBodyFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      actions: |
        definitions:
          - name: restart
            action.lua: |
              return obj
  resource.customizations.actions.apps_Deployment: |
    definitions:
      - name: restart
        action.lua: |
          obj.metadata.name = "changed"
          return obj
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if !hasDiagnosticMessage(diags, "conflicting resource customization settings discovered") {
		t.Fatalf("diagnostics = %#v, want conflicting actions diagnostic", diags)
	}
}

func TestLoadConfigMapInvalidIgnoreResourceUpdatesNamesSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.ignoreResourceUpdates.apps_Deployment: |
    jsonPointers: [
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if !hasDiagnosticMessage(diags, "invalid resource customization ignoreResourceUpdates settings") {
		t.Fatalf("diagnostics = %#v, want ignoreResourceUpdates parse diagnostic", diags)
	}
}

func TestLoadConfigMapInvalidResourceCustomizationSkipsSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.ignoreDifferences.apps_Deployment: |
    jsonPointers: [
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 1 || diags[0].Severity != "error" {
		t.Fatalf("diagnostics = %#v, want one error", diags)
	}
	if len(settings.ResourceCustomizations) != 0 {
		t.Fatalf("ResourceCustomizations = %#v, want none", settings.ResourceCustomizations)
	}
}

func TestLoadConfigMapInvalidSplitResourceCustomizationKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.ignoreDifferences.apps_Deployment_extra: |
    jsonPointers:
      - /spec/replicas
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 1 || diags[0].Severity != "error" {
		t.Fatalf("diagnostics = %#v, want one error", diags)
	}
	if len(settings.ResourceCustomizations) != 0 {
		t.Fatalf("ResourceCustomizations = %#v, want none", settings.ResourceCustomizations)
	}
}

func TestLoadRepositorySecretIgnoresCredentials(t *testing.T) {
	settings, diags, err := LoadRepositorySecret(filepath.Join("..", "..", "testdata", "settings", "repo-secret.yaml"))
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	repo := settings.HelmRepositories["ghcr.io/example/charts"]
	if !repo.EnableOCI {
		t.Fatalf("EnableOCI = false, want true")
	}
	if repo.Name != "charts" || repo.Type != "helm" {
		t.Fatalf("repo = %#v", repo)
	}
}

func TestRepositorySecretDoesNotRetainRawSensitiveData(t *testing.T) {
	settings, diags, err := LoadRepositorySecret(filepath.Join("..", "..", "testdata", "settings", "repo-secret.yaml"))
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	repo := settings.HelmRepositories["ghcr.io/example/charts"]
	if repo.URL != "ghcr.io/example/charts" || repo.Project != "" {
		t.Fatalf("repo metadata = %#v", repo)
	}
	if repo.Provenance.Path == "" {
		t.Fatalf("expected provenance")
	}
	serialized, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(serialized), "should-not-be-read") {
		t.Fatalf("sensitive Secret data was retained: %s", serialized)
	}
}

func TestLoadRepositorySecretDataParsesEnableOCI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo-secret.yaml")
	encoded := func(value string) string {
		return base64.StdEncoding.EncodeToString([]byte(value))
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: charts
  labels:
    argocd.argoproj.io/secret-type: repository
data:
  name: %s
  type: %s
  url: %s
  enableOCI: %s
`, encoded("charts"), encoded("helm"), encoded("ghcr.io/example/charts"), encoded("true"))), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadRepositorySecret(path)
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	repo := settings.HelmRepositories["ghcr.io/example/charts"]
	if !repo.EnableOCI || repo.Name != "charts" || repo.Type != "helm" {
		t.Fatalf("repo = %#v", repo)
	}
	if repo.Provenance.Pointer != "data.url" {
		t.Fatalf("provenance = %#v", repo.Provenance)
	}
}

func TestLoadRepositorySecretInvalidEnableOCIReturnsSafeDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo-secret.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Secret
metadata:
  name: charts
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: charts
  type: helm
  url: ghcr.io/example/charts
  enableOCI: typo-secret-value
  username: should-not-leak
  password: should-not-leak
  bearerToken: should-not-leak
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadRepositorySecret(path)
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Severity != "error" || diags[0].Category != "settings" {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
	if diags[0].Provenance.Path != path || diags[0].Provenance.Pointer != "stringData.enableOCI" {
		t.Fatalf("provenance = %#v", diags[0].Provenance)
	}

	serialized, err := json.Marshal(struct {
		Settings    ArgoSettings `json:"settings"`
		Diagnostics any          `json:"diagnostics"`
	}{Settings: settings, Diagnostics: diags})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, sensitive := range []string{"typo-secret-value", "should-not-leak"} {
		if strings.Contains(string(serialized), sensitive) {
			t.Fatalf("sensitive Secret data was retained or leaked: %s", serialized)
		}
	}
}

func TestMergeSettingsDetectsConflict(t *testing.T) {
	left := DefaultSettings()
	left.KustomizeBuildOptions = []Value[string]{{Value: "--enable-helm", Provenance: Provenance{Path: "a.yaml"}}}
	right := DefaultSettings()
	right.KustomizeBuildOptions = []Value[string]{{Value: "--load-restrictor=LoadRestrictionsNone", Provenance: Provenance{Path: "b.yaml"}}}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1", len(diags))
	}
	if diags[0].Severity != "error" {
		t.Fatalf("severity = %s, want error", diags[0].Severity)
	}
}

func TestMergeResourceCustomizationsIgnoresProvenance(t *testing.T) {
	left := DefaultSettings()
	left.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		IgnoreDifferences: OverrideIgnoreDifferences{JSONPointers: []string{"/spec/replicas"}},
		Provenance:        Provenance{Path: "a.yaml"},
	}
	right := DefaultSettings()
	right.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		IgnoreDifferences: OverrideIgnoreDifferences{JSONPointers: []string{"/spec/replicas"}},
		Provenance:        Provenance{Path: "b.yaml"},
	}

	merged, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	customization := merged.ResourceCustomizations["apps/Deployment"]
	if customization.Provenance.Path != "a.yaml" {
		t.Fatalf("customization provenance = %#v", customization.Provenance)
	}
}

func TestMergeSettingsCombinesResourceCustomizationSections(t *testing.T) {
	left := DefaultSettings()
	left.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		IgnoreDifferences: OverrideIgnoreDifferences{JSONPointers: []string{"/spec/replicas"}},
		Provenance:        Provenance{Path: "left.yaml"},
	}
	right := DefaultSettings()
	right.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		IgnoreResourceUpdates: OverrideIgnoreDifferences{JQPathExpressions: []string{".status"}},
		KnownTypeFields:       []KnownTypeField{{Field: "spec.template.spec", Type: "core/v1/PodSpec"}},
		HasHealthLua:          true,
		healthLuaFingerprint:  "health-fingerprint",
		HasUseOpenLibs:        true,
		UseOpenLibs:           true,
		Actions: ResourceActionsSummary{
			HasActions:      true,
			HasDiscoveryLua: true,
			ActionNames:     []string{"restart"},
			fingerprint:     "actions-fingerprint",
		},
		Provenance: Provenance{Path: "right.yaml"},
	}

	merged, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 0 {
		t.Fatalf("Diagnostics = %#v", diags)
	}
	got := merged.ResourceCustomizations["apps/Deployment"]
	if len(got.IgnoreDifferences.JSONPointers) != 1 || len(got.KnownTypeFields) != 1 {
		t.Fatalf("merged customization = %#v", got)
	}
	if len(got.IgnoreResourceUpdates.JQPathExpressions) != 1 || !got.HasHealthLua || !got.HasUseOpenLibs || !got.UseOpenLibs || !got.Actions.HasActions {
		t.Fatalf("merged customization = %#v", got)
	}
}

func TestMergeSettingsDetectsAdvancedResourceCustomizationConflict(t *testing.T) {
	left := DefaultSettings()
	left.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		KnownTypeFields: []KnownTypeField{{Field: "spec.template.spec", Type: "core/v1/PodSpec"}},
		Provenance:      Provenance{Path: "left.yaml"},
	}
	right := DefaultSettings()
	right.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		KnownTypeFields: []KnownTypeField{{Field: "spec.template.spec", Type: "core/Quantity"}},
		Provenance:      Provenance{Path: "right.yaml"},
	}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Category != "settings" {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
}

func TestMergeSettingsDetectsUseOpenLibsConflict(t *testing.T) {
	left := DefaultSettings()
	left.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		HasUseOpenLibs: true,
		UseOpenLibs:    false,
		Provenance:     Provenance{Path: "left.yaml"},
	}
	right := DefaultSettings()
	right.ResourceCustomizations["apps/Deployment"] = ResourceCustomization{
		HasUseOpenLibs: true,
		UseOpenLibs:    true,
		Provenance:     Provenance{Path: "right.yaml"},
	}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Category != "settings" {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
}

func TestMergeSettingsIgnoreResourceUpdatesEnabledDefaultDoesNotConflict(t *testing.T) {
	left := DefaultSettings()
	right := DefaultSettings()
	right.IgnoreResourceUpdatesEnabled = Value[bool]{
		Value:      false,
		Provenance: Provenance{Path: "right.yaml"},
	}

	merged, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 0 {
		t.Fatalf("Diagnostics = %#v", diags)
	}
	if merged.IgnoreResourceUpdatesEnabled.Value {
		t.Fatalf("IgnoreResourceUpdatesEnabled = true, want false")
	}
}

func TestMergeSettingsDetectsIgnoreResourceUpdatesEnabledConflict(t *testing.T) {
	left := DefaultSettings()
	left.IgnoreResourceUpdatesEnabled = Value[bool]{
		Value:      false,
		Provenance: Provenance{Path: "left.yaml"},
	}
	right := DefaultSettings()
	right.IgnoreResourceUpdatesEnabled = Value[bool]{
		Value:      true,
		Provenance: Provenance{Path: "right.yaml"},
	}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Category != "settings" {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
}

func TestMergeCompareOptionsIgnoresProvenance(t *testing.T) {
	left := DefaultSettings()
	left.CompareOptions = ResourceCompareOptions{
		IgnoreResourceStatusField: "crd",
		IgnoreAggregatedRoles:     true,
		Provenance:                Provenance{Path: "a.yaml"},
	}
	right := DefaultSettings()
	right.CompareOptions = ResourceCompareOptions{
		IgnoreResourceStatusField: "crd",
		IgnoreAggregatedRoles:     true,
		Provenance:                Provenance{Path: "b.yaml"},
	}

	merged, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if merged.CompareOptions.Provenance.Path != "a.yaml" {
		t.Fatalf("CompareOptions provenance = %#v", merged.CompareOptions.Provenance)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasDiagnosticMessage(diags []diagnostic.Diagnostic, fragment string) bool {
	for _, diag := range diags {
		if strings.Contains(diag.Message, fragment) {
			return true
		}
	}
	return false
}

func hasDiagnosticCode(diags []diagnostic.Diagnostic, code string) bool {
	for _, diag := range diagnostic.WithStableCodes(diags) {
		if diag.Code == code {
			return true
		}
	}
	return false
}

func assertJSONDoesNotContain(t *testing.T, value any, forbidden string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), forbidden) {
		t.Fatalf("JSON output contains %q: %s", forbidden, data)
	}
}
