package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
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

func TestDefaultSettingsUseArgoTrackingDefaults(t *testing.T) {
	settings := DefaultSettings()
	if settings.TrackingMethod.Value != "annotation" {
		t.Fatalf("TrackingMethod = %#v, want annotation", settings.TrackingMethod)
	}
	if settings.InstanceLabelKey.Value != "app.kubernetes.io/instance" {
		t.Fatalf("InstanceLabelKey = %#v", settings.InstanceLabelKey)
	}
	if settings.InstallationID.Value != "" {
		t.Fatalf("InstallationID = %#v, want empty default", settings.InstallationID)
	}
}

func TestLoadHelmValuesConfigManagementPlugins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cmp:
    plugins:
      kustomize-build-with-helm:
        version: v1
        generate:
          command: [sh, -c]
          args: [kustomize build --enable-helm]
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
	plugin, ok := settings.ConfigManagementPlugins["kustomize-build-with-helm-v1"]
	if !ok {
		t.Fatalf("ConfigManagementPlugins = %#v, want versioned plugin key", settings.ConfigManagementPlugins)
	}
	if plugin.Name != "kustomize-build-with-helm" || plugin.Version != "v1" || plugin.EffectiveName() != "kustomize-build-with-helm-v1" {
		t.Fatalf("plugin = %#v", plugin)
	}
	if got := strings.Join(append(plugin.GenerateCommand, plugin.GenerateArgs...), " "); got != "sh -c kustomize build --enable-helm" {
		t.Fatalf("plugin command = %q", got)
	}
}

func TestLoadHelmValuesRawConfigManagementPluginYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cmp:
    plugins:
      kustomize-build-with-helm.yaml: |
        apiVersion: argoproj.io/v1alpha1
        kind: ConfigManagementPlugin
        metadata:
          name: kustomize-build-with-helm
        spec:
          version: v2
          generate:
            command: [kustomize, build]
            args: [--enable-helm]
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
	plugin, ok := settings.ConfigManagementPlugins["kustomize-build-with-helm-v2"]
	if !ok {
		t.Fatalf("ConfigManagementPlugins = %#v, want plugin from raw Helm values YAML", settings.ConfigManagementPlugins)
	}
	if got := strings.Join(append(plugin.GenerateCommand, plugin.GenerateArgs...), " "); got != "kustomize build --enable-helm" {
		t.Fatalf("plugin command = %q", got)
	}
}

func TestLoadConfigManagementPluginConfigMapDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmp.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cmp-cm
data:
  kustomize-build-with-helm.yaml: |
    apiVersion: argoproj.io/v1alpha1
    kind: ConfigManagementPlugin
    metadata:
      name: kustomize-build-with-helm
    spec:
      generate:
        command: [kustomize, build]
        args: [--enable-helm]
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadConfigManagementPluginConfigMapDocument(path, 0)
	if err != nil {
		t.Fatalf("LoadConfigManagementPluginConfigMapDocument() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	plugin, ok := settings.ConfigManagementPlugins["kustomize-build-with-helm"]
	if !ok {
		t.Fatalf("ConfigManagementPlugins = %#v, want rendered ConfigMap plugin", settings.ConfigManagementPlugins)
	}
	if got := strings.Join(append(plugin.GenerateCommand, plugin.GenerateArgs...), " "); got != "kustomize build --enable-helm" {
		t.Fatalf("plugin command = %q", got)
	}
}

func TestMergeDiscoveredConfigManagementPluginConflictsAreRedacted(t *testing.T) {
	leftPath := filepath.Join(t.TempDir(), "left.yaml")
	rightPath := filepath.Join(t.TempDir(), "right.yaml")
	left := DefaultSettings()
	left.ConfigManagementPlugins["cmp"] = configManagementPluginFromSpec("cmp", cmpPluginSpec{
		Generate: cmpCommand{Command: []string{"sh", "-c"}, Args: []string{"secret-token-one"}},
	}, leftPath, "configs.cmp.plugins.cmp")
	right := DefaultSettings()
	right.ConfigManagementPlugins["cmp"] = configManagementPluginFromSpec("cmp", cmpPluginSpec{
		Generate: cmpCommand{Command: []string{"sh", "-c"}, Args: []string{"secret-token-two"}},
	}, rightPath, "configs.cmp.plugins.cmp")

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one conflict", diags)
	}
	if strings.Contains(diags[0].Message, "secret-token") {
		t.Fatalf("diagnostic leaked command: %#v", diags[0])
	}
	if !strings.Contains(diags[0].Message, "cmp") {
		t.Fatalf("diagnostic = %#v, want plugin name", diags[0])
	}
}

