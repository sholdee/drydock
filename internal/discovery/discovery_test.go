package discovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScanFindsDirectApplications(t *testing.T) {
	root := t.TempDir()
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(root, "apps", "argocd", "app.yaml"))

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	app := result.Applications[0]
	if app.Application.Name != "guestbook" || app.Path != filepath.Join("apps", "argocd", "app.yaml") {
		t.Fatalf("unexpected app: %#v", app)
	}
}

func TestScanHonorsAppManifestPathDirectory(t *testing.T) {
	root := t.TempDir()
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(root, "selected", "app.yaml"))
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(root, "ignored", "app.yaml"))

	result, err := Scan(root, Options{AppManifestPaths: []string{"selected"}})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Path != filepath.Join("selected", "app.yaml") {
		t.Fatalf("Path = %s", result.Applications[0].Path)
	}
}

func TestScanHonorsAppManifestPathFile(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join("selected", "app.yaml")
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(root, selected))
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(root, "ignored", "app.yaml"))

	result, err := Scan(root, Options{AppManifestPaths: []string{selected}})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Path != selected {
		t.Fatalf("Path = %s", result.Applications[0].Path)
	}
}

func TestScanSkipsInternalDirectories(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml")
	mustCopy(t, fixture, filepath.Join(root, "visible", "app.yaml"))
	mustCopy(t, fixture, filepath.Join(root, ".git", "app.yaml"))
	mustCopy(t, fixture, filepath.Join(root, ".out", "app.yaml"))
	mustCopy(t, fixture, filepath.Join(root, ".cache", "app.yaml"))
	mustCopy(t, fixture, filepath.Join(root, ".cache-build", "app.yaml"))

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Path != filepath.Join("visible", "app.yaml") {
		t.Fatalf("Path = %s", result.Applications[0].Path)
	}
}

func TestScanSkipsSymlinkedYAML(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(outside, "outside-app.yaml"))
	mustSymlink(t, filepath.Join(outside, "outside-app.yaml"), filepath.Join(root, "apps", "outside-app.yaml"))

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 0 {
		t.Fatalf("Applications = %#v, want no symlinked apps", result.Applications)
	}
}

func TestScanRejectsExplicitSymlinkPath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(outside, "outside-app.yaml"))
	link := filepath.Join(root, "apps", "outside-app.yaml")
	mustSymlink(t, filepath.Join(outside, "outside-app.yaml"), link)

	_, err := Scan(root, Options{AppManifestPaths: []string{filepath.Join("apps", "outside-app.yaml")}})
	if err == nil {
		t.Fatal("Scan() error = nil, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Scan() error = %v, want symlink error", err)
	}
}

func TestScanRejectsExplicitPathWithSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(outside, "outside-app.yaml"))
	mustSymlink(t, outside, filepath.Join(root, "apps"))

	_, err := Scan(root, Options{AppManifestPaths: []string{filepath.Join("apps", "outside-app.yaml")}})
	if err == nil {
		t.Fatal("Scan() error = nil, want symlink error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Scan() error = %v, want symlink error", err)
	}
}

func TestScanFindsApplicationSetAndSettingsCandidates(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "apps", "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: generated
  namespace: argocd
spec: {}
`)
	mustWriteFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  application.instanceLabelKey: app.kubernetes.io/instance
`)
	mustWriteFile(t, filepath.Join(root, "settings", "repo-secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: repo
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: https://github.com/example/repo
  password: super-secret
`)
	mustWriteFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cm:
    resource.exclusions: |
      - apiGroups: ["events.k8s.io"]
`)
	mustWriteFile(t, filepath.Join(root, "settings", "compare-values.yaml"), `configs:
  cm:
    resource.compareoptions: |
      ignoreResourceStatusField: none
`)

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if !reflect.DeepEqual(result.ApplicationSetPath, []string{filepath.Join("apps", "appset.yaml")}) {
		t.Fatalf("ApplicationSetPath = %#v", result.ApplicationSetPath)
	}
	wantSettings := []SettingsCandidate{
		{Path: filepath.Join("settings", "argocd-cm.yaml"), Kind: "argocd-cm"},
		{Path: filepath.Join("settings", "compare-values.yaml"), Kind: "argocd-values"},
		{Path: filepath.Join("settings", "repo-secret.yaml"), Kind: "repository-secret"},
		{Path: filepath.Join("settings", "values.yaml"), Kind: "argocd-values"},
	}
	if !reflect.DeepEqual(result.SettingsCandidates, wantSettings) {
		t.Fatalf("SettingsCandidates = %#v, want %#v", result.SettingsCandidates, wantSettings)
	}
	if strings.Contains(fmt.Sprintf("%#v", result), "super-secret") {
		t.Fatalf("discovery result leaked Secret data: %#v", result)
	}
}

