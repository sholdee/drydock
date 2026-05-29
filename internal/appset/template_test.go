package appset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateTemplatePatchAppliesMetadataSyncPolicyAndPreservesProject(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: template-patch
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
          - name: alpha
            namespace: alpha-ns
  template:
    metadata:
      name: '{{ .name }}'
      labels:
        base: keep
      annotations:
        base: keep
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{ .name }}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
  templatePatch: |
    metadata:
      labels:
        patched: '{{ .name }}'
      annotations:
        patched: '{{ .namespace }}'
    spec:
      project: forbidden
      destination:
        namespace: '{{ .namespace }}'
      syncPolicy:
        automated:
          prune: true
        syncOptions:
          - CreateNamespace=true
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}

	assertTemplatePatchMetadata(t, apps[0])
	assertTemplatePatchSyncPolicy(t, apps[0])
}

func assertTemplatePatchMetadata(t *testing.T, generated GeneratedApplication) {
	t.Helper()

	app := generated.Application
	if app.Namespace != "argocd" {
		t.Fatalf("Application namespace = %q, want ApplicationSet namespace", app.Namespace)
	}
	if app.Labels["base"] != "keep" || app.Labels["patched"] != "alpha" {
		t.Fatalf("labels = %#v, want base and patched labels", app.Labels)
	}
	if app.Annotations["base"] != "keep" || app.Annotations["patched"] != "alpha-ns" {
		t.Fatalf("annotations = %#v, want base and patched annotations", app.Annotations)
	}
	if app.Spec.Project != "default" {
		t.Fatalf("project = %q, want default preserved", app.Spec.Project)
	}
	if app.Spec.Destination.Namespace != "alpha-ns" {
		t.Fatalf("destination namespace = %q, want patched namespace", app.Spec.Destination.Namespace)
	}
}

func assertTemplatePatchSyncPolicy(t *testing.T, generated GeneratedApplication) {
	t.Helper()

	app := generated.Application
	if app.Spec.SyncPolicy == nil {
		t.Fatal("syncPolicy = nil, want patched syncPolicy")
	}
	if app.Spec.SyncPolicy.Automated == nil || app.Spec.SyncPolicy.Automated.Prune == nil || !*app.Spec.SyncPolicy.Automated.Prune {
		t.Fatalf("syncPolicy.automated.prune = %#v, want true", app.Spec.SyncPolicy.Automated)
	}
	if len(app.Spec.SyncPolicy.SyncOptions) != 1 || app.Spec.SyncPolicy.SyncOptions[0] != "CreateNamespace=true" {
		t.Fatalf("syncOptions = %#v, want CreateNamespace=true", app.Spec.SyncPolicy.SyncOptions)
	}
}

func TestGenerateTemplatePatchReturnsInvalidPatchError(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: template-patch-invalid
spec:
  generators:
    - list:
        elements:
          - name: alpha
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: apps/{{name}}
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
  templatePatch: hello world
`)

	_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil {
		t.Fatal("GenerateFromYAML() error = nil, want invalid templatePatch error")
	}
	if !strings.Contains(err.Error(), "invalid templatePatch") {
		t.Fatalf("error = %v, want invalid templatePatch", err)
	}
}

func TestGenerateTemplatePatchSemanticFixture(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "semantic-remediation", "appset-template-patch", "basic")
	data, err := os.ReadFile(filepath.Join(root, "appset.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	apps, diags, err := GenerateFromYAML(root, "appset.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}
	app := apps[0].Application
	if app.Name != "alpha" {
		t.Fatalf("name = %q, want alpha", app.Name)
	}
	if app.Spec.Destination.Namespace != "alpha" {
		t.Fatalf("destination namespace = %q, want alpha", app.Spec.Destination.Namespace)
	}
	if app.Spec.SyncPolicy == nil || len(app.Spec.SyncPolicy.SyncOptions) != 1 || app.Spec.SyncPolicy.SyncOptions[0] != "CreateNamespace=true" {
		t.Fatalf("syncPolicy = %#v, want CreateNamespace sync option", app.Spec.SyncPolicy)
	}
}