func TestConfigManagementPluginsDoNotSerializeRawCommands(t *testing.T) {
	settings := DefaultSettings()
	settings.ConfigManagementPlugins["cmp"] = configManagementPluginFromSpec("cmp", cmpPluginSpec{
		Generate: cmpCommand{Command: []string{"sh", "-c"}, Args: []string{"kustomize build --enable-helm"}},
	}, "values.yaml", "configs.cmp.plugins.cmp")

	jsonData, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	yamlData, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	combined := string(jsonData) + string(yamlData)
	for _, forbidden := range []string{"ConfigManagementPlugins", "kustomize build", "--enable-helm"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("serialized settings leaked %q\njson:\n%s\nyaml:\n%s", forbidden, jsonData, yamlData)
		}
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

func TestLoadConfigMapInstallationID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  installationID: cluster-one
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
	if settings.InstallationID.Value != "cluster-one" {
		t.Fatalf("InstallationID = %#v", settings.InstallationID)
	}
	if got := settings.InstallationID.Provenance; got.Path != path || got.Pointer != "data.installationID" {
		t.Fatalf("provenance = %#v", got)
	}
}

func TestDefaultHelmValuesFileSchemes(t *testing.T) {
	settings := DefaultSettings()
	assertValueStrings(t, settings.HelmValuesFileSchemes, []string{"https", "http"})
	if settings.HelmValuesFileSchemesSet {
		t.Fatal("HelmValuesFileSchemesSet = true, want false for default settings")
	}
}

func TestLoadConfigMapHelmValuesFileSchemes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  helm.valuesFileSchemes: s3, git, https
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
	assertValueStrings(t, settings.HelmValuesFileSchemes, []string{"s3", "git", "https"})
	if !settings.HelmValuesFileSchemesSet {
		t.Fatal("HelmValuesFileSchemesSet = false, want true")
	}
	if got := settings.HelmValuesFileSchemes[0].Provenance; got.Path != path || got.Pointer != "data.helm.valuesFileSchemes" {
		t.Fatalf("provenance = %#v", got)
	}
}

func TestLoadConfigMapHelmValuesFileSchemesEmptyDisablesRemoteURLs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  helm.valuesFileSchemes: ""
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
	if len(settings.HelmValuesFileSchemes) != 0 {
		t.Fatalf("HelmValuesFileSchemes = %#v, want empty explicit setting", settings.HelmValuesFileSchemes)
	}
	if !settings.HelmValuesFileSchemesSet {
		t.Fatal("HelmValuesFileSchemesSet = false, want true")
	}
	if got := settings.HelmValuesFileSchemesSource; got.Path != path || got.Pointer != "data.helm.valuesFileSchemes" {
		t.Fatalf("HelmValuesFileSchemesSource = %#v", got)
	}
}

func TestLoadHelmValuesHelmValuesFileSchemes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cm:
    helm.valuesFileSchemes: s3, git
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
	assertValueStrings(t, settings.HelmValuesFileSchemes, []string{"s3", "git"})
	if got := settings.HelmValuesFileSchemes[0].Provenance; got.Path != path || got.Pointer != "configs.cm.helm.valuesFileSchemes" {
		t.Fatalf("provenance = %#v", got)
	}
}