func TestScanDiscoversAppProjects(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
  namespace: argocd
spec:
  sourceRepos:
    - https://github.com/example/*
  destinations:
    - server: https://kubernetes.default.svc
      namespace: workloads
`)

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Projects) != 1 {
		t.Fatalf("Projects = %#v, want one AppProject", result.Projects)
	}
	if result.Projects[0].Path != filepath.Join("projects", "platform.yaml") {
		t.Fatalf("Project path = %q", result.Projects[0].Path)
	}
	if result.Projects[0].Project.Name != "platform" {
		t.Fatalf("Project name = %q, want platform", result.Projects[0].Project.Name)
	}
}

func TestScanRequiresCandidateGVK(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "fake-app.yaml"), `apiVersion: example.com/v1
kind: Application
metadata:
  name: not-argo
`)
	mustWriteFile(t, filepath.Join(root, "fake-appset.yaml"), `apiVersion: example.com/v1
kind: ApplicationSet
metadata:
  name: not-argo
`)
	mustWriteFile(t, filepath.Join(root, "fake-cm.yaml"), `apiVersion: example.com/v1
kind: ConfigMap
metadata:
  name: argocd-cm
`)
	mustWriteFile(t, filepath.Join(root, "fake-secret.yaml"), `apiVersion: example.com/v1
kind: Secret
metadata:
  name: repo
  labels:
    argocd.argoproj.io/secret-type: repository
`)

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 0 {
		t.Fatalf("Applications = %#v, want none", result.Applications)
	}
	if len(result.ApplicationSetPath) != 0 {
		t.Fatalf("ApplicationSetPath = %#v, want none", result.ApplicationSetPath)
	}
	if len(result.SettingsCandidates) != 0 {
		t.Fatalf("SettingsCandidates = %#v, want none", result.SettingsCandidates)
	}
}

func TestDefaultScanIgnoresUnrelatedMalformedYAML(t *testing.T) {
	root := t.TempDir()
	mustCopy(t, filepath.Join("..", "..", "testdata", "applications", "direct-app.yaml"), filepath.Join(root, "apps", "argocd", "app.yaml"))
	mustWriteFile(t, filepath.Join(root, "values.yaml"), `replicaCount: 1
image:
  tag: latest
`)
	mustWriteFile(t, filepath.Join(root, "unrelated-values.yaml"), `configs:
  cm:
    unrelated: value
`)
	mustWriteFile(t, filepath.Join(root, "templates", "deployment.yaml"), `{{- if .Values.enabled }}
apiVersion: apps/v1
kind: Deployment
{{- end
`)
	mustWriteFile(t, filepath.Join(root, "list.yaml"), `- not
- an
- object
`)

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Path != filepath.Join("apps", "argocd", "app.yaml") {
		t.Fatalf("Path = %s", result.Applications[0].Path)
	}
	if len(result.SettingsCandidates) != 0 {
		t.Fatalf("SettingsCandidates = %#v, want none", result.SettingsCandidates)
	}
}

func TestExplicitPathFailsMalformedYAML(t *testing.T) {
	root := t.TempDir()
	malformed := filepath.Join("apps", "bad-app.yaml")
	mustWriteFile(t, filepath.Join(root, malformed), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: [
`)

	_, err := Scan(root, Options{AppManifestPaths: []string{malformed}})
	if err == nil {
		t.Fatal("Scan() error = nil, want malformed YAML error")
	}
}

func mustCopy(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, dst, string(data))
}

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(newname), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldname, newname); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
