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

func TestLoadClusterSecretAllowsOnlyMetadataFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster-secret.yaml")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Secret
metadata:
  name: in-cluster
  labels:
    argocd.argoproj.io/secret-type: cluster
stringData:
  name: in-cluster
  server: https://kubernetes.default.svc/
  namespaces: "default, kube-system, , workloads"
  clusterResources: "TRUE"
  project: platform
  config: '{"bearerToken":"should-not-be-read"}'
  bearerToken: should-not-be-read
  password: should-not-be-read
`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadClusterSecret(path)
	if err != nil {
		t.Fatalf("LoadClusterSecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	cluster := settings.Clusters["https://kubernetes.default.svc"]
	if cluster.Name != "in-cluster" || cluster.Server != "https://kubernetes.default.svc" || cluster.Project != "platform" {
		t.Fatalf("cluster metadata = %#v", cluster)
	}
	if !cluster.ClusterResources {
		t.Fatalf("ClusterResources = false, want true")
	}
	if got, want := strings.Join(cluster.Namespaces, ","), "default,kube-system,workloads"; got != want {
		t.Fatalf("Namespaces = %q, want %q", got, want)
	}
	if cluster.Provenance.Path != path || cluster.Provenance.Pointer != "stringData.server" {
		t.Fatalf("provenance = %#v", cluster.Provenance)
	}
	assertNoClusterSecretLeak(t, settings, diags)
}

func TestLoadClusterSecretDataDecodesAllowlistedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster-secret.yaml")
	encoded := func(value string) string {
		return base64.StdEncoding.EncodeToString([]byte(value))
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: prod
  labels:
    argocd.argoproj.io/secret-type: cluster
data:
  name: %s
  server: %s
  namespaces: %s
  clusterResources: %s
  project: %s
  config: %s
`, encoded("prod"), encoded("https://prod.example/"), encoded("prod,shared"), encoded("true"), encoded("platform"), encoded(`{"tlsClientConfig":{"certData":"should-not-be-read"}}`))), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, diags, err := LoadClusterSecret(path)
	if err != nil {
		t.Fatalf("LoadClusterSecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	cluster := settings.Clusters["https://prod.example"]
	if cluster.Name != "prod" || cluster.Project != "platform" || !cluster.ClusterResources {
		t.Fatalf("cluster metadata = %#v", cluster)
	}
	if got, want := strings.Join(cluster.Namespaces, ","), "prod,shared"; got != want {
		t.Fatalf("Namespaces = %q, want %q", got, want)
	}
	if cluster.Provenance.Pointer != "data.server" {
		t.Fatalf("provenance = %#v", cluster.Provenance)
	}
	assertNoClusterSecretLeak(t, settings, diags)
}

func TestMergeSettingsDetectsClusterSecretConflict(t *testing.T) {
	left := DefaultSettings()
	left.Clusters["https://prod.example"] = ClusterSettings{
		Name:       "prod-a",
		Server:     "https://prod.example",
		Provenance: Provenance{Path: "left.yaml", Pointer: "stringData.server"},
	}
	right := DefaultSettings()
	right.Clusters["https://prod.example"] = ClusterSettings{
		Name:       "prod-b",
		Server:     "https://prod.example",
		Provenance: Provenance{Path: "right.yaml", Pointer: "stringData.server"},
	}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1: %#v", len(diags), diags)
	}
	if diags[0].Category != "settings" || !strings.Contains(diags[0].Message, "conflicting cluster settings discovered") {
		t.Fatalf("diagnostic = %#v", diags[0])
	}
	if diags[0].Provenance.Path != "right.yaml" || diags[0].Provenance.Pointer != "stringData.server" {
		t.Fatalf("provenance = %#v", diags[0].Provenance)
	}
}

func assertNoClusterSecretLeak(t *testing.T, settings ArgoSettings, diags []diagnostic.Diagnostic) {
	t.Helper()
	serialized, err := json.Marshal(struct {
		Settings    ArgoSettings            `json:"settings"`
		Diagnostics []diagnostic.Diagnostic `json:"diagnostics"`
	}{Settings: settings, Diagnostics: diags})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, sensitive := range []string{"should-not-be-read", "config", "bearerToken", "tlsClientConfig", "certData", "password"} {
		if strings.Contains(string(serialized), sensitive) {
			t.Fatalf("cluster Secret credential data was retained or leaked: %s", serialized)
		}
	}
}