func TestLoadHelmValuesInstallationID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cm:
    installationID: cluster-two
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
	if settings.InstallationID.Value != "cluster-two" {
		t.Fatalf("InstallationID = %#v", settings.InstallationID)
	}
	if got := settings.InstallationID.Provenance; got.Path != path || got.Pointer != "configs.cm.installationID" {
		t.Fatalf("provenance = %#v", got)
	}
}

func TestLoadConfigMapKustomizeVersionedBuildOptionsAndPathWarn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  kustomize.buildOptions: --enable-helm
  kustomize.buildOptions.v4.5.7: --enable-alpha-plugins
  kustomize.path.v4.5.7: /custom-tools/kustomize_4_5_7
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if got := settings.KustomizeBuildOptions; len(got) != 1 || got[0].Value != "--enable-helm" {
		t.Fatalf("KustomizeBuildOptions = %#v", got)
	}
	if len(diags) != 2 {
		t.Fatalf("diagnostics = %#v, want two warnings", diags)
	}
	assertSettingWarning(t, diags, "kustomize.buildOptions.v4.5.7 parsed but not applied", "data.kustomize.buildOptions.v4.5.7")
	assertSettingWarning(t, diags, "kustomize.path.v4.5.7 parsed but not applied", "data.kustomize.path.v4.5.7")
	assertDiagnosticStableCode(t, diags, "kustomize.buildOptions.v4.5.7", "settings.metadata-only")
	assertDiagnosticStableCode(t, diags, "kustomize.path.v4.5.7", "settings.metadata-only")
}

