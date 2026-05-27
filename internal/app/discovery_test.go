package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func TestListApplicationsDiscoversRenderedKustomizeAndRenderedSettingsWin(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "argocd", "base", "application.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  source:
    repoURL: https://github.com/example/repo
    targetRevision: HEAD
    path: apps/raw
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  application.instanceLabelKey: app.kubernetes.io/raw
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "kustomization.yaml"), `resources:
  - application.yaml
  - argocd-cm.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "overlays", "prod", "kustomization.yaml"), `resources:
  - ../../base
patches:
  - target:
      group: argoproj.io
      version: v1alpha1
      kind: Application
      name: demo
    patch: |-
      - op: replace
        path: /spec/source/path
        value: apps/rendered
  - target:
      version: v1
      kind: ConfigMap
      name: argocd-cm
    patch: |-
      - op: replace
        path: /data/application.instanceLabelKey
        value: app.kubernetes.io/rendered
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path:                   root,
		DiscoverKustomizePaths: []string{filepath.Join("argocd", "overlays", "prod")},
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %#v, want one rendered Application", result.Applications)
	}
	if got := result.Applications[0].Spec.Source.Path; got != "apps/rendered" {
		t.Fatalf("Application source path = %q, want rendered path", got)
	}
	if got := result.Settings.InstanceLabelKey.Value; got != "app.kubernetes.io/rendered" {
		t.Fatalf("InstanceLabelKey = %q, want rendered settings", got)
	}
	if len(result.ApplicationInputs) != 1 || len(result.ApplicationInputs[0].Paths) != 1 || result.ApplicationInputs[0].Paths[0] != "argocd/overlays/prod" {
		t.Fatalf("ApplicationInputs = %#v, want rendered discovery path", result.ApplicationInputs)
	}
}

func TestListApplicationsRejectsUnsafeDiscoverKustomizePath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "overlay", "kustomization.yaml"), "resources: []\n")

	_, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path:                   root,
		DiscoverKustomizePaths: []string{filepath.Join(root, "overlay")},
	})
	if err == nil || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("ListApplications() error = %v, want relative path error", err)
	}

	if err := os.Symlink(filepath.Join(root, "overlay"), filepath.Join(root, "linked-overlay")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	_, err = Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path:                   root,
		DiscoverKustomizePaths: []string{"linked-overlay"},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink component") {
		t.Fatalf("ListApplications() error = %v, want symlink path error", err)
	}
}

func TestListApplicationsWarnsWhenApplicationSetGeneratesZeroApplications(t *testing.T) {
	root := t.TempDir()
	writeZeroApplicationSet(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.Applications) != 0 {
		t.Fatalf("Applications = %#v, want none", result.Applications)
	}
	diag, ok := diagnosticByCategory(result.Diagnostics, "appset")
	if !ok {
		t.Fatalf("Diagnostics = %#v, want appset warning", result.Diagnostics)
	}
	if diag.Severity != diagnostic.SeverityWarning || !strings.Contains(diag.Message, "ApplicationSet argocd/empty generated zero Applications") {
		t.Fatalf("Diagnostic = %#v, want zero-generated warning", diag)
	}

	_, err = Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil || !strings.Contains(err.Error(), "generated zero Applications") {
		t.Fatalf("strict ListApplications() error = %v, want zero-generated error", err)
	}
}

func writeZeroApplicationSet(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: empty
  namespace: argocd
spec:
  generators:
    - list:
        elements: []
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      destination:
        server: https://kubernetes.default.svc
        namespace: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: apps/{{name}}
`)
}
