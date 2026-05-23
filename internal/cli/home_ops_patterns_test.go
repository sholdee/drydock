package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/remote"
)

func TestDiffAppsHomeOpsPatternFixture(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"diff", "apps",
		"--path-orig", filepath.Join(fixtureRoot, "baseline"),
		"--path", filepath.Join(fixtureRoot, "current"),
		"--offline",
		"--chart-cache-dir", filepath.Join(fixtureRoot, "chart-cache"),
		"--exit-code=false",
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	for _, want := range []string{
		"Application: argocd/plain",
		"example/plain:v1",
		"example/plain:v2",
		"fixture.example.test/patch-mode: baseline",
		"fixture.example.test/patch-mode: current",
		"Application: argocd/nested",
		"nested-a",
		"Application: argocd/generator",
		"mode=baseline",
		"mode=current",
		"generated-secret",
		"token: <redacted-before>",
		"token: <redacted-after>",
		"Application: argocd/component-consumer",
		"ConfigMap: components/cache-settings",
		"mode: baseline",
		"mode: current",
		"Application: argocd/http-chart",
		"example/http:v1",
		"example/http:v2",
		"Application: argocd/inline-chart",
		"mode: baseline",
		"mode: current",
		"Application: argocd/multi-chart",
		"value: baseline",
		"value: current",
		"Application: argocd/oci-crds",
		"example/gateway:v1",
		"example/gateway:v2",
		"CustomResourceDefinition",
		"widgets.example.com",
		"Application: argocd/direct-http",
		"example/direct-http:v1",
		"example/direct-http:v2",
		"Application: argocd/direct-oci",
		"example/direct-oci:v1",
		"example/direct-oci:v2",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	for _, forbidden := range []string{"cmVkYWN0ZWQtYmFzZWxpbmU=", "cmVkYWN0ZWQtY3VycmVudA=="} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("stdout leaked Secret value %q:\n%s", forbidden, stdout.String())
		}
	}
}

func TestDiffAppsHomeOpsPatternFixtureStrictChangedOnly(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"diff", "apps",
		"--path-orig", filepath.Join(fixtureRoot, "baseline"),
		"--path", filepath.Join(fixtureRoot, "current"),
		"--offline",
		"--chart-cache-dir", filepath.Join(fixtureRoot, "chart-cache"),
		"--strict-changed-only",
		"--exit-code=false",
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "changed-only") {
		t.Fatalf("stderr contains changed-only diagnostic:\n%s", stderr.String())
	}
	for _, want := range []string{
		"Application: argocd/plain",
		"fixture.example.test/patch-mode: baseline",
		"fixture.example.test/patch-mode: current",
		"Application: argocd/component-consumer",
		"ConfigMap: components/cache-settings",
		"mode: baseline",
		"mode: current",
		"Application: argocd/generator",
		"Secret: generator/generated-secret",
		"token: <redacted-before>",
		"token: <redacted-after>",
		"Application: argocd/http-chart",
		"example/http:v1",
		"example/http:v2",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	for _, forbidden := range []string{
		"ConfigMap: nested/nested-b",
		"ConfigMap: multi-chart/beta",
		"cmVkYWN0ZWQtYmFzZWxpbmU=",
		"cmVkYWN0ZWQtY3VycmVudA==",
	} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("stdout contains forbidden %q:\n%s", forbidden, stdout.String())
		}
	}
}

func seedRemoteResourceCache(t *testing.T, cacheDir string, rawURL string, body string) {
	t.Helper()
	key, err := remote.NewCacheKey(remote.Request{URL: rawURL})
	if err != nil {
		t.Fatalf("remote cache key: %v", err)
	}
	path := remote.CachePath(cacheDir, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create remote cache dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write remote cache file: %v", err)
	}
}

func TestBuildAppsHomeOpsRemoteResourceFromCache(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns", "unsupported-remote")
	remoteURL := "https://raw.githubusercontent.com/rancher/system-upgrade-controller/refs/tags/v0.19.2/pkg/crds/yaml/generated/upgrade.cattle.io_plans.yaml"
	cacheDir := t.TempDir()
	seedRemoteResourceCache(t, cacheDir, remoteURL, `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: plans.upgrade.cattle.io
spec:
  group: upgrade.cattle.io
  names:
    kind: Plan
    plural: plans
  scope: Namespaced
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", fixtureRoot,
		"--offline",
		"--remote-cache-dir", cacheDir,
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"plans.upgrade.cattle.io",
		"system-upgrade-plan",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestBuildAppsHomeOpsRemoteResourceOfflineCacheMiss(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns", "unsupported-remote")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", fixtureRoot,
		"--offline",
		"--remote-cache-dir", t.TempDir(),
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want offline cache miss")
	}
	if !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("error = %v, want offline cache miss", err)
	}
	if strings.Contains(stdout.String(), "system-upgrade-plan") {
		t.Fatalf("stdout rendered fixture despite cache miss:\n%s", stdout.String())
	}
}

func TestBuildAppsHomeOpsRemoteResourceRejectsCacheInsideRepo(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns", "unsupported-remote")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", fixtureRoot,
		"--offline",
		"--remote-cache-dir", filepath.Join(fixtureRoot, ".argocd-local", "remotes"),
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want cache containment error")
	}
	if !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("error = %v, want cache containment error", err)
	}
	if strings.Contains(stdout.String(), "system-upgrade-plan") {
		t.Fatalf("stdout rendered fixture despite invalid cache dir:\n%s", stdout.String())
	}
}
