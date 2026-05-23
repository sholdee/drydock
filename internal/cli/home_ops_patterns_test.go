package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
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

func TestBuildAppsHomeOpsUnsupportedRemoteResource(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "home-ops-patterns", "unsupported-remote")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", fixtureRoot,
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want unsupported remote Kustomize resource error")
	}
	combined := err.Error() + "\n" + stderr.String()
	if !strings.Contains(combined, "remote") {
		t.Fatalf("error output missing remote Kustomize context:\nerror: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "system-upgrade-plan") {
		t.Fatalf("stdout rendered unsupported remote fixture:\n%s", stdout.String())
	}
}
