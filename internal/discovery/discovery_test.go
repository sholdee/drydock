package discovery

import (
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

	result, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if !reflect.DeepEqual(result.ApplicationSetPath, []string{filepath.Join("apps", "appset.yaml")}) {
		t.Fatalf("ApplicationSetPath = %#v", result.ApplicationSetPath)
	}
	wantSettings := []SettingsCandidate{
		{Path: filepath.Join("settings", "argocd-cm.yaml"), Kind: "argocd-cm"},
		{Path: filepath.Join("settings", "repo-secret.yaml"), Kind: "repository-secret"},
	}
	if !reflect.DeepEqual(result.SettingsCandidates, wantSettings) {
		t.Fatalf("SettingsCandidates = %#v, want %#v", result.SettingsCandidates, wantSettings)
	}
	if strings.Contains(fmt.Sprintf("%#v", result), "super-secret") {
		t.Fatalf("discovery result leaked Secret data: %#v", result)
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

func mustWriteFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
