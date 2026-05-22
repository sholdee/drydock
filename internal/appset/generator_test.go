package appset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateGitDirectoriesApplications(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data, err := os.ReadFile(filepath.Join(root, "app-set.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 2 {
		t.Fatalf("len(apps) = %d, want 2", len(apps))
	}

	if apps[0].Application.Name != "adguard" || apps[1].Application.Name != "powerdns-conf" {
		t.Fatalf("generated order = %q, %q", apps[0].Application.Name, apps[1].Application.Name)
	}

	byName := map[string]GeneratedApplication{}
	for _, app := range apps {
		byName[app.Application.Name] = app
	}
	if byName["adguard"].Application.Namespace != "argocd" {
		t.Fatalf("generated namespace = %s", byName["adguard"].Application.Namespace)
	}
	if byName["adguard"].Application.APIVersion != "argoproj.io/v1alpha1" || byName["adguard"].Application.Kind != "Application" {
		t.Fatalf("generated TypeMeta = %s/%s", byName["adguard"].Application.APIVersion, byName["adguard"].Application.Kind)
	}
	adguard := byName["adguard"].Application
	if adguard.Spec.GetSource().Path != "apps/adguard" {
		t.Fatalf("adguard path = %s", adguard.Spec.GetSource().Path)
	}
	if byName["powerdns-conf"].Application.Spec.Destination.Namespace != "powerdns" {
		t.Fatalf("regexReplaceAll namespace = %s", byName["powerdns-conf"].Application.Spec.Destination.Namespace)
	}
}

func TestGenerateGitDirectoriesHonorsExclude(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: apps/*
          - path: apps/powerdns-conf
            exclude: true
  template:
    metadata:
      name: "{{.path.basenameNormalized}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	apps, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err != nil {
		t.Fatalf("GenerateFromYAML() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(apps) != 1 || apps[0].Application.Name != "adguard" {
		t.Fatalf("generated apps = %#v", apps)
	}
}

func TestGenerateMissingKeyOptionReturnsTemplateError(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "appset-git-directories")
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: k3s-apps
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        directories:
          - path: apps/*
  template:
    metadata:
      name: "{{.missing.value}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/home-ops
        targetRevision: master
        path: "{{.path.path}}"
      destination:
        name: in-cluster
        namespace: default
`)

	_, _, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil {
		t.Fatalf("expected missing key template error")
	}
}

func TestGenerateRejectsUnsupportedGenerator(t *testing.T) {
	root := t.TempDir()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: clusters
spec:
  generators:
    - list:
        elements:
          - name: dev
  template:
    metadata:
      name: dev
`)

	_, diags, err := GenerateFromYAML(root, "app-set.yaml", data)
	if err == nil {
		t.Fatalf("expected unsupported generator error")
	}
	if len(diags) == 0 {
		t.Fatalf("expected diagnostics")
	}
	if diags[0].Category != "appset" {
		t.Fatalf("diagnostic category = %q", diags[0].Category)
	}
}
