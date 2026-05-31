package paritycompare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareMatchesCanonicalResources(t *testing.T) {
	root := t.TempDir()
	argocdDir := filepath.Join(root, "argocd")
	drydockDir := filepath.Join(root, "drydock")
	writeFile(t, filepath.Join(argocdDir, "demo.yaml"), configMap("demo", "same"))
	writeFile(t, filepath.Join(drydockDir, "demo.yaml"), configMap("demo", "same"))

	result, err := Compare(Options{
		ArgoCDDir:  argocdDir,
		DrydockDir: drydockDir,
		OutDir:     filepath.Join(root, "out"),
	})
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if result.Applications != 1 || result.Resources != 1 || result.Differences != 0 {
		t.Fatalf("Compare() = %#v, want one matching resource", result)
	}
}

func TestCompareReportsPerResourceDiff(t *testing.T) {
	root := t.TempDir()
	argocdDir := filepath.Join(root, "argocd")
	drydockDir := filepath.Join(root, "drydock")
	outDir := filepath.Join(root, "out")
	writeFile(t, filepath.Join(argocdDir, "demo.yaml"), configMap("demo", "left"))
	writeFile(t, filepath.Join(drydockDir, "demo.yaml"), configMap("demo", "right"))

	result, err := Compare(Options{
		ArgoCDDir:  argocdDir,
		DrydockDir: drydockDir,
		OutDir:     outDir,
	})
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if result.Differences != 1 || len(result.DiffFiles) != 1 {
		t.Fatalf("Compare() = %#v, want one diff", result)
	}
	diff, err := os.ReadFile(result.DiffFiles[0])
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if !strings.Contains(string(diff), "-    \"value\": \"left\"") || !strings.Contains(string(diff), "+    \"value\": \"right\"") {
		t.Fatalf("diff missing expected value change:\n%s", diff)
	}
}

func TestCompareAppliesJSONPointerIgnores(t *testing.T) {
	root := t.TempDir()
	argocdDir := filepath.Join(root, "argocd")
	drydockDir := filepath.Join(root, "drydock")
	ignoreFile := filepath.Join(root, "ignore.yaml")
	writeFile(t, filepath.Join(argocdDir, "demo.yaml"), configMapWithTracking("demo", "argocd"))
	writeFile(t, filepath.Join(drydockDir, "demo.yaml"), configMapWithTracking("demo", "drydock"))
	writeFile(t, ignoreFile, "jsonPointers:\n  - /metadata/annotations/argocd.argoproj.io~1tracking-id\n")

	result, err := Compare(Options{
		ArgoCDDir:  argocdDir,
		DrydockDir: drydockDir,
		OutDir:     filepath.Join(root, "out"),
		IgnoreFile: ignoreFile,
	})
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if result.Differences != 0 {
		t.Fatalf("Compare() differences = %d, want ignored tracking annotation", result.Differences)
	}
}

func TestCompareRejectsInvalidJSONPointerEscape(t *testing.T) {
	root := t.TempDir()
	argocdDir := filepath.Join(root, "argocd")
	drydockDir := filepath.Join(root, "drydock")
	ignoreFile := filepath.Join(root, "ignore.yaml")
	writeFile(t, filepath.Join(argocdDir, "demo.yaml"), configMap("demo", "same"))
	writeFile(t, filepath.Join(drydockDir, "demo.yaml"), configMap("demo", "same"))
	writeFile(t, ignoreFile, "jsonPointers:\n  - /metadata/annotations/bad~escape\n")

	_, err := Compare(Options{
		ArgoCDDir:  argocdDir,
		DrydockDir: drydockDir,
		OutDir:     filepath.Join(root, "out"),
		IgnoreFile: ignoreFile,
	})
	if err == nil {
		t.Fatal("Compare() error = nil, want invalid JSON pointer escape")
	}
	if !strings.Contains(err.Error(), "invalid JSON pointer escape") {
		t.Fatalf("Compare() error = %q, want invalid JSON pointer escape", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func configMap(name, value string) string {
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: ` + name + `
data:
  value: ` + value + `
`
}

func configMapWithTracking(name, tracking string) string {
	return `apiVersion: v1
kind: ConfigMap
metadata:
  name: ` + name + `
  annotations:
    argocd.argoproj.io/tracking-id: ` + tracking + `
data:
  value: same
`
}