func TestLoadHelmValuesKustomizeVersionedPathWarns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(`configs:
  cm:
    kustomize.path.v5.0.0: /custom-tools/kustomize_5_0_0
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, diags, err := LoadFromHelmValues(path)
	if err != nil {
		t.Fatalf("LoadFromHelmValues() error = %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one warning", diags)
	}
	assertSettingWarning(t, diags, "kustomize.path.v5.0.0 parsed but not applied", "configs.cm.kustomize.path.v5.0.0")
}

func TestLoadFromConfigMapDocumentLoadsLaterDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multi.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Namespace
metadata:
  name: ignored
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.compareoptions: |
    ignoreAggregatedRoles: true
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMapDocument(path, 1)
	if err != nil {
		t.Fatalf("LoadFromConfigMapDocument() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if !settings.CompareOptions.IgnoreAggregatedRoles {
		t.Fatalf("IgnoreAggregatedRoles = false, want true")
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
	if len(diags) != 4 {
		t.Fatalf("len(diags) = %d, want 4 warnings: %#v", len(diags), diags)
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

func TestLoadConfigMapResourceCustomizationRetainsHealthLuaWithoutWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	const healthLua = `return { status = "Healthy", message = "SUPER_SECRET_HEALTH_TOKEN" }`
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      health.lua: |
        return { status = "Healthy", message = "SUPER_SECRET_HEALTH_TOKEN" }
      health.lua.useOpenLibs: true
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if hasDiagnosticMessage(diags, "resource customizations health Lua") {
		t.Fatalf("diagnostics = %#v, want no health Lua metadata-only warning", diags)
	}
	if hasDiagnosticMessage(diags, "resource customizations useOpenLibs") {
		t.Fatalf("diagnostics = %#v, want no useOpenLibs metadata-only warning", diags)
	}
	customization := settings.ResourceCustomizations["apps/Deployment"]
	if !customization.HasHealthLua {
		t.Fatalf("HasHealthLua = false, want true")
	}
	if strings.TrimSpace(customization.HealthLua) != healthLua {
		t.Fatalf("HealthLua = %q, want source %q", customization.HealthLua, healthLua)
	}
	const wantHash = "5891509de2d4c98e33ce3c17387504bc74033b0bfc02f2a307ccf58a8e826a9b"
	if customization.HealthLuaSHA256 != wantHash {
		t.Fatalf("HealthLuaSHA256 = %q, want %q", customization.HealthLuaSHA256, wantHash)
	}
	if !customization.HasUseOpenLibs || !customization.UseOpenLibs {
		t.Fatalf("useOpenLibs = present %v value %v, want present true", customization.HasUseOpenLibs, customization.UseOpenLibs)
	}
	serialized, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(serialized), "SUPER_SECRET_HEALTH_TOKEN") {
		t.Fatalf("JSON output contains health Lua source: %s", serialized)
	}
	if !strings.Contains(string(serialized), wantHash) {
		t.Fatalf("JSON output = %s, want health Lua hash %q", serialized, wantHash)
	}
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

func TestSplitActionsAndIgnoreResourceUpdatesEmitMetadataOnlyDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.health.apps_Deployment: |
    return { status = "Healthy" }
  resource.customizations.actions.apps_Deployment: |
    definitions:
      - name: restart
        action.lua: |
          return obj
  resource.customizations.ignoreResourceUpdates.apps_Deployment: |
    jsonPointers:
      - /status
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	assertDiagnosticStableCode(t, diags, "resource customizations actions", "settings.metadata-only")
	assertDiagnosticStableCode(t, diags, "resource customizations ignoreResourceUpdates", "settings.metadata-only")
}

func TestLoadConfigMapSplitResourceCustomizationRetainsHealthLuaWithoutWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	const healthLua = `return { status = "Healthy", message = "SUPER_SECRET_SPLIT_HEALTH_TOKEN" }`
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.health.apps_Deployment: |
    return { status = "Healthy", message = "SUPER_SECRET_SPLIT_HEALTH_TOKEN" }
  resource.customizations.useOpenLibs.apps_Deployment: "true"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if hasDiagnosticMessage(diags, "resource customizations health Lua") {
		t.Fatalf("diagnostics = %#v, want no health Lua metadata-only warning", diags)
	}
	if hasDiagnosticMessage(diags, "resource customizations useOpenLibs") {
		t.Fatalf("diagnostics = %#v, want no useOpenLibs metadata-only warning", diags)
	}
	customization := settings.ResourceCustomizations["apps/Deployment"]
	if !customization.HasHealthLua {
		t.Fatalf("HasHealthLua = false, want true")
	}
	if strings.TrimSpace(customization.HealthLua) != healthLua {
		t.Fatalf("HealthLua = %q, want source %q", customization.HealthLua, healthLua)
	}
	const wantHash = "fdbc7dd6551a80f11a58acbacd68e2420cf710333d872268421c436ca9a37bca"
	if customization.HealthLuaSHA256 != wantHash {
		t.Fatalf("HealthLuaSHA256 = %q, want %q", customization.HealthLuaSHA256, wantHash)
	}
	if !customization.HasUseOpenLibs || !customization.UseOpenLibs {
		t.Fatalf("useOpenLibs = present %v value %v, want present true", customization.HasUseOpenLibs, customization.UseOpenLibs)
	}
	serialized, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(serialized), "SUPER_SECRET_SPLIT_HEALTH_TOKEN") {
		t.Fatalf("JSON output contains health Lua source: %s", serialized)
	}
	if !strings.Contains(string(serialized), wantHash) {
		t.Fatalf("JSON output = %s, want health Lua hash %q", serialized, wantHash)
	}
}

func TestLoadConfigMapCompareOptionsAppliedWithoutMetadataOnlyDiagnostic(t *testing.T) {
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
	if settings.CompareOptions.IgnoreResourceStatusField != "crd" || !settings.CompareOptions.IgnoreAggregatedRoles {
		t.Fatalf("CompareOptions = %#v, want applied compare options", settings.CompareOptions)
	}
	for _, diag := range diagnostic.WithStableCodes(diags) {
		if diag.Code == "settings.metadata-only" {
			t.Fatalf("diagnostics = %#v, did not want metadata-only diagnostic for applied compare options", diags)
		}
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
	if hasDiagnosticMessage(diags, "resource customizations useOpenLibs") {
		t.Fatalf("diagnostics = %#v, want no useOpenLibs metadata-only warning", diags)
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

func TestLoadConfigMapMatchingHealthLuaSectionsMergeRetainsSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	const healthLua = `return { status = "Healthy", message = "MERGED_HEALTH_TOKEN" }`
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations: |
    apps/Deployment:
      health.lua: |
        return { status = "Healthy", message = "MERGED_HEALTH_TOKEN" }
  resource.customizations.health.apps_Deployment: |
    return { status = "Healthy", message = "MERGED_HEALTH_TOKEN" }
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
	customization := settings.ResourceCustomizations["apps/Deployment"]
	if !customization.HasHealthLua {
		t.Fatalf("HasHealthLua = false, want true")
	}
	if strings.TrimSpace(customization.HealthLua) != healthLua {
		t.Fatalf("HealthLua = %q, want source %q", customization.HealthLua, healthLua)
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
	if err := os.WriteFile(path, fmt.Appendf(nil, `apiVersion: v1
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
`, encoded("charts"), encoded("helm"), encoded("ghcr.io/example/charts"), encoded("true")), 0o600); err != nil {
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

func TestLoadRepositorySecretLoadsMultiDocumentRepositorySecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Secret
metadata:
  name: git
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: platform-repo
  type: git
  url: https://github.com/example/platform-repo
---
apiVersion: v1
kind: Secret
metadata:
  name: charts
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: charts
  type: helm
  url: ghcr.io/example/charts
  enableOCI: "true"
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadRepositorySecret(path)
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(settings.HelmRepositories) != 2 {
		t.Fatalf("len(HelmRepositories) = %d, want 2: %#v", len(settings.HelmRepositories), settings.HelmRepositories)
	}
	repo := settings.HelmRepositories["ghcr.io/example/charts"]
	if repo.Name != "charts" || repo.Type != "helm" || !repo.EnableOCI {
		t.Fatalf("OCI repo = %#v", repo)
	}
}

func TestLoadRepositorySecretDocumentLoadsOneDocumentWithoutSecretLeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Secret
metadata:
  name: repo-one
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: https://charts.example.test
  password: super-secret
---
apiVersion: v1
kind: Secret
metadata:
  name: repo-two
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: ghcr.io/example/charts
  enableOCI: "true"
  username: user
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadRepositorySecretDocument(path, 1)
	if err != nil {
		t.Fatalf("LoadRepositorySecretDocument() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := settings.HelmRepositories["ghcr.io/example/charts"].EnableOCI; !got {
		t.Fatalf("EnableOCI = false, want true")
	}
	rendered := fmt.Sprintf("%#v %#v", settings, diags)
	if strings.Contains(rendered, "super-secret") || strings.Contains(rendered, "username") {
		t.Fatalf("settings leaked credential material: %s", rendered)
	}
}

func TestLoadRepositorySecretReportsMultiDocumentRepositoryConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Secret
metadata:
  name: charts-a
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: charts-a
  type: helm
  url: https://charts.example.test
---
apiVersion: v1
kind: Secret
metadata:
  name: charts-b
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: charts-b
  type: helm
  url: https://charts.example.test
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, diags, err := LoadRepositorySecret(path)
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Severity != "error" || !strings.Contains(diags[0].Message, "conflicting repository settings discovered") {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
	if diags[0].Provenance.Path != path || diags[0].Provenance.Pointer != "stringData.url" {
		t.Fatalf("provenance = %#v", diags[0].Provenance)
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

func TestMergeSettingsHelmValuesFileSchemesExplicitEmptyOverridesDefault(t *testing.T) {
	left := DefaultSettings()
	right := DefaultSettings()
	right.HelmValuesFileSchemes = nil
	right.HelmValuesFileSchemesSet = true
	right.HelmValuesFileSchemesSource = Provenance{Path: "right.yaml", Pointer: "data.helm.valuesFileSchemes"}

	merged, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(merged.HelmValuesFileSchemes) != 0 {
		t.Fatalf("HelmValuesFileSchemes = %#v, want empty explicit setting", merged.HelmValuesFileSchemes)
	}
	if !merged.HelmValuesFileSchemesSet {
		t.Fatal("HelmValuesFileSchemesSet = false, want true")
	}
}

func TestMergeSettingsDetectsHelmValuesFileSchemesConflict(t *testing.T) {
	left := DefaultSettings()
	left.HelmValuesFileSchemes = valuesFromStrings([]string{"s3"}, Provenance{Path: "left.yaml"})
	left.HelmValuesFileSchemesSet = true
	left.HelmValuesFileSchemesSource = Provenance{Path: "left.yaml", Pointer: "data.helm.valuesFileSchemes"}
	right := DefaultSettings()
	right.HelmValuesFileSchemes = valuesFromStrings([]string{"https"}, Provenance{Path: "right.yaml"})
	right.HelmValuesFileSchemesSet = true
	right.HelmValuesFileSchemesSource = Provenance{Path: "right.yaml", Pointer: "configs.cm.helm.valuesFileSchemes"}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Category != "settings" || !strings.Contains(diags[0].Message, "helm.valuesFileSchemes") {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
	if diags[0].Provenance.Path != "right.yaml" || diags[0].Provenance.Pointer != "configs.cm.helm.valuesFileSchemes" {
		t.Fatalf("provenance = %#v", diags[0].Provenance)
	}
}

func TestMergeSettingsInstallationID(t *testing.T) {
	left := DefaultSettings()
	right := DefaultSettings()
	right.InstallationID = Value[string]{
		Value:      "cluster-one",
		Provenance: Provenance{Path: "right.yaml", Pointer: "data.installationID"},
	}

	merged, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if merged.InstallationID.Value != "cluster-one" {
		t.Fatalf("InstallationID = %#v", merged.InstallationID)
	}
}

func TestMergeSettingsDetectsInstallationIDConflict(t *testing.T) {
	left := DefaultSettings()
	left.InstallationID = Value[string]{
		Value:      "cluster-one",
		Provenance: Provenance{Path: "left.yaml", Pointer: "data.installationID"},
	}
	right := DefaultSettings()
	right.InstallationID = Value[string]{
		Value:      "cluster-two",
		Provenance: Provenance{Path: "right.yaml", Pointer: "configs.cm.installationID"},
	}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Category != "settings" || !strings.Contains(diags[0].Message, "installationID") {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
	if diags[0].Provenance.Path != "right.yaml" || diags[0].Provenance.Pointer != "configs.cm.installationID" {
		t.Fatalf("provenance = %#v", diags[0].Provenance)
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
	return slices.Contains(values, want)
}

func assertValueStrings(t *testing.T, got []Value[string], want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("values = %#v, want %#v", valuesOnly(got), want)
	}
	for i := range want {
		if got[i].Value != want[i] {
			t.Fatalf("values = %#v, want %#v", valuesOnly(got), want)
		}
	}
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

func assertDiagnosticStableCode(t *testing.T, diags []diagnostic.Diagnostic, fragment, code string) {
	t.Helper()
	for _, diag := range diagnostic.WithStableCodes(diags) {
		if strings.Contains(diag.Message, fragment) {
			if diag.Code != code {
				t.Fatalf("diagnostic code for %q = %q, want %q: %#v", fragment, diag.Code, code, diag)
			}
			return
		}
	}
	t.Fatalf("diagnostics = %#v, missing message containing %q", diags, fragment)
}

func assertSettingWarning(t *testing.T, diags []diagnostic.Diagnostic, fragment, pointer string) {
	t.Helper()
	for _, diag := range diags {
		if !strings.Contains(diag.Message, fragment) {
			continue
		}
		if diag.Severity != diagnostic.SeverityWarning || diag.Category != "settings" {
			t.Fatalf("diagnostic = %#v, want settings warning", diag)
		}
		if diag.Provenance.Pointer != pointer {
			t.Fatalf("diagnostic provenance = %#v, want pointer %q", diag.Provenance, pointer)
		}
		return
	}
	t.Fatalf("diagnostics = %#v, missing settings warning containing %q", diags, fragment)
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
