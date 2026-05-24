package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/remote"
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

func TestDiffAppsRemoteKustomizePatternFixture(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns", "remote-kustomize")
	remoteRoot := filepath.Join(fixtureRoot, "remote")

	acquirer := &recordingCLIRemoteAcquirer{paths: map[string]string{
		"https://github.com/example/remote-base.git":                     filepath.Join(remoteRoot, "base"),
		"https://github.com/example/remote-component.git":                filepath.Join(remoteRoot, "component"),
		"https://github.com/example/remote-patch.git":                    filepath.Join(remoteRoot, "patch"),
		"https://raw.githubusercontent.com/example/remote/resource.yaml": filepath.Join(remoteRoot, "http", "resource.yaml"),
	}}
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{
		Orchestrator: app.Orchestrator{RemoteResourceAcquirer: acquirer},
	})
	cmd.SetArgs([]string{
		"diff", "apps",
		"--path-orig", filepath.Join(fixtureRoot, "baseline"),
		"--path", filepath.Join(fixtureRoot, "current"),
		"--offline",
		"--remote-cache-dir", filepath.Join(t.TempDir(), "remote-cache"),
		"--remote-bearer-token", "fixture-token",
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
		"Application: argocd/remote-pattern",
		"ConfigMap: remote-pattern/local",
		"-  value: baseline",
		"+  value: current",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got, want := len(acquirer.requests), 8; got != want {
		t.Fatalf("remote acquire calls = %d, want %d", got, want)
	}
	for _, want := range []string{
		"https://github.com/example/remote-base.git",
		"https://github.com/example/remote-component.git",
		"https://github.com/example/remote-patch.git",
		"https://raw.githubusercontent.com/example/remote/resource.yaml",
	} {
		if !recordedRemoteRequest(acquirer.requests, want) {
			t.Fatalf("remote acquire requests missing %q: %#v", want, acquirer.requests)
		}
	}
	if !recordedHTTPRemoteCredential(acquirer.requests, acquirer.options, "https://raw.githubusercontent.com/example/remote/resource.yaml", "fixture-token") {
		t.Fatalf("remote HTTP credential was not passed to fake acquirer: requests=%#v options=%#v", acquirer.requests, acquirer.options)
	}
}

func TestDiffAppsApplicationSetCombinationPatternFixture(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns", "appset-combinations")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"diff", "apps",
		"--path-orig", filepath.Join(fixtureRoot, "baseline"),
		"--path", filepath.Join(fixtureRoot, "current"),
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
		"Application: argocd/matrix-alpha-dev",
		"ConfigMap: dev/matrix-config",
		"value: matrix-baseline",
		"value: matrix-current",
		"Application: argocd/merge-beta",
		"ConfigMap: merge/merge-beta",
		"value: merge-baseline",
		"value: merge-current",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

type recordingCLIRemoteAcquirer struct {
	paths    map[string]string
	requests []remote.Request
	options  []remote.Options
}

func (acquirer *recordingCLIRemoteAcquirer) Acquire(_ context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	for _, key := range []string{request.RepoURL, request.URL} {
		if key == "" {
			continue
		}
		if path, ok := acquirer.paths[key]; ok {
			return remote.Result{Path: path, URL: request.URL, Revision: request.Revision}, nil
		}
	}
	return remote.Result{}, &remoteFixtureMiss{request: request}
}

type remoteFixtureMiss struct {
	request remote.Request
}

func (err *remoteFixtureMiss) Error() string {
	return "missing remote fixture for " + err.request.RepoURL + err.request.URL
}

func recordedRemoteRequest(requests []remote.Request, want string) bool {
	for _, request := range requests {
		if request.RepoURL == want || request.URL == want {
			return true
		}
	}
	return false
}

func recordedHTTPRemoteCredential(requests []remote.Request, options []remote.Options, url, bearerToken string) bool {
	for i, request := range requests {
		if request.URL != url || request.Kind != remote.RequestHTTPFile || i >= len(options) {
			continue
		}
		if options[i].Credentials.BearerToken == bearerToken {
			return true
		}
	}
	return false
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
