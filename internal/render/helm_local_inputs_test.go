package render

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

func writeHelmInputTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func TestCollectHelmLocalInputPathsIncludesValueFilesAndFileParameters(t *testing.T) {
	root := t.TempDir()
	writeHelmInputTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeHelmInputTestFile(t, filepath.Join(root, "charts", "demo", "values-prod.yaml"), "value: prod\n")
	writeHelmInputTestFile(t, filepath.Join(root, "charts", "demo", "files", "secret.txt"), "secret\n")

	got, err := CollectHelmLocalInputPaths(HelmLocalInputOptions{
		RepoRoot: root,
		Source:   ResolvedSource{Path: "charts/demo"},
		Options: RenderOptions{
			ValueFiles: []string{"values-prod.yaml"},
			HelmFileParameters: []argoappv1.HelmFileParameter{
				{Name: "secret", Path: "files/secret.txt"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CollectHelmLocalInputPaths() error = %v", err)
	}
	want := []HelmLocalInputPath{
		{Path: "charts/demo"},
		{Path: "charts/demo/files/secret.txt"},
		{Path: "charts/demo/values-prod.yaml"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestCollectHelmLocalInputPathsIncludesSameRepoRefValueFile(t *testing.T) {
	root := t.TempDir()
	writeHelmInputTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	writeHelmInputTestFile(t, filepath.Join(root, "shared", "demo.yaml"), "value: shared\n")

	got, err := CollectHelmLocalInputPaths(HelmLocalInputOptions{
		RepoRoot: root,
		Source:   ResolvedSource{Path: "charts/demo"},
		Options: RenderOptions{
			RefRoots:   map[string]string{"$values": "."},
			ValueFiles: []string{"$values/shared/demo.yaml"},
		},
	})
	if err != nil {
		t.Fatalf("CollectHelmLocalInputPaths() error = %v", err)
	}
	want := []HelmLocalInputPath{
		{Path: "charts/demo"},
		{Path: "shared/demo.yaml"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestCollectHelmLocalInputPathsIncludesOptionalMissingValueFile(t *testing.T) {
	root := t.TempDir()
	writeHelmInputTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")

	got, err := CollectHelmLocalInputPaths(HelmLocalInputOptions{
		RepoRoot: root,
		Source:   ResolvedSource{Path: "charts/demo"},
		Options: RenderOptions{
			RefRoots:                map[string]string{"$values": "."},
			ValueFiles:              []string{"$values/optional/demo.yaml"},
			IgnoreMissingValueFiles: true,
		},
	})
	if err != nil {
		t.Fatalf("CollectHelmLocalInputPaths() error = %v", err)
	}
	want := []HelmLocalInputPath{
		{Path: "charts/demo"},
		{Path: "optional/demo.yaml", Optional: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestCollectHelmLocalInputPathsRejectsRemoteValueFile(t *testing.T) {
	root := t.TempDir()
	writeHelmInputTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")

	_, err := CollectHelmLocalInputPaths(HelmLocalInputOptions{
		RepoRoot: root,
		Source:   ResolvedSource{Path: "charts/demo"},
		Options:  RenderOptions{ValueFiles: []string{"https://values.example.invalid/demo.yaml"}},
	})
	if err == nil {
		t.Fatalf("CollectHelmLocalInputPaths() error = nil, want remote URL rejection")
	}
}

func TestCollectHelmLocalInputPathsRejectsEnvSubstitutedValueFile(t *testing.T) {
	root := t.TempDir()
	writeHelmInputTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")

	_, err := CollectHelmLocalInputPaths(HelmLocalInputOptions{
		RepoRoot: root,
		Source:   ResolvedSource{Path: "charts/demo"},
		Options: RenderOptions{
			RefRoots:   map[string]string{"$values": "."},
			ValueFiles: []string{"$values/$ARGOCD_APP_NAME.yaml"},
		},
	})
	if err == nil {
		t.Fatalf("CollectHelmLocalInputPaths() error = nil, want env substitution rejection")
	}
}
