package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if len(diags) != 2 {
		t.Fatalf("diagnostics = %#v, want warnings for jq and managedFieldsManagers", diags)
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
