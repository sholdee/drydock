package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffAppsSimulatedRenovateChartBump(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "renovate-diff")
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
		"Application: argocd/renovate",
		"renovate-operator",
		"-          image: ghcr.io/example/renovate-operator:4.8.0",
		"+          image: ghcr.io/example/renovate-operator:4.8.1",
		"RENOVATE_LOG_LEVEL",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiffImagesSimulatedRenovateChartBump(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "testdata", "renovate-diff")
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{
		"diff", "images",
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
		"- ghcr.io/example/renovate-operator:4.8.0",
		"+ ghcr.io/example/renovate-operator:4.8.1",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
